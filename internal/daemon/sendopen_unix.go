//go:build unix

package daemon

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// descend opens the file rest leads to below root, a part at a time, each opened in the folder
// before it by openat. The kernel does the checks in the one call that opens, so a part swapped
// between a look and the opening can't get past them: O_NOFOLLOW refuses a link, and O_NONBLOCK keeps
// a named pipe from holding the open until something writes to it, past any cancellation, as
// os.Root's opening would (Sol's reviews of #773). What was opened is then looked at through its
// handle: a folder for each part but the last, never the git directory, and a file for the last.
func (s *sendRoots) descend(root sendRoot, rest []string) (*os.File, error) {
	cur, err := root.r.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { cur.Close() }()
	for i, part := range rest {
		if sendRace != nil {
			sendRace(part)
		}
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_NOCTTY | unix.O_CLOEXEC
		if i < len(rest)-1 {
			flags |= unix.O_DIRECTORY // a device or pipe isn't even opened
		}
		fd, err := unix.Openat(int(cur.Fd()), part, flags, 0)
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EMLINK) {
			return nil, errOutside // a link, swapped in since local looked (EMLINK is FreeBSD's word)
		}
		if errors.Is(err, unix.ENOTDIR) {
			return nil, errOutside // swapped for something not a folder
		}
		if err != nil {
			return nil, &os.PathError{Op: "openat", Path: part, Err: err}
		}
		f := os.NewFile(uintptr(fd), part)
		info, err := f.Stat()
		switch {
		case err != nil:
			f.Close()
			return nil, err
		case i == len(rest)-1:
			if !info.Mode().IsRegular() {
				f.Close()
				return nil, errNotFile
			}
			return f, nil
		case !info.IsDir() || s.git != nil && os.SameFile(info, s.git):
			f.Close()
			return nil, errOutside
		}
		cur.Close()
		cur = f
	}
	return nil, errNotFile
}
