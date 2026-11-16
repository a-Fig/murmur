package web

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The harness runs the page's markOpenRead with a stand-in fetch whose /api/read fails, and a
// render that, as the page's does, asks markOpenRead again in a microtask. Timers are held, not
// run, until the harness fires them.
const markReadHarness = `
const vm = require("vm");
const fs = require("fs");
const src = fs.readFileSync(process.argv[2], "utf8");
(async () => {
  let reads = 0, renders = 0, failing = true;
  const timers = [];
  const ctx = {
    ui: { token: "t", view: "messages", conversation: "c", reading: null, newMark: null,
      state: { account: { unread: ["m1", "m2"] } }, ids: new Set(["m1", "m2"]), readLocal: new Set(), readHeld: false,
      messages: [{ id: "m1", sortAt: 1, conv: "c" }, { id: "m2", sortAt: 2, conv: "d" }] },
    document: { hidden: false, hasFocus: () => true },
    inApp: () => false,
    conversationOf: (m) => ({ key: m.conv }),
    deny: () => {},
    setTimeout: (f, ms) => timers.push({ f, ms }),
    queueMicrotask,
    fetch: async (path) => {
      await new Promise((r) => setImmediate(r)); // as a network would, so a page that asks without end still lets the harness go on
      if (path === "/api/read") reads++;
      if (failing) return { ok: false, status: 502, statusText: "Bad Gateway", json: async () => ({ error: "the daemon is down" }) };
      return { ok: true, status: 204, json: async () => null };
    },
  };
  ctx.render = () => { renders++; queueMicrotask(ctx.markOpenRead); };
  vm.createContext(ctx);
  vm.runInContext(src + "\nthis.markOpenRead = markOpenRead;", ctx);
  const settle = async () => { for (let i = 0; i < 50; i++) await new Promise((r) => setImmediate(r)); };
  const show = (label) => {
    const unread = ["m1", "m2"].filter((id) => !ctx.ui.readLocal.has(id)).join(" ");
    const mark = ctx.ui.newMark ? ctx.ui.newMark.key + "/" + ctx.ui.newMark.id : "none";
    console.log(label + ": " + reads + " read, " + renders + " render, timers " + timers.map((t) => t.ms).join(" ") + ", unread " + unread + ", new " + mark);
    reads = 0; renders = 0;
  };
  ctx.markOpenRead();
  await settle();
  show("fails");
  timers.shift().f(); // the wait is over, and the daemon still fails
  await settle();
  show("fails again");
  ctx.ui.conversation = "d"; // opened while the mark is held: it shows as new, and waits its turn
  ctx.markOpenRead();
  await settle();
  show("another opened");
  failing = false;
  timers.shift().f();
  await settle();
  show("then works");
})().catch((e) => { console.error(e); process.exit(1); });
`

// A conversation open while /api/read fails (#652): the page marks it unread again, and asks once
// more only after a poll's wait, not at once from the render that shows it unread, which would ask
// and redraw again without pause. A conversation opened during the wait still marks where its
// unread messages start ("New"), and is read once the wait is over.
func TestAFailedReadWaitsBeforeAskingAgain(t *testing.T) {
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
	write(src, pageFunctions(t, "POLL_MS", "AuthError", "api", "pageHidden", "unreadIds", "markOpenRead"))
	harness := filepath.Join(dir, "harness.js")
	write(harness, markReadHarness)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, node, harness, src).CombinedOutput()
	if err != nil {
		t.Fatalf("the harness: %v\n%s", err, b)
	}
	got := strings.TrimSpace(strings.ReplaceAll(string(b), "\r\n", "\n"))
	want := strings.Join([]string{
		"fails: 1 read, 2 render, timers 3000, unread m1 m2, new c/m1",
		"fails again: 1 read, 2 render, timers 3000, unread m1 m2, new c/m1",
		"another opened: 0 read, 1 render, timers 3000, unread m1 m2, new d/m2",
		"then works: 1 read, 1 render, timers , unread m1, new d/m2",
	}, "\n")
	if got != want {
		t.Errorf("the harness printed\n%s\nwant\n%s", got, want)
	}
}
