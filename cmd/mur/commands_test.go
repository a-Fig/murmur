package main

import (
	"path/filepath"
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
	t.Logf("mur status:\n%s\nmur who:\n%s\nmur inbox:\n%s\nmur send nobody-here:\n%s", status, who, inbox, out)
}
