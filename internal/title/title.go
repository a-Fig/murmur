// Package title reads the title of the conversation a session is in, from the state of the client
// that runs it, for `who` to show next to the session's name when the agent hasn't set a status.
//
// Each client keeps its titles in its own files, which belong to it, are versioned by it, and are
// open for writing in it while Murmur reads them (seen 2026-10-30: T3 0.1.x, Codex 0.164.0, Claude
// Code 2.1.312). So every reader only reads, waits no longer than a moment, and says what went
// wrong as an error, which the caller shrugs off: a missing file, table or column, a lock, and a
// file written in a shape this code hasn't met are all errors, never a crash. A reader that finds
// the conversation but no title returns "" and no error: it has none, or the user cleared it.
//
// Where a title lives, and what is not it:
//
//   - T3: orchestration_v2_projection_threads.title in T3's state database (t3db.TitleOf).
//   - Codex outside T3: the thread's explicit name, threads.name in ~/.codex/state_N.sqlite or
//     thread_name in ~/.codex/session_index.jsonl. Codex's own threads.title and threads.preview
//     hold the opening message, not a name, and are never read.
//   - Claude Code outside T3: the session's transcript, ~/.claude/projects/<project>/<session>.jsonl,
//     holds lines {"type":"custom-title","customTitle":...} for a name the user gave it and
//     {"type":"ai-title","aiTitle":...} for the one Claude Code made up. Claude Code rewrites them
//     near the end of the file, so the last 128 KB is enough; a session with neither has no title,
//     and the first prompt is not used in its place.
package title

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/scrub"
	"github.com/a-Fig/murmur/internal/t3db"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// Max is the longest title, the same as a status line.
const Max = 120

// Total bounds one read of a title; a variable, so tests needn't wait it out.
var Total = 3 * time.Second

// tailBytes is how much of the end of a Claude Code transcript is searched for its title.
const tailBytes = 128 << 10

// maxIndexLine bounds how many bytes of a single line codexIndex buffers before discarding the
// rest of that line, so a corrupt or newline-less session_index.jsonl cannot spike memory.
const maxIndexLine = 1 << 20

// Clean makes a title fit for presence: on one line, at most Max runes, and "" if it holds
// something scrub would stop in a status (a title is the user's words or a model's, and goes to
// everyone who reads the bus). The whole title is checked before it is cut: a token that runs
// past the cut would otherwise be left too short for scrub to know, and go out in part.
func Clean(s string) string {
	if scrub.Check(s) != nil {
		return ""
	}
	s = bus.OneLine(s, Max)
	if scrub.Check(s) != nil {
		return ""
	}
	return s
}

// DefaultCodexDir is where Codex keeps its state when CODEX_HOME isn't set: ~/.codex. It doesn't
// read CODEX_HOME: the caller is the daemon, which one session started for all of them, so its own
// CODEX_HOME is that session's home and says nothing about the others (#88).
func DefaultCodexDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(h, ".codex")
}

// Codex reads the explicit name of Codex thread id from the Codex state directory dir: the name in
// its state database, else in its session index.
func Codex(ctx context.Context, dir, id string) (string, error) {
	if dir == "" || id == "" {
		return "", errors.New("no Codex directory or thread to look up")
	}
	name, dbErr := codexDB(ctx, dir, id)
	if name != "" {
		return name, nil
	}
	name, idxErr := codexIndex(filepath.Join(dir, "session_index.jsonl"), id)
	switch {
	case name != "":
		return name, nil
	case idxErr != nil && !errors.Is(idxErr, os.ErrNotExist):
		return "", errors.Join(dbErr, idxErr) // an index that can't be read doesn't say there's no name
	case dbErr != nil:
		return "", dbErr // the index has no name, but the database, which may, could not be read
	}
	return "", nil
}

// codexDB reads threads.name from the newest state_N.sqlite in dir.
func codexDB(ctx context.Context, dir, id string) (string, error) {
	path := latestState(dir)
	if path == "" {
		return "", errors.New("Codex's state database isn't there")
	}
	ctx, cancel := context.WithTimeout(ctx, Total)
	defer cancel()
	db, err := t3db.OpenReadOnly(path)
	if err != nil {
		return "", err
	}
	defer db.Close()
	var name sql.NullString
	err = db.QueryRowContext(ctx, `SELECT name FROM threads WHERE id = ?`, id).Scan(&name)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("reading Codex's database: %w", err)
	}
	return name.String, nil
}

