package bus

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"math/bits"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/a-Fig/murmur/internal/brand"
)

// File is one file a message carries (#570, docs/design/files.md). The file itself isn't in the
// event: it is encrypted with Key, in the blob on the message's files ref (Event.FilesAt), which a
// device fetches only when the message is for it. The event is encrypted for the team, so whoever
// can read the message can read its files, and nobody else.
type File struct {
	Name string `json:"name"`
	Size int64  `json:"size"` // of the file, not of its blob
	Sum  string `json:"sum"`  // SHA-256 of the file, in hex
	Key  string `json:"key"`  // the AES-256 key its blob is encrypted with, in base64
}

// What a message's files may come to. Every recipient's device fetches them, and the remote keeps
// them until the sender deletes the ref.
const (
	MaxFiles      = 8
	MaxFile       = 10 << 20
	MaxFilesTotal = 20 << 20
	// maxFileName is the longest name FileName gives, in runes: under the 255 bytes a file system
	// allows a name, whatever the letters.
	maxFileName = 60
	// fileOverhead is how much longer a file's blob is than the file: the nonce and GCM's tag.
	fileOverhead = 12 + 16
)

// FilesRef is the ref that holds a message's files: one commit, whose tree has one blob, the pack
// (PackFiles). at is the event's FilesAt, random, so the ref says neither which message it is for
// nor which device sent it.
func FilesRef(at string) string { return brand.FilesPrefix + at }

// NewFilesAt makes the name of a message's files ref.
func NewFilesAt() string { return RandHex(32) }

