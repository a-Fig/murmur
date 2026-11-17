package daemon_test

import (
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
)

// #51: a Claude Code session's ID was the start of Claude Code's own, and is a hash of it now, so a
// conversation from the command line that is resumed after the upgrade comes back at another
// address. It keeps its name, which follows Claude Code's ID rather than the address, and what is
// sent to its old address, from another device as from this one, is the new session's.
func TestAConversationResumedAtAHashedIDKeepsItsNameAndMessages(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	b := newTimedDevice(t, remote, 300*time.Millisecond, 0)
	a.session("aaaaaa", "claude-sender")

	// Before the upgrade: the ID is Claude Code's first six characters.
	old, conn, _ := b.sessionPID("0a1b2c", "0a1b2c3d-aaaa-bbbb-cccc-ddddeeeeffff", procP)
	name := b.named(old)
	conn.Close()
	eventually(t, "the old session to stop", func() bool { return busLines(b, old, "stopped") == 1 })

	// After it: the same conversation, resumed, at its hashed ID.
	resumed, _, _ := b.sessionPID("9e4f17", "0a1b2c3d-aaaa-bbbb-cccc-ddddeeeeffff", procQ)
	b.nameIs(resumed, name)
	eventually(t, "presence", func() bool { return !a.listsSession(old) && a.nameOf(resumed) == name })

	if r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: old, Subject: "from another device"}); r.Error != "" {
		t.Fatalf("send: %+v", r)
	}
	b.noticed("0a1b2c3d-aaaa-bbbb-cccc-ddddeeeeffff", procQ, "from another device")
	if r := b.do(proto.Request{Op: proto.OpSend, To: old, Subject: "from this device"}); r.Error != "" {
		t.Fatalf("send: %+v", r)
	}
	b.noticed("0a1b2c3d-aaaa-bbbb-cccc-ddddeeeeffff", procQ, "from this device")
}
