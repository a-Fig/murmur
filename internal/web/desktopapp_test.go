package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The harness runs the page's code for the desktop app (#23) with a stand-in window: murmurApp,
// when given, records what the page tells it.
const desktopAppHarness = `
const vm = require("vm");
const fs = require("fs");
const src = fs.readFileSync(process.argv[2], "utf8");
function run(withApp) {
  const told = [];
  const ctx = {
    NAME: "Murmur",
    ui: { state: { account: { name: "tyler", unread: [] } }, ids: new Set(), readLocal: new Set() },
    named: new Map(),
    location: { host: "127.0.0.1:9" },
    document: { hidden: false, hasFocus: () => true, documentElement: {} },
    getComputedStyle: () => ({ colorScheme: "dark" }),
    window: {},
  };
  if (withApp) ctx.window.murmurApp = (n) => { told.push(n); return Promise.resolve(); };
  vm.createContext(ctx);
  vm.runInContext(src + "\nlet toldApp = \"\";\nthis.tell = tellApp; this.notesOf = appNotes;", ctx);
  const msg = (id, from) => ({ id, from, fromAddr: "laptop/" + from, subject: "about " + id });
  const unread = (...ids) => { ctx.ui.state.account.unread = ids; for (const id of ids) ctx.ui.ids.add(id); };
  // Nothing new: the title and count, once.
  ctx.tell();
  ctx.tell();
  // Two arrive, one to the person; their own message and one that isn't theirs make no note.
  unread("m1");
  ctx.tell(ctx.notesOf([msg("m1", "quiet-otter"), msg("m2", "brave-heron"), msg("m3", "tyler")]));
  // Five arrive at once: one note sums them up.
  unread("m1", "a", "b", "c", "d");
  ctx.tell(ctx.notesOf(["a", "b", "c", "d"].map((id) => msg(id, "keen-pike"))));
  // Read in the window: the count goes, and the theme is light.
  unread();
  ctx.getComputedStyle = () => ({ colorScheme: "light" });
  ctx.tell();
  console.log(JSON.stringify(told));
}
run(false);
run(true);
`

// The page tells the desktop app its title, unread count and theme when they change, and the
// messages to the person as they arrive; in a browser, with no murmurApp, it tells nothing.
func TestPageTellsTheDesktopApp(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "page.js")
	if err := os.WriteFile(src, []byte(pageFunctions(t, "inApp", "tellApp", "appNotes", "unreadIds", "unreadCount", "nameOf", "fromName", "me", "myName")), 0o644); err != nil {
		t.Fatal(err)
	}
	harness := filepath.Join(dir, "harness.js")
	if err := os.WriteFile(harness, []byte(desktopAppHarness), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := exec.Command(node, harness, src).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	want := []string{
		`[]`,
		`[{"host":"127.0.0.1:9","title":"Murmur","unread":0,"notes":[],"seen":true,"dark":true},` +
			`{"host":"127.0.0.1:9","title":"(1) Murmur","unread":1,"notes":[{"title":"quiet-otter","body":"about m1"}],"seen":true,"dark":true},` +
			`{"host":"127.0.0.1:9","title":"(5) Murmur","unread":5,"notes":[{"title":"Murmur","body":"4 new messages"}],"seen":true,"dark":true},` +
			`{"host":"127.0.0.1:9","title":"Murmur","unread":0,"notes":[],"seen":true,"dark":false}]`,
	}
	if len(lines) != 2 || lines[0] != want[0] || lines[1] != want[1] {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

// The harness calls the page's api with a stand-in fetch, in a browser tab and in the app's window,
// and prints whether each call said it was hidden. The window says whether it is in front, as the
// app does each time that changes; a page from before #638 has nothing to take it.
const pageHiddenHarness = `
const vm = require("vm");
const fs = require("fs");
const src = fs.readFileSync(process.argv[2], "utf8");
(async () => {
  const out = [];
  for (const [label, hidden, front] of [
    ["tab shown", false, null],
    ["tab in the background", true, null],
    ["app not yet told", false, null],
    ["app in front", false, true],
    ["app behind another window", false, false],
    ["app back in front", false, true],
    ["app minimized", true, false],
  ]) {
    let sent = null;
    const ctx = {
      ui: { token: "t", conn: "ok" },
      document: { hidden, hasFocus: () => true },
      window: label.startsWith("app") ? { murmurApp: () => Promise.resolve() } : {},
      fetch: async (path, init) => { sent = init.headers; return { ok: true, status: 200, json: async () => ({}) }; },
      polls: 0,
    };
    vm.createContext(ctx);
    vm.runInContext(src + "\nthis.call = api; this.tell = typeof appSaysFront === 'function' ? appSaysFront : () => {}; poll = () => this.polls++;", ctx);
    if (label === "app back in front") ctx.tell(false);
    if (front !== null) ctx.tell(front);
    await ctx.call("/api/state");
    out.push(label + ": " + (sent["X-Page-Hidden"] === "1" ? "hidden" : "seen") + (ctx.polls ? ", polled" : ""));
  }
  console.log(out.join("\n"));
})();
`

// The desktop app's window under another window stays visible to its page, so the window says
// whether it is in front, and the page's calls are marked hidden while it isn't (#638): an app left
// open behind the editor doesn't keep the person active or hold off an update's restart. Back in
// front, the page polls at once.
func TestTheAppBehindAnotherWindowIsHidden(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "page.js")
	names := []string{"api", "inApp"}
	if b, _ := static.ReadFile("static/assets/app.js"); strings.Contains(string(b), "\nfunction appSaysFront(") {
		names = append(names, "pageHidden", "appSaysFront")
	}
	if err := os.WriteFile(src, []byte(pageFunctions(t, names...)+"\nlet poll;"), 0o644); err != nil {
		t.Fatal(err)
	}
	harness := filepath.Join(dir, "harness.js")
	if err := os.WriteFile(harness, []byte(pageHiddenHarness), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := exec.Command(node, harness, src).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	want := strings.Join([]string{
		"tab shown: seen",
		"tab in the background: hidden",
		"app not yet told: seen",
		"app in front: seen",
		"app behind another window: hidden",
		"app back in front: seen, polled",
		"app minimized: hidden",
	}, "\n")
	if got := strings.TrimSpace(strings.ReplaceAll(string(b), "\r\n", "\n")); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}
