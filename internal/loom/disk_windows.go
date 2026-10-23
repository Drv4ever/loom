//go:build windows

package loom

import (
	"fmt"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	getDiskFreeSpaceExW = kernel32.NewProc("GetDiskFreeSpaceExW")
)

// CheckFreeDiskSpace verifies that the volume containing dir has at least requiredBytes free.
func CheckFreeDiskSpace(dir string, requiredBytes int64) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil
	}
	dirPtr, err := syscall.UTF16PtrFromString(abs)
	if err != nil {
		return nil
	}
	var freeBytesAvailable uint64
	var totalBytes uint64
	var totalFreeBytes uint64

	r1, _, _ := getDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(dirPtr)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFreeBytes)),
	)
	if r1 == 0 {
		return nil
	}

	if int64(freeBytesAvailable) < requiredBytes {
		return fmt.Errorf("insufficient disk space: need %d bytes, available %d bytes", requiredBytes, freeBytesAvailable)
	}
	return nil
}
