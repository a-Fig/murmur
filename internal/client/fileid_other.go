//go:build !windows

package client

import "os"

// fileID is a file's FileInfo, whose device and inode stay with the file when it is renamed.
type fileID struct{ fi os.FileInfo }

func fileIdentity(path string) (fileID, error) {
	fi, err := os.Stat(path)
	return fileID{fi}, err
}

func sameFile(a, b fileID) bool { return os.SameFile(a.fi, b.fi) }
