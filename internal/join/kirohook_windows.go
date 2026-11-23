//go:build windows

package join

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// publishNewFile atomically publishes a complete staged sibling file at dst on Windows. It tries
// os.Link first (which handles long paths natively on NTFS), and falls back to MoveFileW with
// extended-length path prefixing for filesystems without hard links (such as exFAT and FAT32),
// failing without overwriting if dst already exists.
func publishNewFile(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	} else if os.IsExist(err) {
		return err
	}
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
	if len(p) < 248 || strings.HasPrefix(p, `\\?\`) {
		return p
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if strings.HasPrefix(abs, `\\`) {
		return `\\?\UNC\` + abs[2:]
	}
	return `\\?\` + abs
}
