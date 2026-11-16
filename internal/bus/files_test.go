package bus_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/gitx"
)

// A file's name becomes a name on the receiver's disk, inside a directory Murmur chose, so no name
// a sender writes can be a path, hidden, or one Windows keeps for a device.
func TestFileName(t *testing.T) {
	for in, want := range map[string]string{
		"shot.png":                        "shot.png",
		"../../etc/passwd":                "passwd",
		`C:\Users\ada\notes.txt`:          "notes.txt",
		"a b\tc.log":                      "a_b_c.log",
		".env":                            "env",
		"...":                             "file",
		"":                                "file",
		"con.txt":                         "_con.txt",
		"COM1":                            "_COM1",
		"comet.txt":                       "comet.txt",
		"trailing.":                       "trailing",
		"résumé.pdf":                      "résumé.pdf",
		"x\x00y\x1b[31m.txt":              "x_y__31m.txt",
		strings.Repeat("a", 200):          strings.Repeat("a", 60),
		strings.Repeat("b", 200) + ".png": strings.Repeat("b", 56) + ".png",
		"con." + strings.Repeat("c", 70):  "_con." + strings.Repeat("c", 55),
		strings.Repeat("d", 59) + ".":     strings.Repeat("d", 59),
		strings.Repeat("e", 59) + "..x":   strings.Repeat("e", 58) + ".x",
	} {
		got := bus.FileName(in)
		if got != want {
			t.Errorf("FileName(%q) = %q, want %q", in, got, want)
		}
		if again := bus.FileName(got); again != got {
			t.Errorf("FileName of %q, a name it made, is %q", got, again)
		}
	}
}

// A file's blob opens only as the file its event describes, at its place in its message.
func TestSealedFileOpensOnlyAsItself(t *testing.T) {
	plain := []byte("line one\r\nline two\x00\x1a\xff")
	blob, f, err := bus.SealFile(plain, "0123456789abcdef", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := bus.OpenFile(blob, f, "0123456789abcdef", 1); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("opened %q, %v", got, err)
	}
	if bytes.Contains(blob, []byte("line one")) {
		t.Fatal("the blob holds the file in the clear")
	}
	flipped := bytes.Clone(blob)
	flipped[len(flipped)-1] ^= 1
	smaller := f
	smaller.Size--
	other := f
	other.Sum = strings.Repeat("0", 64)
	for what, open := range map[string]func() ([]byte, error){
		"at another place":     func() ([]byte, error) { return bus.OpenFile(blob, f, "0123456789abcdef", 0) },
		"in another message":   func() ([]byte, error) { return bus.OpenFile(blob, f, "fedcba9876543210", 1) },
		"with a byte changed":  func() ([]byte, error) { return bus.OpenFile(flipped, f, "0123456789abcdef", 1) },
		"cut short":            func() ([]byte, error) { return bus.OpenFile(blob[:20], f, "0123456789abcdef", 1) },
		"said to be smaller":   func() ([]byte, error) { return bus.OpenFile(blob, smaller, "0123456789abcdef", 1) },
		"said to be different": func() ([]byte, error) { return bus.OpenFile(blob, other, "0123456789abcdef", 1) },
	} {
		if got, err := open(); err == nil {
			t.Errorf("a blob %s opened: %q", what, got)
		}
	}
}

// What a reader takes of a message's files is held to what Murmur writes. A file it can't take
// keeps its place, since blobs are named by place.
func TestCleanFiles(t *testing.T) {
	_, good, err := bus.SealFile([]byte("x"), "0123456789abcdef", 0)
	if err != nil {
		t.Fatal(err)
	}
	named := func(name string) bus.File { f := good; f.Name = name; return f }
	huge := named("huge.bin")
	huge.Size = bus.MaxFile + 1
	e := bus.Event{Type: bus.Message, FilesAt: bus.NewFilesAt(), Files: []bus.File{
		named("a.txt"), named("../A.TXT"), {Name: "nokey.txt", Size: 1}, huge, named("a.txt"),
	}}
	for range bus.MaxFiles {
		e.Files = append(e.Files, named("more"))
	}
	e.Clean()
	var names []string
	for _, f := range e.Files {
		names = append(names, f.Name)
	}
	if got, want := strings.Join(names, " "), "a.txt 2-A.TXT nokey.txt huge.bin 3-a.txt more 2-more 3-more"; got != want {
		t.Errorf("names: %s\nwant:  %s", got, want)
	}
	if e.Files[0].Readable() != true || e.Files[2].Readable() || e.Files[3].Readable() {
		t.Errorf("readable: %+v", e.Files[:4])
	}
	if e.Files[3].Size != 0 {
		t.Errorf("a file over the limit kept its size: %+v", e.Files[3])
	}
	if e.FilesAt == "" {
		t.Error("a files ref's name didn't survive")
	}
	started := bus.Event{Type: bus.SessionStarted, FilesAt: bus.NewFilesAt(), Files: []bus.File{named("a.txt")}}
	started.Clean()
	if len(started.Files) != 0 || started.FilesAt != "" {
		t.Errorf("an event that isn't a message kept files: %+v", started)
	}
	// A ref's name is what NewFilesAt makes, so no event can point a reader at another ref.
	for _, at := range []string{"", "mesh/some-device", "../heads/main", strings.Repeat("g", 32), strings.Repeat("A", 32), strings.Repeat("a", 31)} {
		forged := bus.Event{Type: bus.Message, FilesAt: at, Files: []bus.File{named("a.txt")}}
		forged.Clean()
		if forged.FilesAt != "" {
			t.Errorf("a files ref called %q was kept", at)
		}
	}
}

