package daemon_test

import (
	"context"
	"net"
	"testing"

	"github.com/a-Fig/murmur/internal/proto"
)

// A Cursor session that registered unnamed (for example because a tool call ended its wait before
// its hook ran) and learned its chat while disconnected keeps its name when it reconnects, both
// within grace and after a daemon restart (#918).
func TestReconnectWithLateNativeKeepsSessionName(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	d := newDevice(t, remote)

	// 1. In-grace reconnect: drop the connection, then reconnect with the newly learned Native ID.
	c1, r1, err := d.c.Hello(context.Background(), proto.Hello{ID: "c11111", Agent: "cursor"})
	if err != nil {
		t.Fatalf("first hello: %v", err)
	}
	name1 := d.named(r1.Self)
	c1.Close()

	var c1b net.Conn
	var r1b proto.Response
	eventually(t, "in-grace reconnect with native", func() bool {
		c, r, err := d.c.Hello(context.Background(), proto.Hello{
			ID: "c11111", Agent: "cursor", Reconnect: true,
			Route: proto.Route{Native: "chat-in-grace"},
		})
		if err != nil {
			return false
		}
		c1b, r1b = c, r
		return true
	})
	t.Cleanup(func() { c1b.Close() })
	if got := d.named(r1b.Self); got != name1 {
		t.Fatalf("in-grace reconnect changed name: %q -> %q", name1, got)
	}

	// 2. Post-restart reconnect: register unnamed, restart the daemon, reconnect with Native set.
	c2, r2, err := d.c.Hello(context.Background(), proto.Hello{ID: "c22222", Agent: "cursor"})
	if err != nil {
		t.Fatalf("second session hello: %v", err)
	}
	name2 := d.named(r2.Self)
	c2.Close()

	d.restart()
	c2b, r2b, err := d.c.Hello(context.Background(), proto.Hello{
		ID: "c22222", Agent: "cursor", Reconnect: true,
		Route: proto.Route{Native: "chat-after-restart"},
	})
	if err != nil {
		t.Fatalf("post-restart hello: %v", err)
	}
	t.Cleanup(func() { c2b.Close() })
	if got := d.named(r2b.Self); got != name2 {
		t.Fatalf("post-restart reconnect changed name: %q -> %q", name2, got)
	}

	// Resuming the same chat later in a fresh session (new ID, same Native) gets that same name.
	c2b.Close()
	d.restart()
	c2c, r2c, err := d.c.Hello(context.Background(), proto.Hello{
		ID: "c33333", Agent: "cursor",
		Route: proto.Route{Native: "chat-after-restart"},
	})
	if err != nil {
		t.Fatalf("resumed hello: %v", err)
	}
	t.Cleanup(func() { c2c.Close() })
	if got := d.named(r2c.Self); got != name2 {
		t.Fatalf("resumed chat name: %q, want %q", got, name2)
	}
}