// validFilesAt says s is a name NewFilesAt makes.
func validFilesAt(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// FileName makes of what a sender called a file a name a receiver can save it under, on any
// system, inside the directory Murmur chose: letters, digits, '.', '_' and '-' only, so never a
// path, a hidden file, or a name Windows keeps for a device. What a name loses is replaced by '_'.
func FileName(s string) string {
	if i := strings.LastIndexAny(s, `/\`); i >= 0 {
		s = s[i+1:]
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '.' || r == '_' || r == '-' || unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	// A trailing dot goes too: Windows drops one, and two names become one.
	name := strings.Trim(b.String(), ".")
	if name == "" {
		return "file"
	}
	stem, _, _ := strings.Cut(name, ".")
	switch up := strings.ToUpper(stem); {
	case up == "CON", up == "PRN", up == "AUX", up == "NUL",
		len(up) == 4 && (strings.HasPrefix(up, "COM") || strings.HasPrefix(up, "LPT")) && up[3] >= '0' && up[3] <= '9':
		name = "_" + name
	}
	// Cut last, so that what comes out is within the length whatever was added, and a second pass
	// leaves it as it is.
	if r := []rune(name); len(r) > maxFileName {
		// Cut the stem, so "a-long-name.png" stays a .png.
		ext := ""
		if i := strings.LastIndex(name, "."); i > 0 && len(name)-i <= 12 {
			ext = name[i:]
		}
		stem := []rune(strings.TrimSuffix(name, ext))
		name = strings.TrimRight(string(stem[:maxFileName-len([]rune(ext))])+ext, ".")
	}
	return name
}

// validFile says f is what Murmur writes of a file: a size within the limit, a SHA-256 and a key.
func validFile(f File) bool {
	if f.Size < 0 || f.Size > MaxFile || len(f.Sum) != 2*sha256.Size {
		return false
	}
	if _, err := hex.DecodeString(f.Sum); err != nil {
		return false
	}
	key, err := b64.DecodeString(f.Key)
	return err == nil && len(key) == 32
}

// cleanFiles holds a message's files to what Murmur writes. A file that isn't keeps its place, with
// nothing but a name: its blob is named by that place, and a reader shows it as one that couldn't be
// read. Names are made safe and distinct, since they become file names on the receiver's disk. Files
// past MaxFiles are dropped, and any other event carries none.
func cleanFiles(typ string, in []File) []File {
	if typ != Message {
		return nil
	}
	return CleanFiles(in)
}

// CleanFiles is cleanFiles for a message's files: a sender runs it too, so the names it writes are
// the names every receiver saves the files under.
func CleanFiles(in []File) []File {
	if len(in) == 0 {
		return nil
	}
	if len(in) > MaxFiles {
		in = in[:MaxFiles]
	}
	out := make([]File, 0, len(in))
	seen := map[string]bool{}
	for _, f := range in {
		if !validFile(f) {
			f = File{Name: f.Name}
		}
		name := FileName(f.Name)
		for n := 2; seen[strings.ToLower(name)]; n++ {
			// Through FileName again, which may cut it: a second clean then gives the same name.
			name = FileName(strconv.Itoa(n) + "-" + FileName(f.Name))
		}
		seen[strings.ToLower(name)] = true
		f.Name = name
		out = append(out, f)
	}
	return out
}

// Readable says the file's blob can be opened: cleanFiles left it its size, sum and key.
func (f File) Readable() bool { return f.Key != "" }

func fileAAD(at string, index int) []byte {
	return []byte("file\x00" + at + "\x00" + strconv.Itoa(index))
}

// SealFile encrypts a file as the index'th on the files ref called at, and returns the blob to
// store and the size, sum and key the event says of it. The blob is bound to the ref and the
// place: moved to another, it doesn't open. It is bound to the ref and not to the message, so
// that several messages can one day share an upload.
func SealFile(plain []byte, at string, index int) (blob []byte, f File, err error) {
	key := make([]byte, 32)
	nonce := make([]byte, 12)
	if _, err := rand.Read(key); err != nil {
		return nil, File{}, err
	}
	if _, err := rand.Read(nonce); err != nil {
		return nil, File{}, err
	}
	aead, err := gcm(key)
	if err != nil {
		return nil, File{}, err
	}
	sum := sha256.Sum256(plain)
	blob = aead.Seal(nonce, nonce, plain, fileAAD(at, index))
	return blob, File{Size: int64(len(plain)), Sum: hex.EncodeToString(sum[:]), Key: b64.EncodeToString(key)}, nil
}

// errBadFile is a file's blob that isn't the file its event describes.
var errBadFile = errors.New("a file that doesn't open")

// OpenFile decrypts the blob of the index'th file on the files ref called at, and holds it to the
// size and sum the event gives.
func OpenFile(blob []byte, f File, at string, index int) ([]byte, error) {
	key, err := b64.DecodeString(f.Key)
	if err != nil || len(key) != 32 || len(blob) < fileOverhead {
		return nil, errBadFile
	}
	aead, err := gcm(key)
	if err != nil {
		return nil, errBadFile
	}
	plain, err := aead.Open(nil, blob[:12], blob[12:], fileAAD(at, index))
	if err != nil {
		return nil, errBadFile
	}
	sum := sha256.Sum256(plain)
	want, _ := hex.DecodeString(f.Sum)
	if int64(len(plain)) != f.Size || subtle.ConstantTimeCompare(sum[:], want) != 1 {
		return nil, errBadFile
	}
	return plain, nil
}

// Scratch returns r with dir as the place git writes objects, and the clone's own objects still
// readable. What a files commit is made of, and what a fetch of one brings, goes there, and the
// caller deletes dir after: nothing names those objects once the push or the read is done, and in
// the clone they would stay until a garbage collection that may be months away, up to 23 MB a
// message. It is r itself if the clone's objects aren't where a git directory keeps them, or can't
// be named as an alternate.
func (r *Repo) Scratch(dir string) *Repo {
	objects := filepath.Join(r.Git.Dir, "objects")
	// Git reads the alternates as a list, so a clone whose path holds the list's separator (a
	// colon, outside Windows) can't be named there.
	if info, err := os.Stat(objects); err != nil || !info.IsDir() || strings.ContainsRune(objects, os.PathListSeparator) {
		return r
	}
	c := &Repo{Git: r.Git, Remote: r.Remote, Name: r.Name, Email: r.Email, Page: r.Page}
	c.Git.Env = append(slices.Clone(r.Git.Env), "GIT_OBJECT_DIRECTORY="+dir, "GIT_ALTERNATE_OBJECT_DIRECTORIES="+objects)
	return c
}

// minPack is the smallest pack: files under it all look alike.
const minPack = 4 << 10

// MaxPack is the largest pack a message within the limits makes: PackFiles adds under an eighth.
const MaxPack = (MaxFilesTotal + MaxFiles*fileOverhead) / 8 * 9

// packLen is how long a pack holding n bytes is made: n rounded up so that only the first few bits
// of its length are set (Padmé: at most 12% longer, less as it grows), and no shorter than minPack.
// A length then says about how large the files are, and not their size.
func packLen(n int) int {
	if n <= minPack {
		return minPack
	}
	e := bits.Len(uint(n)) - 1 // floor(log2 n)
	z := e - bits.Len(uint(e)) // the low bits a length may not use
	mask := 1<<z - 1
	return (n + mask) &^ mask
}

// PackFiles joins a message's sealed files, in order, into the one blob its files ref holds, and
// fills it out with random bytes to a length that hides how many files there are and their sizes
// (packLen). A reader finds each file by the sizes its event gives.
func PackFiles(sealed [][]byte) ([]byte, error) {
	n := 0
	for _, b := range sealed {
		n += len(b)
	}
	pack := make([]byte, 0, packLen(n))
	for _, b := range sealed {
		pack = append(pack, b...)
	}
	pack = pack[:cap(pack)]
	if _, err := rand.Read(pack[n:]); err != nil {
		return nil, err
	}
	return pack, nil
}

// ErrNoFile is a file that isn't on its message's files ref.
var ErrNoFile = errors.New("not on the message's files ref")

// PackedFile returns the index'th sealed file of a pack, by the sizes files gives. A file after
// one whose size isn't known (see cleanFiles) can't be found.
func PackedFile(pack []byte, files []File, index int) ([]byte, error) {
	if index < 0 || index >= len(files) {
		return nil, ErrNoFile
	}
	off := int64(0)
	for _, f := range files[:index] {
		if !f.Readable() {
			return nil, ErrNoFile
		}
		off += f.Size + fileOverhead
	}
	end := off + files[index].Size + fileOverhead
	if !files[index].Readable() || end > int64(len(pack)) {
		return nil, ErrNoFile
	}
	return pack[off:end], nil
}

// NewFilesCommit writes a pack as a commit with no parent, whose tree holds it as "0", and returns
// the commit. Like NewCommit it writes objects and nothing else, with plumbing that never reads the
// index or the working tree. The commit says nothing of its writer or of when: one name and one
// date for every device.
func (r *Repo) NewFilesCommit(ctx context.Context, pack []byte) (string, error) {
	out, err := r.Git.Run(ctx, string(pack), "hash-object", "-t", "blob", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	out, err = r.Git.Run(ctx, "100644 blob "+strings.TrimSpace(out)+"\t0\n", "mktree")
	if err != nil {
		return "", err
	}
	// In the environment, which goes before any configuration: a name or a date set there, or as
	// author.name in the user's config, would otherwise be the commit's.
	who, mail := brand.Name, "files@"+brand.Name+".invalid"
	g := r.Git
	g.Env = append(slices.Clone(g.Env), "GIT_AUTHOR_DATE="+filesDate, "GIT_COMMITTER_DATE="+filesDate,
		"GIT_AUTHOR_NAME="+who, "GIT_COMMITTER_NAME="+who, "GIT_AUTHOR_EMAIL="+mail, "GIT_COMMITTER_EMAIL="+mail)
	out, err = g.Run(ctx, "files\n", "commit-tree", "--no-gpg-sign", strings.TrimSpace(out), "-F", "-")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// filesDate is the date on every files commit.
const filesDate = "2000-01-01T00:00:00Z"

// ReadPack returns the pack of the files commit at rev, which was fetched. One over max bytes
// isn't read: git stores a blob compressed, so a small fetch can hold one of any size (see
// MaxRecord).
func (r *Repo) ReadPack(ctx context.Context, rev string, max int64) ([]byte, error) {
	name := rev + ":0"
	out, err := r.Git.Run(ctx, name+"\n", "cat-file", "--batch-check=%(objecttype) %(objectsize)")
	if err != nil {
		return nil, err
	}
	f := strings.Fields(out)
	if len(f) != 2 || f[0] != "blob" {
		return nil, ErrNoFile
	}
	size, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil {
		return nil, ErrNoFile
	}
	if size > max {
		return nil, fmt.Errorf("%w: files of %d bytes, over %d", ErrTooLarge, size, max)
	}
	out, err = r.Git.Run(ctx, "", "cat-file", "blob", name)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// FetchFiles fetches the commit a files ref holds on the remote and returns it. Nothing local
// names it after, whatever the clone's own refspecs say (--refmap=): the objects are read once.
// ErrNoFile if the ref isn't on the remote. The fetch asks for the ref by name, as not every server
// gives an object asked for by its ID; a ref that moves between the listing and the fetch reads as
// files that aren't there.
//
// The fetch is stopped once what it has written passes what a pack of max bytes takes on the wire,
// and ErrTooLarge returned (#707): anyone who can push could point a files ref at a gigabyte, and
// every device it is for would download it, and only then find it too large. Git has no such limit
// for a fetch (receive.maxInputSize is a push's), so the pack being written is watched: it is kept
// as one pack, which grows as it arrives, rather than unpacked into objects in memory.
func (r *Repo) FetchFiles(ctx context.Context, ref string, max int64) (string, error) {
	sha, err := r.RemoteRef(ctx, ref)
	if err != nil {
		return "", err
	}
	if sha == "" {
		return "", ErrNoFile
	}
	packs := filepath.Join(r.objectDir(), "pack")
	had := map[string]bool{}
	if ents, err := os.ReadDir(packs); err == nil {
		for _, e := range ents {
			had[e.Name()] = true
		}
	}
	// What the fetch wrote: the pack, as it grows, and once it is done its index too.
	fetched := func() (n int64, names []string) {
		ents, _ := os.ReadDir(packs)
		for _, e := range ents {
			if had[e.Name()] {
				continue
			}
			names = append(names, e.Name())
			if info, err := e.Info(); err == nil {
				n += info.Size()
			}
		}
		return n, names
	}
	// Encrypted files don't compress, so a pack of max bytes takes about max on the wire: zlib's
	// framing, the commit, the tree and the pack's index take far less than this allows over it.
	limit := max + max/32 + 256<<10
	fctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stop := make(chan struct{})
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		t := time.NewTicker(50 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if n, _ := fetched(); n > limit {
					cancel(ErrTooLarge)
					return
				}
			}
		}
	}()
	_, err = r.Git.Run(fctx, "", "-c", "gc.auto=0", "-c", "maintenance.auto=false",
		"-c", "fetch.unpackLimit=1", "-c", "core.bigFileThreshold=1m", "fetch", "--quiet",
		"--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", "--refmap=",
		"--negotiation-tip="+brand.RefRoot+"*", r.Remote, ref)
	close(stop)
	<-watched
	n, names := fetched()
	if errors.Is(context.Cause(fctx), ErrTooLarge) || err == nil && n > limit {
		// A fetch this fast was done before it could be stopped; either way none of it is kept.
		for _, name := range names {
			os.Remove(filepath.Join(packs, name))
		}
		return "", fmt.Errorf("%w: the files ref holds over %d bytes", ErrTooLarge, limit)
	}
	if err != nil {
		return "", err
	}
	return sha, nil
}

// objectDir is where git writes this repo's objects: the scratch directory, if it is one.
func (r *Repo) objectDir() string {
	for i := len(r.Git.Env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(r.Git.Env[i], "GIT_OBJECT_DIRECTORY="); ok {
			return v
		}
	}
	return filepath.Join(r.Git.Dir, "objects")
}

// DeleteLocal removes a local ref, if it is there.
func (r *Repo) DeleteLocal(ctx context.Context, ref string) error {
	_, err := r.Git.Run(ctx, "", "update-ref", "-d", ref)
	return err
}
