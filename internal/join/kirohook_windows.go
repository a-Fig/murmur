//go:build windows

package join

import (
	"path/filepath"
	"strings"
	"syscall"
)

// publishNewFile atomically publishes a complete staged sibling file at dst on Windows using
// MoveFileW with extended-length path normalization, which works on NTFS, exFAT, and FAT32,
// avoids leaving a second hard link behind, and fails without overwriting if dst already exists.
func publishNewFile(src, dst string) error {
	from, err := syscall.UTF16PtrFromString(winLongPath(src))
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(winLongPath(dst))
	if err != nil {
		return err
	}
	return syscall.MoveFile(from, to)
}

func winLongPath(p string) string {
	if len(p) < 248 || strings.HasPrefix(p, `\\?\`) || strings.HasPrefix(p, `\\.\`) {
		return p
	}
	abs, err := filepath.Abs(p)
	if err != nil || strings.HasPrefix(abs, `\\?\`) || strings.HasPrefix(abs, `\\.\`) {
		return p
	}
	if strings.HasPrefix(abs, `\\`) {
		return `\\?\UNC\` + abs[2:]
	}
	return `\\?\` + abs
}
