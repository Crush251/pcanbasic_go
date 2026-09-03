package gocan

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/zhuzx17/gocan/raw"
)

const (
	// MiniCANFDConfig holds the vendor adapter initialization parameters.
	MiniCANFDDefaultNominalBitrate  = 1_000_000
	MiniCANFDDefaultDataBitrate     = 5_000_000
	MiniCANFDDefaultConfig          = 0x07
	MiniCANFDDefaultModel           = 0
	MiniCANFDDefaultCANType         = 1
	MiniCANFDDefaultFrameType       = 0x04
	MiniCANFDMaxDataLength          = 64
	MiniCANFDDefaultTransmitTimeout = 100 * time.Millisecond
	MiniCANFDDefaultReceiveTimeout  = 100 * time.Millisecond
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
func LookupMiniCANFDDevices(libraryPath string) (devices []MiniCANFDDeviceInfo, err error) {
	lib, release, err := acquireMiniCANFDLibrary(libraryPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		if releaseErr := release(); releaseErr != nil {
			err = errors.Join(err, releaseErr)
		}
	}()
	count := lib.scanDevice()
	if count < 0 {
		return nil, fmt.Errorf("MiniCANFD CAN_ScanDevice failed with status %d", count)
	}
	devices = make([]MiniCANFDDeviceInfo, 0, count)
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
	lib, release, err := acquireMiniCANFDLibrary(cfg.LibraryPath)
	if err != nil {
		return nil, err
	}
	count := lib.scanDevice()
	if count < 0 {
		_ = release()
		return nil, fmt.Errorf("MiniCANFD CAN_ScanDevice failed with status %d", count)
	}
	if int32(cfg.DeviceIndex) >= count {
		_ = release()
		return nil, fmt.Errorf("MiniCANFD device index %d out of range (found %d): %w", cfg.DeviceIndex, count, ErrIllParamValue)
	}
	if status := lib.openDevice(uint32(cfg.DeviceIndex), uint32(cfg.ChannelIndex)); status != 0 {
		_ = release()
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
		_ = release()
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
		_ = release()
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
	bus.minicanfd = &miniCANFDBackend{lib: lib, device: uint32(cfg.DeviceIndex), channel: uint32(cfg.ChannelIndex), frameType: cfg.FrameType, release: release}
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
	release   func() error
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
	if status := m.lib.transmit(m.device, m.channel, &msg, 1, int32(MiniCANFDDefaultTransmitTimeout/time.Millisecond)); status <= 0 {
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
	var releaseErr error
	if m.release != nil {
		releaseErr = m.release()
		m.release = nil
	}
	if status != 0 {
		m.closed = true
		return errors.Join(fmt.Errorf("MiniCANFD CAN_CloseDevice failed with status %d", status), releaseErr)
	}
	m.closed = true
	return releaseErr
}

func (m *miniCANFDBackend) reset() error {
	m.txMu.Lock()
	defer m.txMu.Unlock()
	m.rxMu.Lock()
	defer m.rxMu.Unlock()
	if m.closed {
		return ErrBusClosed
	}
	if m.lib.reset == nil {
		return ErrNotSupported
	}
	if status := m.lib.reset(m.device, m.channel); status != 0 {
		return fmt.Errorf("MiniCANFD CAN_Reset failed with status %d", status)
	}
	// CAN_Reset clears vendor filters. Restore the documented default
	// receive-all filter when the SDK exposes CAN_SetFilter.
	if m.lib.setFilter != nil {
		if status := m.lib.setFilter(m.device, m.channel, 0, 1, 0, 0, 1); status != 0 {
			return fmt.Errorf("MiniCANFD CAN_SetFilter failed with status %d", status)
		}
	}
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
	reset       func(uint32, uint32) int32
	setFilter   func(uint32, uint32, int8, int8, uint32, uint32, int8) int32
	runtimeInit func() int32
	runtimeExit func() int32
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
	if !frame.Has(FlagFD) && len(frame.Data) > 8 {
		return miniCANFDMsg{}, ErrDataTooLong
	}
	if frame.Has(FlagFD) && frame.Has(FlagRemote) {
		return miniCANFDMsg{}, ErrRemoteOnFD
	}
	if len(frame.Data) > MiniCANFDMaxDataLength {
		return miniCANFDMsg{}, ErrDataTooLong
	}
	dlc, ok := dataLenToMiniCANFDDLC(len(frame.Data))
	if !ok {
		return miniCANFDMsg{}, ErrInvalidFDLength
	}
	frameType := defaultFrameType &^ 0x0c
	if frame.Has(FlagFD) {
		frameType |= 0x04
		if frame.Has(FlagBRS) {
			frameType |= 0x08
		}
	}
	msg := miniCANFDMsg{ID: frame.ID, FrameType: frameType, DLC: dlc, ExternFlag: boolByte(frame.Has(FlagExtended)), RemoteFlag: boolByte(frame.Has(FlagRemote))}
	copy(msg.Data[:], frame.Data)
	return msg, nil
}

func frameFromMiniCANFDMsg(msg miniCANFDMsg) (Frame, error) {
	length, ok := miniCANFDDLCToLength(msg.DLC)
	if !ok || length > len(msg.Data) {
		return Frame{}, ErrInvalidFDLength
	}
	var flags FrameFlags
	if msg.FrameType&0x04 != 0 {
		flags |= FlagFD
	}
	if msg.FrameType&0x08 != 0 {
		flags |= FlagBRS
	}
	if msg.ExternFlag != 0 {
		flags |= FlagExtended
	}
	if msg.RemoteFlag != 0 {
		flags |= FlagRemote
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

type miniCANFDRuntime struct {
	lib         *miniCANFDLib
	refs        int
	initialized bool
	exited      bool
}

var miniCANFDRuntimes = struct {
	sync.Mutex
	byPath map[string]*miniCANFDRuntime
}{byPath: make(map[string]*miniCANFDRuntime)}

var miniCANFDLoadMu sync.Mutex

func acquireMiniCANFDLibrary(path string) (*miniCANFDLib, func() error, error) {
	miniCANFDLoadMu.Lock()
	defer miniCANFDLoadMu.Unlock()
	key := path
	miniCANFDRuntimes.Lock()
	if runtime := miniCANFDRuntimes.byPath[key]; runtime != nil {
		runtime.refs++
		miniCANFDRuntimes.Unlock()
		return runtime.lib, func() error { return releaseMiniCANFDRuntime(key, runtime) }, nil
	}
	miniCANFDRuntimes.Unlock()

	handle, err := miniCANFDDlopen(path)
	if err != nil {
		return nil, nil, err
	}
	lib := &miniCANFDLib{}
	if err := registerMiniCANFDFuncs(handle, lib); err != nil {
		return nil, nil, err
	}
	runtime := &miniCANFDRuntime{lib: lib, refs: 1}
	if lib.runtimeInit != nil {
		if status := lib.runtimeInit(); status != 0 {
			return nil, nil, fmt.Errorf("MiniCANFD LibCANbus_Init failed with status %d", status)
		}
		runtime.initialized = true
	}
	miniCANFDRuntimes.Lock()
	if existing := miniCANFDRuntimes.byPath[key]; existing != nil {
		existing.refs++
		miniCANFDRuntimes.Unlock()
		if runtime.initialized && runtime.lib.runtimeExit != nil {
			_ = runtime.lib.runtimeExit()
		}
		return existing.lib, func() error { return releaseMiniCANFDRuntime(key, existing) }, nil
	}
	miniCANFDRuntimes.byPath[key] = runtime
	miniCANFDRuntimes.Unlock()
	return lib, func() error { return releaseMiniCANFDRuntime(key, runtime) }, nil
}

func releaseMiniCANFDRuntime(key string, runtime *miniCANFDRuntime) error {
	miniCANFDLoadMu.Lock()
	defer miniCANFDLoadMu.Unlock()
	miniCANFDRuntimes.Lock()
	if runtime.refs > 0 {
		runtime.refs--
	}
	last := runtime.refs == 0 && !runtime.exited
	if last {
		runtime.exited = true
		delete(miniCANFDRuntimes.byPath, key)
	}
	miniCANFDRuntimes.Unlock()
	if last && runtime.initialized && runtime.lib.runtimeExit != nil {
		if status := runtime.lib.runtimeExit(); status != 0 {
			return fmt.Errorf("MiniCANFD LibCANbus_Exit failed with status %d", status)
		}
	}
	return nil
}
