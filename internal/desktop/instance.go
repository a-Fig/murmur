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

	"github.com/a-Fig/murmur/internal/here"
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

// waitForWindow is how long a request that comes while the window is being made waits for it:
// starting a daemon can take seconds. It is answered "ok" only once the window has come forward,
// so a window that fails while being made sends the asker on to open its own.
const waitForWindow = 30 * time.Second

// showFunc brings the window forward, and moves it to the repo at gitDir unless that is "". It
// returns at once, and calls reply once it is done, with whether the window came forward: on the
// window's own thread, so a close already waiting there comes first (Sol review).
type showFunc func(gitDir string, reply func(ok bool))

// raiseWait is how long a window that has been made has to come forward.
const raiseWait = 5 * time.Second

// Instance is the window's claim to be the one window. Until the window is made, what is asked of
// it waits for it.
type Instance struct {
	path string
	rec  instance
	ln   net.Listener

	mu      sync.Mutex
	show    showFunc      // set once the window can come forward
	asked   bool          // asked to before it could
	pending string        // the repo asked for then, or ""
	shown   bool          // the window was made, and came forward for what was asked
	closing bool          // the window has gone, or is going
	ready   chan struct{} // closed once the window is made, or isn't to be
	once    sync.Once
}

// Claim opens the window's port and records it at path, unless a window is open already: then
// that one comes forward, showing the repo at gitDir unless that is "", and Claim returns nil.
// Two launches at once each find no window before either has claimed, so the finding and the
// claiming happen under a lock file beside the record.
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
	in := &Instance{path: path, ln: ln, ready: make(chan struct{}), rec: instance{PID: os.Getpid(), Addr: ln.Addr().String(), Key: hex.EncodeToString(k)}}
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
// what lets it go. A holder keeps it while a window it found is being made, up to waitForWindow,
// so a lock is taken for one left by a process that died holding it only after twice that. The
// lock holds a token of its own, so a holder that outlived that doesn't let go of the next one's.
// One that can't be taken at all is gone without, as a second window is better than none.
func lockFile(path string) func() {
	t := make([]byte, 8)
	rand.Read(t)
	token := hex.EncodeToString(t)
	deadline := time.Now().Add(5 * time.Second)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.WriteString(token)
			f.Close()
			return func() {
				if b, err := os.ReadFile(path); err == nil && string(b) == token {
					os.Remove(path)
				}
			}
		}
		if fi, serr := os.Stat(path); serr == nil && time.Since(fi.ModTime()) > 2*waitForWindow {
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
	switch show := in.show; {
	case in.closing:
		in.mu.Unlock()
		return // no "ok", so the one asking opens a window of its own
	case show != nil:
		in.mu.Unlock()
		c.SetDeadline(time.Now().Add(raiseWait + 2*time.Second)) // or a slow raise's "ok" is lost
		done := make(chan bool, 1)
		show(req.GitDir, func(ok bool) { done <- ok })
		select {
		case ok := <-done:
			if !ok {
				return
			}
		case <-time.After(raiseWait):
			return
		}
	default:
		in.asked = true
		if req.GitDir != "" {
			in.pending = req.GitDir // a later click on the launcher doesn't undo it
		}
		in.mu.Unlock()
		c.SetDeadline(time.Now().Add(waitForWindow + raiseWait + 2*time.Second))
		select {
		case <-in.ready:
		case <-time.After(waitForWindow + raiseWait):
			return
		}
		in.mu.Lock()
		shown := in.shown
		in.mu.Unlock()
		if !shown {
			return
		}
	}
	c.Write([]byte("ok\n"))
}

// onShow sets what bringing the window forward does, once it can, and does it now if it was
// asked for already.
func (in *Instance) onShow(show showFunc) {
	in.mu.Lock()
	if in.closing {
		in.mu.Unlock()
		return
	}
	in.show = show
	asked, pending := in.asked, in.pending
	in.asked, in.pending = false, ""
	in.mu.Unlock()
	// What waited for the window is answered once it has come forward, or hasn't.
	settle := func(ok bool) {
		in.mu.Lock()
		in.shown = ok
		in.mu.Unlock()
		in.once.Do(func() { close(in.ready) })
	}
	if asked {
		show(pending, settle)
	} else {
		settle(true)
	}
}

// isClosing says whether the window is going.
func (in *Instance) isClosing() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.closing
}

// closingDown says the window is going: what is asked of it from now on isn't answered, and what
// was waiting for it to be made is told it won't be.
func (in *Instance) closingDown() {
	if in == nil {
		return
	}
	in.mu.Lock()
	in.show, in.closing = nil, true
	in.mu.Unlock()
	in.once.Do(func() { close(in.ready) })
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

// WindowOpen says a window is recorded at path, its process still runs, and something answers on
// its port. It asks nothing of it: a connection that sends no request is closed unanswered. A
// window that crashed leaves its record, and another program may take its port since: the process
// is asked after too, so both must have happened (Opus review).
func WindowOpen(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var rec instance
	if json.Unmarshal(b, &rec) != nil || rec.Addr == "" || rec.Key == "" || !here.Alive(rec.PID) {
		return false
	}
	c, err := net.DialTimeout("tcp", rec.Addr, time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
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
	c.SetDeadline(time.Now().Add(waitForWindow + raiseWait + 3*time.Second))
	req, _ := json.Marshal(request{Key: rec.Key, GitDir: gitDir})
	if _, err := c.Write(append(req, '\n')); err != nil {
		return false
	}
	reply, _ := bufio.NewReader(io.LimitReader(c, 16)).ReadString('\n')
	return reply == "ok\n"
}
