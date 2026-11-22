package gitx

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/a-Fig/murmur/internal/proc"
)

// ErrNoReader means this git has no `cat-file --batch-command` (it came in git 2.36, and mur runs on
// 2.31), or the names asked for aren't ones a Reader takes. The caller reads another way.
var ErrNoReader = errors.New("no object reader")

// Object is what a Reader says of one object name.
type Object struct {
	Name    string
	Type    string // "commit", "tree", ...; empty when Missing
	Size    int64
	Missing bool   // nothing here has that name
	Data    []byte // from Contents only
}

// Reader answers what objects a repository holds, and reads them, through one git that stays
// running between questions (`git cat-file --batch-command`), where Git.Run starts a git for each.
// Starting a process is most of what a small git command costs on Windows: 110 to 240 ms on CI's
// runners (#623), for a read that takes git a millisecond.
//
// The git is started by the first question and ends once none has come for the linger given, so a
// burst of reads shares one and an idle daemon keeps none. Kept short, that also bounds how long
// one process has the repository's pack files open: a Git for Windows that can't delete a pack
// another process holds asks its user whether to try again. Git 2.53 for Windows deleted packs
// under a live reader (2026-11-15); older ones weren't tried.
//
// A Reader is safe for concurrent use; questions wait their turn, each for no longer than its own
// context lasts.
type Reader struct {
	git    Git
	linger time.Duration

	// turn holds one token, taken by whoever is using the fields below. It is a channel, not a
	// mutex, so that a question can give up waiting when its context ends: with a git each, a
	// read with 30 s to spare never waited behind one that had a minute.
	turn     chan struct{}
	retiring sync.WaitGroup // gits told to go, not yet gone
	p        *readerProc
	idle     *time.Timer
	asks     int  // how many questions have been put, so an idle timer can tell it is out of date
	none     bool // this git has no --batch-command
	closed   bool
}

type readerProc struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	in     io.WriteCloser
	out    *bufio.Reader
	outEnd *os.File // what out reads from, closed by stop
	errb   *tailBuffer
	done   chan struct{} // closed once the process has been waited for
	werr   error         // from Wait, once done is closed
	asked  bool          // has answered a question
}

// NewReader returns a Reader on g's repository. Close it when done with it.
func (g Git) NewReader(linger time.Duration) *Reader {
	return &Reader{git: g, linger: linger, turn: make(chan struct{}, 1)}
}

// Info says, for each name, whether an object is here, and its type and size, without reading it.
// The answers come in the order asked.
func (r *Reader) Info(ctx context.Context, names []string) ([]Object, error) {
	return r.ask(ctx, "info", names, 0)
}

// Contents is Info, with each object's bytes. An object over max bytes is an error and none are
// returned: ask Info first, and only for those worth reading.
func (r *Reader) Contents(ctx context.Context, names []string, max int64) ([]Object, error) {
	return r.ask(ctx, "contents", names, max)
}

// Close ends the Reader's git, and waits for it to be gone, so that nothing of the repository is
// held open after it returns. A Reader that is closed answers ErrNoReader.
func (r *Reader) Close() {
	r.turn <- struct{}{}
	defer func() { <-r.turn }()
	r.closed = true
	r.end()
	r.retiring.Wait() // and those an idle timer, or a confirming of what was missing, had told to go
}

