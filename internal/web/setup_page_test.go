package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The harness executes the real page helper and setup functions inside Node with a lightweight
// virtual DOM Builder (h) and fake /api/account fetch, verifying what the person sees and sends
// when opening a new repo with or without cross-repo offers (#1277).
const setupPageHarness = `
const vm = require("vm");
const fs = require("fs");
const src = fs.readFileSync(process.argv[2], "utf8");

function makeNode(tag, attrs, children) {
  return { tag, attrs: attrs || {}, children };
}
function flatten(items, out = []) {
  for (const x of items) {
    if (x == null || x === false) continue;
    if (Array.isArray(x)) flatten(x, out);
    else out.push(x);
  }
  return out;
}
function h(tag, attrs, ...rest) {
  return makeNode(tag, attrs, flatten(rest));
}
function textOf(node) {
  if (node == null || node === false) return "";
  if (typeof node === "string" || typeof node === "number") return String(node);
  let s = "";
  for (const c of node.children || []) {
    const t = textOf(c);
    if (!t) continue;
    if (s && typeof c === "object" && c.tag !== "span") s += " ";
    s += t;
  }
  return s;
}
function findAll(node, pred, out = []) {
  if (!node || typeof node !== "object") return out;
  if (pred(node)) out.push(node);
  for (const c of node.children || []) findAll(c, pred, out);
  return out;
}

async function runCase(label, accountState, fn) {
  const calls = [];
  let apiReply = (body) => ({ ok: true, json: { name: body.name, addr: "tyler-mbp-7a20/human", nick: body.nick || "" } });
  const ctx = {
    ui: { state: { account: accountState, devices: [{ id: "tyler-mbp-7a20", self: true }] }, setup: null, rowErrors: {}, accountDraft: null, nickDraft: null },
    h,
    dialog: (label, _close, ...kids) => makeNode("div", { class: "dialog", "aria-label": label }, flatten(kids)),
    document: { activeElement: null },
    root: { querySelector: () => null },
    icon: (n) => "[icon:" + n + "]",
    faceOf: (n) => "[face:" + n + "]",
    me: () => (ctx.ui.state?.account?.name ? ctx.ui.state.account : null),
    myName: () => ctx.me()?.name || "",
    render: () => {},
    poll: () => { calls.push("poll"); },
    moveToName: (a, b) => { calls.push("move:" + a + "->" + b); },
    refocus: () => {},
    deny: () => {},
    AuthError: class AuthError extends Error {},
    api: async (path, opts) => {
      const body = opts?.body ? JSON.parse(opts.body) : null;
      calls.push(path + ":" + JSON.stringify(body));
      const res = apiReply(body);
      if (!res.ok) throw new Error(res.error);
      return res.json;
    },
    setApiReply: (handler) => { apiReply = handler; },
    calls,
  };
  vm.runInNewContext(src, ctx);
  await fn(ctx);
}

(async () => {
  // 1. No other repo yet: primaryOffer is null, setup opens plain single-field dialog.
  await runCase("no-offers", { suggested: "ada" }, async (c) => {
    if (c.primaryOffer() !== null) throw new Error("expected null offer");
    c.askForName(null);
    const tree = c.setupDialog();
    const txt = textOf(tree);
    if (!txt.includes("Choose your name") || !txt.includes("Agents see it, and send to it.")) {
      throw new Error("unexpected text: " + txt);
    }
    if (findAll(tree, (n) => n.attrs?.["data-key"] === "setup-nick").length !== 0) {
      throw new Error("did not expect setup-nick field with no offers");
    }
    await c.submitName();
    if (!c.calls.includes('/api/account:{"name":"ada"}')) {
      throw new Error("calls=" + JSON.stringify(c.calls));
    }
  });

  // 2. Usable offer: 1-click offer dialog states team visibility, submits name + nick atomically.
  await runCase("usable-offer", {
    suggested: "tyler",
    offers: [{ name: "tyler", nick: "tyler-laptop", repo: "a-Fig/murmur" }],
  }, async (c) => {
    c.askForName(null);
    const tree = c.setupDialog();
    const txt = textOf(tree);
    if (!txt.includes("Use tyler and tyler-laptop, as in a-Fig/murmur?")) throw new Error("missing offer title: " + txt);
    if (!txt.includes("Accepting shares your name with teammates in this repo.")) throw new Error("missing sharing notice: " + txt);
    await c.submitName();
    if (!c.calls.includes('/api/account:{"name":"tyler","nick":"tyler-laptop"}')) {
      throw new Error("calls=" + JSON.stringify(c.calls));
    }
    if (c.ui.setup !== null) throw new Error("setup did not close");
  });

  // 3. Usable offer with blank nick: leaves nick omitted so device uses person's name by default.
  await runCase("offer-default-nick", {
    suggested: "tyler",
    offers: [{ name: "tyler", repo: "a-Fig/murmur" }],
  }, async (c) => {
    c.askForName(null);
    const tree = c.setupDialog();
    if (!textOf(tree).includes("Use tyler, as in a-Fig/murmur?")) throw new Error("unexpected title: " + textOf(tree));
    await c.submitName();
    if (!c.calls.includes('/api/account:{"name":"tyler"}')) {
      throw new Error("calls=" + JSON.stringify(c.calls));
    }
  });

  // 4. Raced accept in Offer mode: switches to edit mode, keeps error and polls.
  await runCase("raced-offer", {
    suggested: "tyler-fig",
    offers: [{ name: "tyler", nick: "tyler-laptop", repo: "a-Fig/murmur" }],
  }, async (c) => {
    c.askForName(null);
    c.setApiReply(() => ({ ok: false, error: "tyler is taken, by the person on maya-mbp." }));
    await c.submitName();
    if (c.ui.setup?.mode !== "edit" || !String(c.ui.setup.error).includes("taken")) {
      throw new Error("expected edit mode with error, got " + JSON.stringify(c.ui.setup));
    }
    if (c.ui.setup.name !== "tyler-fig" || c.ui.setup.nick !== "tyler-laptop") {
      throw new Error("expected suggested fallback + preserved nick, got " + JSON.stringify(c.ui.setup));
    }
  });

  // 5. Taken offer upfront: no 1-click primaryOffer, opens edit mode with suggested + other repo's free nick.
  await runCase("taken-offer-upfront", {
    suggested: "tyler-fig",
    offers: [{ name: "tyler", nick: "tyler-laptop", repo: "a-Fig/murmur", taken: "maya-mbp-91c0" }],
  }, async (c) => {
    if (c.primaryOffer() !== null) throw new Error("taken offer must not be primaryOffer");
    if (c.offeredNick() !== "tyler-laptop") throw new Error("expected free nick fallback");
    c.askForName(null);
    const tree = c.setupDialog();
    const txt = textOf(tree);
    if (!txt.includes("tyler from a-Fig/murmur is already taken by maya-mbp-91c0.")) {
      throw new Error("missing upfront taken warning: " + txt);
    }
    if (findAll(tree, (n) => n.attrs?.["data-key"] === "setup-accept").length !== 0) {
      throw new Error("taken offer must not render setup-accept button");
    }
    const nameInput = findAll(tree, (n) => n.attrs?.["data-key"] === "setup-name")[0];
    const nickInput = findAll(tree, (n) => n.attrs?.["data-key"] === "setup-nick")[0];
    if (nameInput?.attrs?.value !== "tyler-fig" || nickInput?.attrs?.value !== "tyler-laptop") {
      throw new Error("unexpected fallback inputs: name=" + nameInput?.attrs?.value + " nick=" + nickInput?.attrs?.value);
    }
  });

  // 6. Settings acceptOffer & saveAccount nick-collision fallback routes error to nick row.
  await runCase("settings-accept", {
    suggested: "tyler-fig",
    offers: [{ name: "tyler", nick: "tyler-laptop", repo: "a-Fig/murmur" }],
  }, async (c) => {
    await c.acceptOffer(c.ui.state.account.offers[0]);
    if (!c.calls.includes('/api/account:{"name":"tyler","nick":"tyler-laptop"}')) {
      throw new Error("settings acceptOffer calls=" + JSON.stringify(c.calls));
    }
  });

  await runCase("settings-nick-collision-after-claim", {
    suggested: "tyler-fig",
    offers: [{ name: "tyler", nick: "shared-nick", repo: "a-Fig/murmur" }],
  }, async (c) => {
    c.setApiReply(() => ({ ok: false, error: "claimed tyler, but shared-nick is the name of the person on peer-1." }));
    await c.acceptOffer(c.ui.state.account.offers[0]);
    if (!String(c.ui.rowErrors.nick || "").includes("shared-nick")) {
      throw new Error("expected nick row error, got " + JSON.stringify(c.ui.rowErrors));
    }
  });

  console.log("setupPageHarness: ok");
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
`

func TestSetupOfferPageLogic(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "page.js")
	harness := filepath.Join(dir, "harness.js")
	fnSrc := pageFunctions(
		t,
		"accountOffers", "usableOffers", "primaryOffer", "offeredNick",
		"takenOfferNotice", "askForName", "submitName", "setupDialog",
		"acceptOffer", "saveAccount", "putAccount",
	)
	if err := os.WriteFile(src, []byte(fnSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(harness, []byte(setupPageHarness), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, harness, src).CombinedOutput()
	if err != nil {
		t.Fatalf("setup harness failed: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "setupPageHarness: ok" {
		t.Fatalf("unexpected output: %s", out)
	}
}
