package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The harness prints, for each change to a state, whether the page's signature of it changed.
const shownStateHarness = `
const vm = require("vm");
const fs = require("fs");
const ctx = { Date, JSON };
vm.createContext(ctx);
vm.runInContext(fs.readFileSync(process.argv[2], "utf8") + ";this.shownState = shownState;", ctx);
const now = Date.now();
const at = (secondsAgo) => new Date(now - secondsAgo * 1000).toISOString();
const state = () => ({
  device: "here",
  devices: [
    { id: "here", self: true, reachable: true, lastSeen: at(0), person: { name: "ada", addr: "here/human", lastActive: at(3) },
      sessions: [{ name: "quiet-otter", addr: "here/aaaaaa", lastActive: at(400), doing: "tests" }] },
    { id: "there", reachable: true, lastSeen: at(40), person: { name: "linus", addr: "there/human", lastActive: at(600) }, sessions: [] },
    { id: "gone", reachable: false, lastSeen: at(7200), sessions: [] },
  ],
});
const sig = (s) => JSON.stringify(ctx.shownState(s));
const base = sig(state());
const changed = (name, edit) => { const s = state(); edit(s.devices); console.log(name + ": " + (sig(s) === base ? "same" : "changed")); };
changed("this device seen again", (d) => { d[0].lastSeen = at(-3); });
changed("this device's person polled again", (d) => { d[0].person.lastActive = at(0); });
changed("another device seen again", (d) => { d[1].lastSeen = at(1); });
changed("a person away came back", (d) => { d[1].person.lastActive = at(2); });
changed("a person here went away", (d) => { d[0].person.lastActive = at(600); });
changed("a session did something", (d) => { d[0].sessions[0].lastActive = at(1); });
changed("a session's status", (d) => { d[0].sessions[0].doing = "review"; });
changed("an unreachable device's last sighting", (d) => { d[2].lastSeen = at(60); });
changed("a device went unreachable", (d) => { d[1].reachable = false; });
console.log("no state: " + JSON.stringify(ctx.shownState(null)));
`

// The page rebuilds when its signature of the state changes. The times that move on every poll
// and aren't shown stay out of it, so a page nobody touches isn't rebuilt on every poll (#618),
// and everything the page does show still changes it.
func TestPageSignatureLeavesOutWhatIsNotShown(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node")
	}
	dir := t.TempDir()
	src, harness := filepath.Join(dir, "page.js"), filepath.Join(dir, "harness.js")
	for path, text := range map[string]string{src: pageFunctions(t, "when", "personOnline", "shownState"), harness: shownStateHarness} {
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := exec.Command(node, harness, src).CombinedOutput()
	if err != nil {
		t.Fatalf("the harness: %v\n%s", err, b)
	}
	got := strings.TrimSpace(strings.ReplaceAll(string(b), "\r\n", "\n"))
	want := strings.Join([]string{
		"this device seen again: same",
		"this device's person polled again: same",
		"another device seen again: same",
		"a person away came back: changed",
		"a person here went away: changed",
		"a session did something: changed",
		"a session's status: changed",
		"an unreachable device's last sighting: changed",
		"a device went unreachable: changed",
		"no state: null",
	}, "\n")
	if got != want {
		t.Errorf("the harness printed\n%s\nwant\n%s", got, want)
	}
}