func (r *Reader) ask(ctx context.Context, verb string, names []string, max int64) ([]Object, error) {
	for _, n := range names {
		if !IsObjectName(n) {
			// These go to git a line each, where anything else could be read as another command.
			return nil, ErrNoReader
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	select {
	case r.turn <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-r.turn }()
	objs, fresh, err := r.askOnce(ctx, verb, names, max)
	if err != nil && (!fresh || errors.Is(err, proc.ErrStartHung)) && ctx.Err() == nil && !errors.Is(err, ErrNoReader) {
		// The git that was kept had gone (killed, or its repository moved), or a new one's start
		// hung (#217): ask a new one, once.
		objs, fresh, err = r.askOnce(ctx, verb, names, max)
	}
	if err != nil {
		return nil, err
	}
	if fresh {
		return objs, nil
	}
	// A git that has run a while could, in principle, not see what arrived since it looked. Git
	// looks again before saying an object is missing, and a test holds it to that, but a wrong
	// "missing" makes a caller build over history that is there, so a new git is asked to confirm.
	var missing []string
	for _, o := range objs {
		if o.Missing {
			missing = append(missing, o.Name)
		}
	}
	if len(missing) == 0 {
		return objs, nil
	}
	r.end()
	again, _, err := r.askOnce(ctx, verb, missing, max)
	if err != nil {
		return nil, err
	}
	for i, j := 0, 0; i < len(objs) && j < len(again); i++ {
		if objs[i].Missing {
			objs[i] = again[j]
			j++
		}
	}
	return objs, nil
}

// askOnce puts one question to the running git, or to a new one, and says which it was.
func (r *Reader) askOnce(ctx context.Context, verb string, names []string, max int64) (objs []Object, fresh bool, err error) {
	if r.closed || r.none {
		return nil, true, ErrNoReader
	}
	if err := ctx.Err(); err != nil {
		return nil, true, err
	}
	if r.idle != nil {
		r.idle.Stop()
	}
	r.asks++
	asks := r.asks
	p := r.p
	fresh = p == nil
	var began time.Time
	if fresh {
		began = time.Now()
		if p, err = r.start(); err != nil {
			return nil, fresh, err
		}
		r.p = p
	}
	// Killing git isn't enough to end a read of its output: in a partial clone, cat-file can start
	// a fetch, whose processes hold the pipe open after it. So the pipe is closed here too.
	stop := context.AfterFunc(ctx, func() {
		p.cancel()
		p.outEnd.Close()
	})
	objs, err = p.ask(verb, names, max)
	if !stop() && err == nil {
		err = ctx.Err() // answered, but the git is being killed
	}
	if fresh && Observe != nil {
		Observe("cat-file", began, time.Since(began))
	}
	if err != nil {
		r.p = nil
		grace := time.Duration(0)
		if ctx.Err() == nil && (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) {
			// A git that has closed its output is on its way out, and says why in how it exits:
			// killed first, one without --batch-command couldn't be told from one that failed.
			grace = endGrace
		}
		p.stop(grace)
		switch {
		case ctx.Err() != nil:
			err = ctx.Err()
		case fresh && noBatchCommand(p):
			r.none, err = true, ErrNoReader
		default:
			err = &Error{Args: p.cmd.Args[1:], Stderr: p.errb.String(), Err: err}
		}
		return nil, fresh, err
	}
	p.asked = true
	r.idle = time.AfterFunc(r.linger, func() {
		r.turn <- struct{}{}
		defer func() { <-r.turn }()
		if r.p == p && r.asks == asks {
			r.end()
		}
	})
	return objs, fresh, nil
}

// readerArgs is the git a Reader keeps. A test changes it to stand in for an older git.
var readerArgs = []string{"cat-file", "--batch-command"}

// noBatchCommand says p ended as a git that doesn't know --batch-command does: with its usage,
// before answering anything.
func noBatchCommand(p *readerProc) bool {
	var exit *exec.ExitError
	return !p.asked && errors.As(p.werr, &exit) && exit.ExitCode() == 129
}

func (r *Reader) start() (*readerProc, error) {
	// The process outlives the question that starts it, so it has a context of its own.
	ctx, cancel := context.WithCancel(context.Background())
	args := append(r.git.dirArgs(), readerArgs...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never"), r.git.Env...)
	hideWindow(cmd)
	p := &readerProc{cmd: cmd, cancel: cancel, errb: &tailBuffer{}, done: make(chan struct{})}
	cmd.Stderr = p.errb
	in, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, &Error{Args: args, Err: err}
	}
	// A pipe of this package's own, not cmd.StdoutPipe: Wait closes that one when the process
	// exits, which would cut short an answer still being read, and Wait is called at once here.
	out, outW, err := os.Pipe()
	if err != nil {
		cancel()
		return nil, &Error{Args: args, Err: err}
	}
	cmd.Stdout = outW
	p.in, p.out, p.outEnd = in, bufio.NewReaderSize(out, 64<<10), out
	err = proc.StartWithin(cmd, startLimit, cancel)
	outW.Close() // the child has its own
	if err != nil {
		cancel()
		out.Close()
		return nil, &Error{Args: args, Err: err}
	}
	go func() {
		p.werr = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

// endGrace is how long a git that has been asked to go, or seems to be going, is given before it
// is killed.
const endGrace = 2 * time.Second

// stop waits up to grace for the process to exit by itself, kills it if it hasn't, and returns
// once it is gone.
func (p *readerProc) stop(grace time.Duration) {
	if grace > 0 {
		t := time.NewTimer(grace)
		select {
		case <-p.done:
		case <-t.C:
		}
		t.Stop()
	}
	p.cancel()
	<-p.done
	p.outEnd.Close()
}

// end stops the git that is kept, if there is one. It asks it to go by closing its input, and
// kills it if that takes long. It doesn't wait for it: Close does, through retiring.
func (r *Reader) end() {
	if r.idle != nil {
		r.idle.Stop()
	}
	p := r.p
	if p == nil {
		return
	}
	r.p = nil
	p.in.Close() // cat-file ends when its input does
	r.retiring.Add(1)
	go func() {
		defer r.retiring.Done()
		p.stop(endGrace)
	}()
}

// ask writes the questions and reads the answers. The writing has a goroutine of its own: git
// answers as it reads, and with enough names both pipes would otherwise fill, each side waiting for
// the other to read.
func (p *readerProc) ask(verb string, names []string, max int64) ([]Object, error) {
	wrote := make(chan error, 1)
	go func() {
		var b strings.Builder
		for _, n := range names {
			b.WriteString(verb + " " + n + "\n")
		}
		_, err := io.WriteString(p.in, b.String())
		wrote <- err
	}()
	objs := make([]Object, 0, len(names))
	for _, n := range names {
		o, err := p.answer(n, verb == "contents", max)
		if err != nil {
			return nil, err
		}
		objs = append(objs, o)
	}
	if err := <-wrote; err != nil {
		return nil, err
	}
	return objs, nil
}

// answer reads git's answer about name: "<name> <type> <size>", then for contents the object and a
// newline; or "<name> missing".
func (p *readerProc) answer(name string, contents bool, max int64) (Object, error) {
	line, err := p.out.ReadString('\n')
	if err != nil {
		return Object{}, fmt.Errorf("reading git's answer: %w", err)
	}
	f := strings.Fields(line)
	if len(f) == 2 && f[0] == name && f[1] == "missing" {
		return Object{Name: name, Missing: true}, nil
	}
	if len(f) != 3 || f[0] != name {
		return Object{}, fmt.Errorf("git answered %q for %s", strings.TrimSpace(line), name)
	}
	size, err := strconv.ParseInt(f[2], 10, 64)
	if err != nil || size < 0 {
		return Object{}, fmt.Errorf("git answered %q for %s", strings.TrimSpace(line), name)
	}
	o := Object{Name: name, Type: f[1], Size: size}
	if !contents {
		return o, nil
	}
	if size > max {
		return Object{}, fmt.Errorf("%s is %d bytes, over %d", name, size, max)
	}
	o.Data = make([]byte, size+1)
	if _, err := io.ReadFull(p.out, o.Data); err != nil {
		return Object{}, fmt.Errorf("reading %s: %w", name, err)
	}
	if o.Data[size] != '\n' {
		return Object{}, fmt.Errorf("reading %s: no newline after it", name)
	}
	o.Data = o.Data[:size]
	return o, nil
}

// IsObjectName says s is a full object name as git prints one: 40 hex digits, or 64 with SHA-256.
func IsObjectName(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// tailBuffer keeps the last of what is written to it: a git that runs for long could otherwise
// fill memory with warnings.
type tailBuffer struct {
	mu sync.Mutex
	b  []byte
}

const tailKeep = 8 << 10

func (t *tailBuffer) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, b...)
	if len(t.b) > tailKeep {
		t.b = append([]byte(nil), t.b[len(t.b)-tailKeep:]...)
	}
	return len(b), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}
