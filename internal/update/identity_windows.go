package update

import (
	"os"

	"golang.org/x/sys/windows"
)

// openIdentity opens path for its attributes only, sharing delete as well as read and write. Every
// daemon on the machine looks at the binary every few seconds (watch.go), and os.Open shares no
// delete: another daemon's rename of a new binary over the path, in the moment one is open, failed
// with a sharing violation, and that update waited an hour for its next check.
func openIdentity(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}
