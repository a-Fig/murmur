//go:build !windows

package join

import (
	"errors"
	"os"
	"syscall"
)

// publishNewFile atomically publishes a complete staged sibling file at dst via os.Link (failing
// if dst already exists as a file or symlink). On filesystems that don't support hard links
// (such as a mounted exFAT/FAT32 volume), it falls back to os.Rename after confirming dst is absent.
func publishNewFile(src, dst string) error {
	err := os.Link(src, dst)
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EXDEV) {
		if _, lerr := os.Lstat(dst); errors.Is(lerr, os.ErrNotExist) {
			return os.Rename(src, dst)
		} else if lerr == nil {
			return os.ErrExist
		} else {
			return lerr
		}
	}
	return err
}
