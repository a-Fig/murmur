package daemon_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/proto"
)

// A loose bus ref left full of null bytes, as a T3 restart left one on 2026-11-03, failed every
// fetch in the clone, and the device heard nothing more (#644). The daemon removes it, the next
// sync writes it again, and messages arrive.
func TestARefLeftFullOfNullBytesIsRepaired(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "")
	eventually(t, "presence", func() bool { return a.seesSession(bob) })

	ref := filepath.Join(b.gitDir, filepath.FromSlash(brand.PresencePrefix+b.self()))
	if _, err := os.Stat(ref); err != nil {
		t.Fatalf("b's presence ref isn't a loose file to break: %v", err)
	}
	if err := os.WriteFile(ref, make([]byte, 41), 0o644); err != nil {
		t.Fatal(err)
	}

	a.send("aaaaaa", bob, "After the crash", "Can you still hear me?")
	eventually(t, "the message reaches b", func() bool {
		return len(b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"}).Messages) == 1
	})
	eventually(t, "the ref written again", func() bool {
		got, err := os.ReadFile(ref)
		return err == nil && !bytes.Contains(got, []byte{0})
	})
	git(t, filepath.Dir(b.gitDir), "fetch", "-q", "origin") // the user's own fetch works again
}
