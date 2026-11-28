package title

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func piInfo(name string) string {
	return `{"type":"session_info","id":"i","parentId":"p","timestamp":"2026-11-23T13:30:55.799Z","name":"` + name + `"}`
}

// The name is the last session_info entry's, read once from the start and then only from where the
// last read ended: an append is found, a line still being written waits for its newline, and an
// empty name clears it. A name set early in a large file is found too (#1146).
func TestPiNameIsTheLastSessionInfo(t *testing.T) {
	t.Parallel()
	p := &piReader{m: map[string]*piScan{}}
	path := filepath.Join(t.TempDir(), "s.jsonl")
	turn := `{"type":"message","message":{"content":"` + strings.Repeat("x", 4096) + `"}}`
	big := `{"type":"message","message":{"content":"` + strings.Repeat("y", maxPiLine+10) + `"}}`
	if err := os.WriteFile(path, []byte(lines(`{"type":"session","id":"s1"}`, piInfo("First"), big, strings.Repeat(turn+"\n", 64))), 0o644); err != nil {
		t.Fatal(err)
	}
	read := func(want string) {
		t.Helper()
		if got, err := p.read(path); err != nil || got != want {
			t.Fatalf("got (%q, %v), want %q", got, err, want)
		}
	}
	add := func(s string) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(s); err != nil {
			t.Fatal(err)
		}
	}
	read("First")
	end := p.m[path].end
	add(lines(turn, piInfo("  Second  ")))
	read("Second")
	if p.m[path].end <= end {
		t.Fatalf("read to %d, before %d", p.m[path].end, end)
	}
	half := piInfo("Third")
	add(half[:20])
	read("Second")
	add(half[20:] + "\n")
	read("Third")
	add(lines(`{"type":"session_info","id":"j","parentId":"i"}`))
	read("")
}

// A file Pi rewrites (a migration to a newer format writes it whole) is read again from the start:
// a shorter one, and one of the same length whose bytes before the last read's end changed.
func TestPiFileRewrittenIsReadAgain(t *testing.T) {
	t.Parallel()
	p := &piReader{m: map[string]*piScan{}}
	path := filepath.Join(t.TempDir(), "s.jsonl")
	for _, c := range []struct{ content, want string }{
		{lines(`{"type":"session"}`, piInfo("Alpha"), `{"type":"message"}`), "Alpha"},
		{lines(`{"type":"session"}`, piInfo("Beta")), "Beta"},
		{lines(`{"type":"session"}`, piInfo("Gamm")), "Gamm"}, // as long as the last
	} {
		if err := os.WriteFile(path, []byte(c.content), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, err := p.read(path); err != nil || got != c.want {
			t.Fatalf("got (%q, %v), want %q", got, err, c.want)
		}
	}
}

// A session file that isn't there yet (Pi writes it at the first message) has no name, and a
// directory where it should be is an error, which keeps the title the session had.
func TestPiFailsSoftly(t *testing.T) {
	t.Parallel()
	p := &piReader{m: map[string]*piScan{}}
	dir := t.TempDir()
	if got, err := p.read(filepath.Join(dir, "none.jsonl")); got != "" || err != nil {
		t.Errorf("missing: (%q, %v)", got, err)
	}
	if _, err := p.read(dir); err == nil {
		t.Error("a directory read without error")
	}
}