var stateFile = regexp.MustCompile(`^state_(\d+)\.sqlite$`)

// latestState is the state database of the highest version in dir ("" if none): Codex numbers
// them, and a newer one replaces the file of an older.
func latestState(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	best, bestN := "", -1
	for _, e := range entries {
		m := stateFile.FindStringSubmatch(e.Name())
		if m == nil || e.IsDir() {
			continue
		}
		if n, err := strconv.Atoi(m[1]); err == nil && n > bestN {
			best, bestN = e.Name(), n
		}
	}
	if best == "" {
		return ""
	}
	return filepath.Join(dir, best)
}

// codexIndex finds the last name given to thread id in Codex's session index, a file of lines
// {"id":...,"thread_name":...,"updated_at":...}. Oversized lines (over maxIndexLine bytes) are
// streamed past and skipped without buffering the whole line in memory.
func codexIndex(path, id string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil {
		return "", err
	} else if fi.IsDir() {
		return "", errors.New("the session index is a directory")
	}
	var name string
	r := bufio.NewReaderSize(f, 64<<10)
	needle := []byte(id)
	for {
		line, tooLong, err := readLineBounded(r, maxIndexLine)
		if !tooLong && len(line) > 0 && bytes.Contains(line, needle) {
			var e struct {
				ID   string `json:"id"`
				Name string `json:"thread_name"`
			}
			if json.Unmarshal(line, &e) == nil && e.ID == id {
				name = e.Name
			}
		}
		if err != nil {
			if err == io.EOF {
				return name, nil
			}
			return "", err
		}
	}
}

// readLineBounded reads one '\n'-terminated line (or the final line before io.EOF) from r,
// capping buffered memory strictly below limit bytes (matching the original len(line) < 1<<20
// cutoff). Lines of limit bytes or longer are drained through '\n' or io.EOF and reported with
// tooLong=true and line=nil.
func readLineBounded(r *bufio.Reader, limit int) (line []byte, tooLong bool, err error) {
	var buf []byte
	for {
		frag, rerr := r.ReadSlice('\n')
		if rerr == bufio.ErrBufferFull {
			if !tooLong {
				if len(buf)+len(frag) >= limit {
					tooLong, buf = true, nil
				} else {
					buf = append(buf, frag...)
				}
			}
			continue
		}
		if tooLong {
			return nil, true, rerr
		}
		if len(buf) == 0 {
			if len(frag) >= limit {
				return nil, true, rerr
			}
			return frag, false, rerr
		}
		if len(buf)+len(frag) >= limit {
			return nil, true, rerr
		}
		return append(buf, frag...), false, rerr
	}
}

// Claude reads the title of the Claude Code session whose transcript is at path: the name the user
// gave it if they did, else the one Claude Code made up, the last of either in the file.
func Claude(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	if fi.IsDir() {
		return "", errors.New("the transcript is a directory")
	}
	return claudeTail(f, fi.Size())
}

// claudeTail scans the last tailBytes of r (of observed size) for Claude Code's custom/ai title
// lines. Short reads (e.g. concurrent truncation) are sliced to the bytes actually read.
func claudeTail(r io.ReaderAt, size int64) (string, error) {
	if size <= 0 {
		return "", nil
	}
	off := max(size-tailBytes, 0)
	buf := make([]byte, size-off)
	n, err := r.ReadAt(buf, off)
	if err != nil && err != io.EOF {
		return "", err
	}
	buf = buf[:n]
	if off > 0 { // the window starts inside a line
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		} else {
			buf = nil
		}
	}
	var custom, auto string
	for _, line := range bytes.Split(buf, []byte("\n")) {
		var e struct {
			Type        string `json:"type"`
			CustomTitle string `json:"customTitle"`
			AITitle     string `json:"aiTitle"`
		}
		if !bytes.Contains(line, []byte("-title\"")) || json.Unmarshal(line, &e) != nil {
			continue
		}
		switch e.Type {
		case "custom-title":
			custom = e.CustomTitle
		case "ai-title":
			auto = e.AITitle
		}
	}
	if custom != "" {
		return custom, nil
	}
	return auto, nil
}
