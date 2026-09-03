//go:build !linux && !(windows && amd64)

package gocan

import "fmt"

func miniCANFDDlopen(path string) (uintptr, error) {
	return 0, fmt.Errorf("MiniCANFD dynamic library is unsupported on this platform")
}

func miniCANFDPlatformDefaultConfig() uint8 { return MiniCANFDDefaultConfig }

func registerMiniCANFDFuncs(handle uintptr, lib *miniCANFDLib) error {
	return fmt.Errorf("MiniCANFD dynamic library is unsupported on this platform")
}
