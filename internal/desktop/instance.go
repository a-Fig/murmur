package desktop

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// One window at a time (#616): a launcher clicked twice, or `mur app` typed while the app is open,
// brings the open window forward rather than opening a second one. The window's process listens on
// a loopback port and records it, with its process and a key, in a file in the app's directory; a
// later `mur app` that finds the window there asks it to come forward, and to show a repo when one
// was asked for, and stops. The key keeps any other local program from making the window jump,
// which is all the port can do. The process claims the port as it starts, before the seconds a
// daemon can take to start, so a second click in that time finds it too.

type instance struct {
	PID  int    `json:"pid"`
	Addr string `json:"addr"`
	Key  string `json:"key"`
}

// request is what a later `mur app` sends: the key, and the repo to show, if one was asked for.
type request struct {
	Key    string `json:"key"`
	GitDir string `json:"gitDir,omitempty"`
}

// maxRequest is as much of a request as is read: a key and a repo's path.
const maxRequest = 8 << 10

// Instance is the window's claim to be the one window. Until the window is made, what is asked of
// it waits for it.
type Instance struct {
	path string
	rec  instance
	ln   net.Listener

	mu      sync.Mutex
	show    func(gitDir string) // set once the window can come forward
	asked   bool                // asked to before it could
	pending string              // the repo last asked for then, or ""
	closing bool                // the window has gone, or is going
}

// Claim opens the window's port and records it at path, unless a window is open already: then
// that one comes forward, showing the repo at gitDir unless that is "", and Claim returns nil. Two launches at once each find no window before
// either has claimed, so the finding and the claiming happen under a lock file beside the record.
func Claim(path, gitDir string) (*Instance, error) {
	unlock := lockFile(path + ".lock")
	defer unlock()
	if BringForward(path, gitDir) {
		return nil, nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	k := make([]byte, 16)
	rand.Read(k)
	in := &Instance{path: path, ln: ln, rec: instance{PID: os.Getpid(), Addr: ln.Addr().String(), Key: hex.EncodeToString(k)}}
	b, _ := json.Marshal(in.rec)
	// A file of its own, renamed into place, so two windows starting together don't share one.
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err == nil {
		_, err = tmp.Write(b)
		if cerr := tmp.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Rename(tmp.Name(), path)
		}
		if err != nil {
			os.Remove(tmp.Name())
		}
	}
	if err != nil {
		ln.Close()
		return nil, err
	}
	go in.serve()
	return in, nil
}

// lockFile takes the lock at path, waiting up to five seconds for whoever holds it, and returns
// what lets it go. A lock older than ten seconds was left by a process that died holding it; one
// that can't be taken at all is gone without, as a second window is better than none.
func lockFile(path string) func() {
	deadline := time.Now().Add(5 * time.Second)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			return func() { os.Remove(path) }
		}
		if fi, serr := os.Stat(path); serr == nil && time.Since(fi.ModTime()) > 10*time.Second {
			os.Remove(path)
			continue
		}
		if !os.IsExist(err) || time.Now().After(deadline) {
			return func() {}
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (in *Instance) serve() {
	for {
		c, err := in.ln.Accept()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
		go in.answer(c)
	}
}

func (in *Instance) answer(c net.Conn) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	line, err := bufio.NewReader(io.LimitReader(c, maxRequest)).ReadString('\n')
	var req request
	if err != nil || json.Unmarshal([]byte(line), &req) != nil || req.Key != in.rec.Key {
		return
	}
	in.mu.Lock()
	show, closing := in.show, in.closing
	if show == nil && !closing {
		in.asked = true
		if req.GitDir != "" {
			in.pending = req.GitDir // a later click on the launcher doesn't undo it
		}
	}
	in.mu.Unlock()
	if closing {
		return // no "ok", so the one asking opens a window of its own
	}
	if show != nil {
		show(req.GitDir)
	}
	c.Write([]byte("ok\n"))
}

// onShow sets what bringing the window forward does, once it can, and does it now if it was
// asked for already.
func (in *Instance) onShow(show func(gitDir string)) {
	in.mu.Lock()
	in.show = show
	asked, pending := in.asked, in.pending
	in.asked, in.pending = false, ""
	in.mu.Unlock()
	if asked {
		show(pending)
	}
}

// closingDown says the window is going: what is asked of it from now on isn't answered.
func (in *Instance) closingDown() {
	if in == nil {
		return
	}
	in.mu.Lock()
	in.show, in.closing = nil, true
	in.mu.Unlock()
}

// Close closes the port and takes the record out, if it is still this window's: a second window
// started meanwhile may have written its own, and the lock keeps one from starting meanwhile.
func (in *Instance) Close() {
	if in == nil {
		return
	}
	in.closingDown()
	unlock := lockFile(in.path + ".lock")
	defer unlock()
	in.ln.Close()
	if b, err := os.ReadFile(in.path); err == nil {
		var now instance
		if json.Unmarshal(b, &now) == nil && now == in.rec {
			os.Remove(in.path)
		}
	}
}

// BringForward asks the window recorded at path, if one is open, to come forward, and to show
// the repo at gitDir unless that is "", and says whether it did.
func BringForward(path, gitDir string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var rec instance
	if json.Unmarshal(b, &rec) != nil || rec.Addr == "" || rec.Key == "" {
		return false
	}
	c, err := net.DialTimeout("tcp", rec.Addr, time.Second)
	if err != nil {
		return false
	}
	defer c.Close()
	allowForeground(rec.PID)
	c.SetDeadline(time.Now().Add(3 * time.Second))
	req, _ := json.Marshal(request{Key: rec.Key, GitDir: gitDir})
	if _, err := c.Write(append(req, '\n')); err != nil {
		return false
	}
	reply, _ := bufio.NewReader(io.LimitReader(c, 16)).ReadString('\n')
	return reply == "ok\n"
}
