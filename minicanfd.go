package gocan

import (
	"fmt"
	"sync"
	"time"

	"github.com/zhuzx17/gocan/raw"
)

const (
	// MiniCANFDConfig holds the vendor adapter initialization parameters.
	MiniCANFDDefaultNominalBitrate = 1_000_000
	MiniCANFDDefaultDataBitrate    = 5_000_000
	MiniCANFDDefaultConfig         = 0x07
	MiniCANFDDefaultModel          = 0
	MiniCANFDDefaultCANType        = 1
	MiniCANFDDefaultFrameType      = 0x04
	MiniCANFDMaxDataLength         = 64
	MiniCANFDDefaultReceiveTimeout = 10 * time.Millisecond
)

// MiniCANFDConfig describes one channel of a MiniCANFD vendor adapter.
// The dynamic library is loaded on demand and is never unloaded while the
// process is running, because vendor libraries may retain global USB state.
type MiniCANFDConfig struct {
	DeviceIndex  int
	ChannelIndex int
	// Channel is an alias for ChannelIndex kept for callers that use the
	// vendor SDK's shorter terminology. When non-zero and ChannelIndex is zero,
	// Channel is used.
	Channel     int
	NominalRate uint32
	DataRate    uint32
	Config      uint8
	Model       uint8
	CANType     uint8
	FrameType   uint8
	LibraryPath string
}

// MiniCANFDDeviceInfo describes an adapter returned by CAN_ReadDevInfo.
// It identifies the USB-CANFD adapter, not a L30/O20 device on the bus.
type MiniCANFDDeviceInfo struct {
	Index           int
	HardwareType    string
	SerialNumber    string
	HardwareVersion string
	FirmwareVersion string
	ManufactureDate string
}

// LookupMiniCANFDDevices loads the vendor library and returns all scanned
// adapter indices. Device metadata is read when the vendor API provides it.
func LookupMiniCANFDDevices(libraryPath string) ([]MiniCANFDDeviceInfo, error) {
	lib, err := loadMiniCANFDLibrary(libraryPath)
	if err != nil {
		return nil, err
	}
	count := lib.scanDevice()
	if count < 0 {
		return nil, fmt.Errorf("MiniCANFD CAN_ScanDevice failed with status %d", count)
	}
	devices := make([]MiniCANFDDeviceInfo, 0, count)
	for index := int32(0); index < count; index++ {
		info := MiniCANFDDeviceInfo{Index: int(index)}
		if err := lib.readDeviceInfo(uint(index), &info); err != nil {
			// An adapter can still be opened when optional metadata is unavailable.
			devices = append(devices, info)
			continue
		}
		devices = append(devices, info)
	}
	if len(devices) == 0 && count > 0 {
		for index := int32(0); index < count; index++ {
			devices = append(devices, MiniCANFDDeviceInfo{Index: int(index)})
		}
	}
	return devices, nil
}