// A pack's length hides how many files it holds and their sizes: it is one of a few lengths near
// what it holds, never under 4 KB, and each file is found in it by the sizes the event gives.
func TestPackFiles(t *testing.T) {
	var sealed [][]byte
	var files []bus.File
	for i, plain := range [][]byte{[]byte("one"), bytes.Repeat([]byte("two"), 3000), {}} {
		blob, f, err := bus.SealFile(plain, "0123456789abcdef", i)
		if err != nil {
			t.Fatal(err)
		}
		sealed, files = append(sealed, blob), append(files, f)
	}
	pack, err := bus.PackFiles(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if len(pack) != 9216 { // 3+9000+0 and 28 each is 9087, which rounds up to a multiple of 512
		t.Errorf("a pack of %d bytes", len(pack))
	}
	for i, want := range sealed {
		got, err := bus.PackedFile(pack, files, i)
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("file %d: %d bytes, %v; want %d", i, len(got), err, len(want))
		}
	}
	if _, err := bus.PackedFile(pack, files, 3); !errors.Is(err, bus.ErrNoFile) {
		t.Errorf("a place past the last: %v", err)
	}
	// A file after one whose size isn't known can't be found, and one that would end past the
	// pack isn't there.
	files[0] = bus.File{Name: "unreadable"}
	if _, err := bus.PackedFile(pack, files, 1); !errors.Is(err, bus.ErrNoFile) {
		t.Errorf("a file after an unreadable one: %v", err)
	}
	if _, err := bus.PackedFile(pack[:100], files[1:], 0); !errors.Is(err, bus.ErrNoFile) {
		t.Errorf("a file longer than its pack: %v", err)
	}
	for _, n := range []int{0, 1, 4096} {
		if pack, _ := bus.PackFiles([][]byte{make([]byte, n)}); len(pack) != 4096 {
			t.Errorf("%d bytes made a pack of %d", n, len(pack))
		}
	}
	for _, n := range []int{4097, 70_000, 1 << 20, 3<<20 + 17, 20 << 20} {
		pack, _ := bus.PackFiles([][]byte{make([]byte, n)})
		if len(pack) < n || len(pack) > n+n/8 {
			t.Errorf("%d bytes made a pack of %d", n, len(pack))
		}
	}
	if a, _ := bus.PackFiles([][]byte{make([]byte, 70_000)}); true {
		b, _ := bus.PackFiles([][]byte{make([]byte, 30_000), make([]byte, 40_100)})
		if len(a) != len(b) {
			t.Errorf("one file and two of about the same size made packs of %d and %d", len(a), len(b))
		}
		if bytes.Equal(a[70_000:], make([]byte, len(a)-70_000)) {
			t.Error("the filling is zeros, which would show where the files end")
		}
	}
}

// A pack goes through git as it is, whatever its bytes. One over the limit it is read with isn't
// read, and the commit says nothing of who wrote it or when.
func TestFilesCommit(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "--bare", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	ctx := context.Background()
	r := &bus.Repo{Git: gitx.Git{Dir: dir}, Name: "a-device", Email: "a-device@example.invalid"}
	pack := []byte("text\r\nwith both\nline ends\r\n\x00\x1a")
	for range 2 {
		for b := range 256 {
			pack = append(pack, byte(b))
		}
	}
	sha, err := r.NewFilesCommit(ctx, pack)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := r.ReadPack(ctx, sha, int64(len(pack))); err != nil || !bytes.Equal(got, pack) {
		t.Errorf("the pack: %d bytes, %v; want %d", len(got), err, len(pack))
	}
	if _, err := r.ReadPack(ctx, sha, int64(len(pack))-1); !errors.Is(err, bus.ErrTooLarge) {
		t.Errorf("a pack over the limit: %v", err)
	}
	empty, err := r.NewCommit(ctx, "", "no files")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadPack(ctx, empty, 1000); !errors.Is(err, bus.ErrNoFile) {
		t.Errorf("a commit with no pack: %v", err)
	}
	again, err := r.NewFilesCommit(ctx, pack)
	if err != nil || again != sha {
		t.Errorf("the same pack made another commit, %s and %s (%v): something in it changes", sha, again, err)
	}
	out, err := exec.Command("git", "--git-dir="+dir, "cat-file", "commit", sha).Output()
	if err != nil || strings.Contains(string(out), "a-device") {
		t.Errorf("the commit names its writer: %v\n%s", err, out)
	}
	if got, err := exec.Command("git", "--git-dir="+dir, "for-each-ref").Output(); err != nil || len(got) != 0 {
		t.Errorf("writing files left refs: %q, %v", got, err)
	}
}

