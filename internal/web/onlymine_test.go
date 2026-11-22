package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The harness runs the page's own "only yours" filter and its toggle against a few conversations:
// ann is the person here, and the others are sessions.
const onlyMineHarness = `
const vm = require("vm");
const fs = require("fs");
const src = fs.readFileSync(process.argv[2], "utf8");
const ctx = {
  ui: { state: { account: { name: "ann", addr: "laptop/human" } }, messages: [], conversation: null, restored: null, draftTo: null, draftGroup: null, outbox: [], filter: null },
  named: new Map(),
  onlyMine: false,
  setOnlyMine: (v) => (ctx.onlyMine = v),
};
vm.createContext(ctx);
vm.runInContext(src + "\nthis.onlyYours = onlyYours; this.mineToggle = mineToggle;", ctx);
const msg = (id, from, toKind, to) => ({ id, from, toKind, to });
ctx.ui.messages = [
  msg("1", "ann", "session", "otter"),          // a DM of ann's
  msg("2", "otter", "session", "heron"),        // two sessions talking
  msg("3", "ann", "group", "group:ui"),         // a group ann wrote in
  msg("4", "otter", "group", "group:ui"),
  msg("5", "heron", "group", "group:db"),       // a group ann didn't
];
const list = [
  { key: 'dm:["ann","otter"]', kind: "dm", messages: [ctx.ui.messages[0]] },
  { key: 'dm:["heron","otter"]', kind: "dm", messages: [ctx.ui.messages[1]] },
  { key: "tag:group:ui", kind: "group", messages: [ctx.ui.messages[2], ctx.ui.messages[3]] },
  { key: "tag:group:db", kind: "group", messages: [ctx.ui.messages[4]] },
  { key: "tag:group:quiet", kind: "group", messages: [], quiet: true }, // a group nobody wrote in
  { key: 'dm:["ann","wren"]', kind: "dm", messages: [] },                // one ann just began
];
const keys = (l) => l.map((c) => c.key).join(" ");
console.log("off: " + keys(ctx.onlyYours(list)));
ctx.onlyMine = true;
console.log("on: " + keys(ctx.onlyYours(list)));
ctx.ui.conversation = "tag:group:db"; // opened from Recent
console.log("on, another's open: " + keys(ctx.onlyYours(list)));
ctx.ui.conversation = null;
ctx.ui.outbox = [{ key: "tag:group:db", status: "failed" }]; // a send that failed
console.log("on, one failed: " + keys(ctx.onlyYours(list)));
ctx.ui.outbox = [];
ctx.ui.filter = "laptop/otter"; // a picked session's conversations
console.log("on, a session picked: " + keys(ctx.onlyYours(list)));
ctx.ui.filter = null;
ctx.ui.state.account = {};
console.log("no account: " + keys(ctx.onlyYours(list)));
ctx.ui.state.account = { name: "ann", addr: "laptop/human" };
ctx.onlyMine = false;
ctx.ui.conversation = "tag:group:db";
ctx.mineToggle();
console.log("toggled on over another's: " + ctx.onlyMine + " " + ctx.ui.conversation);
ctx.onlyMine = false;
ctx.ui.conversation = "tag:group:ui";
ctx.mineToggle();
console.log("toggled on over ann's: " + ctx.onlyMine + " " + ctx.ui.conversation);
ctx.ui.conversation = "tag:group:db";
ctx.mineToggle();
console.log("toggled off: " + ctx.onlyMine + " " + ctx.ui.conversation);
`

// The "only yours" toggle took over from the Your messages view (#637): it leaves the person's DMs,
// the group chats they wrote in, and one they just began, and drops others' and quiet groups. A
// conversation opened from elsewhere stays while open; turning the toggle on closes one that isn't
// theirs, so the pane and the list agree. One with a send waiting or failed is theirs too. It
// narrows all conversations, not a picked session's; with no account there is nothing to filter by.
func TestOnlyYoursConversations(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "page.js")
	write := func(path, text string) {
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(src, pageFunctions(t, "me", "myName", "nameOf", "fromName", "toName", "conversationOf", "groupConversation", "dmKey",
		"draftConversation", "myMessages", "yourKeys", "onlyYours", "mineToggle"))
	harness := filepath.Join(dir, "harness.js")
	write(harness, onlyMineHarness)
	b, err := exec.Command(node, harness, src).CombinedOutput()
	if err != nil {
		t.Fatalf("the harness: %v\n%s", err, b)
	}
	got := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(b), "\r\n", "\n")), "\n")
	want := []string{
		`off: dm:["ann","otter"] dm:["heron","otter"] tag:group:ui tag:group:db tag:group:quiet dm:["ann","wren"]`,
		`on: dm:["ann","otter"] tag:group:ui dm:["ann","wren"]`,
		`on, another's open: dm:["ann","otter"] tag:group:ui tag:group:db dm:["ann","wren"]`,
		`on, one failed: dm:["ann","otter"] tag:group:ui tag:group:db dm:["ann","wren"]`,
		`on, a session picked: dm:["ann","otter"] dm:["heron","otter"] tag:group:ui tag:group:db tag:group:quiet dm:["ann","wren"]`,
		`no account: dm:["ann","otter"] dm:["heron","otter"] tag:group:ui tag:group:db tag:group:quiet dm:["ann","wren"]`,
		`toggled on over another's: true null`,
		`toggled on over ann's: true tag:group:ui`,
		`toggled off: false tag:group:db`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
