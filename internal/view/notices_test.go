package view_test

import (
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/view"
)

// A notice names each new message, then how many are unread (#202); with none new, it is the count.
func TestNoticesCount(t *testing.T) {
	msgs := []proto.MessageView{{From: "d/a", FromName: "quiet-otter", FromAgent: "Claude Code", Subject: "auth?"}}
	for _, c := range []struct {
		msgs   []proto.MessageView
		unread int
		want   []string
		not    []string
	}{
		{msgs, 1, []string{`[Murmur] quiet-otter (d/a, Claude Code): "auth?"`, "1 message unread. Messages from teammates' agents"}, nil},
		{msgs, 3, []string{"\n3 messages unread. "}, nil},
		{msgs, 0, []string{"\nMessages from teammates' agents"}, []string{"unread"}},
		{nil, 2, []string{"[Murmur] 2 messages unread. Messages from teammates' agents"}, []string{"auth?"}},
	} {
		got := view.Notices(c.msgs, c.unread)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("Notices(%d messages, %d) = %q, lacks %q", len(c.msgs), c.unread, got, w)
			}
		}
		for _, w := range c.not {
			if strings.Contains(got, w) {
				t.Errorf("Notices(%d messages, %d) = %q, has %q", len(c.msgs), c.unread, got, w)
			}
		}
	}
}

// Files its user's Receive files switch keeps from a session are one line in its turn and inbox,
// with their count and why, and counted in its notice (#860). Their names stay unsaid.
func TestFilesKeptFromTheSessionAreCounted(t *testing.T) {
	for _, c := range []struct {
		kept         int
		line, notice string
	}{
		{1, "It came with a file, which you don't have: your user turned off receiving files.\n", `"logs", with a file`},
		{3, "It came with 3 files, which you don't have: your user turned off receiving files.\n", `"logs", with 3 files`},
	} {
		m := proto.MessageView{From: "d/a", FromName: "quiet-otter", FromAgent: "Claude Code", Subject: "logs", FilesKept: c.kept}
		turn := view.Turn(m)
		inbox := view.Inbox(proto.Response{Messages: []proto.MessageView{m}})
		for what, got := range map[string]string{"turn": turn, "inbox": inbox} {
			if !strings.Contains(got, c.line) {
				t.Errorf("%d kept, %s lacks %q:\n%s", c.kept, what, c.line, got)
			}
			if strings.Contains(got, "saved on this machine") {
				t.Errorf("%d kept, %s lists files:\n%s", c.kept, what, got)
			}
		}
		if got := view.Notices([]proto.MessageView{m}, 1); !strings.Contains(got, c.notice) {
			t.Errorf("%d kept, notice lacks %q:\n%s", c.kept, c.notice, got)
		}
	}
	if got := view.Turn(proto.MessageView{From: "d/a", Subject: "logs"}); strings.Contains(got, "files") {
		t.Errorf("a message with no files mentions them:\n%s", got)
	}
}
