//go:build !windows

package loom

import (
	"fmt"
	"syscall"
)

// CheckFreeDiskSpace verifies that the volume containing dir has at least requiredBytes free.
func CheckFreeDiskSpace(dir string, requiredBytes int64) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return nil
	}
	freeBytes := int64(stat.Bavail) * int64(stat.Bsize)
	if freeBytes < requiredBytes {
		return fmt.Errorf("insufficient disk space: need %d bytes, available %d bytes", requiredBytes, freeBytes)
	}
	return nil
}
