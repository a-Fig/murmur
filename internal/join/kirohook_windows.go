//go:build windows

package join

import "syscall"

// publishNewFile atomically publishes a complete staged sibling file at dst on Windows using
// MoveFileW, which works on NTFS, exFAT, and FAT32 and fails without overwriting if dst already
// exists (regular file, symlink, or directory).
func publishNewFile(src, dst string) error {
	from, err := syscall.UTF16PtrFromString(src)
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(dst)
	if err != nil {
		return err
	}
	return syscall.MoveFile(from, to)
}