// A files ref is fetched when its pack is within the limit, and the fetch is refused, leaving
// nothing behind, when it holds more (#707).
func TestFetchFilesCapsTheDownload(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	there := &bus.Repo{Git: gitx.Git{Dir: remote}}
	pack := make([]byte, 2<<20)
	rand.Read(pack) // sealed files don't compress
	sha, err := there.NewFilesCommit(ctx, pack)
	if err != nil {
		t.Fatal(err)
	}
	ref := bus.FilesRef("0123456789abcdef")
	if out, err := exec.Command("git", "--git-dir="+remote, "update-ref", ref, sha).CombinedOutput(); err != nil {
		t.Fatalf("update-ref: %v: %s", err, out)
	}

	local := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "--bare", local).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	scratch := t.TempDir()
	here := (&bus.Repo{Git: gitx.Git{Dir: local}, Remote: remote}).Scratch(scratch)
	if _, err := here.FetchFiles(ctx, ref, 64<<10); !errors.Is(err, bus.ErrTooLarge) {
		t.Fatalf("a ref over the limit: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(scratch, "pack", "*")); len(left) > 0 {
		t.Fatalf("a refused fetch left %q", left)
	}
	got, err := here.FetchFiles(ctx, ref, int64(len(pack)))
	if err != nil || got != sha {
		t.Fatalf("a ref within the limit: %s, %v", got, err)
	}
	if b, err := here.ReadPack(ctx, sha, int64(len(pack))); err != nil || !bytes.Equal(b, pack) {
		t.Fatalf("the fetched pack: %d bytes, %v", len(b), err)
	}
}

// A files ref over the limit on a remote that sends slowly, as one across a network does, is
// stopped while it arrives, rather than downloaded whole and refused after (#707).
func TestFetchFilesStopsASlowDownload(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh to slow the remote with")
	}
	ctx := context.Background()
	remote := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	there := &bus.Repo{Git: gitx.Git{Dir: remote}}
	pack := make([]byte, 16<<20)
	rand.Read(pack)
	sha, err := there.NewFilesCommit(ctx, pack)
	if err != nil {
		t.Fatal(err)
	}
	ref := bus.FilesRef("0123456789abcdef")
	if out, err := exec.Command("git", "--git-dir="+remote, "update-ref", ref, sha).CombinedOutput(); err != nil {
		t.Fatalf("update-ref: %v: %s", err, out)
	}
	// 256 KiB every 100 ms: the whole ref would take over six seconds.
	slow := filepath.Join(t.TempDir(), "slow.sh")
	os.WriteFile(slow, []byte(slowUploadPack), 0o755)
	local := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "--bare", local}, {"--git-dir=" + local, "remote", "add", "slow", remote}, {"--git-dir=" + local, "config", "remote.slow.uploadpack", "sh " + filepath.ToSlash(slow)}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	scratch := t.TempDir()
	here := (&bus.Repo{Git: gitx.Git{Dir: local}, Remote: "slow"}).Scratch(scratch)
	start := time.Now()
	if _, err := here.FetchFiles(ctx, ref, 64<<10); !errors.Is(err, bus.ErrTooLarge) {
		t.Fatalf("a slow ref over the limit: %v", err)
	}
	took := time.Since(start)
	t.Logf("stopped after %v", took)
	if took > 4*time.Second {
		t.Fatalf("took %v: the fetch wasn't stopped as it arrived", took)
	}
}

// slowUploadPack is git-upload-pack with what it sends passed on 256 KiB every 100 ms at most.
const slowUploadPack = `chunk=$(mktemp)
git-upload-pack "$@" | while dd bs=262144 count=1 of="$chunk" 2>/dev/null && [ -s "$chunk" ]; do
	cat "$chunk" || break # the fetch has gone, as a network's sender finds
	sleep 0.1
done
rm -f "$chunk"
`
