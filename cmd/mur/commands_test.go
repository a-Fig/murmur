package main

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// In a T3 thread's shell, the commands act as the thread's session, which stays after each exits: a
// later command is the same session, with the status an earlier one set (#913). A send to a name
// nobody has is refused, with the daemon's reason.
func TestTheCommandsActAsTheThread(t *testing.T) {
	b := newBox(t)
	b.mustRun(b.clone, "init")
	b.mustRun(b.clone, "join")
	b.extraEnv = []string{"T3CODE_THREAD_ID=thread-913", "T3CODE_CLI_PATH=" + filepath.Join(b.root, "t3")}

	status := b.mustRun(b.clone, "status", "trying", "the", "commands")
	who := b.mustRun(b.clone, "who")
	if !strings.Contains(who, "trying the commands") || !strings.Contains(who, "(you)") {
		t.Errorf("who after status:\n%s", who)
	}
	inbox := b.mustRun(b.clone, "inbox")
	out, code := b.run(b.clone, "send", "nobody-here", "a subject", "a body")
	if code != 1 {
		t.Errorf("send to nobody: exit %d\n%s", code, out)
	}
	// lookup of the thread's own session, by the address who gives, then of one that doesn't exist (#1119).
	self := regexp.MustCompile(`device-[0-9a-f]+/[0-9a-f]+`).FindString(who)
	lookup := b.mustRun(b.clone, "lookup", self, "--messages", "3")
	if self == "" || !strings.Contains(lookup, self) || !strings.Contains(lookup, "(you)") {
		t.Errorf("lookup of %q:\n%s", self, lookup)
	}
	missing, code := b.run(b.clone, "lookup", "nobody-here")
	if code != 1 {
		t.Errorf("lookup of nobody: exit %d\n%s", code, missing)
	}
	t.Logf("mur lookup %s:\n%s\nmur lookup nobody-here:\n%s", self, lookup, missing)
	// tags: add two, see them in who, take one off, and list (#1120).
	added := b.mustRun(b.clone, "tags", "--add", "reviewing,group:ui")
	whoTagged := b.mustRun(b.clone, "who")
	removed := b.mustRun(b.clone, "tags", "--remove", "reviewing")
	listed := b.mustRun(b.clone, "tags")
	if !strings.Contains(added, "reviewing") || !strings.Contains(whoTagged, "reviewing") || strings.Contains(removed, "reviewing") || !strings.Contains(listed, "group:ui") {
		t.Errorf("tags:\n%s\n%s\n%s\n%s", added, whoTagged, removed, listed)
	}
	t.Logf("mur tags --add reviewing,group:ui:\n%s\nmur tags --remove reviewing:\n%s\nmur tags:\n%s", added, removed, listed)
	// mute: nobody to start with, a name that isn't one, and unmuting one that wasn't muted (#1121).
	none := b.mustRun(b.clone, "mute")
	noName, code1 := b.run(b.clone, "mute", "nobody-here")
	notMuted, code2 := b.run(b.clone, "mute", "--unmute", "nobody-here")
	if !strings.Contains(none, "muted nobody") || code1 != 1 || code2 != 1 {
		t.Errorf("mute: %q, %d %q, %d %q", none, code1, noName, code2, notMuted)
	}
	t.Logf("mur mute:\n%s\nmur mute nobody-here:\n%s\nmur mute --unmute nobody-here:\n%s", none, noName, notMuted)
	t.Logf("mur status:\n%s\nmur who:\n%s\nmur inbox:\n%s\nmur send nobody-here:\n%s", status, who, inbox, out)
}
