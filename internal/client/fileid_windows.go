package client

import "syscall"

// fileID names a file by its volume and index, which stay with the file when it is renamed.
type fileID struct{ volume, high, low uint32 }

// fileIdentity reads path's fileID through a handle that shares delete, so a rename over the file
// (an update's swap) isn't refused while it is open. It is read from the handle at once: os.Stat
// may load a file's ID only when SameFile asks, from whatever is at the path by then.
func fileIdentity(path string) (fileID, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return fileID{}, err
	}
	const readAttributes = 0x80 // FILE_READ_ATTRIBUTES
	h, err := syscall.CreateFile(p, readAttributes, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return fileID{}, err
	}
	defer syscall.CloseHandle(h)
	var d syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &d); err != nil {
		return fileID{}, err
	}
	return fileID{d.VolumeSerialNumber, d.FileIndexHigh, d.FileIndexLow}, nil
}

func sameFile(a, b fileID) bool { return a == b }
