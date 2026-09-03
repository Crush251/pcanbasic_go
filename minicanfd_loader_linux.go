//go:build linux

package gocan

import (
	"fmt"
	"os"
	"runtime"

	"github.com/ebitengine/purego"
)

func miniCANFDDlopen(path string) (uintptr, error) {
	// Some vendor builds reference libusb symbols without declaring a DT_NEEDED
	// dependency. Preload libusb into the global namespace before resolving the
	// vendor object so those symbols are available to the loader.
	// Ignore a missing system libusb here: some vendor builds link it
	// statically. If the selected library actually needs the shared symbols,
	// its Dlopen error below will report the unresolved dependency.
	_, _ = purego.Dlopen("libusb-1.0.so.0", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if path == "" {
		path = os.Getenv("MINICANFD_LIBRARY_PATH")
	}
	if path == "" {
		path = os.Getenv("LINKERBOT_CANFD_LIB")
	}
	if path == "" {
		path = "libcanbus.so"
		if runtime.GOARCH == "arm64" {
			path = "libcanbus_arm64.so"
		}
	}
	return purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
}

func miniCANFDPlatformDefaultConfig() uint8 { return MiniCANFDDefaultConfig }

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
	for _, symbol := range []struct {
		target any
		name   string
	}{
		{&lib.scanDevice, "CAN_ScanDevice"}, {&lib.openDevice, "CAN_OpenDevice"},
		{&lib.closeDevice, "CAN_CloseDevice"}, {&lib.readDevInfo, "CAN_ReadDevInfo"},
		{&lib.initFD, "CANFD_Init"}, {&lib.transmit, "CANFD_Transmit"}, {&lib.receive, "CANFD_Receive"},
	} {
		if err := register(symbol.target, symbol.name); err != nil {
			return err
		}
	}
	registerOptional := func(target any, name string) {
		defer func() { _ = recover() }()
		purego.RegisterLibFunc(target, handle, name)
	}
	registerOptional(&lib.reset, "CAN_Reset")
	registerOptional(&lib.setFilter, "CAN_SetFilter")
	registerOptional(&lib.runtimeInit, "LibCANbus_Init")
	registerOptional(&lib.runtimeExit, "LibCANbus_Exit")
	return nil
}
