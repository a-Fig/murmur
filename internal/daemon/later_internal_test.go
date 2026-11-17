package daemon

import "testing"

// The later of two servers is held to its own key only if the two are of one client process
// (#38): a second program on the same conversation takes the conversation's name once the first
// has gone, as it always has. And the later is known as that before its name lands.
func TestOnlyASiblingStaysTheLater(t *testing.T) {
	t.Parallel()
	d := tagsDaemon(t)
	d.st.NameKeys = map[string]string{}
	d.mu.Lock()
	defer d.mu.Unlock()
	a, b := d.sessions["aaaaaa"], d.sessions["bbbbbb"]
	a.key, a.clientPID, b.clientPID = "claude:shared", 100, 100
	if got := d.uniqueKeyLocked("claude:shared", b.id, b.clientPID); got != "claude:shared/bbbbbb" || len(d.st.Later[b.id]) != 1 {
		t.Fatalf("the second server of the process has key %q, later %v", got, d.st.Later)
	}
	// Its sibling gone, and no name landed for either: it is still the later.
	delete(d.sessions, a.id)
	if got := d.uniqueKeyLocked("claude:shared", b.id, b.clientPID); got != "claude:shared/bbbbbb" {
		t.Errorf("back first, the later server has key %q", got)
	}
	if got := d.uniqueKeyLocked("claude:other", b.id, b.clientPID); got != "claude:other" {
		t.Errorf("in another conversation it has key %q", got)
	}
	// The two move to another conversation, where it is the later again, and it comes back to the
	// first before its sibling does (/clear, then /resume): still the later there.
	a.key = "claude:next"
	d.sessions[a.id] = a
	if got := d.uniqueKeyLocked("claude:next", b.id, b.clientPID); got != "claude:next/bbbbbb" {
		t.Errorf("in the next conversation it has key %q", got)
	}
	if got := d.uniqueKeyLocked("claude:shared", b.id, b.clientPID); got != "claude:shared/bbbbbb" {
		t.Errorf("back in the first conversation before its sibling, it has key %q", got)
	}
	delete(d.sessions, a.id)
	a.key = "claude:shared"
	// Another program on the conversation is the later only while the first is there.
	d.sessions[a.id] = a
	c := &session{id: "cccccc", clientPID: 200}
	d.sessions[c.id] = c
	if got := d.uniqueKeyLocked("claude:shared", c.id, c.clientPID); got != "claude:shared/cccccc" || len(d.st.Later[c.id]) != 0 {
		t.Errorf("a second program has key %q, later %v", got, d.st.Later)
	}
	delete(d.sessions, a.id)
	if got := d.uniqueKeyLocked("claude:shared", c.id, c.clientPID); got != "claude:shared" {
		t.Errorf("the first program gone, the second has key %q", got)
	}
	// It goes when the session's inbox does.
	d.dropStoppedLocked(b.id)
	if len(d.st.Later) != 0 {
		t.Errorf("after the session was dropped: %v", d.st.Later)
	}
}
