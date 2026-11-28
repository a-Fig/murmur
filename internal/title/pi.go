package title

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync"
	"time"
)

// Pi keeps a session's name (`/name`, `--name`, `pi.setSessionName()`) as a session_info entry,
// {"type":"session_info",...,"name":"Refactor auth module"}, appended to the session file where the
// name was set; the last one in the file is the name, and an empty one clears it (Pi 1.6.0,
// SessionManager.getSessionName, 2026-11-28). A file grows with every turn, so a name set early is
// far from its end, and the whole file is read: once, then only what was appended since (#1146).

// maxPiLine bounds how much of one line of a Pi session file is buffered: a message line holding
// an image can be megabytes, and a session_info line is a name.
const maxPiLine = 1 << 20

// piTailKeep is how many bytes before the read offset are kept to tell an append from a rewrite:
// Pi rewrites a file whole when it migrates it to a newer format.
const piTailKeep = 64

// piFiles keeps where each Pi session file was read to, and the name found up to there.
var piFiles = piReader{m: map[string]*piScan{}}

type piReader struct {
	mu sync.Mutex
	m  map[string]*piScan
}

type piScan struct {
	end  int64  // just past the last whole line read
	tail []byte // the bytes just before end, as they were
	name string
	used time.Time
}

// Pi reads the name of the Pi session whose session file is at path, "" if it has none or it was
// cleared. A file that isn't there yet has no name: Pi writes it at the conversation's first
// message.
func Pi(path string) (string, error) {
	return piFiles.read(path)
}

func (p *piReader) read(path string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		delete(p.m, path)
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	if fi.IsDir() {
		return "", errors.New("the session file is a directory")
	}
	size := fi.Size()
	sc := p.m[path]
	if sc != nil && (size < sc.end || !sameAt(f, sc.end-int64(len(sc.tail)), sc.tail)) {
		sc = nil // rewritten, or replaced by another file: read from the start
	}
	if sc == nil {
		sc = &piScan{}
	}
	if size > sc.end {
		done, name, found, err := piScanFrom(f, sc.end, size)
		if err != nil {
			return "", err
		}
		if found {
			sc.name = name
		}
		if done > 0 {
			sc.end += done
			n := min(int64(piTailKeep), sc.end)
			sc.tail = make([]byte, n)
			if _, err := f.ReadAt(sc.tail, sc.end-n); err != nil {
				return "", err
			}
		}
	}
	sc.used = time.Now()
	p.m[path] = sc
	p.pruneLocked(sc.used)
	return sc.name, nil
}

// pruneLocked forgets files not read for an hour: their sessions have ended or have a status.
func (p *piReader) pruneLocked(now time.Time) {
	for path, sc := range p.m {
		if now.Sub(sc.used) > time.Hour {
			delete(p.m, path)
		}
	}
}

// sameAt says r holds want at off.
func sameAt(r io.ReaderAt, off int64, want []byte) bool {
	if len(want) == 0 {
		return true
	}
	got := make([]byte, len(want))
	n, err := r.ReadAt(got, off)
	return n == len(want) && (err == nil || err == io.EOF) && bytes.Equal(got, want)
}

// piScanFrom reads the whole lines of r from off up to size, and returns how many bytes they took
// and the name of the last session_info among them, if any (found). A line still being written,
// with no newline yet, is left for the next read.
func piScanFrom(r io.ReaderAt, off, size int64) (done int64, name string, found bool, err error) {
	br := bufio.NewReaderSize(io.NewSectionReader(r, off, size-off), 64<<10)
	var line []byte
	var lineLen int64
	long := false
	for {
		frag, rerr := br.ReadSlice('\n')
		lineLen += int64(len(frag))
		if !long {
			if len(line)+len(frag) >= maxPiLine {
				long, line = true, nil
			} else {
				line = append(line, frag...)
			}
		}
		if rerr == bufio.ErrBufferFull {
			continue
		}
		if rerr == io.EOF {
			return done, name, found, nil
		}
		if rerr != nil {
			return 0, "", false, rerr
		}
		done += lineLen
		if !long && bytes.Contains(line, []byte(`"session_info"`)) {
			var e struct {
				Type string `json:"type"`
				Name string `json:"name"`
			}
			if json.Unmarshal(line, &e) == nil && e.Type == "session_info" {
				name, found = strings.TrimSpace(e.Name), true
			}
		}
		line, lineLen, long = line[:0], 0, false
	}
}