// OpenMiniCANFD opens and initializes one vendor CAN FD channel.
func OpenMiniCANFD(cfg MiniCANFDConfig, opts ...Option) (*Bus, error) {
	if cfg.ChannelIndex == 0 && cfg.Channel != 0 {
		cfg.ChannelIndex = cfg.Channel
	}
	if cfg.ChannelIndex < 0 || cfg.Channel < 0 || cfg.DeviceIndex < 0 {
		return nil, fmt.Errorf("open MiniCANFD: device/channel index must be non-negative: %w", ErrIllParamValue)
	}
	if cfg.NominalRate == 0 {
		cfg.NominalRate = MiniCANFDDefaultNominalBitrate
	}
	if cfg.DataRate == 0 {
		cfg.DataRate = MiniCANFDDefaultDataBitrate
	}
	if cfg.Config == 0 {
		cfg.Config = miniCANFDPlatformDefaultConfig()
	}
	if cfg.CANType == 0 {
		cfg.CANType = MiniCANFDDefaultCANType
	}
	if cfg.FrameType == 0 {
		cfg.FrameType = MiniCANFDDefaultFrameType
	}
	lib, err := loadMiniCANFDLibrary(cfg.LibraryPath)
	if err != nil {
		return nil, err
	}
	count := lib.scanDevice()
	if count < 0 {
		return nil, fmt.Errorf("MiniCANFD CAN_ScanDevice failed with status %d", count)
	}
	if int32(cfg.DeviceIndex) >= count {
		return nil, fmt.Errorf("MiniCANFD device index %d out of range (found %d): %w", cfg.DeviceIndex, count, ErrIllParamValue)
	}
	if status := lib.openDevice(uint32(cfg.DeviceIndex), uint32(cfg.ChannelIndex)); status != 0 {
		return nil, fmt.Errorf("MiniCANFD CAN_OpenDevice failed with status %d", status)
	}
	opened := true
	cleanup := func() {
		if opened {
			_ = lib.closeDevice(uint32(cfg.DeviceIndex), uint32(cfg.ChannelIndex))
		}
	}
	vendorCfg := miniCANFDConfig{
		NomBaud: cfg.NominalRate, DatBaud: cfg.DataRate,
		Config: cfg.Config, Model: cfg.Model, Cantype: cfg.CANType,
	}
	if status := lib.initFD(uint32(cfg.DeviceIndex), uint32(cfg.ChannelIndex), &vendorCfg); status != 0 {
		cleanup()
		return nil, fmt.Errorf("MiniCANFD CANFD_Init failed with status %d", status)
	}

	busCfg := newDefaultConfig()
	for _, opt := range opts {
		if opt != nil {
			opt(busCfg)
		}
	}
	if busCfg.receiveMode == ModeEvent {
		_ = lib.closeDevice(uint32(cfg.DeviceIndex), uint32(cfg.ChannelIndex))
		return nil, fmt.Errorf("open MiniCANFD: event receive mode: %w", ErrNotSupported)
	}
	bus := &Bus{
		adapt:   nil,
		ch:      raw.TPCANHandle(cfg.ChannelIndex),
		isFD:    true,
		cfg:     busCfg,
		rxCh:    make(chan Frame, busCfg.rxBufferSize),
		errCh:   make(chan error, busCfg.errBufferSize),
		closing: make(chan struct{}),
	}
	bus.minicanfd = &miniCANFDBackend{lib: lib, device: uint32(cfg.DeviceIndex), channel: uint32(cfg.ChannelIndex), frameType: cfg.FrameType}
	opened = false
	bus.startReader()
	return bus, nil
}

// miniCANFDBackend is implemented as a Bus backend so callers can reuse the
// normal Send/Receive/ReadOne/Close API and the Dashboard CAN FD session.
type miniCANFDBackend struct {
	txMu      sync.Mutex
	rxMu      sync.Mutex
	lib       *miniCANFDLib
	device    uint32
	channel   uint32
	frameType uint8
	closed    bool
}

func (m *miniCANFDBackend) send(frame Frame) error {
	m.txMu.Lock()
	defer m.txMu.Unlock()
	if m.closed {
		return ErrBusClosed
	}
	msg, err := miniCANFDFrameFromFrame(frame, m.frameType)
	if err != nil {
		return err
	}
	if status := m.lib.transmit(m.device, m.channel, &msg, 1, 10); status <= 0 {
		return fmt.Errorf("MiniCANFD CANFD_Transmit failed with status %d", status)
	}
	return nil
}

func (m *miniCANFDBackend) readFrame() (Frame, error) {
	m.rxMu.Lock()
	defer m.rxMu.Unlock()
	if m.closed {
		return Frame{}, ErrBusClosed
	}
	var msg miniCANFDMsg
	status := m.lib.receive(m.device, m.channel, &msg, 1, int32(MiniCANFDDefaultReceiveTimeout/time.Millisecond))
	if status == 0 {
		return Frame{}, errQueueEmpty
	}
	if status < 0 {
		return Frame{}, fmt.Errorf("MiniCANFD CANFD_Receive failed with status %d", status)
	}
	return frameFromMiniCANFDMsg(msg)
}

func (m *miniCANFDBackend) close() error {
	m.txMu.Lock()
	defer m.txMu.Unlock()
	m.rxMu.Lock()
	defer m.rxMu.Unlock()
	if m.closed {
		return nil
	}
	status := m.lib.closeDevice(m.device, m.channel)
	if status != 0 {
		return fmt.Errorf("MiniCANFD CAN_CloseDevice failed with status %d", status)
	}
	m.closed = true
	return nil
}

type miniCANFDConfig struct {
	NomBaud  uint32
	DatBaud  uint32
	NomPre   uint16
	NomTseg1 uint8
	NomTseg2 uint8
	NomSJW   uint8
	DatPre   uint8
	DatTseg1 uint8
	DatTseg2 uint8
	DatSJW   uint8
	Config   uint8
	Model    uint8
	Cantype  uint8
}

type miniCANFDMsg struct {
	ID         uint32
	TimeStamp  uint32
	FrameType  uint8
	DLC        uint8
	ExternFlag uint8
	RemoteFlag uint8
	BusStatus  uint8
	ErrStatus  uint8
	TECounter  uint8
	RECounter  uint8
	Data       [MiniCANFDMaxDataLength]uint8
}

