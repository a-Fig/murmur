//go:build !unix

package daemon

import (
	"cmp"
	"os"
)

// descend opens the file rest leads to below root, a part at a time: each part is looked at in
// its parent's handle, refused if it is a link or the git directory, and the folder then opened
// as a handle of its own must be the one looked at. There are no named pipes in a folder here to
// hold an opening, as on Unix (sendopen_unix.go).
func (s *sendRoots) descend(root sendRoot, rest []string) (*os.File, error) {
	cur := root.r
	defer func() {
		if cur != root.r {
			cur.Close()
		}
	}()
	for i, part := range rest {
		info, err := cur.Lstat(part)
		if err != nil {
			return nil, err
		}
		if linkIn(info, func() error { _, err := cur.Readlink(part); return err }) {
			return nil, errOutside // swapped in since local looked
		}
		if sendRace != nil {
			sendRace(part)
		}
		if i == len(rest)-1 {
			if !plainFile(info) {
				return nil, errNotFile
			}
			f, err := cur.Open(part)
			if err != nil {
				return nil, err
			}
			if got, err := f.Stat(); err != nil || !plainFile(got) || !os.SameFile(got, info) {
				f.Close()
				return nil, cmp.Or(err, errOutside) // swapped since the look
			}
			return f, nil
		}
		if !info.IsDir() || s.git != nil && os.SameFile(info, s.git) {
			return nil, errOutside
		}
		next, err := cur.OpenRoot(part)
		if err != nil {
			return nil, err
		}
		if got, err := next.Stat("."); err != nil || !os.SameFile(got, info) {
			next.Close()
			return nil, cmp.Or(err, errOutside) // swapped since the look
		}
		if cur != root.r {
			cur.Close()
		}
		cur = next
	}
	return nil, errNotFile
}
