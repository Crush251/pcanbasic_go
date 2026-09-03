//go:build windows && amd64

package gocan

import (
	"fmt"
	"os"
	"syscall"

	"github.com/ebitengine/purego"
)

func miniCANFDDlopen(path string) (uintptr, error) {
	if path == "" {
		path = os.Getenv("MINICANFD_LIBRARY_PATH")
	}
	if path == "" {
		path = os.Getenv("LINKERBOT_CANFD_LIB")
	}
	if path == "" {
		path = "HCanbus.dll"
	}
	handle, err := syscall.LoadLibrary(path)
	if err != nil {
		return 0, err
	}
	return uintptr(handle), nil
}

// The Windows SDK enables automatic retransmission and wake-up with 0x06;
// Linux libcanbus additionally enables the adapter's internal resistor (0x01).
func miniCANFDPlatformDefaultConfig() uint8 { return 0x06 }

func registerMiniCANFDFuncs(handle uintptr, lib *miniCANFDLib) (err error) {
	register := func(target any, name string) (registerErr error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				registerErr = fmt.Errorf("MiniCANFD symbol %s is missing: %v", name, recovered)
			}
		}()
		purego.RegisterLibFunc(target, handle, name)
		return nil
	}
	var open func(uint32) int32
	var close func(uint32) int32
	var init func(uint32, *miniCANFDConfig) int32
	var tx func(uint32, *miniCANFDMsg, uint32, int32) int32
	var rx func(uint32, *miniCANFDMsg, uint32, int32) int32
	for _, symbol := range []struct {
		target any
		name   string
	}{
		{&lib.scanDevice, "CAN_ScanDevice"}, {&open, "CAN_OpenDevice"},
		{&close, "CAN_CloseDevice"}, {&lib.readDevInfo, "CAN_ReadDevInfo"},
		{&init, "CANFD_Init"}, {&tx, "CANFD_Transmit"}, {&rx, "CANFD_Receive"},
	} {
		if err := register(symbol.target, symbol.name); err != nil {
			return err
		}
	}
	lib.openDevice = func(device, _ uint32) int32 { return open(device) }
	lib.closeDevice = func(device, _ uint32) int32 { return close(device) }
	lib.initFD = func(device, _ uint32, cfg *miniCANFDConfig) int32 { return init(device, cfg) }
	lib.transmit = func(device, _ uint32, msg *miniCANFDMsg, count uint32, timeout int32) int32 {
		return tx(device, msg, count, timeout)
	}
	lib.receive = func(device, _ uint32, msg *miniCANFDMsg, count uint32, timeout int32) int32 {
		return rx(device, msg, count, timeout)
	}
	return nil
}