type miniCANFDDevInfo struct {
	HWType [32]byte
	HWSer  [32]byte
	HWVer  [32]byte
	FWVer  [32]byte
	MFDate [32]byte
}

type miniCANFDLib struct {
	scanDevice  func() int32
	openDevice  func(uint32, uint32) int32
	closeDevice func(uint32, uint32) int32
	readDevInfo func(uint32, *miniCANFDDevInfo) int32
	initFD      func(uint32, uint32, *miniCANFDConfig) int32
	transmit    func(uint32, uint32, *miniCANFDMsg, uint32, int32) int32
	receive     func(uint32, uint32, *miniCANFDMsg, uint32, int32) int32
}

func (m *miniCANFDLib) readDeviceInfo(index uint, info *MiniCANFDDeviceInfo) error {
	var rawInfo miniCANFDDevInfo
	if status := m.readDevInfo(uint32(index), &rawInfo); status != 0 {
		return fmt.Errorf("CAN_ReadDevInfo status %d", status)
	}
	info.HardwareType = miniCANFDString(rawInfo.HWType[:])
	info.SerialNumber = miniCANFDString(rawInfo.HWSer[:])
	info.HardwareVersion = miniCANFDString(rawInfo.HWVer[:])
	info.FirmwareVersion = miniCANFDString(rawInfo.FWVer[:])
	info.ManufactureDate = miniCANFDString(rawInfo.MFDate[:])
	return nil
}

func miniCANFDString(value []byte) string {
	if index := bytesIndexByte(value, 0); index >= 0 {
		value = value[:index]
	}
	return string(value)
}

func bytesIndexByte(value []byte, needle byte) int {
	for index, item := range value {
		if item == needle {
			return index
		}
	}
	return -1
}

func miniCANFDFrameFromFrame(frame Frame, defaultFrameType uint8) (miniCANFDMsg, error) {
	if !frame.Has(FlagFD) {
		return miniCANFDMsg{}, fmt.Errorf("MiniCANFD requires CAN FD frame: %w", ErrFDNotSupportedOnBus)
	}
	if frame.Has(FlagRemote) {
		return miniCANFDMsg{}, ErrRemoteOnFD
	}
	if len(frame.Data) > MiniCANFDMaxDataLength {
		return miniCANFDMsg{}, ErrDataTooLong
	}
	dlc, ok := dataLenToMiniCANFDDLC(len(frame.Data))
	if !ok {
		return miniCANFDMsg{}, ErrInvalidFDLength
	}
	msg := miniCANFDMsg{ID: frame.ID, FrameType: defaultFrameType, DLC: dlc, ExternFlag: boolByte(frame.Has(FlagExtended))}
	if frame.Has(FlagBRS) {
		// Vendor FrameType is firmware-defined; retain the configured value.
		msg.FrameType = defaultFrameType
	}
	copy(msg.Data[:], frame.Data)
	return msg, nil
}

func frameFromMiniCANFDMsg(msg miniCANFDMsg) (Frame, error) {
	length, ok := miniCANFDDLCToLength(msg.DLC)
	if !ok || length > len(msg.Data) {
		return Frame{}, ErrInvalidFDLength
	}
	flags := FlagFD
	if msg.ExternFlag != 0 {
		flags |= FlagExtended
	}
	return Frame{ID: msg.ID, Data: append([]byte(nil), msg.Data[:length]...), Flags: flags, TimestampMicros: uint64(msg.TimeStamp), ReceivedAt: time.Now()}, nil
}

func boolByte(value bool) uint8 {
	if value {
		return 1
	}
	return 0
}

func dataLenToMiniCANFDDLC(length int) (uint8, bool) {
	if length >= 0 && length <= 8 {
		return uint8(length), true
	}
	switch length {
	case 12:
		return 9, true
	case 16:
		return 10, true
	case 20:
		return 11, true
	case 24:
		return 12, true
	case 32:
		return 13, true
	case 48:
		return 14, true
	case 64:
		return 15, true
	default:
		return 0, false
	}
}

func miniCANFDDLCToLength(dlc uint8) (int, bool) {
	if dlc <= 8 {
		return int(dlc), true
	}
	lengths := [...]int{12, 16, 20, 24, 32, 48, 64}
	if int(dlc-9) >= len(lengths) {
		return 0, false
	}
	return lengths[dlc-9], true
}

func loadMiniCANFDLibrary(path string) (*miniCANFDLib, error) {
	handle, err := miniCANFDDlopen(path)
	if err != nil {
		return nil, err
	}
	lib := &miniCANFDLib{}
	if err := registerMiniCANFDFuncs(handle, lib); err != nil {
		return nil, err
	}
	return lib, nil
}
