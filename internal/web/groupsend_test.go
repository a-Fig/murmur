package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pageFunctions returns the source of these top-level declarations in the page's app.js: each
// starts a line ("function x(", "async function x(", "class x ", "const x =") and a function or
// class ends at the first line that is only "}".
func pageFunctions(t *testing.T, names ...string) string {
	t.Helper()
	b, err := static.ReadFile("static/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := strings.ReplaceAll(string(b), "\r\n", "\n")
	var out []string
	for _, n := range names {
		found := false
		for _, lead := range []string{"function " + n + "(", "async function " + n + "(", "class " + n + " ", "const " + n + " ="} {
			i := strings.Index(src, "\n"+lead)
			if i < 0 {
				continue
			}
			rest := src[i+1:]
			end := strings.Index(rest, "\n")
			if !strings.HasPrefix(lead, "const ") || strings.HasSuffix(rest[:end], "{") {
				end = strings.Index(rest, "\n}\n") + 2
			}
			out = append(out, rest[:end])
			found = true
			break
		}
		if !found {
			t.Fatalf("app.js has no top-level %s", n)
		}
	}
	return strings.Join(out, "\n")
}

// The harness runs the page's own tag and send code with a stand-in fetch: /api/tags fails for
// the sessions in failing, with the daemon's error, and /api/send records what it was asked.
const groupSendHarness = `
const vm = require("vm");
const fs = require("fs");
const src = fs.readFileSync(process.argv[2], "utf8");
async function run(name, sessions, failing, then) {
  const calls = [];
  const ctx = {
    ui: { token: "t", state: { devices: [{ id: "laptop", sessions }] }, outbox: [], notes: new Map(), tagMenu: null, notice: "" },
    document: { hidden: false },
    stateAskedAt: 0, timer: 0, setTimeout: () => 0, clearTimeout: () => {},
    render: () => {}, poll: () => {}, deny: () => {}, announce: (t) => calls.push("announce " + t),
    devices: () => ctx.ui.state.devices,
    fetch: async (path, init) => {
      const body = JSON.parse(init.body);
      calls.push(path + " " + (body.addr || body.to) + (body.text && body.text !== "hello" ? " " + body.text : ""));
      if (path === "/api/tags" && failing.has(body.addr)) {
        return { ok: false, status: 400, statusText: "Bad Request", json: async () => ({ error: "a session has at most 12 tags" }) };
      }
      return { ok: true, status: 200, json: async () => (path === "/api/send" ? { id: "m1", note: "group:x has 1 other member(s) right now." } : {}) };
    },
  };
  vm.createContext(ctx);
  vm.runInContext(src + "\nthis.tagSessions = tagSessions; this.deliver = deliver;", ctx);
  const newItem = (text) => ({ cid: text, key: "group:x", to: "group:x", text, status: "sending" });
  const show = (label, item) => {
    const note = ctx.ui.notes.get("group:x");
    console.log(label + ": " + item.status + " | " + (item.error || "") + " | " + (note ? (note.warn ? "warn " : "") + note.text : "") + " | " + calls.join(", "));
    calls.length = 0;
  };
  const item = newItem("hello");
  ctx.tagSessions(sessions, "group:x", true); // as startGroup does, without waiting
  if (then === "two at once") {
    const second = newItem("hello2");
    await Promise.all([ctx.deliver(item), ctx.deliver(second)]);
    return show(name, second);
  }
  await ctx.deliver(item);
  show(name, item);
  if (then === "edit and send") {
    // Edit takes the message back into the composer; sending it again is a new message.
    const again = newItem("hello2");
    await ctx.deliver(again);
    show(name + ", sent anew", again);
  }
  if (then === "try again") {
    failing.clear();
    await ctx.deliver(item);
    show(name + ", tried again", item);
  }
  if (then === "added by hand") {
    failing.clear();
    await ctx.tagSessions([sessions[0]], "group:x", true); // as the tag menu does
    const again = newItem("hello2");
    await ctx.deliver(again);
    show(name + ", added by hand", again);
  }
}
(async () => {
  const s = (name) => ({ name, addr: "laptop/" + name, tags: [] });
  await run("all join", [s("a"), s("b")], new Set(), "two at once");
  await run("one fails", [s("a"), s("b")], new Set(["laptop/a"]), "added by hand");
  await run("all fail", [s("a"), s("b")], new Set(["laptop/a", "laptop/b"]), "edit and send");
  await run("all fail", [s("a"), s("b")], new Set(["laptop/a", "laptop/b"]), "try again");
})().catch((e) => { console.error(e); process.exit(1); });
`

// Starting a group from the page and sending at once (#535): a member whose add fails isn't in the
// group, so the page says it didn't get the message, until it is added; with nobody in the group
// the page sends nothing and says so, for a message written anew too; Try again tries the adds
// again; and two messages sent at once still go in the order they were written.
func TestGroupSendSaysWhoWasntAdded(t *testing.T) {
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
	write(src, pageFunctions(t, "AuthError", "api", "HIDDEN", "OWN_MEANING", "sortsBy", "tagWanted", "TAG_WAIT_MS", "tagsOf",
		"hasTag", "inGroup", "isGroupTag", "sessionByAddr", "tagQueues", "groupJoins", "queueTagChange", "tagSessions", "deliver", "namesOf"))
	harness := filepath.Join(dir, "harness.js")
	write(harness, groupSendHarness)
	b, err := exec.Command(node, harness, src).CombinedOutput()
	if err != nil {
		t.Fatalf("the harness: %v\n%s", err, b)
	}
	got := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(b), "\r\n", "\n")), "\n")
	want := []string{
		"all join: sent |  | group:x has 1 other member(s) right now. | /api/tags laptop/a, /api/tags laptop/b, /api/send group:x, /api/send group:x hello2, announce Sent., announce Sent.",
		"one fails: sent |  | warn Couldn't add a to group:x, so they didn't get this: a session has at most 12 tags | /api/tags laptop/a, /api/tags laptop/b, /api/send group:x, announce Sent, but not to a.",
		"one fails, added by hand: sent |  | group:x has 1 other member(s) right now. | /api/tags laptop/a, /api/send group:x hello2, announce Sent.",
		"all fail: failed | Not sent: couldn't add a, b to group:x: a session has at most 12 tags |  | /api/tags laptop/a, /api/tags laptop/b",
		"all fail, sent anew: failed | Not sent: couldn't add a, b to group:x: a session has at most 12 tags |  | ",
		"all fail: failed | Not sent: couldn't add a, b to group:x: a session has at most 12 tags |  | /api/tags laptop/a, /api/tags laptop/b",
		"all fail, tried again: sent |  | group:x has 1 other member(s) right now. | /api/tags laptop/a, /api/tags laptop/b, /api/send group:x, announce Sent.",
	}
	if len(got) != len(want) {
		t.Fatalf("the harness printed\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d:\n got %s\nwant %s", i, got[i], want[i])
		}
	}
}
