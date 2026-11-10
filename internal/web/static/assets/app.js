// The page. Plain DOM, no framework and no build step: the daemon embeds these files as they
// are. Everything from the bus is untrusted text, so it only ever reaches the page as text nodes.
import { icon } from "./icons.js";

const NAME = document.querySelector('meta[name="app-name"]').content;
const COMMAND = document.querySelector('meta[name="app-command"]')?.content || "mur";
const TOKEN_KEY = NAME + ":token";
const POLL_MS = 3000;
const HIDDEN_POLL_MS = 15000;
const MAX_MESSAGES = 2000; // the page's limit for what isn't the person's own
const MAX_OWN_MESSAGES = 2000; // and for what is, as the daemon keeps it (maxPersonLog)
const VIEWS = ["sessions", "messages", "recent", "settings"];
const TITLES = { sessions: "Sessions", messages: "Conversations", recent: "Recent", settings: "Settings" };
const NARROW = window.matchMedia("(max-width: 767px)"); // style.css's mobile width

const ui = {
  view: "messages",
  token: null,
  conn: "connecting", // connecting, ok, down, denied
  state: null,
  messages: [], // oldest first
  ids: new Set(),
  cursor: "",
  settings: null,
  settingsError: "",
  rowErrors: {},
  filter: null, // a session name
  conversation: null, // the open conversation's key
  conversationAnchor: null, // a message in it
  chatListTop: 0,
  muting: new Set(),
  tagging: new Set(), // the sessions whose tags are being changed, by their row key
  tagInput: null, // the row key of the session whose tag field is open
  tagFilter: null, // a tag: the session lists show only the sessions with it
  tagMenu: null, // { addrs, x, top or bottom, q, err }: the tag menu, open on those sessions
  selecting: false, // All sessions picks sessions, to tag them together
  selected: new Set(), // their addresses
  madeTags: new Set(), // tags made here, offered in the menu even once no session has them
  restarting: 0, // when the restart onto an installed update was asked for
  reloadDue: false, // a new daemon is serving, and this page should load its page when it can
  // A person's account (#242).
  setup: null, // the name being chosen: { name, error, busy, then, inline }, then what to do once there is one, inline the composer asking
  picker: null, // the new-message list: { q, index }
  draftTo: null, // who a conversation with no messages yet is with
  restored: null, // the conversation a failed message's text just went back into
  outbox: [], // what the person sent that isn't among the messages yet, or that failed
  notes: new Map(), // a conversation's key → what the daemon said of the last message sent in it
  readLocal: new Set(), // messages marked read that the state doesn't know of yet
  reading: null, // the conversation someone was last seen looking at, null if nobody is looking
  newMark: null, // the open conversation's first unread message: { key, id }
  armed: null, // the group chat whose next send goes: its key
  relayTo: new Map(), // a conversation between two others → which of them its bar writes to
  relayed: new Map(), // a conversation between two others → what its bar last sent: { cid, to, key }
};

class AuthError extends Error {
  constructor(token) {
    super("unauthorized");
    this.token = token;
  }
}

// The token arrives once, in the URL fragment, which never reaches a server or a Referer. It is
// kept for this tab only, and taken out of the address bar at once. The fragment may also name a
// view: #messages&token=...
function takeToken() {
  const parts = location.hash.slice(1).split("&");
  const tok = parts.find((p) => p.startsWith("token="));
  if (tok) {
    let value = tok.slice(6);
    try {
      value = decodeURIComponent(value);
    } catch {}
    sessionStorage.setItem(TOKEN_KEY, value);
    const view = parts.find((p) => VIEWS.includes(p.split("/")[0])) || "messages";
    history.replaceState(null, "", location.pathname + location.search + "#" + view);
  }
  return sessionStorage.getItem(TOKEN_KEY);
}

async function api(path, init = {}) {
  const token = ui.token;
  const headers = { Authorization: "Bearer " + token };
  if (init.body) headers["Content-Type"] = "application/json";
  // A tab nobody can see still polls, but mustn't keep the daemon running for good.
  if (document.hidden) headers["X-Page-Hidden"] = "1";
  const res = await fetch(path, { ...init, headers, cache: "no-store" });
  if (res.status === 401) throw new AuthError(token);
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.error || res.status + " " + res.statusText);
  }
  return res.status === 204 ? null : res.json();
}

// DOM.

function h(tag, props, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (v == null || v === false) continue;
    if (k === "class") el.className = v;
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else el.setAttribute(k, v === true ? "" : String(v));
  }
  for (const kid of kids.flat(Infinity)) {
    if (kid == null || kid === false || kid === "") continue;
    el.append(kid instanceof Node ? kid : String(kid));
  }
  return el;
}

// Time.

function when(s) {
  if (!s) return null;
  const t = Date.parse(s);
  return Number.isNaN(t) || t < Date.UTC(2000, 0, 1) ? null : t;
}

function ago(t) {
  const s = Math.max(0, (Date.now() - t) / 1000);
  if (s < 45) return "now";
  if (s < 3600) return Math.max(1, Math.round(s / 60)) + "m ago";
  if (s < 86400) return Math.floor(s / 3600) + "h ago";
  return Math.floor(s / 86400) + "d ago";
}

function stamp(t) {
  return new Date(t).toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
    second: "2-digit",
  });
}

// An element whose text is a relative time, kept current by the ticker. A bare one is a duration
// ("40m") rather than a time ("40m ago").
function agoEl(tag, cls, t, prefix = "", bare = false) {
  return h(tag, { class: cls, "data-ago": t, "data-prefix": prefix, "data-bare": bare ? "1" : null, title: stamp(t) }, prefix + (bare ? lasted(t) : ago(t)));
}

function tick() {
  for (const el of document.querySelectorAll("[data-ago]")) {
    const t = Number(el.dataset.ago);
    el.textContent = el.dataset.prefix + (el.dataset.bare ? lasted(t) : ago(t));
  }
}

// Lookups over the latest state.

function devices() {
  const list = [...(ui.state?.devices || [])];
  const rank = (d) => (d.self ? 0 : d.reachable ? 1 : 2);
  list.sort((a, b) => rank(a) - rank(b) || a.id.localeCompare(b.id));
  for (const d of list) {
    d.sessions = [...(d.sessions || [])].sort(
      (a, b) => (when(a.started) || 0) - (when(b.started) || 0) || a.name.localeCompare(b.name),
    );
  }
  return list;
}

// The latest name for each address the page has seen. A session works as device/session until its
// name lands, and the page keeps messages from before that, so it goes by the address the bus
// carried and puts in whatever the address is called now.
const named = new Map();

let namesLearned = 0; // goes up whenever an address gets a new name

function learn(addr, name) {
  if (addr && name && name !== addr && named.get(addr) !== name) {
    named.set(addr, name);
    namesLearned++;
  }
}

function nameOf(addr, shown) {
  return (addr && named.get(addr)) || shown;
}

function fromName(m) {
  return nameOf(m.fromAddr, m.from);
}

function toName(m) {
  return m.toKind === "session" ? nameOf(m.toAddr, m.to) : m.to;
}

function findSession(name) {
  for (const d of ui.state?.devices || []) {
    for (const s of d.sessions || []) if (s.name === name) return { device: d, session: s };
  }
  return null;
}

function sessionCount() {
  return (ui.state?.devices || []).reduce((n, d) => n + (d.sessions?.length || 0), 0);
}

function plural(n, word) {
  return n + " " + word + (n === 1 ? "" : "s");
}

// Shell.

const root = document.getElementById("root");

// Said to a screen reader, politely: new arrivals. Outside root, so a rebuild doesn't replace it
// before it is read.
const liveRegion = Object.assign(document.createElement("div"), { className: "sr-only" });
liveRegion.setAttribute("aria-live", "polite");
document.body.append(liveRegion);
function announce(text) {
  liveRegion.textContent = "";
  setTimeout(() => (liveRegion.textContent = text), 50);
}
let content; // the scrolling pane, kept across renders so its scroll position is too

function render() {
  if (ui.conn === "denied" || !ui.token) {
    root.replaceChildren(
      h(
        "div",
        { class: "gate" },
        h("p", { class: "gate-title" }, NAME),
        h("p", { class: "gate-line" }, "Open the page again from its link."),
      ),
    );
    content = null;
    return;
  }
  const keep = document.activeElement?.dataset?.key;
  const caret = caretOf(document.activeElement);
  const scroll = content ? content.scrollTop : 0;
  const chat = chatScroll();
  const side = root.querySelector(".sidebar-content, .mini-scroll")?.scrollTop || 0;
  const paneScroll = root.querySelector(".fx-pane-in")?.scrollTop || 0; // All sessions' details scroll on their own
  if (!content) content = h("div", { class: "content" });
  const inner = page(); // before the top bar, which counts what this settles
  content.className = "content" + (inner.classList.contains("chat-page") ? " content-chat" : "");
  content.replaceChildren(inner);
  const skip = h("a", { class: "skip-link", href: "#", "data-key": "skip", onclick: skipToContent }, "Skip to content");
  // replaceChildren, unlike h(), writes null and false out as text: only what is there goes in.
  const layers = [ui.setup && !ui.setup.inline && setupDialog(), ui.tagMenu && tagMenu(), ui.rowMenu && rowMenu(), ui.pickMenu && pickMenu()];
  root.replaceChildren(skip, h("div", { class: "app" }, sidebar(), h("main", { class: "main" }, NARROW.matches ? topbar() : null, content)), ...layers.filter((x) => x instanceof Node));
  content.scrollTop = scroll;
  fleetFit();
  fleetRoving();
  recentRoving();
  fitPreviews();
  const pane = root.querySelector(".fx-pane-in");
  if (pane) pane.scrollTop = paneScroll;
  const sideNow = root.querySelector(".sidebar-content, .mini-scroll");
  if (sideNow) sideNow.scrollTop = side;
  sideFade(root.querySelector(".sidebar-content"));
  restoreChatScroll(chat);
  if (keep) {
    const el = root.querySelector(`[data-key="${CSS.escape(keep)}"]`);
    el?.focus({ preventScroll: true });
    restoreCaret(el, caret);
  }
  chatRoving(); // after focus is back where it was
  sideRoving();
  fitComposers();
  document.title = (unreadCount() ? "(" + unreadCount() + ") " : "") + NAME;
  queueMicrotask(markOpenRead);
}

// "Skip to content", first for Tab and shown when it has focus: past the sidebar, to the page's
// search where it has one, else its heading.
function skipToContent(e) {
  e.preventDefault();
  const to = root.querySelector('.main [data-key="fx-q"], .main [data-key="cq"]') || root.querySelector(".main h1, .main h2");
  if (!to) return;
  if (!to.matches("input, button, a")) to.tabIndex = -1;
  to.focus();
}

// A rebuild makes new fields, whose caret starts at one end.
function caretOf(el) {
  try {
    return el && typeof el.selectionStart === "number" ? [el.selectionStart, el.selectionEnd] : null;
  } catch {
    return null;
  }
}

function restoreCaret(el, caret) {
  try {
    if (el && caret) el.setSelectionRange(caret[0], caret[1]);
  } catch {}
}

function navButton(view, iconName, count) {
  return h(
    "li",
    null,
    h(
      "button",
      {
        class: "menu-button",
        "aria-current": ui.view === view ? "page" : null,
        "data-key": "nav-" + view,
        onclick: () => {
          ui.filter = null;
          go(view);
        },
      },
      icon(iconName),
      h("span", { class: "label" }, TITLES[view]),
      count != null && h("span", { class: "menu-count" }, count),
    ),
  );
}

// The sidebar is the sessions themselves, under their devices: every session, every conversation
// and the newest messages at the top, Settings at the foot. It folds to a strip of faces, and
// remembers that.
const MINI_KEY = NAME + ":sidebar-mini";
let mini = false;
try {
  mini = localStorage.getItem(MINI_KEY) === "1";
} catch {}
function setMini(v) {
  mini = v;
  try {
    localStorage.setItem(MINI_KEY, v ? "1" : "0");
  } catch {}
  render();
}

function miniSidebar() {
  const pick = (view) => () => {
    ui.filter = null;
    if (view === "messages") ui.conversation = null;
    go(view);
  };
  const top = (view, iconName, title, current) =>
    h("button", { class: "mini-btn" + (current ? " current" : ""), title, "aria-label": title, "data-key": "mini-" + view, onclick: pick(view) }, icon(iconName));
  return h(
    "aside",
    { class: "sidebar mini", "data-app-sidebar": true, "aria-label": NAME },
    h("button", { class: "mini-btn mini-open", title: "Show the sidebar", "aria-label": "Show the sidebar", "data-key": "mini-open", onclick: () => setMini(false) }, icon("panel")),
    h(
      "div",
      { class: "mini-scroll" },
      miniYou(),
      miniSessions(),
      top("messages", "message", "All conversations", ui.view === "messages" && !ui.filter),
      top("recent", "clock", "Recent messages", ui.view === "recent"),
      ui.tagFilter &&
        h(
          "button",
          {
            class: "mini-btn current",
            title: "Tagged " + ui.tagFilter + ": show every session",
            "aria-label": "Tagged " + ui.tagFilter + ": show every session",
            "data-key": "mini-tag",
            onclick: () => {
              ui.tagFilter = null;
              render();
            },
          },
          icon("tag"),
        ),
      devices().map((d) => {
        const live = shownSessions(liveSessions(d));
        if (ui.tagFilter && !live.length) return null;
        return [
          h("div", { class: "mini-dev", title: deviceLabel(d) + " · " + live.length }, deviceDot(d)),
          live.map((s) => {
            const key = s.addr || s.name;
            return h(
              "button",
              {
                class: "mini-face" + (ui.view === "messages" && ui.filter === key ? " current" : ""),
                title: s.name + (s.doing ? ": " + s.doing : ""),
                "aria-label": s.name,
                "data-key": "mini-s-" + s.name,
                onclick: () => {
                  ui.filter = key;
                  ui.conversation = null;
                  go("messages");
                },
              },
              presence(s.name, d),
            );
          }),
        ];
      }),
    ),
    miniUpdate(),
    h("button", { class: "mini-btn mini-foot" + (ui.view === "settings" ? " current" : ""), title: "Settings", "aria-label": "Settings", onclick: () => go("settings") }, icon("settings")),
  );
}

// All sessions, with a yellow dot while a question waits on one of them.
function miniSessions() {
  const n = ui.state ? runningNeeds().size : 0;
  const label = n ? "All sessions, " + n + (n === 1 ? " needs" : " need") + " a look" : "All sessions";
  return h(
    "button",
    {
      class: "mini-btn" + (ui.view === "sessions" ? " current" : ""),
      title: label,
      "aria-label": label,
      "data-key": "mini-sessions",
      onclick: () => {
        ui.filter = null;
        go("sessions");
      },
    },
    icon("users"),
    n > 0 && h("span", { class: "mini-need" }),
  );
}

function miniYou() {
  const n = unreadCount();
  return h(
    "button",
    { class: "mini-btn" + (isMyFilter() ? " current" : ""), title: "Your messages", "aria-label": n ? "Your messages, " + n + " unread" : "Your messages", "data-key": "mini-you", onclick: openYours },
    icon("user"),
    n > 0 && h("span", { class: "mini-unread" }),
  );
}

function sidebar() {
  if (mini) return miniSidebar();
  const devs = devices();
  const needs = ui.state ? runningNeeds() : new Map();
  return h(
    "aside",
    { class: "sidebar", "data-app-sidebar": true, "aria-label": NAME },
    h(
      "div",
      { class: "sidebar-header" },
      h("span", { class: "brand" }, NAME),
      h("button", { class: "fold-btn", title: "Hide the sidebar", "aria-label": "Hide the sidebar", "data-key": "fold", onclick: () => setMini(true) }, icon("panel")),
    ),
    h(
      "div",
      { class: "sidebar-content", onscroll: (e) => sideFade(e.currentTarget) },
      h(
        "div",
        { class: "sidebar-group g-top" },
        yourRow(),
        topRow("sessions", "users", "All sessions", ui.state ? devs.reduce((n, d) => n + liveSessions(d).length, 0) : null, needs.size),
        allButton(),
        topRow("recent", "clock", "Recent messages", ui.messages.length),
      ),
      ui.notice && h("button", { class: "side-notice", title: "Dismiss", onclick: () => ((ui.notice = ""), render()) }, ui.notice),
      devs.length > 0 && h("div", { class: "side-head" }, "Devices"),
      devs.map((d) => {
        const live = shownSessions(liveSessions(d));
        if (ui.tagFilter && !live.length && !shownSessions(quietSessions(d)).length) return null;
        const open = sideOpen(d);
        const owing = live.some((s) => needs.has(s.name));
        return h(
          "div",
          { class: "sidebar-group dev-group" },
          h(
            "button",
            {
              class: "device-link dev-fold" + (d.reachable ? "" : " stale"),
              "aria-expanded": open ? "true" : "false",
              "data-key": "dev-" + d.id,
              title: (d.reachable ? d.id : d.id + ", last seen " + ago(when(d.lastSeen) || 0)) + (open ? "" : owing ? " · a session here needs a look" : "") + " · " + plural(live.length, "session"),
              onclick: () => {
                sideOpenSet(d, !open);
                render();
              },
            },
            h("span", { class: "label", title: deviceLabel(d) }, deviceDot(d), deviceLabel(d)),
            d.reachable ? h("span", { class: "menu-count" }, live.length) : h("span", { class: "menu-count" }, "offline"),
          ),
          open &&
          live.length > 0 &&
            h(
              "ul",
              { class: "sidebar-menu side-sessions" },
              live.map((s) => {
                const key = s.addr || s.name;
                return h(
                  "li",
                  { class: "side-li" },
                  h(
                    "button",
                    {
                      class: "side-session",
                      "aria-current": ui.view === "messages" && ui.filter === key ? "page" : null,
                      "data-key": "ss-" + s.name,
                      title: s.doing ? s.name + ": " + s.doing : s.name,
                      onclick: () => {
                        ui.filter = key;
                        ui.conversation = null;
                        go("messages");
                      },
                    },
                    ...sideSessionBody(d, s, needs.has(s.name)),
                  ),
                );
              }),
            ),
          open && quietFold(d),
          open && !ui.tagFilter && settledFold(d.id),
        );
      }),
      peopleGroup(),
    ),
    sidebarFoot(),
  );
}

// Which device sections of the sidebar are open, remembered across reloads: this device's starts
// open, the others folded, so the sidebar holds a whole team without scrolling past each session.
const SIDE_OPEN_KEY = NAME + ":side-open";

function sideOpenAll() {
  try {
    return JSON.parse(localStorage.getItem(SIDE_OPEN_KEY) || "{}") || {};
  } catch {
    return {};
  }
}

function sideOpen(d) {
  const all = ui.sideOpen || (ui.sideOpen = sideOpenAll());
  return Object.hasOwn(all, d.id) ? !!all[d.id] : !!d.self;
}

function sideOpenSet(d, open) {
  const all = { ...sideOpenAll(), [d.id]: open };
  ui.sideOpen = all;
  try {
    localStorage.setItem(SIDE_OPEN_KEY, JSON.stringify(all));
  } catch {}
}

// The foot says who you are here, once you are someone.
function sidebarFoot() {
  const a = me();
  if (!a) return h("div", { class: "sidebar-foot" }, h("ul", { class: "sidebar-menu" }, updateItem(), navButton("settings", "settings", null)));
  const update = updateItem();
  return h(
    "div",
    { class: "sidebar-foot" },
    update && h("ul", { class: "sidebar-menu" }, update),
    h(
      "div",
      { class: "foot-account" },
      h(
        "button",
        { class: "account-chip", title: "Your account", "data-key": "account-chip", onclick: () => go("settings") },
        avatar(a.name),
        h("span", { class: "account-name truncate" }, a.name),
      ),
      h("button", { class: "foot-settings", title: "Settings", "aria-label": "Settings", "aria-current": ui.view === "settings" ? "page" : null, "data-key": "nav-settings", onclick: () => go("settings") }, icon("settings")),
    ),
  );
}

// Your messages, at the top of the sidebar, with what is unread. With no account yet, it is where
// you get one.
function yourRow() {
  const a = me();
  const n = unreadCount();
  const count = a ? yourConversationCount() : 0;
  const title = !ui.state ? null : !a ? "Talk to your agents" : a.taken ? takenLine(a) : n ? n + " unread" : count ? plural(count, "conversation") : "No messages yet";
  return h(
    "button",
    { class: "all-convs top-you", "aria-current": isMyFilter() ? "page" : null, "data-key": "top-you", title, onclick: openYours },
    h("span", { class: "all-ico ico-you" }, icon("user")),
    h("span", { class: "all-title" }, "Your messages"),
    n > 0 ? h("span", { class: "unread-badge", "aria-label": n + " unread" }, n) : count > 0 && h("span", { class: "all-n" }, count),
  );
}

// The human tag (#267): a person's account has it, worked out from the address, and nothing else
// does. It looks like the session tags it sits among, and can't be taken off.
function humanTag() {
  return h("span", { class: "badge tag badge-outline tag-human", title: "A person, not an agent. " + NAME + " gives this tag to every person's account, and only to one." }, "human");
}

function peopleGroup() {
  const others = people().filter((p) => p.name !== myName());
  if (!others.length) return null;
  return h(
    "div",
    { class: "sidebar-group dev-group people-group" },
    h("div", { class: "side-head" }, "People"),
    h(
      "ul",
      { class: "sidebar-menu side-sessions" },
      others.map((p) =>
        h(
          "li",
          { class: "side-li" },
          h(
            "button",
            { class: "side-session", "data-key": "person-" + p.name, title: "Message " + p.name + " · " + (personOnline(p) ? "On " + NAME + " now" : deviceLabel(p.device)), onclick: () => startChat(p.name) },
            stateDot(personOnline(p) ? "on" : "off", "side-dot"),
            h("span", { class: "side-session-name truncate" }, p.name),
          ),
        ),
      ),
    ),
  );
}

// A newer release (#240). Like T3 Code's sidebar and Discord's top bar, it is small, it stays until
// it's dealt with, and it acts in one click: a restart onto an installed update, or the way to
// install one by hand.
const RESTART_WAIT_MS = 20000; // how long the page waits for the new daemon before giving up

function restartStuck() {
  return ui.restarting > 0 && ui.conn === "down" && Date.now() - ui.restarting > RESTART_WAIT_MS;
}

function updateInfo() {
  const v = ui.state?.version;
  if (!v) return null;
  const to = v.latest ? " " + v.latest : "";
  if (ui.restarting) {
    return restartStuck()
      ? { kind: "busy", text: "Run " + COMMAND + " ui again", title: "The restarted daemon serves the page at a new address." }
      : { kind: "busy", text: "Restarting", title: "Restarting onto" + (to || " the update") + ". The page reconnects on its own." };
  }
  if (v.ready) return { kind: "ready", text: "Restart to update", title: NAME + to + " is installed. Restart its daemon onto it now.", act: restartNow };
  if (!v.latest) return null;
  if (v.auto) return { kind: "auto", text: "Updating to " + v.latest, title: NAME + " installs it on its own." };
  return { kind: "manual", text: v.latest + " is out", title: "Updates are off or failing here. Settings has the command that installs it.", act: () => go("settings") };
}

function updateIcon(info) {
  return info.kind === "busy" && !restartStuck() ? icon("rotateCw", "spin") : icon("download");
}

function updateItem() {
  const info = updateInfo();
  if (!info) return null;
  const cls = "menu-button update update-" + info.kind;
  // Ready is a quiet line with a green dot; Version in Settings has the Restart button too.
  const kids = [info.kind === "ready" ? h("span", { class: "update-dot", "aria-hidden": "true" }) : updateIcon(info), h("span", { class: "label" }, info.text)];
  return h(
    "li",
    null,
    info.act
      ? h("button", { class: cls, title: info.title, "data-key": "update", onclick: info.act }, kids)
      : h("div", { class: cls, title: info.title, role: "status" }, kids),
  );
}

function miniUpdate() {
  const info = updateInfo();
  if (!info) return null;
  const label = info.text + ". " + info.title;
  return h(
    "button",
    { class: "mini-btn mini-update update-" + info.kind, title: label, "aria-label": label, "data-key": "mini-update", disabled: !info.act, onclick: info.act },
    updateIcon(info),
  );
}

async function restartNow() {
  if (ui.restarting) return;
  ui.restarting = Date.now();
  delete ui.rowErrors.version;
  render();
  try {
    await api("/api/restart", { method: "POST" });
  } catch (e) {
    ui.restarting = 0;
    if (e instanceof AuthError) return deny(e.token);
    ui.rowErrors.version = "Couldn't restart: " + e.message;
    render();
    return;
  }
  poll();
}

// Settings' Version section: this version, and what is known of a newer one.
function versionSection() {
  const v = ui.state?.version;
  if (!v?.running) return null;
  let description = null;
  let control = null;
  if (ui.restarting) {
    description = restartStuck() ? "Run " + COMMAND + " ui again to reopen the page." : "Restarting onto " + (v.latest || "the update") + ".";
  } else if (v.ready) {
    description = (v.latest || "A newer version") + " is installed and starts when the daemon restarts.";
    control = h("button", { class: "btn btn-outline btn-xs", "data-key": "version-restart", onclick: restartNow }, "Restart now");
  } else if (v.latest && v.auto) {
    description = v.latest + " is out, and installing on its own.";
  } else if (v.latest) {
    description = v.latest + " is out. Updates are off or failing here; this installs it:";
  }
  const current = !ui.restarting && !v.ready && !v.latest;
  const row = settingsRow("version", [NAME + " " + v.running, current && h("span", { class: "set-uptodate" }, "· Up to date")], description, control);
  if (v.latest && !v.auto && !v.ready && !ui.restarting && v.install) {
    row.append(
      h(
        "div",
        { class: "install-line" },
        h("code", null, v.install),
        h(
          "button",
          {
            class: "btn btn-ghost btn-xs",
            title: "Copy",
            "aria-label": "Copy the command",
            "data-key": "copy-install",
            onclick: async () => {
              try {
                await navigator.clipboard.writeText(v.install);
              } catch {
                return;
              }
              ui.copiedAt = Date.now();
              render();
              setTimeout(render, SAVED_MS);
            },
          },
          Date.now() - (ui.copiedAt || 0) < SAVED_MS ? [icon("check"), h("span", { role: "status" }, "Copied")] : icon("copy"),
        ),
      ),
    );
  }
  return h("section", { class: "section" }, h("div", { class: "section-head" }, h("h2", { class: "section-title" }, "Version")), h("div", { class: "group" }, row));
}

// Four states, as a colored dot on the face:
//   on    green   reachable, and active in the last five minutes
//   idle  yellow  reachable, but quiet for longer
//   dnd   red     running, but on do not disturb: messages wait in its inbox
//   off   grey    its device is unreachable
const STATE_WORDS = { on: "Active", idle: "Idle", dnd: "Do not disturb", off: "Offline" };
const PERSON_TIPS = { on: "On " + NAME + " now", off: "Not on " + NAME + " now" };

// The products, by the names people know them by, wherever an agent is named.
const AGENT_NAMES = { "claude-code": "Claude Code", codex: "Codex", t3: "T3 Code" };
function agentName(a) {
  return (a && AGENT_NAMES[a]) || a || "";
}

// A model: tag (#211, derived for T3 sessions), shown beside the agent.
function modelOf(s) {
  const t = (s.tags || []).find((x) => x && typeof x.name === "string" && x.name.startsWith("model:"));
  return t ? t.name.slice(6) : "";
}

function sessionState(d, s) {
  if (!d || !d.reachable) return "off";
  if (s && (onDnd(s) || wakeNever(d, s))) return "dnd";
  const t = s && when(s.lastActive);
  return t && Date.now() - t < 5 * 60000 ? "on" : "idle";
}

// The do-not-disturb tag (#211) rides in every device's presence, so it works for any device's
// sessions: { tags: [{ name, src }] }, src "" (set by the session), "human" or "derived".
function onDnd(s) {
  return (s.tags || []).some((t) => t && t.name === "do-not-disturb");
}

// This device's settings say which of its sessions have wake-ups switched off.
function wakeNever(d, s) {
  if (!d.self || !ui.settings) return false;
  const own = ui.settings.sessionWake?.[s.name];
  return (own ? own.when : ui.settings.wake?.when) === "never";
}

// Quiet: running, but idle for a day, or hidden by a person with the hidden tag (#211). Agents
// in Claude Code, Codex and the rest never settle, so a session left open in a forgotten terminal
// stays in presence for good; this keeps it out of the way. An idle one comes back once it does
// anything. A hidden one stays hidden until someone shows it again: a person's tag stays until a
// person takes it off, and the fold still shows its dot.
const QUIET_MS = 24 * 60 * 60 * 1000;
const HIDDEN = "hidden";

// A Hide or Show this page has asked for, by address, until the state it polls agrees: another
// device's sessions carry the tag only once that device has read the change.
const hideWanted = new Map();

function isHidden(s) {
  const tagged = (s.tags || []).some((t) => t && t.name === HIDDEN);
  if (!hideWanted.has(s.addr)) return tagged;
  if (hideWanted.get(s.addr) === tagged) {
    hideWanted.delete(s.addr);
    return tagged;
  }
  return hideWanted.get(s.addr);
}

function isQuiet(d, s) {
  if (isHidden(s)) return true;
  const t = when(s.lastActive) || when(s.started);
  return !!t && Date.now() - t > QUIET_MS;
}

function liveSessions(d) {
  return d.sessions.filter((s) => !isQuiet(d, s));
}

function quietSessions(d) {
  return d.sessions.filter((s) => isQuiet(d, s));
}

// Hiding is a tag a person sets, so it holds on every device that shows the session.
async function setHidden(s, on) {
  ui.notice = "";
  try {
    await api("/api/tags", { method: "POST", body: JSON.stringify({ addr: s.addr, add: on ? [HIDDEN] : [], remove: on ? [] : [HIDDEN] }) });
    hideWanted.set(s.addr, on);
  } catch (e) {
    if (e instanceof AuthError) return deny(e.token);
    ui.notice = "Couldn't " + (on ? "hide " : "show ") + s.name + ": " + e.message;
  }
  render();
}

// Each device's quiet sessions, folded under its live ones.
function quietFold(d) {
  const rows = shownSessions(quietSessions(d));
  if (!rows.length) return null;
  ui.openQuiet = ui.openQuiet || new Set();
  const open = ui.openQuiet.has(d.id);
  return h(
    "div",
    { class: "settled" },
    h(
      "button",
      {
        class: "settled-toggle",
        "aria-expanded": open ? "true" : "false",
        "data-key": "quiet-" + d.id,
        onclick: () => {
          open ? ui.openQuiet.delete(d.id) : ui.openQuiet.add(d.id);
          render();
        },
      },
      icon(open ? "chevronDown" : "chevronRight", "settled-caret"),
      h("span", null, rows.length + " quiet"),
    ),
    open &&
      h(
        "div",
        { class: "settled-list" },
        rows.map((s) => {
          const key = s.addr || s.name;
          const t = when(s.lastActive);
          return h(
            "div",
            { class: "quiet-item" },
            h(
              "button",
              {
                class: "settled-row" + (ui.view === "messages" && ui.filter === key ? " current" : ""),
                title: s.doing || s.name,
                "data-key": "q-" + s.name,
                onclick: () => {
                  ui.filter = key;
                  ui.conversation = null;
                  go("messages");
                },
              },
              h("span", { class: "settled-name truncate" }, s.name),
              h("span", { class: "settled-when", title: t ? stamp(t) : null }, isHidden(s) ? "hidden" : t ? ago(t).replace(" ago", "") : ""),
            ),
          );
        }),
      ),
  );
}

// Someone's state, in the words that go with its dot: "Active now", "Idle 22m", "On Murmur now".
function presenceOf(name, d) {
  const p = findPerson(name);
  if (p) {
    const st = personOnline(p) ? "on" : "off";
    return { st, word: PERSON_TIPS[st] };
  }
  const f = findSession(name);
  if (f) return { st: sessionState(f.device, f.session), word: stateWord(f.device, f.session) };
  if (d) {
    const st = sessionState(d, null);
    return { st, word: STATE_WORDS[st] };
  }
  return { st: "off", word: isSettled(name) ? "Settled" : "Not running" };
}

// A face and its dot are one picture, named for who it is and what state they are in, so the dot's
// colour is never all that says it.
function labelled(el, name, word) {
  el.removeAttribute("aria-hidden");
  el.setAttribute("role", "img");
  el.setAttribute("aria-label", name + " · " + word);
  el.title = name + " · " + word;
  return el;
}

// Every state dot, wherever it is: its shape says the state as well as its colour, as Slack's do,
// so it reads without telling green from yellow from red. Active and idle are filled, do not disturb
// has a white bar across it, and offline is a hollow ring. cls sizes and places it.
function stateDot(st, cls) {
  return h("span", { class: cls + " sdot sdot-" + st, "aria-hidden": "true" });
}

// A face with its presence dot inside it.
function dotted(name) {
  const a = avatar(name);
  const { word } = presenceOf(name);
  a.classList.add("dotted");
  return labelled(a, name, word);
}

// A device: grey when unreachable, green when any of its sessions is active, else yellow.
function deviceState(d) {
  if (!d || !d.reachable) return "off";
  return d.sessions.some((s) => sessionState(d, s) === "on") ? "on" : "idle";
}
const DEVICE_WORDS = { on: "Online, a session is active", idle: "Online, all its sessions idle", off: "Unreachable" };

function deviceDot(d, cls = "") {
  const st = deviceState(d);
  const dot = stateDot(st, ("dev-dot pres-" + st + " " + cls).trim());
  dot.removeAttribute("aria-hidden");
  dot.setAttribute("role", "img");
  dot.setAttribute("aria-label", DEVICE_WORDS[st]);
  dot.title = DEVICE_WORDS[st];
  return dot;
}

// A face, named with its state for screen readers. No dot on it (#183): where the state matters, its
// dot and word sit beside the face.
function presence(name, d) {
  const { word } = presenceOf(name, d);
  return labelled(h("span", { class: "pres-wrap" }, avatar(name)), name, word);
}

// The sidebar's top rows, one line each: an icon, a title and a count. Questions waiting take the
// count's place on All sessions, as unread mail does: the number to act on.
function topRow(view, iconName, title, n, need = 0) {
  return h(
    "button",
    {
      class: "all-convs top-" + view,
      "aria-current": ui.view === view ? "page" : null,
      "data-key": "top-" + view,
      onclick: () => {
        ui.filter = null;
        go(view);
      },
    },
    h("span", { class: "all-ico" }, icon(iconName)),
    h("span", { class: "all-title" }, title),
    need ? h("span", { class: "all-need", title: need + (need === 1 ? " needs" : " need") + " a look" }, need + " waiting") : n != null && h("span", { class: "all-n" }, n),
  );
}

// The sidebar fades at its foot while there is more of it below.
function sideFade(el) {
  el?.classList.toggle("more", el.scrollHeight - el.scrollTop - el.clientHeight > 4);
}

// Settled sessions: stopped, or settled in T3, so their device leaves them out of presence (#189).
// The page knows them only from messages, by address, so each is grouped under the device its
// address names. Hidden by default, never gone.
function settledFold(id) {
  const rows = settledOn(id);
  if (!rows.length) return null;
  ui.openSettled = ui.openSettled || new Set();
  const open = ui.openSettled.has(id);
  return h(
    "div",
    { class: "settled" },
    h(
      "button",
      {
        class: "settled-toggle",
        "aria-expanded": open ? "true" : "false",
        "data-key": "fold-" + id,
        onclick: () => {
          open ? ui.openSettled.delete(id) : ui.openSettled.add(id);
          render();
        },
      },
      icon(open ? "chevronDown" : "chevronRight", "settled-caret"),
      h("span", null, plural(rows.length, "settled session")),
    ),
    open && h("div", { class: "settled-list" }, rows.map(settledRow)),
  );
}

// Worked out again only when the state or the messages change: a render asks many times.
let settledMemo = { state: null, n: -1, cursor: null, learned: -1, rows: [], names: new Set() };

function settledSessions() {
  const memo = settledMemo;
  if (memo.state === ui.state && memo.n === ui.messages.length && memo.cursor === ui.cursor && memo.learned === namesLearned) return memo.rows;
  const rows = findSettled();
  settledMemo = { state: ui.state, n: ui.messages.length, cursor: ui.cursor, learned: namesLearned, rows, names: new Set(rows.map((r) => r.name)) };
  return rows;
}

function findSettled() {
  // Running by name or by address: presence can still carry a session's device/session while the
  // messages already have the name that landed for it.
  const running = new Set();
  const runningAddrs = new Set();
  for (const d of ui.state?.devices || []) {
    for (const s of d.sessions || []) {
      running.add(s.name);
      running.add(nameOf(s.addr, s.name));
      if (s.addr) runningAddrs.add(s.addr);
    }
  }
  const seen = new Map();
  const note = (name, addr, m) => {
    if (!name || running.has(name) || (addr && runningAddrs.has(addr)) || name.includes("/") || isPerson(name)) return;
    const r = seen.get(name) || { name, device: "", last: 0, lastMsg: null, peers: new Set() };
    if (addr && addr.includes("/")) {
      r.device = addr.split("/")[0];
      r.addr = addr;
    }
    const t = when(m.at) || 0;
    if (t > r.last) {
      r.last = t;
      r.lastMsg = m;
    }
    seen.set(name, r);
  };
  for (const m of ui.messages) {
    note(fromName(m), m.fromAddr, m);
    if (m.toKind === "session") note(toName(m), m.toAddr, m);
    if (m.toKind === "session") {
      const a = seen.get(fromName(m));
      if (a) a.peers.add(toName(m));
      const b = seen.get(toName(m));
      if (b) b.peers.add(fromName(m));
    }
  }
  return [...seen.values()].sort((a, b) => b.last - a.last);
}

function settledOn(id) {
  return settledSessions().filter((r) => r.device === id);
}

function isSettled(name) {
  settledSessions();
  return settledMemo.names.has(name) && !findSession(name);
}

function settledRow(r) {
  return h(
    "button",
    {
      class: "settled-row" + (ui.view === "messages" && ui.filter && nameOf(ui.filter, ui.filter) === r.name ? " current" : ""),
      title: r.name + ", last seen " + ago(r.last),
      "data-key": "settled-" + r.name,
      onclick: () => {
        ui.filter = r.addr || r.name;
        ui.conversation = null;
        go("messages");
      },
    },
    h("span", { class: "settled-name truncate" }, r.name),
    h("span", { class: "settled-when", title: stamp(r.last) }, ago(r.last).replace(" ago", "")),
  );
}

// A device's one name, wherever it is shown: "This device", or its host as people say it, with its
// owner's name when the host doesn't already carry it ("maya-mbp", "Tyler's mac-mini").
function deviceLabel(d) {
  if (d.self) return "This device";
  const dev = d.host || host(d.id);
  return (d.owner && !dev.toLowerCase().includes(d.owner.toLowerCase()) ? d.owner + "'s " : "") + dev;
}

// A device ID ends in four hex digits that tell two clones on one machine apart.
function host(id) {
  return String(id).replace(/-[0-9a-f]{4}$/, "");
}

// The sidebar is a few stops for Tab, not one a row: People and each device's sessions (with their
// folds) are one stop each, moved through with the up and down arrows, Home and End, and the tag
// chips are one, moved through with the arrows either way. The stop is the focused row, else the
// current one, else the first.
function sideStops() {
  const out = [];
  for (const g of root.querySelectorAll(".sidebar-content .dev-group")) {
    const tags = [...g.querySelectorAll(".tag-filter .tf")];
    const rows = [...g.querySelectorAll("button.dev-fold, button.side-session, button.settled-toggle, button.settled-row")];
    if (tags.length) out.push({ items: tags, across: true });
    else if (rows.length) out.push({ items: rows, across: false });
  }
  return out;
}

function sideRoving() {
  for (const { items } of sideStops()) {
    const want = items.find((el) => el === document.activeElement) || items.find((el) => el.getAttribute("aria-current") === "page" || el.classList.contains("current") || el.getAttribute("aria-pressed") === "true") || items[0];
    for (const el of items) el.tabIndex = el === want ? 0 : -1;
  }
}

document.addEventListener("keydown", (e) => {
  if (e.altKey || e.ctrlKey || e.metaKey || !e.target?.closest?.(".sidebar-content")) return;
  const stop = sideStops().find((st) => st.items.includes(e.target));
  if (!stop) return;
  const i = stop.items.indexOf(e.target);
  const moves = { ArrowUp: i - 1, ArrowDown: i + 1, Home: 0, End: stop.items.length - 1 };
  if (stop.across) Object.assign(moves, { ArrowLeft: i - 1, ArrowRight: i + 1 });
  const to = moves[e.key];
  if (to == null) return;
  e.preventDefault();
  const next = stop.items[Math.max(0, Math.min(stop.items.length - 1, to))];
  for (const el of stop.items) el.tabIndex = el === next ? 0 : -1;
  next.focus();
});

// Every conversation, between the sidebar's other two top rows.
function allButton() {
  const all = ui.view === "messages" && !ui.filter;
  const count = allConversations().filter(isCounted).length;
  return h(
    "button",
    {
      class: "all-convs",
      "aria-current": all ? "page" : null,
      "data-key": "all",
      onclick: () => {
        ui.filter = null;
        ui.conversation = null;
        go("messages");
      },
    },
    h("span", { class: "all-ico" }, icon("message")),
    h("span", { class: "all-title" }, "All conversations"),
    h("span", { class: "all-n" }, count),
  );
}

function allConversations() {
  const keep = ui.filter;
  ui.filter = null;
  const list = conversations();
  ui.filter = keep;
  return list;
}

// A session in the sidebar is one line, for getting around, as in Slack's and Discord's lists: its
// face and state, its name, and the yellow dot that marks it in All sessions when it owes an answer.
// All sessions has the rest.
function sideSessionBody(d, s, owes) {
  return [h("span", { class: "side-session-name truncate" }, s.name), owes && h("span", { class: "sr-only" }, ", needs a look")];
}

function stateWord(d, s) {
  const st = sessionState(d, s);
  const t = when(s.lastActive);
  if (st === "on") return "Active now";
  if (st === "idle") return t ? "Idle " + ago(t).replace(" ago", "") : "Idle";
  return STATE_WORDS[st];
}

// All sessions (#183): every session in one table, to answer "what is each one doing, and does any
// of them need a person?" for a dozen at once. Rows group by device, branch or job; a search, a
// state filter and sortable columns narrow and order them, and one opens in a pane beside the
// table. Running, Quiet and Settled are tabs over it: quiet sessions are the same table, settled
// ones a list, since the page knows them only from their messages.

// What needs a person: a direct question, from a session or a person, that the session it was put
// to hasn't answered for 15 minutes. A question stops counting after a day. Offline and do not
// disturb explain why a question waits rather than being reasons of their own: closed laptops
// overnight would otherwise flag every morning. Idle sessions and two sessions on one branch are
// normal work, so they aren't reasons.
const NO_ANSWER_MS = 15 * 60000;
const GIVE_UP_MS = 24 * 3600000;

// The question a message asks: its subject if that is one and starts the thread, or else the first
// sentence of its body that ends in "?", or else of its subject (send makes the body optional).
// Code blocks and quoted lines ("> ...", usually the question being answered) are left out, since
// their "?"s aren't this message's questions. Inline code and links are set aside while sentences
// are found, so their "?" or "." can't end one, and put back plainly: code without its backticks,
// a link as its text or as what it points at ("#36", a file name). A "?" may sit inside closing
// marks (**bold?**, (aside?), asked: "quoted?"), and a period ends a sentence only before a capital.
// Worked out once a message: a render asks for many.
const questions = new WeakMap();

function questionIn(m) {
  if (!questions.has(m)) {
    const subject = (m.subject || "").trim();
    const q = /\?\s*$/.test(subject) && !isReply(m) ? subject : askedIn(m.body || "") || (isReply(m) ? "" : askedIn(subject));
    questions.set(m, cutAtWord(q, 110));
  }
  return questions.get(m);
}

function safeDecode(x) {
  try {
    return decodeURIComponent(x);
  } catch {
    return x;
  }
}

function askedIn(raw) {
  const kept = [];
  const keep = (x) => "" + (kept.push(x) - 1) + "";
  // A link says what it points at: "#36" for a pull request or issue, a short SHA for a commit,
  // the last part of its path for anything else (a file, a page), or its host.
  const link = (url) => {
    const [where, ...path] = url.replace(/^https?:\/\//, "").split(/[?#]/)[0].split("/").filter(Boolean);
    const n = url.match(/\/(?:pull|issues)\/(\d+)/);
    const sha = url.match(/\/commit\/([0-9a-f]{7,40})/i);
    return n ? "#" + n[1] : sha ? sha[1].slice(0, 7) : path.length ? safeDecode(path[path.length - 1]) : where;
  };
  const text = raw
    .replace(/```[\s\S]*?```/g, " ")
    .split("\n")
    .filter((line) => !/^\s*>/.test(line))
    // A single newline is a wrap inside a sentence. A blank line, a heading, a list item or the
    // line after a heading starts a new one.
    .reduce((out, line, i, all) => {
      const starts = !line.trim() || !all[i - 1]?.trim() || /^\s*(?:#|[-*+]\s|\d+[.)]\s)/.test(line) || /^\s*#/.test(all[i - 1]);
      return out + (i === 0 ? "" : starts ? "\n" : " ") + line;
    }, "")
    .replace(/`([^`\n]*)`/g, (_, c) => keep(c))
    .replace(/\[([^\]\n]+)\]\(https?:\/\/[^)\s]*\)/g, (_, t) => keep(t))
    .replace(/<(https?:\/\/[^>\s]+)>/g, "$1")
    // A link ends before the punctuation after it: "review https://…/pull/36?" still asks.
    .replace(/https?:\/\/[^\s<>]+/g, (u) => {
      const [, url, tail] = u.match(/^(.*?)([.,;:!?)\]'"”’]*)$/);
      return keep(link(url)) + tail;
    })
    // A quote that opens mid-sentence is something said, not asked: `I ran "what?" and it failed`
    // and `You asked "which runner?" Use windows-2025.` don't end at its "?". The sentence still
    // asks when it ends there: `Did you mean "Sure?"`. One that starts a sentence or follows a
    // colon is the question itself: `Jun asked: "is it signed?"`.
    // A straight quote opens only at the start or after a space or bracket; 27" is inches.
    .replace(/((?<=^|[\s(\[])"|“)([^"“”\n]*?)(["”])/g, (all, o, inner, c, at, str) => {
      const before = str.slice(0, at);
      const mid = /\S/.test(before.split(/[.!?]\s+|\n/).pop()) && !/:\s*$/.test(before);
      return mid ? o + inner.replace(/\?/g, "") + c : all;
    });
  const CLOSE = { '"': '"', "”": "“", "’": "‘", ")": "(", "]": "[", "*": "*", _: "_" };
  const clean = (x) => {
    const [, stem0, tail] = x.match(/^([\s\S]*?\?)([*_)\]"”’]*)$/);
    let stem = stem0;
    // Start after the mark that opens what the closing marks close, when that mark starts the
    // sentence or follows a colon: `asked: "is it?"` is “is it?”, but `say "Sure?"` stays whole.
    for (const c of new Set(tail)) {
      const at = stem.lastIndexOf(CLOSE[c]);
      if (at < 0) continue;
      if (/(?:^|:)\s*[*_(\["“‘]*$/.test(stem.slice(0, at))) stem = stem.slice(at + 1);
      else {
        stem = x;
        break;
      }
    }
    return stem
      .replace(/^\s*(?:[-*+]|\d+[.)])\s+(?:\[[ xX]\]\s+)?/, "")
      .replace(/^[*_\s]+/, "")
      .replace(/\s+/g, " ")
      .replace(/(\d+)/g, (_, i) => kept[i])
      .trim();
  };
  // "?" and "!" end a sentence; "." only before a capital, a digit or an opening mark, so "e.g. the
  // zip", "the U.S. build" and "3 p.m. your time" don't.
  const found = text
    .split(/(?<=[!?][*_)\]"”’]*)\s+|(?<=\.[*_)\]"”’]*)\s+(?=[\p{Lu}\d"“‘(\[*_])|\n+/u)
    .map((x) => x.trim().replace(//g, "?"))
    .find((x) => /\?[*_)\]"”’]*$/.test(x));
  return found ? clean(found) : "";
}

// Text no longer than max, cut at a word rather than inside one.
function cutAtWord(q, max) {
  if (q.length <= max) return q;
  const cut = q.slice(0, max - 1);
  const sp = cut.lastIndexOf(" ");
  return (sp > max * 0.55 ? cut.slice(0, sp) : cut).trimEnd() + "…";
}

// A reply is known by its "Re:" subject; the bus has no reply field. What a message's thread is
// about is its subject without "Re:", compared case-blind, so "Re: signing mur.exe" belongs with
// "Signing mur.exe".
function topicOf(m) {
  return (m.subject || "").replace(/^(re:\s*)+/i, "").trim();
}

function isReply(m) {
  return /^re:/i.test(m.subject || "");
}

// Whether the one asked has written back to the asker since: anything in this thread, or a new
// message elsewhere that asks nothing. A reply in another thread answers that thread, and a new
// question of its own answers nothing.
function answeredSince(m) {
  const to = toName(m);
  const from = fromName(m);
  const topic = topicOf(m).toLowerCase();
  return ui.messages.some((r) => r.sortAt > m.sortAt && r.toKind === "session" && fromName(r) === to && toName(r) === from && (topicOf(r).toLowerCase() === topic || (!isReply(r) && !questionIn(r))));
}

// Every question still waiting on a running session, worked out again only when the state, the
// messages or the minute change: the sidebar and the table ask for it many times a render.
let waitMemo = { key: null, list: [] };

function waitingQuestions() {
  const key = [ui.state, ui.messages.length, ui.cursor, namesLearned, Math.floor(Date.now() / 60000)];
  if (waitMemo.key && key.every((x, i) => x === waitMemo.key[i])) return waitMemo.list;
  const running = new Set();
  for (const d of ui.state?.devices || []) for (const s of d.sessions || []) running.add(s.name);
  const now = Date.now();
  const list = [];
  for (const m of ui.messages) {
    if (m.toKind !== "session") continue;
    const to = toName(m);
    const at = when(m.at);
    if (!running.has(to) || to === fromName(m) || !at || now - at < NO_ANSWER_MS || now - at > GIVE_UP_MS) continue;
    if (questionIn(m) && !answeredSince(m)) list.push(m);
  }
  waitMemo = { key, list };
  return list;
}

// Who owes an answer among rows ({ d, s }), by name: the oldest question first, with the rest. With
// all, questions marked answered count too (for the details' Undo lines).
function fleetNeeds(rows, all = false) {
  const names = new Set(rows.map((r) => r.s.name));
  const dismissed = fleetDismissed();
  const asks = new Map();
  for (const m of waitingQuestions()) {
    const to = toName(m);
    if (!names.has(to) || (!all && Object.hasOwn(dismissed, m.id))) continue;
    if (!asks.has(to)) asks.set(to, []);
    asks.get(to).push(m);
  }
  const out = new Map();
  for (const [to, list] of asks) {
    list.sort((a, b) => a.sortAt - b.sortAt);
    const m = list[0];
    const asker = fromName(m);
    // "More" counts conversations, as the details list them: a nudge in the same thread is one.
    const convs = new Set(list.map((x) => JSON.stringify([fromName(x), topicOf(x).toLowerCase()]))).size;
    const more = convs > 1 ? " And " + plural(convs - 1, "more question") + "." : "";
    // When the question isn't the subject (a body's "Fine?"), the subject says what it's about.
    const topic = topicOf(m);
    const about = topic && !topic.includes(questionIn(m).replace(/…$/, "")) ? " about “" + cutAtWord(topic, 60) + "”" : "";
    // An asker that has ended since still counts, since its question waits on a running session,
    // and says so. A person never ends.
    const ended = findSession(asker) || isPerson(asker) ? "" : " (ended)";
    // text is a function: a row and the pane each need their own copy of its nodes. Read from the
    // waiting side, and the wait ticks with the page's other times. A note on why it can't answer
    // yet goes after the wait, before the question, so it is never what gets cut short.
    const quote = "“" + questionIn(m) + "”" + more;
    out.set(to, {
      at: when(m.at),
      m,
      msgs: list,
      short: () => [h("b", null, asker), ended, " has waited ", agoEl("span", "fx-wait", when(m.at), "", true), " · ", "“" + questionIn(m) + "”"],
      text: (note) => [
        h("b", null, asker),
        ended,
        " has waited ",
        agoEl("span", "fx-wait", when(m.at), "", true),
        note ? [h("span", { class: "fx-reason-note" }, " · " + note), " · " + (about ? about.trim() + ": " : "") + quote] : about + ": " + quote,
      ],
    });
  }
  return out;
}

// Why a flagged session can't answer yet, beside its reason: it stays flagged, since the question
// still waits, but nobody should go looking for a reply.
function waitNote(d, s) {
  const st = sessionState(d, s);
  return st === "off" ? "offline, can't reply until it's back" : st === "dnd" ? "do not disturb" : "";
}

// Group by and the groups shut under it are remembered across reloads, since a person tends to
// work one way; the state filter and search start fresh. Both stored sets are read once and written
// as changes against what storage holds now, so two tabs don't overwrite each other; another tab's
// change arrives through the storage event.
const FLEET_BY_KEY = NAME + ":fleet-by";
const FLEET_SHUT_KEY = NAME + ":fleet-shut";

function fleetStored(key, empty) {
  try {
    return JSON.parse(localStorage.getItem(key) || empty);
  } catch {
    return JSON.parse(empty);
  }
}

function fleetShut() {
  if (!ui.fxCollapsed) ui.fxCollapsed = new Set(fleetStored(FLEET_SHUT_KEY, "[]").map(String));
  return ui.fxCollapsed;
}

function fleetShutSet(k, shut) {
  const all = new Set(fleetStored(FLEET_SHUT_KEY, "[]").map(String));
  shut ? all.add(k) : all.delete(k);
  try {
    localStorage.setItem(FLEET_SHUT_KEY, JSON.stringify([...all]));
  } catch {}
  ui.fxCollapsed = all;
}

// Questions a person has said nobody is waiting on (an answer the finder misread, or one given out
// of band), by message id and when, in this browser only. Kept two days, past the one a flag lasts.
const FLEET_DISMISSED_KEY = NAME + ":fleet-not-waiting";

function fleetDismissed() {
  if (!ui.fxDismissed) {
    ui.fxDismissed = {};
    for (const [id, at] of Object.entries(fleetStored(FLEET_DISMISSED_KEY, "{}"))) if (Date.now() - at < 2 * GIVE_UP_MS) ui.fxDismissed[id] = at;
  }
  return ui.fxDismissed;
}

// Marks msgs as not waiting (on true) or waiting again (on false, for Undo).
function fleetDismiss(msgs, on = true) {
  const all = fleetStored(FLEET_DISMISSED_KEY, "{}");
  for (const m of msgs) on ? (all[m.id] = Date.now()) : delete all[m.id];
  try {
    localStorage.setItem(FLEET_DISMISSED_KEY, JSON.stringify(all));
  } catch {}
  ui.fxDismissed = null;
  waitMemo = { key: null, list: [] };
}

addEventListener("storage", (e) => {
  if (e.key !== FLEET_SHUT_KEY && e.key !== FLEET_DISMISSED_KEY && e.key !== null) return;
  ui.fxCollapsed = null;
  ui.fxDismissed = null;
  if (ui.view === "sessions") render();
});

function fleetFilter() {
  if (!ui.fleet) {
    let by = "device";
    try {
      by = localStorage.getItem(FLEET_BY_KEY) || by;
    } catch {}
    ui.fleet = { state: "", by, q: "" };
  }
  if (!["device", "branch", "group"].includes(ui.fleet.by)) ui.fleet.by = "device";
  return ui.fleet;
}

// A job is a group: tag, which a person puts on the sessions working toward one thing.
function groupOf(s) {
  const n = tagsOf(s).names.find((x) => x.startsWith("group:"));
  return n ? n.slice(6) : "";
}

function prOf(s) {
  const prs = ui.state?.prs || {};
  return s.branch && Object.hasOwn(prs, s.branch) ? prs[s.branch] : null;
}

const FX_STATE = { on: "Active", idle: "Idle", dnd: "Do not disturb", off: "Offline" };

function fxDot(st) {
  return stateDot(st, "fx-dot fx-dot-" + st);
}

// A segmented control is one stop for Tab, as a radio group is: ←/→, Home and End move along it and
// pick as they go. An option may carry a tooltip saying what it means.
function segKeys(e) {
  const btns = [...e.currentTarget.querySelectorAll("button")];
  const i = btns.indexOf(document.activeElement);
  const to = { ArrowLeft: i - 1, ArrowRight: i + 1, Home: 0, End: btns.length - 1 }[e.key];
  if (to == null || i < 0) return;
  e.preventDefault();
  const next = btns[Math.max(0, Math.min(btns.length - 1, to))];
  next.focus();
  if (next !== btns[i]) next.click();
}

// The page's bar (#183): one on every page, 44px high, the title with its count beside it in meta,
// the page's tabs, and its controls at the right.
function pageBar({ lead, title, meta, tabs, right, titleTip }) {
  return h(
    "header",
    { class: "bar" },
    lead,
    h("h1", { class: "bar-title", title: titleTip || null }, title),
    meta && h("span", { class: "bar-meta" }, meta),
    tabs,
    h("span", { class: "bar-spacer" }),
    right,
  );
}

// The bar's tabs: text, with a count in meta. One stop for Tab; the arrow keys move along and pick.
// options: [value, label, count, data-key, tip].
function barTabs(label, options, value, onPick) {
  return h(
    "div",
    { class: "bar-tabs", role: "tablist", "aria-label": label, onkeydown: segKeys },
    options.map(([v, text, n, key, tip]) =>
      h(
        "button",
        {
          role: "tab",
          class: "bar-tab",
          "aria-selected": v === value ? "true" : "false",
          tabindex: v === value ? "0" : "-1",
          "data-key": key || "tab-" + label + "-" + v,
          title: tip || null,
          onclick: () => {
            if (v !== value) onPick(v);
          },
        },
        text,
        n != null && h("span", { class: "bar-tab-n" }, n),
      ),
    ),
  );
}

// A menu button in the bar ("State All", "Group Device"): its value shows on it, and its menu holds
// the choices with their counts. items: [value, label, count, lead]. The menu reads them afresh on
// each render, so its counts keep up while it is open.
function menuButton(key, label, items, value, onPick) {
  const cur = items.find(([v]) => v === value) || items[0];
  if (ui.pickMenu?.key === key) Object.assign(ui.pickMenu, { items, value: cur[0], onPick, label });
  return h(
    "button",
    {
      class: "bar-menu",
      "data-key": key,
      "aria-haspopup": "menu",
      "aria-expanded": ui.pickMenu?.key === key ? "true" : "false",
      disabled: items.length < 2 || null,
      onclick: (e) => openPickMenu(key, e.currentTarget, label, items, cur[0], onPick),
    },
    h("span", null, label),
    h("b", null, cur[1]),
  );
}

function openPickMenu(key, anchor, label, items, value, onPick) {
  const r = anchor.getBoundingClientRect();
  ui.pickMenu = { key, label, items, value, onPick, x: Math.max(8, Math.min(r.left, innerWidth - 248)), top: r.bottom + 4 };
  render();
  (root.querySelector('.pick-menu [aria-checked="true"]') || root.querySelector(".pick-menu [role=menuitemradio]"))?.focus();
}

// Closing gives the keyboard back to the menu's button.
function closePickMenu(back = true) {
  const key = ui.pickMenu?.key;
  ui.pickMenu = null;
  render();
  if (back && key) root.querySelector(`[data-key="${CSS.escape(key)}"]`)?.focus({ preventScroll: true });
}

function pickMenu() {
  const m = ui.pickMenu;
  // Built after the page, so its button is looked for in the page just built.
  if (!content?.querySelector(`[data-key="${CSS.escape(m.key)}"]`)) {
    ui.pickMenu = null;
    return null;
  }
  const menu = h(
    "div",
    {
      class: "row-menu pick-menu",
      role: "menu",
      "aria-label": m.label,
      onkeydown: (e) => {
        const items = [...e.currentTarget.querySelectorAll("[role=menuitemradio]")];
        const i = items.indexOf(document.activeElement);
        const to = { ArrowDown: i + 1, ArrowUp: i - 1, Home: 0, End: items.length - 1 }[e.key];
        if (to != null) {
          e.preventDefault();
          items[(to + items.length) % items.length]?.focus();
        } else if (e.key === "Tab") {
          e.preventDefault();
          closePickMenu();
        }
      },
    },
    m.items.map(([v, label, n, lead]) =>
      h(
        "button",
        {
          class: "row-menu-item",
          role: "menuitemradio",
          "aria-checked": v === m.value ? "true" : "false",
          tabindex: "-1",
          "data-key": "pm-" + m.key + "-" + v,
          onclick: () => {
            const key = m.key;
            ui.pickMenu = null;
            m.onPick(v);
            root.querySelector(`[data-key="${CSS.escape(key)}"]`)?.focus({ preventScroll: true });
          },
        },
        h("span", { class: "pm-check", "aria-hidden": "true" }, v === m.value && icon("check")),
        lead,
        h("span", { class: "truncate" }, label),
        n != null && h("span", { class: "pm-n" }, n),
      ),
    ),
  );
  menu.style.left = m.x + "px";
  menu.style.top = m.top + "px";
  return h(
    "div",
    {
      class: "menu-scrim",
      onclick: (e) => {
        if (e.target === e.currentTarget) closePickMenu(false);
      },
      onkeydown: (e) => {
        if (e.key === "Escape") {
          e.preventDefault();
          closePickMenu();
        }
      },
    },
    menu,
  );
}

// The bar's search: a field with its "/" hint.
function barSearch(key, label, value, onInput) {
  return h(
    "label",
    { class: "bar-search" },
    icon("search"),
    h("input", { type: "search", placeholder: "Search", "aria-label": label, value: value || null, "data-key": key, oninput: (e) => onInput(e.target.value) }),
    !value && h("kbd", { class: "fx-kbd", "aria-hidden": "true" }, "/"),
  );
}

function fxSeg(label, options, value, onPick) {
  const current = options.some(([v]) => v === value) ? value : options[0][0];
  return h(
    "div",
    {
      class: "fx-seg",
      role: "group",
      "aria-label": label,
      onkeydown: segKeys,
    },
    options.map(([v, text, n, tip]) =>
      h(
        "button",
        {
          "aria-pressed": value === v ? "true" : "false",
          class: value === v ? "on" : "",
          tabindex: v === current ? "0" : "-1",
          "data-key": "fs-" + label + ":" + v,
          title: tip || (Array.isArray(text) ? text.map((x) => x.textContent).join("").trim() || null : null),
          onclick: () => onPick(v),
        },
        text,
        n != null && h("span", { class: "fx-seg-n" + (n === 0 ? " zero" : "") }, n),
      ),
    ),
  );
}

// An empty cell: a muted dash, so a gap reads as nothing there rather than as missing.
function fxDash() {
  return h("span", { class: "fx-dash", title: "None" }, "—");
}

// A PR chip, which opens the pull request. A draft is grey and says "draft" in words, so the grey
// isn't a code to learn.
function fxPr(pr) {
  const props = { class: "fx-pr" + (pr.draft ? " draft" : ""), title: (pr.draft ? "Draft pull request #" : "Pull request #") + pr.number };
  const label = "#" + pr.number + (pr.draft ? " draft" : "");
  return pr.url && /^https:\/\//.test(pr.url) ? h("a", { ...props, href: pr.url, target: "_blank", rel: "noreferrer" }, label) : h("span", props, label);
}

// How long since t, as a duration: "40m", "1h 59m", "2d 3h".
function lasted(t) {
  const m = Math.max(1, Math.round((Date.now() - t) / 60000));
  if (m < 60) return m + "m";
  const hrs = Math.floor(m / 60);
  if (hrs < 24) return hrs + "h" + (m % 60 ? " " + (m % 60) + "m" : "");
  return Math.floor(hrs / 24) + "d" + (hrs % 24 ? " " + (hrs % 24) + "h" : "");
}

// "8:53 PM" today, "Sat 8:53 PM" within the week, "Nov 2, 8:53 PM" before that.
function sinceWhen(t) {
  const d = new Date(t);
  const time = d.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
  if (d.toDateString() === new Date().toDateString()) return time;
  if (Date.now() - t < 6 * 86400000) return d.toLocaleDateString([], { weekday: "short" }) + " " + time;
  return d.toLocaleDateString([], { month: "short", day: "numeric" }) + ", " + time;
}

// While the daemon doesn't answer, the page says so and how old what it shows is, ahead of
// anything else, so a stopped page doesn't read as a quiet team.
function fleetDown() {
  if (ui.conn !== "down" || ui.restarting) return null;
  return h("span", { class: "fx-down" }, "Can't reach the daemon", ui.okAt ? agoEl("span", null, ui.okAt, " · last updated ") : " · retrying");
}

// The rows a tab lists, under the tag filter.
function fleetRows(tab) {
  return devices().flatMap((d) => shownSessions(tab === "quiet" ? quietSessions(d) : liveSessions(d)).map((s) => ({ d, s })));
}

// Who owes an answer among the running sessions, for the sidebar: every one, whatever the lists
// are filtered by, as its other counts are.
function runningNeeds() {
  return fleetNeeds(devices().flatMap((d) => liveSessions(d).map((s) => ({ d, s }))));
}

function sessionsPage() {
  const tab = ui.sessTab || "running";
  if (!ui.state) return h("div", { class: "fx" }, pageBar({ title: "All sessions", meta: fleetDown() || "Connecting…" }));
  const F = fleetFilter();
  const devs = devices();
  const live = fleetRows("running");
  const quiet = fleetRows("quiet");
  const settledN = ui.tagFilter ? 0 : settledSessions().length;
  const settledShown = ui.tagFilter ? 0 : settledSessions().filter((r) => !F.q.trim() || settledText(r).includes(F.q.trim().toLowerCase())).length;
  const all = tab === "quiet" ? quiet : live;
  // Job is offered only once a session has one (a group: tag); until then it would group nothing.
  const jobs = [...live, ...quiet].some((r) => groupOf(r.s));
  if (!jobs && F.by === "group") F.by = "device";
  const q = F.q.trim().toLowerCase();
  // Search covers what a row shows: the asker and question it waits on, its tags and its PR too.
  const allNeeds = fleetNeeds(all);
  const text = (r) => {
    const need = allNeeds.get(r.s.name);
    const pr = prOf(r.s);
    return [r.s.name, FX_STATE[sessionState(r.d, r.s)], r.s.doing, r.s.branch, r.s.agent, modelOf(r.s), deviceLabel(r.d), ...tagsOf(r.s).names, pr && "#" + pr.number + (pr.draft ? " draft" : ""), ...(need ? need.msgs.flatMap((m) => [fromName(m), m.subject, questionIn(m)]) : [])].join(" ");
  };
  const searched = all.filter((r) => !q || text(r).toLowerCase().includes(q));
  const searchedNeeds = fleetNeeds(searched);
  const rows = searched.filter((r) => !F.state || (F.state === "need" ? searchedNeeds.has(r.s.name) : sessionState(r.d, r.s) === F.state));
  const needs = fleetNeeds(rows);
  // Open details stay open when their session falls out of the filter (it went idle while you
  // read, say); they close only when the session itself is gone.
  if (ui.fleetSel && !findSession(ui.fleetSel)) {
    ui.fleetSel = null;
    history.replaceState(null, "", fleetHash(null));
  }
  if (tab === "settled") ui.selected.clear();
  const sel = ui.fleetSel;
  const set = (k) => (v) => {
    F[k] = v;
    if (k === "by") {
      try {
        localStorage.setItem(FLEET_BY_KEY, v);
      } catch {}
    }
    render();
  };
  const clear = () => {
    F.q = "";
    F.state = "";
    render();
    root.querySelector('[data-key="fx-q"]')?.focus();
  };
  const count = (st) => searched.filter((r) => sessionState(r.d, r.s) === st).length;
  const n = allNeeds.size;
  const what = tab === "quiet" ? "quiet session" : "session";
  const tagged = ui.tagFilter ? " tagged " + ui.tagFilter : "";
  const hiddenN = quiet.filter((r) => isHidden(r.s)).length;
  // Beside the title: how many, and when a search or a state narrows them, how many of how many.
  let sub;
  if (tab === "settled") sub = plural(settledN, "session") + " ended";
  else if ((q || F.state) && all.length) sub = ["Showing " + rows.length + " of " + all.length + tagged, rows.length > 0 && [" · ", h("button", { class: "fx-clear", "data-key": "fx-clear", onclick: clear }, "Clear")]];
  else if (all.length) sub = (tab === "quiet" ? plural(all.length, "quiet session") + tagged : all.length + " running" + tagged + " on " + plural(new Set(all.map((r) => r.d.id)).size, "device")) + (hiddenN && tab === "quiet" ? " · " + hiddenN + " hidden" : "");
  else sub = "No " + what + "s" + tagged;
  const down = fleetDown();
  // What a selection can hold: the sessions in front of the person, so a filter, a tab or a shut
  // group leaves none picked out of sight.
  const pickable = tab === "settled" ? [] : visibleRows(rows, needs, F).filter((r) => r.s.addr).map((r) => r.s);
  const inView = new Set(pickable.map((s) => s.addr));
  for (const a of ui.selected) if (!inView.has(a)) ui.selected.delete(a);

  // The details open in a pane beside the table at every width, as a thread does in Slack: the
  // table narrows (and drops columns) rather than going away.
  const split = h(
    "div",
    { class: "fx-split" + (sel ? " has-pane" : "") },
    h(
      "div",
      { class: "fx" },
      pageBar({
        title: "All sessions",
        meta: down ? [down, " · ", sub] : sub,
        tabs: barTabs(
          "Which sessions",
          [
            ["running", "Running", null, "fs-tab-running", "Running, and active in the last day"],
            ["quiet", "Quiet", quiet.length, "fs-tab-quiet", "Idle for more than a day, or hidden"],
            ["settled", "Settled", settledN, "fs-tab-settled", "Ended; kept while their messages are"],
          ],
          tab,
          (v) => {
            ui.sessTab = v;
            // A selection belongs to the tab it was made on.
            ui.selected.clear();
            render();
          },
        ),
        right: [
          menuButton(
            "fx-state",
            "State",
            tab === "settled"
              ? [["", "All", settledShown]]
              : // A state none of the sessions is in isn't offered, unless it is the one picked.
                [
                  ["", "All", searched.length],
                  ["need", "Needs a look", searchedNeeds.size],
                  ...["on", "idle", "dnd", "off"].map((st) => [st, FX_STATE[st], count(st), fxDot(st)]),
                ].filter(([v, , n]) => v === "" || n > 0 || v === F.state),
            tab === "settled" ? "" : F.state,
            set("state"),
          ),
          menuButton(
            "fx-by",
            "Group",
            tab === "settled" ? [["device", "Device"]] : [["device", "Device"], ["branch", "Branch"], jobs && ["group", "Job"]].filter(Boolean),
            tab === "settled" ? "device" : F.by,
            set("by"),
          ),
          tagMenuButton(),
          barSearch("fx-q", "Search sessions", F.q, (v) => {
            F.q = v;
            render();
          }),
        ],
      }),
      ui.selected.size > 0 && tab !== "settled" && h("div", { class: "fx-toolbar" }, selectBar(pickable)),
      tab === "settled" ? settledTable(q, F) : fleetTable(rows, needs, sel, { q: F.q.trim(), state: F.state, total: all.length, tab, clear }),
    ),
    sel && h("div", { class: "fx-scrim", "aria-hidden": "true" }),
    sel && fleetPane(sel),
  );
  if (sel) split.style.setProperty("--fx-pane-w", fleetPaneWidth() + "px"); // the page's CSP refuses style attributes
  return split;
}

// Picking sessions on All sessions, to tag them together: how many, and the tag menu for them.
function selectBar(pickable) {
  const n = ui.selected.size;
  const all = n === pickable.length;
  const clear = () => {
    ui.selected.clear();
    render();
    root.querySelector(".fx-open")?.focus({ preventScroll: true });
  };
  return h(
    "div",
    { class: "fx-select-bar", role: "group", "aria-label": "Selected sessions" },
    h("span", { class: "fx-select-n", "aria-live": "polite" }, n + " selected"),
    h("button", { class: "bar-menu", "data-key": "select-tag", onclick: (e) => openTagMenu([...ui.selected], e.currentTarget) }, icon("tag"), "Tag"),
    h("button", { class: "bar-menu", "data-key": "select-clear", onclick: clear }, "Clear"),
    !all &&
      h(
        "button",
        {
          class: "bar-menu bar-quiet",
          "data-key": "select-all",
          onclick: () => {
            ui.selected = new Set(pickable.map((s) => s.addr));
            render();
          },
        },
        "Select all " + pickable.length,
      ),
  );
}

// The table's columns and what each sorts by. A column sorts on its first click in the order a
// person reaches for (names A to Z, Active first, the latest activity first), flips on the second,
// and goes back to the table's own order on the third.
const FX_COLS = [
  ["who", "Session", "name"],
  ["agent", "Agent", "agent"],
  ["state", "State", "state"],
  ["doing", "Working on", null],
  ["branch", "Branch", "branch"],
  ["last", "Last active", "active"],
];
// Settled sessions are known from their messages: what they last talked about, and when. No branch.
const FX_SETTLED_COLS = [
  ["who", "Session", "name"],
  ["state", "State", null],
  ["doing", "Last conversation", null],
  ["last", "Last message", "active"],
];
const FX_STATE_ORDER = { on: 0, idle: 1, dnd: 2, off: 3 };

function fleetSortVal(k, r) {
  if (k === "name") return r.s.name;
  if (k === "agent") return agentName(r.s.agent);
  if (k === "state") return FX_STATE_ORDER[sessionState(r.d, r.s)];
  if (k === "branch") return r.s.branch || "￿";
  return -(when(r.s.lastActive) || 0);
}

function fleetCompare(S) {
  return (a, b) => {
    const x = fleetSortVal(S.key, a);
    const y = fleetSortVal(S.key, b);
    const c = typeof x === "number" ? x - y : x.localeCompare(y);
    return c * S.dir || a.s.name.localeCompare(b.s.name);
  };
}

function fleetKey(F, r) {
  return F.by === "device" ? r.d.id : F.by === "branch" ? r.s.branch || "" : groupOf(r.s);
}

// The rows in groups, in a fixed order, so groups and rows don't swap places as agents work: this
// device, then devices by name, offline ones last; branches and jobs by name, with none last. In a
// group, rows go by name, or by the column sorted on.
function fleetGroups(rows, F) {
  const S = F.sort || {};
  const rank = (r) => (F.by === "device" ? (r.d.self ? 0 : r.d.reachable ? 1 : 2) : fleetKey(F, r) ? 0 : 1);
  const gname = (r) => (F.by === "device" ? deviceLabel(r.d) + " " + r.d.id : fleetKey(F, r));
  const within = S.key ? fleetCompare(S) : (a, b) => a.s.name.localeCompare(b.s.name);
  const sorted = [...rows].sort((a, b) => rank(a) - rank(b) || gname(a).localeCompare(gname(b)) || within(a, b));
  const groups = new Map();
  for (const r of sorted) {
    const k = fleetKey(F, r);
    if (!groups.has(k)) groups.set(k, []);
    groups.get(k).push(r);
  }
  return groups;
}

// Which groups are shut. While a search or state filter is on, a separate set, empty to start, so
// every match shows, a heading still folds and unfolds, and none of it touches what's shut once the
// filter clears (that set comes back then).
function fleetCollapsed(F) {
  const filtering = !!(F.q.trim() || F.state);
  if (!filtering) ui.fxCollapsedFiltered = null;
  return filtering ? (ui.fxCollapsedFiltered ||= new Set()) : fleetShut();
}

// Sessions that owe an answer lead the table, in a summary of their own, the longest wait first.
// They stay in their own groups too, so each group is whole and its count true.
function fleetPinned(rows, needs, F) {
  const S = F.sort || {};
  // Unsorted: questions a session can answer now, oldest first, then those waiting on a session
  // that is offline or on do not disturb, oldest first.
  const later = (r) => (waitNote(r.d, r.s) ? 1 : 0);
  return rows.filter((r) => needs.has(r.s.name)).sort(S.key ? fleetCompare(S) : (a, b) => later(a) - later(b) || needs.get(a.s.name).at - needs.get(b.s.name).at);
}

function visibleRows(rows, needs, F) {
  const shut = fleetCollapsed(F);
  const rest = [...fleetGroups(rows, F)].flatMap(([k, list]) => (shut.has(F.by + ":" + k) ? [] : list));
  return [...fleetPinned(rows, needs, F), ...rest];
}

// The table's header row, sortable, shared by every tab.
function fleetHead(F, cols = FX_COLS, actions = true) {
  const S = F.sort || {};
  return h(
    "div",
    { class: "fx-tr fx-th", role: "row" },
    cols.map(([c, name, k]) => {
      const on = k && S.key === k;
      // Last active's first order is the latest first: descending by time.
      const order = on ? ((k === "active") === S.dir > 0 ? "descending" : "ascending") : null;
      return h(
        "div",
        { class: "fx-c fx-c-" + c, role: "columnheader", "aria-sort": order },
        k
          ? h(
              "button",
              {
                class: "fx-sort" + (on ? " on" : ""),
                "data-key": "fx-sort-" + k,
                onclick: () => {
                  F.sort = !on ? { key: k, dir: 1 } : S.dir > 0 ? { key: k, dir: -1 } : {};
                  render();
                },
              },
              name,
              h("span", { class: "fx-sort-ico", "aria-hidden": "true" }, icon(order === "ascending" ? "chevronUp" : "chevronDown")),
            )
          : name,
      );
    }),
    actions && h("div", { class: "fx-c fx-c-act", role: "columnheader" }, h("span", { class: "sr-only" }, "Actions")),
  );
}

// A heading is a row of one cell across the table.
function fxWide(cls, ...kids) {
  return h("div", { class: "fx-grow", role: "row" }, h("div", { class: "fx-gcell " + cls, role: "cell", "aria-colspan": "7" }, kids));
}

// A group's heading, which folds it.
function fxGroupHead(gk, shut, collapsed, filtering, label, n) {
  return fxWide(
    "",
    h(
      "button",
      {
        class: "fx-group",
        "data-key": "fg-" + gk,
        "aria-expanded": shut ? "false" : "true",
        onclick: () => {
          if (filtering) shut ? collapsed.delete(gk) : collapsed.add(gk);
          else fleetShutSet(gk, !shut);
          ui.fxUnfolded = shut ? gk : null;
          render();
          ui.fxUnfolded = null;
        },
      },
      icon(shut ? "chevronRight" : "chevronDown", "fx-chev"),
      label,
      h("span", { class: "fx-group-n" }, n),
    ),
  );
}

function fleetTable(rows, needs, sel, empty) {
  const F = fleetFilter();
  const pinned = fleetPinned(rows, needs, F);
  const groups = fleetGroups(rows, F);
  // Which heading each session is under, so closing one whose group is shut can focus the heading.
  ui.fxGroupOf = {};
  for (const [k, list] of groups) for (const r of list) ui.fxGroupOf[r.s.name] = "fg-" + F.by + ":" + k;
  const label = (k, list) => {
    if (F.by === "device") {
      const d = list[0].d;
      return [h("span", { class: "fx-group-name", title: deviceLabel(d) }, deviceLabel(d)), !d.reachable && agoEl("span", "fx-group-sub", when(d.lastSeen) || 0, "· offline, seen ")];
    }
    if (F.by === "branch") {
      const pr = k ? prOf({ branch: k }) : null;
      return [h("span", { class: "fx-group-name mono", title: k || null }, k || "No branch"), pr && fxPr(pr)];
    }
    return h("span", { class: "fx-group-name", title: k || null }, k || "No job");
  };
  const filtering = !!(F.q.trim() || F.state);
  const collapsed = fleetCollapsed(F);
  let body;
  if (rows.length === 0) {
    const kind = empty.tab === "quiet" ? "quiet sessions" : "sessions";
    const what = empty.state === "need" ? kind + " that need a look" : empty.state ? FX_STATE[empty.state].toLowerCase() + " " + kind : kind;
    const tagged = ui.tagFilter ? " tagged " + ui.tagFilter : "";
    body = empty.total
      ? fxWide(
          "fx-empty",
          h("p", { class: "fx-empty-title" }, empty.q ? "No " + what + " match “" + empty.q + "”" : "No " + what),
          h("button", { class: "fx-btn", "data-key": "fx-empty-clear", onclick: empty.clear }, empty.q && empty.state ? "Clear search and filter" : empty.q ? "Clear search" : "Show all " + kind),
        )
      : fxWide(
          "fx-empty",
          h("p", { class: "fx-empty-title" }, (empty.tab === "quiet" ? "No quiet sessions" : "No sessions running") + tagged),
          ui.tagFilter &&
            h(
              "button",
              {
                class: "fx-btn",
                "data-key": "fx-untag",
                onclick: () => {
                  ui.tagFilter = null;
                  render();
                },
              },
              "Show every session",
            ),
        );
  } else {
    body = [
      pinned.length > 0 && [
        fxWide("fx-group fx-group-need", h("span", { class: "fx-chev fx-need-dot", "aria-hidden": "true" }, h("span", { class: "fx-flag" })), h("span", { class: "fx-group-name" }, "Needs a look"), h("span", { class: "fx-group-n" }, pinned.length)),
        pinned.map(({ d, s }) => fleetRow(d, s, needs.get(s.name), sel === s.name, F, true)),
      ],
      [...groups].map(([k, list]) => {
        const gk = F.by + ":" + k;
        const shut = collapsed.has(gk);
        return [
          fxGroupHead(gk, shut, collapsed, filtering, label(k, list), list.length),
          !shut &&
            list.map(({ d, s }) => {
              const row = fleetRow(d, s, null, sel === s.name, F, false);
              if (ui.fxUnfolded === gk) row.classList.add("enter");
              return row;
            }),
        ];
      }),
    ];
  }
  return h(
    "div",
    { class: "fx-table" + (ui.selected.size ? " picking" : ""), role: "table", "aria-label": "Sessions" },
    empty.total > 0 && h("div", { class: "fx-thead", role: "rowgroup" }, fleetHead(F)),
    h("div", { class: "fx-tbody", role: "rowgroup" }, body),
  );
}

// Parts of a line joined by "·" that wraps between parts, never inside one, and never leaves a "·"
// at either end of a line: each part carries its own leading " · " in a fixed-width box, and every
// line is pulled left by that width inside a clipping box (.fx-parts), so the one that starts a line
// is hidden. The separators are real text, so a copied line reads "a · b"; the first one can't be
// selected. <wbr> is the only place a line may break, and copies as nothing. A part may be
// { kids, cls } to be styled on its own.
function fxParts(parts) {
  return h(
    "span",
    { class: "fx-parts-in" },
    parts.flatMap((x, i) => {
      const part = x && typeof x === "object" && !(x instanceof Node) && !Array.isArray(x) ? x : { kids: x, cls: "" };
      return [i ? h("wbr") : null, h("span", { class: ("fx-part " + part.cls).trim() }, h("span", { class: "fx-sep" + (i ? "" : " lead") }, " · "), part.kids)];
    }),
  );
}

// Picks or unpicks a session; with shift, every row from the last one picked to this one, as the
// table shows them, goes the same way.
function togglePick(s, range) {
  const on = !ui.selected.has(s.addr);
  const order = [...root.querySelectorAll(".fx-pick[data-addr]")].map((b) => b.dataset.addr);
  const i = range && ui.pickFrom ? order.indexOf(ui.pickFrom) : -1;
  const j = order.indexOf(s.addr);
  const span = i >= 0 && j >= 0 ? order.slice(Math.min(i, j), Math.max(i, j) + 1) : [s.addr];
  for (const a of span) on ? ui.selected.add(a) : ui.selected.delete(a);
  ui.pickFrom = s.addr;
  render();
}

// One row model (#183): a 36px line. Session (a box to pick it, over its face on hover or focus and
// shown on every row once one is picked), Agent, State, Working on, Branch with its PR, Last active,
// and the "…". A session that needs a look is in the table twice: in the summary at the top (pinned),
// where Working on holds who waits on it, for how long and for what, and its Agent cell names its
// device; and in its own group as an ordinary row. The summary's copy has keys of its own, "fp-" and
// "fx-morp-", so a key finds one control.
function fleetRow(d, s, need, on, F, pinned) {
  const pr = prOf(s);
  const t = when(s.lastActive);
  const state = sessionState(d, s);
  const picked = !!s.addr && ui.selected.has(s.addr);
  // Under a device's heading the device is in the heading; anywhere else the Agent cell names it.
  const dev = (pinned || F.by !== "device") && deviceLabel(d);
  const tags = tagsOf(s).names;
  let doing;
  if (need) {
    const note = waitNote(d, s);
    const full = h("span", null, need.text(note)).textContent;
    doing = h("span", { class: "fx-status fx-need-line", title: full }, need.short());
  } else doing = s.doing ? h("span", { class: "fx-status", title: s.doing }, s.doing) : fxDash();
  return h(
    "div",
    { class: "fx-tr fx-row" + (on ? " on" : "") + (need ? " need" : "") + (picked ? " picked" : "") + (d.reachable ? "" : " stale"), role: "row" },
    h(
      "div",
      { class: "fx-c fx-c-who", role: "cell" },
      h(
        "span",
        { class: "fx-face" },
        avatar(s.name),
        s.addr &&
          h(
            "button",
            {
              class: "fx-pick" + (picked ? " on" : ""),
              role: "checkbox",
              "aria-checked": picked ? "true" : "false",
              "aria-label": "Select " + s.name,
              "data-key": (pinned ? "fpk-" : "fk-") + s.name,
              "data-addr": s.addr,
              // Shift picks a range; keep it from selecting the text between.
              onmousedown: (e) => {
                if (e.shiftKey) e.preventDefault();
              },
              onclick: (e) => togglePick(s, e.shiftKey),
            },
            picked && icon("check"),
          ),
      ),
      // The name is the row's main control. It covers the row, so a click anywhere opens the details.
      h(
        "button",
        {
          class: "fx-open",
          "data-key": (pinned ? "fp-" : "fl-") + s.name,
          "aria-expanded": on ? "true" : "false",
          "aria-controls": on ? "fx-pane" : null,
          title: s.name,
          onclick: () => (on ? fleetCloseAsked() : fleetOpen(s.name)),
        },
        s.name,
      ),
      tags.length > 0 && h("span", { class: "fx-tags", title: tags.join(", ") }, tags.join(", ")),
    ),
    h("div", { class: "fx-c fx-c-agent", role: "cell", title: [agentName(s.agent), modelOf(s), dev].filter(Boolean).join(" · ") }, agentName(s.agent) || fxDash(), dev && h("span", { class: "fx-agent-dev" }, " · " + dev)),
    h("div", { class: "fx-c fx-c-state", role: "cell", title: FX_STATE[state] }, fxDot(state), h("span", { class: "fx-state-word" }, FX_STATE[state])),
    h("div", { class: "fx-c fx-c-doing", role: "cell" }, doing),
    !pinned && F.by === "branch"
      ? h("div", { class: "fx-c fx-c-branch", role: "cell" }, pr ? fxPr(pr) : null)
      : h("div", { class: "fx-c fx-c-branch", role: "cell" }, s.branch ? [h("span", { class: "mono", title: s.branch }, s.branch), pr && fxPr(pr)] : fxDash()),
    h("div", { class: "fx-c fx-c-last", role: "cell" }, t ? agoEl("span", "", t) : fxDash()),
    h(
      "div",
      { class: "fx-c fx-c-act", role: "cell" },
      h(
        "button",
        {
          class: "fx-more",
          title: "Actions",
          "aria-label": "Actions for " + s.name,
          "aria-haspopup": "menu",
          "aria-expanded": ui.rowMenu?.name === s.name ? "true" : "false",
          "data-key": (pinned ? "fx-morp-" : "fx-more-") + s.name,
          onclick: (e) => openRowMenu(s.name, e.currentTarget),
        },
        icon("more"),
      ),
    ),
  );
}

// A row's actions, from its "…" button, or Shift+F10 or the menu key on its name: what the pane
// offers, without opening it.
function openRowMenu(name, anchor) {
  const r = anchor.getBoundingClientRect();
  const up = innerHeight - r.bottom < 200 && r.top > innerHeight - r.bottom;
  ui.rowMenu = { name, x: Math.max(8, Math.min(r.right - 232, innerWidth - 240)), top: up ? null : r.bottom + 4, bottom: up ? innerHeight - r.top + 4 : null };
  render();
  root.querySelector(".row-menu [role=menuitem]")?.focus();
}

// Closing gives the keyboard back to the row's "…".
function closeRowMenu(back = true) {
  const name = ui.rowMenu?.name;
  ui.rowMenu = null;
  render();
  if (back && name) root.querySelector(`[data-key="${CSS.escape("fx-more-" + name)}"]`)?.focus({ preventScroll: true });
}

function rowMenu() {
  const m = ui.rowMenu;
  const f = findSession(m.name);
  if (!f || ui.view !== "sessions") {
    ui.rowMenu = null;
    return null;
  }
  const s = f.session;
  const hidden = isHidden(s);
  const item = (key, label, iconName, act) =>
    h(
      "button",
      {
        class: "row-menu-item",
        role: "menuitem",
        tabindex: "-1",
        "data-key": "rm-" + key,
        onclick: () => {
          ui.rowMenu = null;
          act();
        },
      },
      icon(iconName),
      h("span", { class: "truncate" }, label),
    );
  const menu = h(
    "div",
    {
      class: "row-menu",
      role: "menu",
      "aria-label": "Actions for " + s.name,
      onkeydown: (e) => {
        const items = [...e.currentTarget.querySelectorAll("[role=menuitem]")];
        const i = items.indexOf(document.activeElement);
        const to = { ArrowDown: i + 1, ArrowUp: i - 1, Home: 0, End: items.length - 1 }[e.key];
        if (to != null) {
          e.preventDefault();
          items[(to + items.length) % items.length]?.focus();
        } else if (e.key === "Tab") {
          e.preventDefault();
          closeRowMenu();
        }
      },
    },
    item("msg", "Message " + s.name, "message", () => startChat(s.name)),
    item("convs", "Open conversations", "arrowRight", () => {
      ui.filter = s.addr || s.name;
      ui.conversation = null;
      go("messages");
    }),
    s.addr &&
      item("tag", "Tag…", "tag", () => {
        render();
        const more = root.querySelector(`[data-key="${CSS.escape("fx-more-" + s.name)}"]`);
        if (more) openTagMenu([s.addr], more);
      }),
    s.addr &&
      item("hide", hidden ? "Show " + s.name : "Hide " + s.name, hidden ? "eye" : "eyeOff", () => {
        setHidden(s, !hidden);
        root.querySelector(`[data-key="${CSS.escape("fx-more-" + s.name)}"]`)?.focus({ preventScroll: true });
      }),
  );
  // Placed through the CSSOM: the page's CSP refuses style attributes.
  menu.style.left = m.x + "px";
  if (m.top != null) menu.style.top = m.top + "px";
  else menu.style.bottom = m.bottom + "px";
  return h(
    "div",
    {
      class: "menu-scrim",
      onclick: (e) => {
        if (e.target === e.currentTarget) closeRowMenu(false);
      },
      onkeydown: (e) => {
        if (e.key === "Escape") {
          e.preventDefault();
          closeRowMenu();
        }
      },
    },
    menu,
  );
}

// Settled sessions, in the same table under their devices: the page knows them only from their
// messages, so a row's state is Ended, its last conversation is the subject it last wrote or was
// sent, and it has no branch column. One opens its conversations.
// What a search looks in for a settled session.
function settledText(r) {
  return [r.name, r.lastMsg?.subject, r.device && host(r.device)].map((x) => (x || "").toLowerCase()).join(" ");
}

function settledTable(q, F) {
  if (ui.tagFilter) return h("div", { class: "fx-table" }, h("div", { class: "fx-empty" }, h("p", { class: "fx-empty-title" }, "No settled session is tagged " + ui.tagFilter)));
  const S = F.sort || {};
  const all = settledSessions().filter((r) => !q || settledText(r).includes(q));
  const val = (r) => (S.key === "name" ? r.name : S.key === "active" ? -r.last : 0);
  const within = (a, b) => {
    const x = val(a);
    const y = val(b);
    return (typeof x === "number" ? x - y : x.localeCompare(y)) * (S.dir || 1) || a.name.localeCompare(b.name);
  };
  const byDev = new Map();
  for (const r of all) {
    if (!byDev.has(r.device)) byDev.set(r.device, []);
    byDev.get(r.device).push(r);
  }
  const label = (id) => {
    const d = (ui.state?.devices || []).find((x) => x.id === id);
    return d ? deviceLabel(d) : id ? host(id) : "Unknown device";
  };
  const filtering = !!q;
  const collapsed = fleetCollapsed(F);
  return h(
    "div",
    { class: "fx-table fx-settled", role: "table", "aria-label": "Settled sessions" },
    settledSessions().length > 0 && h("div", { class: "fx-thead", role: "rowgroup" }, fleetHead(F, FX_SETTLED_COLS, false)),
    h(
      "div",
      { class: "fx-tbody", role: "rowgroup" },
      [...byDev].map(([id, list]) => {
        const gk = "settled:" + id;
        const shut = collapsed.has(gk);
        return [
          fxGroupHead(gk, shut, collapsed, filtering, h("span", { class: "fx-group-name" }, label(id)), list.length),
          !shut &&
            list.sort(within).map((r) =>
              h(
                "div",
                { class: "fx-tr fx-row", role: "row" },
                h(
                  "div",
                  { class: "fx-c fx-c-who", role: "cell" },
                  h(
                    "div",
                    { class: "fx-who" },
                    avatar(r.name),
                    h(
                      "div",
                      { class: "fx-who-text" },
                      h(
                        "button",
                        {
                          class: "fx-open",
                          "data-key": "settled-" + r.name,
                          title: "Open " + r.name + "’s conversations",
                          onclick: () => {
                            ui.filter = r.addr || r.name;
                            ui.conversation = null;
                            go("messages");
                          },
                        },
                        r.name,
                      ),
                      h("span", { class: "fx-subline" }, h("span", { class: "fx-muted" }, "Talked to " + plural(r.peers.size, "session"))),
                    ),
                  ),
                ),
                h("div", { class: "fx-c fx-c-state", role: "cell" }, fxDot("off"), "Ended"),
                h("div", { class: "fx-c fx-c-doing", role: "cell" }, r.lastMsg?.subject ? h("span", { class: "fx-status", title: r.lastMsg.subject }, r.lastMsg.subject) : fxDash()),
                h("div", { class: "fx-c fx-c-last", role: "cell" }, r.last ? agoEl("span", "", r.last) : fxDash()),
              ),
            ),
        ];
      }),
      all.length === 0 && h("div", { class: "fx-empty" }, h("p", { class: "fx-empty-title" }, q ? "No settled sessions match “" + q + "”" : "No settled sessions")),
    ),
  );
}

// The open session is in the address, #sessions/<name>, so Back from its conversation, Forward and
// a reload all find it.
function fleetHash(name) {
  return location.pathname + location.search + "#sessions" + (name ? "/" + encodeURIComponent(name) : "");
}

function fleetFromHash(part) {
  if (!part) return null;
  try {
    return decodeURIComponent(part);
  } catch {
    return null;
  }
}

function fleetOpen(name) {
  if (!ui.fleetSel) ui.fxEnter = true;
  history.replaceState(history.state, "", fleetHash(name));
  ui.fleetSel = name;
  render();
  // Opening the pane narrows the table, so its rows grow or shrink; keep this one in view.
  root.querySelector(`[data-key="fl-${CSS.escape(name)}"], [data-key="fp-${CSS.escape(name)}"]`)?.closest(".fx-row")?.scrollIntoView({ block: "nearest" });
}

// Closing gives the keyboard back to the row, or to its group's heading if the group is shut.
// Escape does the same.
function fleetClose() {
  const name = ui.fleetSel;
  ui.fleetSel = null;
  render();
  const to = name && (root.querySelector(`[data-key="fl-${CSS.escape(name)}"]`) || root.querySelector(`[data-key="fp-${CSS.escape(name)}"]`) || (ui.fxGroupOf?.[name] && root.querySelector(`[data-key="${CSS.escape(ui.fxGroupOf[name])}"]`)));
  to?.focus({ preventScroll: true });
}

function fleetCloseAsked() {
  history.replaceState(history.state, "", fleetHash(null));
  fleetClose();
}

function fleetSetSel(want) {
  if (want === (ui.fleetSel || null)) return render();
  if (!want) return fleetClose();
  ui.fleetSel = want;
  render();
}

// Details reached by Back, Forward or a reload put focus on their row, as a click does. After a
// reload they appear only with the first data, so the poll finishes the job (fleetFocusPending).
function fleetFocusBack() {
  ui.fleetFocusPending = false;
  if (ui.view !== "sessions" || !ui.fleetSel) return;
  if (document.activeElement && document.activeElement !== document.body) return;
  const back = root.querySelector(`[data-key="fl-${CSS.escape(ui.fleetSel)}"]`) || root.querySelector(`[data-key="fp-${CSS.escape(ui.fleetSel)}"]`) || root.querySelector(".fx-pane .fx-icon-btn");
  if (back) back.focus({ preventScroll: true });
  else ui.fleetFocusPending = true;
}

// The table is one stop for Tab: one item in it is tabbable, the open session's row, else the one
// last focused, else the first; ↑/↓, Home and End move between rows and headings. The tabbable row's
// own controls (its PR, its "…") follow it with Tab.
function fleetItems() {
  return [...root.querySelectorAll(".fx-tbody .fx-open, .fx-tbody button.fx-group")];
}

function fleetRoving() {
  const items = fleetItems();
  if (!items.length) return;
  const find = (key) => key && items.find((el) => el.dataset.key === key);
  const sel = ui.fleetSel && [ui.fxFocus, "fl-" + ui.fleetSel, "fp-" + ui.fleetSel].find((k) => k && k.slice(3) === ui.fleetSel && find(k));
  const want = find(sel) || find(ui.fxFocus) || items[0];
  for (const el of root.querySelectorAll(".fx-tbody a, .fx-tbody button")) el.tabIndex = -1;
  want.tabIndex = 0;
  // The row's own controls follow it with Tab: what is shown of them, and one PR link, though the
  // row may hold its chip twice (in the Branch cell and on the line under the name).
  let pr = false;
  for (const el of want.closest(".fx-row")?.querySelectorAll("a, button") || []) {
    if (el === want || !el.getClientRects().length) continue;
    if (el.matches(".fx-pr")) {
      if (pr) continue;
      pr = true;
    }
    el.tabIndex = 0;
  }
}

document.addEventListener("focusin", (e) => {
  const el = e.target;
  if (!el?.matches?.(".fx-tbody .fx-open, .fx-tbody button.fx-group")) return;
  ui.fxFocus = el.dataset.key;
  fleetRoving();
});

// The bar and the table's header stay in view as the table scrolls; the header sits under the bar
// and the selection bar. Where the pane would leave the table under 760px, it lies over the table.
function fleetFit() {
  const split = root.querySelector(".fx-split");
  if (!split) return;
  const pane = split.querySelector(".fx-pane");
  split.classList.toggle("overlay", !!pane && split.clientWidth - pane.offsetWidth < 760);
  // The table's head sticks under the page's bar and, while picking, the selection bar.
  const tops = [split.querySelector(".fx > .bar"), split.querySelector(".fx-toolbar")];
  split.style.setProperty("--fx-tb", tops.reduce((n, el) => n + (el?.offsetHeight || 0), 0) + "px");
}
window.addEventListener("resize", () => {
  fleetFit();
  fitPreviews();
});

// A conversation's preview that is too long for its row ends at a word, with an ellipsis, rather
// than in the middle of one; the whole of it is in the title.
function fitPreviews() {
  for (const el of root.querySelectorAll(".chat-row-preview")) {
    const full = el.dataset.full || el.textContent;
    el.dataset.full = full;
    el.title = full;
    if (el.textContent !== full) el.textContent = full;
    if (el.scrollWidth <= el.clientWidth + 1) continue;
    const words = full.split(/(?<=\s)/);
    let lo = 1;
    let hi = words.length - 1;
    while (lo < hi) {
      const mid = Math.ceil((lo + hi) / 2);
      el.textContent = words.slice(0, mid).join("").trimEnd() + "…";
      if (el.scrollWidth <= el.clientWidth + 1) lo = mid;
      else hi = mid - 1;
    }
    el.textContent = words.slice(0, lo).join("").trimEnd() + "…";
    // One long word: let the line's own ellipsis cut it.
    if (lo <= 1 && el.scrollWidth > el.clientWidth + 1) el.textContent = full;
  }
}

// The pane's width is the reader's: drag its edge, or use ←/→ on it, and it stays that width.
const FLEET_PANE_KEY = NAME + ":fleet-pane-w";
const FLEET_PANE_DEFAULT = 416;

function fleetPaneFit(px) {
  // At least 20rem, and never over more than the content less a strip of the table.
  const room = (content?.clientWidth || innerWidth) - 280;
  return Math.round(Math.max(320, Math.min(px, 720, room)));
}

function fleetPaneWidth() {
  let w = FLEET_PANE_DEFAULT;
  try {
    w = Number(localStorage.getItem(FLEET_PANE_KEY)) || w;
  } catch {}
  return fleetPaneFit(w);
}

function fleetPane(name) {
  const setW = (px, save) => {
    const v = fleetPaneFit(px);
    root.querySelector(".fx-split")?.style.setProperty("--fx-pane-w", v + "px");
    grip.setAttribute("aria-valuenow", v);
    fleetFit();
    if (save) {
      try {
        localStorage.setItem(FLEET_PANE_KEY, String(v));
      } catch {}
    }
  };
  const grip = h("div", {
    class: "fx-grip",
    role: "separator",
    tabindex: "0",
    "aria-orientation": "vertical",
    "aria-label": "Resize the details",
    "aria-valuemin": "320",
    "aria-valuemax": "720",
    "aria-valuenow": String(fleetPaneWidth()),
    "data-key": "fx-grip",
    title: "Drag to resize; double-click to reset",
    onpointerdown: (e) => {
      if (e.button !== 0) return;
      e.preventDefault();
      // From the pane's own right edge: over the table it sits at the window's, past the scrollbar.
      const right = root.querySelector(".fx-pane").getBoundingClientRect().right;
      grip.setPointerCapture(e.pointerId);
      grip.classList.add("drag");
      ui.fxDragging = true;
      const move = (ev) => setW(right - ev.clientX);
      const up = (ev) => {
        grip.removeEventListener("pointermove", move);
        grip.removeEventListener("pointerup", up);
        grip.removeEventListener("pointercancel", up);
        grip.classList.remove("drag");
        ui.fxDragging = false;
        setW(right - ev.clientX, true);
      };
      grip.addEventListener("pointermove", move);
      grip.addEventListener("pointerup", up);
      grip.addEventListener("pointercancel", up);
    },
    onkeydown: (e) => {
      const by = e.key === "ArrowLeft" ? 32 : e.key === "ArrowRight" ? -32 : 0;
      if (!by) return;
      e.preventDefault();
      setW(Number(grip.getAttribute("aria-valuenow")) + by, true);
    },
    ondblclick: () => setW(FLEET_PANE_DEFAULT, true),
  });
  const enter = ui.fxEnter;
  ui.fxEnter = false;
  return h("aside", { class: "fx-pane" + (enter ? " enter" : ""), id: "fx-pane", "aria-label": "Session " + name }, grip, h("div", { class: "fx-pane-in" }, fleetInspector(name)), ui.paneMsg === name && paneCompose(name));
}

// On Conversations, "/" goes to its search, as on All sessions.
document.addEventListener("keydown", (e) => {
  if (ui.view !== "messages" || e.key !== "/" || e.ctrlKey || e.metaKey || e.altKey || ui.setup || ui.tagMenu || ui.pickMenu) return;
  if (["INPUT", "TEXTAREA", "SELECT"].includes(e.target?.tagName) || e.target?.isContentEditable) return;
  const box = root.querySelector('[data-key="cq"]');
  if (!box) return;
  e.preventDefault();
  box.focus();
  box.select();
});

// "/" goes to the search box, and ↑/↓ move between rows, as in most lists people use daily; Escape
// closes the details. A menu or a dialog that is open keeps the keys.
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && ui.tagMenu && !e.defaultPrevented) {
    ui.tagMenu = null;
    render();
    return;
  }
  if (e.key === "Escape" && ui.rowMenu && !e.defaultPrevented) return closeRowMenu();
  if (e.key === "Escape" && !e.defaultPrevented && !ui.setup && e.target?.closest?.(".fx-compose")) {
    e.preventDefault();
    ui.paneMsg = null;
    render();
    root.querySelector('[data-key="fx-msg"]')?.focus();
    return;
  }
  if (ui.view !== "sessions" || ui.tagMenu || ui.rowMenu || ui.pickMenu || ui.setup || e.ctrlKey || e.metaKey || e.altKey) return;
  if ((e.key === "ContextMenu" || (e.shiftKey && e.key === "F10")) && e.target?.classList?.contains("fx-open")) {
    const more = e.target.closest(".fx-row")?.querySelector(".fx-more");
    if (more) {
      e.preventDefault();
      openRowMenu(more.dataset.key.slice(8), more);
    }
    return;
  }
  if (e.key === "Escape" && e.target?.dataset?.key === "fx-q" && e.target.value) return; // the box clears itself
  const tag = e.target?.tagName;
  if (e.key === "/" && tag !== "INPUT" && tag !== "TEXTAREA") {
    const box = root.querySelector('[data-key="fx-q"]');
    if (box) {
      e.preventDefault();
      box.focus();
      box.select();
    }
    return;
  }
  // From the search box, ↓ or Enter goes to the first row, and ↑ from the first row comes back.
  if ((e.key === "ArrowDown" || e.key === "Enter") && e.target?.dataset?.key === "fx-q") {
    const first = root.querySelector(".fx-open");
    if (first) {
      e.preventDefault();
      first.focus();
      first.closest(".fx-row").scrollIntoView({ block: "nearest" });
    }
    return;
  }
  const items = fleetItems();
  const i = items.indexOf(e.target);
  if (i >= 0 && ["ArrowDown", "ArrowUp", "Home", "End"].includes(e.key)) {
    e.preventDefault();
    if (i === 0 && e.key === "ArrowUp") {
      root.querySelector('[data-key="fx-q"]')?.focus();
      return;
    }
    const next = items[{ ArrowDown: i + 1, ArrowUp: i - 1, Home: 0, End: items.length - 1 }[e.key]];
    if (next && next !== e.target) {
      next.focus();
      (next.closest(".fx-row") || next).scrollIntoView({ block: "nearest" });
      // With the details open, they follow the row, as a mail client's reading pane does.
      if (ui.fleetSel && /^f[lp]-/.test(next.dataset.key)) fleetOpen(next.dataset.key.slice(3));
    }
    return;
  }
  if (e.key === "Escape" && !e.defaultPrevented && ui.fleetSel) {
    e.preventDefault();
    fleetCloseAsked();
  }
});

// One session: why it needs a look, what it is, what to do with it, and its direct conversations.
// Broadcasts go to everyone, so they say nothing about this session and stay out.
function fleetInspector(name) {
  const f = findSession(name);
  const d = f.device;
  const s = f.session;
  const state = sessionState(d, s);
  const pr = prOf(s);
  const t = when(s.lastActive);
  const started = when(s.started);
  const seenAt = when(d.lastSeen);
  const agentParts = [agentName(s.agent), modelOf(s)].filter(Boolean);
  const need = fleetNeeds([{ d, s }]).get(name);
  const note = need && waitNote(d, s);
  const hidden = isHidden(s);
  const meta = [
    ["State", [fxDot(state), FX_STATE[state], hidden && h("span", { class: "fx-muted" }, "· hidden")]],
    s.doing && ["Working on", s.doing],
    // "last seen 47m ago" keeps together, so a narrow line breaks after "offline,".
    ["Device", h("span", null, deviceLabel(d) + (d.reachable ? "" : ", offline" + (seenAt ? ", " : "")), !d.reachable && seenAt && agoEl("span", "fx-nowrap", seenAt, "last seen "))],
    // Each part keeps together, so a narrow pane breaks the line at the "·", not inside a model name.
    ["Agent", agentParts.length ? h("span", { class: "fx-parts", title: agentParts.join(" · ") }, fxParts(agentParts)) : fxDash()],
    ["Branch", s.branch ? [h("span", { class: "mono" }, s.branch), pr && fxPr(pr)] : fxDash()],
    groupOf(s) && ["Job", groupOf(s)],
    s.addr && ["Tags", tagsLine(s, true)],
    ["Started", started ? agoEl("span", "", started) : fxDash()],
    ["Last active", t ? agoEl("span", "", t) : fxDash()],
  ].filter(Boolean);
  const owed = need?.msgs || [];
  const unanswered = new Set(waitingQuestions());
  // One row per conversation (the other side and the subject, a reply's "Re:" aside), since a
  // message and its reply open the same one. Conversations holding a question it owes come first,
  // oldest first; then ones marked answered; then its own questions still waiting, oldest
  // first; then the rest, newest first.
  const rank = (m) => (owed.includes(m) ? 0 : fromName(m) === name && unanswered.has(m) ? 1 : 2);
  const convMap = new Map();
  for (const m of ui.messages) {
    if (m.toKind !== "session" || (fromName(m) !== name && toName(m) !== name)) continue;
    const other = fromName(m) === name ? toName(m) : fromName(m);
    const subj = topicOf(m);
    const k = JSON.stringify([other, subj.toLowerCase()]);
    if (!convMap.has(k)) convMap.set(k, { other, subj, msgs: [] });
    convMap.get(k).msgs.push(m);
  }
  // Conversations marked answered whose question would still be waiting: each can be undone.
  const ck = (other, subj) => other + " " + subj.toLowerCase();
  const marked = new Map();
  for (const x of fleetNeeds([{ d, s }], true).get(name)?.msgs || []) {
    if (!Object.hasOwn(fleetDismissed(), x.id)) continue;
    const k = ck(fromName(x), topicOf(x));
    if (!marked.has(k)) marked.set(k, []);
    marked.get(k).push(x);
  }
  for (const x of owed) marked.delete(ck(fromName(x), topicOf(x)));
  const direct = [...convMap.values()]
    .map((c) => {
      const latest = c.msgs.reduce((a, b) => (b.sortAt > a.sortAt ? b : a));
      const waiting = c.msgs.filter((m) => rank(m) < 2).sort((a, b) => rank(a) - rank(b) || a.sortAt - b.sortAt)[0];
      const isMarked = !waiting && marked.has(ck(c.other, c.subj));
      const markedAt = isMarked ? Math.min(...marked.get(ck(c.other, c.subj)).map((x) => x.sortAt)) : 0;
      return { ...c, latest, waiting, isMarked, markedAt, rank: waiting ? rank(waiting) : isMarked ? 0.5 : 2 };
    })
    .sort((a, b) => a.rank - b.rank || (a.rank < 2 ? (a.waiting?.sortAt ?? a.markedAt) - (b.waiting?.sortAt ?? b.markedAt) : b.latest.sortAt - a.latest.sortAt));
  const owing = direct.filter((c) => c.rank === 0);
  // "Mark answered" is for a flag that's wrong: the reply was misread, or the answer came some other
  // way. It clears that one conversation, in this browser, and can be undone in place.
  const dismiss = (c) => () => {
    fleetDismiss(c.msgs.filter((x) => owed.includes(x)));
    render();
    root.querySelector(`[data-key="${CSS.escape("fx-undo-" + ck(c.other, c.subj))}"]`)?.focus({ preventScroll: true });
  };
  const undo = (c) => () => {
    fleetDismiss(marked.get(ck(c.other, c.subj)), false);
    render();
    root.querySelector(`[data-key="${CSS.escape("fx-nw-" + ck(c.other, c.subj))}"]`)?.focus({ preventScroll: true });
  };
  const openMessage = (m) => () => {
    ui.filter = null;
    ui.conversation = conversationOf(m).key;
    ui.conversationAnchor = m.id;
    ui.backTo = true;
    go("messages");
  };
  // The wait, said once: who has waited longest and for how long. Each question is in the list.
  const asker = need && fromName(need.m);
  const wait =
    need &&
    h(
      "p",
      { class: "fx-insp-wait" },
      h("span", { class: "fx-flag", "aria-hidden": "true" }),
      h(
        "span",
        null,
        owing.length > 1 ? [owing.length + " questions waiting · the oldest from ", h("b", null, asker), ", ", agoEl("span", "fx-wait", need.at, "", true)] : [h("b", null, asker), " has waited ", agoEl("span", "fx-wait", need.at, "", true)],
        note && h("span", { class: "fx-insp-note" }, " · " + note),
      ),
    );
  return [
    h(
      "header",
      { class: "fx-insp-head" },
      presence(name, d),
      h("h2", { class: "truncate" }, name),
      h("button", { class: "fx-icon-btn", title: "Close (Esc)", "aria-label": "Close the details", "data-key": "fx-close", onclick: () => fleetCloseAsked() }, icon("x")),
    ),
    wait,
    h(
      "div",
      { class: "fx-actions" },
      h(
        "button",
        {
          class: "fx-btn fx-btn-primary",
          "data-key": "fx-msg",
          "aria-label": "Message " + name,
          "aria-expanded": ui.paneMsg === name ? "true" : "false",
          onclick: () => {
            ui.paneMsg = name;
            render();
            root.querySelector(".fx-compose .composer-input")?.focus();
          },
        },
        icon("message"),
        "Message",
      ),
      h(
        "button",
        {
          class: "fx-btn",
          "data-key": "fx-convs",
          "aria-label": "Open " + name + "’s conversations",
          onclick: () => {
            ui.filter = s.addr || s.name;
            ui.conversation = null;
            ui.backTo = true;
            go("messages");
          },
        },
        "Conversations",
      ),
      s.addr &&
        h(
          "button",
          {
            class: "fx-btn fx-btn-ghost",
            "data-key": "fx-hide",
            title: hidden ? "Show " + name + " again" : "Hide " + name + " until you show it again",
            "aria-label": hidden ? "Show " + name : "Hide " + name,
            onclick: () => setHidden(s, !hidden),
          },
          icon(hidden ? "eye" : "eyeOff"),
          hidden ? "Show" : "Hide",
        ),
    ),
    h("dl", { class: "fx-meta" }, meta.map(([k, v]) => [h("dt", null, k), h("dd", null, v)])),
    h("h3", { class: "fx-h3" }, "Direct conversations", direct.length > 0 && h("span", { class: "fx-badge" }, direct.length)),
    direct.length
      ? h(
          "ul",
          { class: "fx-dms" },
          direct.map((c) => {
            const m = c.waiting || c.latest;
            const out = fromName(m) === name;
            const head =
              c.rank === 0
                ? ["From ", h("b", null, c.other), " · waiting for an answer"]
                : c.rank === 1
                  ? ["To ", h("b", null, c.other), " · no answer yet"]
                  : c.isMarked
                    ? ["From ", h("b", null, c.other), " · marked answered"]
                    : c.msgs.length === 1
                      ? [out ? "To " : "From ", h("b", null, c.other)]
                      : ["With ", h("b", null, c.other), " · " + c.msgs.length + " messages"];
            const k = ck(c.other, c.subj);
            // The subject once: not over a question that starts with it.
            const q = c.rank < 2 ? questionIn(m) : "";
            const bare = (c.subj || "").replace(/…$/, "").trim();
            const inQ = !!q && !!bare && q.replace(/…$/, "").startsWith(bare);
            return h(
              "li",
              { class: "fx-dm-item" + (c.rank === 0 ? " wait" : c.isMarked ? " marked" : "") },
              h(
                "button",
                { class: "fx-dm", "data-key": "fd-" + c.other + ":" + c.subj.toLowerCase(), onclick: openMessage(m) },
                h("span", { class: "fx-dm-head" }, h("span", null, head), agoEl("span", "fx-dm-time", m.sortAt)),
                !inQ && h("span", { class: "fx-dm-subj", title: c.subj }, c.subj || "(no subject)"),
                !!q && q !== c.subj && h("span", { class: "fx-dm-q" }, "“" + q + "”"),
              ),
              c.rank === 0 && h("button", { class: "fx-dm-act", "data-key": "fx-nw-" + k, title: "Count this question as answered, in this browser", onclick: dismiss(c) }, "Mark answered"),
              c.isMarked && h("button", { class: "fx-dm-act", "data-key": "fx-undo-" + k, onclick: undo(c) }, "Undo"),
            );
          }),
        )
      : h("p", { class: "fx-none fx-dms-none" }, "No direct messages"),
  ];
}

// The pane's Message: the composer Your messages has (its wake hint, its name asked in place, its
// way of sending), at the foot of the pane under what scrolls, so a session can be written to without leaving the
// table. After sending, a line says so and opens the conversation.
function paneCompose(name) {
  const key = dmKey(selfName(), name);
  const pair = [...new Set([selfName(), name])].sort();
  const messages = ui.messages.filter((m) => conversationOf(m).key === key).sort((a, b) => a.sortAt - b.sortAt);
  const c = { key, kind: "dm", title: pair.join(" & "), pair, messages };
  const target = targetOf(c);
  if (!target) return null;
  const sent = ui.paneSent?.name === name ? ui.paneSent : null;
  const item = sent && ui.outbox.find((x) => x.cid === sent.cid);
  const open = () => {
    ui.backTo = true;
    startChat(name);
  };
  let line = null;
  if (item?.status === "failed") {
    line = h(
      "p",
      { class: "relay-line relay-failed", role: "alert" },
      "Not sent: " + item.error,
      h("button", { class: "msg-action", "data-key": "pane-retry", onclick: () => deliver(item) }, icon("retry"), "Try again"),
    );
  } else if (item?.status === "sending") {
    line = h("p", { class: "relay-line", role: "status" }, "Sending…");
  } else if (sent) {
    line = h(
      "p",
      { class: "relay-line relay-sent", role: "status" },
      icon("check"),
      "Sent",
      h("span", { class: "relay-sep", "aria-hidden": "true" }, "·"),
      h("button", { class: "relay-open", "data-key": "pane-open", onclick: open }, "Open conversation"),
    );
  }
  return h(
    "div",
    { class: "fx-compose" },
    line,
    waitLine(c),
    composer(c, target, (x) => {
      ui.paneSent = { name, cid: x.cid };
      render();
    }),
  );
}

// Recent: the newest messages first, a page at a time; one opens its conversation.
const RECENT_PAGE = 300;
const RECENT_SEEN_KEY = NAME + ":recent-seen";

function recentPage() {
  if (!ui.state) return recentShell(null, h("p", { class: "g-none" }, "Loading messages…"));
  const all = [...ui.messages].sort((a, b) => b.sortAt - a.sortAt);
  // Scrolled down, the feed holds what it shows: newer messages wait behind a pill instead of
  // pushing what the reader is on down the page. Back at the top, they come in.
  const top = (content?.scrollTop || 0) <= 8;
  if (top) ui.recentHeldAt = null;
  else if (ui.recentHeldAt == null) ui.recentHeldAt = ui.recentShownAt ?? Infinity;
  const held = ui.recentHeldAt;
  const fresh = held == null ? 0 : all.filter((m) => m.sortAt > held).length;
  const shown = held == null ? all : all.filter((m) => m.sortAt <= held);
  const max = ui.recentMax || RECENT_PAGE;
  const list = shown.slice(0, max);
  ui.recentShownAt = list[0]?.sortAt ?? null;
  // Where the reader left off: what came since their last visit to Recent sits above a line, which
  // stays until they leave the page, as a conversation's "New" does. Muted, since what is above it
  // asks nothing of anyone.
  if (ui.recentMark === undefined) ui.recentMark = recentSeen();
  if (list.length && !document.hidden) recentSaw(list[0].sortAt);
  const mark = ui.recentMark;
  const out = [];
  let day = null;
  let newer = false;
  for (const m of list) {
    if (mark != null && m.sortAt > mark) newer = true;
    else if (newer && mark != null) {
      out.push(h("div", { class: "chat-new g-new" }, h("span", null, "Since your last visit")));
      newer = false;
    }
    const at = when(m.at);
    const d = at && new Date(at).toDateString();
    if (d && d !== day) {
      out.push(h("div", { class: "chat-day" }, h("span", null, dayLabel(at))));
      day = d;
    }
    out.push(recentItem(m, at));
  }
  const more = shown.length - list.length;
  return recentShell(
    !all.length ? null : all.length > list.length + fresh ? "Newest " + list.length + " of " + all.length : plural(all.length, "message"),
    list.length
      ? [
          fresh > 0 &&
            h(
              "div",
              { class: "list-pill-wrap g-pill-wrap" },
              h("button", { class: "list-pill", "data-key": "recent-pill", onclick: recentToTop }, icon("arrowUp"), plural(fresh, "new message")),
            ),
          h("div", { class: "g-feed", onkeydown: recentKey }, out),
          // After the feed, a control for Tab to land on: the next page, or the way back up.
          more > 0
            ? h(
                "button",
                {
                  class: "btn g-older",
                  "data-key": "recent-older",
                  onclick: () => {
                    ui.recentMax = max + RECENT_PAGE;
                    render();
                  },
                },
                "Show older messages",
              )
            : h("button", { class: "btn g-older", "data-key": "recent-top", onclick: recentToTop }, icon("arrowUp"), "Back to top"),
        ]
      : h("p", { class: "g-none" }, "No messages yet. Messages between sessions appear here as they're sent."),
  );
}

function recentShell(count, body) {
  return h("div", { class: "framed" }, pageBar({ title: "Recent messages", meta: count }), h("div", { class: "g-page g-recent" }, body));
}

// The newest message the reader has had in front of them on Recent, kept across visits.
function recentSeen() {
  try {
    const n = Number(localStorage.getItem(RECENT_SEEN_KEY));
    return n > 0 ? n : null;
  } catch {
    return null;
  }
}

function recentSaw(at) {
  if (!(at > (recentSeen() || 0))) return;
  try {
    localStorage.setItem(RECENT_SEEN_KEY, String(at));
  } catch {}
}

function recentToTop() {
  content?.scrollTo({ top: 0 });
  ui.recentHeldAt = null;
  render();
  root.querySelector(".g-item")?.focus({ preventScroll: true });
}

// The rows are one Tab stop: arrows, Home and End move between them, as in the conversation list.
function recentKey(e) {
  const items = [...e.currentTarget.querySelectorAll(".g-item")];
  const i = items.indexOf(document.activeElement);
  if (i < 0) return;
  const j = { ArrowDown: i + 1, ArrowUp: i - 1, Home: 0, End: items.length - 1 }[e.key];
  if (j == null || !items[j]) return;
  e.preventDefault();
  items[i].tabIndex = -1;
  items[j].tabIndex = 0;
  ui.recentFocus = items[j].dataset.id;
  items[j].focus();
}

function recentRoving() {
  const items = [...root.querySelectorAll(".g-feed .g-item")];
  if (!items.length) return;
  // A body cut at three lines fades out over its last one.
  for (const b of root.querySelectorAll(".g-feed .g-body")) b.classList.toggle("clamped", b.scrollHeight > b.clientHeight + 2);
  const on = items.find((el) => el.dataset.id === ui.recentFocus) || items[0];
  for (const el of items) el.tabIndex = el === on ? 0 : -1;
}

function recentItem(m, at) {
  const from = fromName(m);
  const c = conversationOf(m);
  const who = m.toKind === "all" ? "Everyone" : toName(m);
  const to = m.toKind === "all" ? [icon("broadcast"), who] : m.toKind === "device" ? [icon("laptop"), who] : who;
  const body = m.fromPerson ? personText(m) : m.body;
  const open = () => {
    ui.filter = null;
    ui.conversation = c.key;
    ui.conversationAnchor = m.id;
    ui.flash = { id: m.id, at: Date.now() };
    go("messages");
    requestAnimationFrame(() => root.querySelector(`.chat-thread [data-id="${CSS.escape(m.id)}"]`)?.scrollIntoView({ block: "center" }));
  };
  return h(
    "button",
    {
      type: "button",
      class: "chat-msg chat-msg-first g-item",
      "data-id": m.id,
      "data-key": "rc-" + m.id,
      tabindex: "-1",
      onfocus: () => (ui.recentFocus = m.id),
      onclick: open,
    },
    avatar(from),
    h(
      "span",
      { class: "chat-msg-main" },
      h(
        "span",
        { class: "chat-msg-head" },
        h("span", { class: "chat-msg-from truncate" }, from),
        m.fromPerson && humanTag(),
        h("span", { class: "g-to" }, icon("arrowRight", "g-to-arrow"), h("span", { class: "g-to-who" }, to)),
        h("span", { class: "chat-msg-time", title: messageDetail(m) }, at && new Date(at).toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" })),
      ),
      !m.fromPerson && m.subject && h("span", { class: "chat-msg-subject" }, m.subject),
      // Links show as text: the whole row is the button.
      body && h("span", { class: "chat-msg-body rich g-body" }, rich(body, true)),
    ),
    h("span", { class: "g-hint", "aria-hidden": "true" }, "Open conversation"),
  );
}

// Everyone reaches every session in the repo; a device's chat reaches every session on it. Anyone
// who has posted is in it too, running or not.
function groupMembers(c) {
  const posted = new Map();
  const addrs = new Map(); // where each sender posted from, for one that isn't running
  for (const m of c.messages) {
    posted.set(fromName(m), (posted.get(fromName(m)) || 0) + 1);
    if (m.fromAddr) addrs.set(fromName(m), m.fromAddr);
  }
  const rows = [];
  for (const d of devices()) {
    if (c.kind === "device" && d.id !== c.title) continue;
    for (const s of d.sessions) rows.push({ name: s.name, d, s, posted: posted.get(s.name) || 0 });
  }
  for (const [name, n] of posted) if (!rows.some((r) => r.name === name)) rows.push({ name, addr: addrs.get(name), d: findPerson(name)?.device || null, s: null, posted: n, person: isPerson(name) });
  return rows;
}

function membersButton(c) {
  const rows = groupMembers(c);
  const pile = rows.slice(0, 5);
  return h(
    "div",
    { class: "g-members-wrap" },
    h(
      "button",
      {
        class: "g-members",
        "aria-expanded": ui.membersOpen ? "true" : "false",
        "data-key": "members",
        onclick: (e) => {
          e.stopPropagation();
          ui.membersOpen = !ui.membersOpen;
          render();
        },
      },
      h("span", { class: "g-pile" }, pile.map((r) => avatar(r.name))),
      h("span", null, plural(rows.length, "member")),
      icon("chevronDown", "g-members-chev"),
    ),
    ui.membersOpen && membersPanel(c, rows),
  );
}

function membersPanel(c, rows) {
  const groups = new Map();
  for (const r of rows) {
    const key = r.person ? "People" : r.d ? (r.d.self ? "This device" : r.d.owner || r.d.id) : "Not running";
    if (!groups.has(key)) groups.set(key, { d: r.person ? null : r.d, host: r.d && !r.person ? host(r.d.id) : "", rows: [] });
    groups.get(key).rows.push(r);
  }
  return h(
    "div",
    { class: "g-members-panel", role: "dialog", "aria-label": "Members", onclick: (e) => e.stopPropagation() },
    h("div", { class: "g-mp-head" }, c.kind === "all" ? "Everyone in this repo" : "Every session on " + host(c.title)),
    [...groups].map(([title, g]) => [
      h("div", { class: "g-mp-sec" }, g.d && deviceDot(g.d), title, g.host && h("span", null, g.host), h("span", { class: "g-mp-n" }, g.rows.length)),
      g.rows.map((r) =>
        h(
          "button",
          {
            class: "g-mp-row" + (r.d && !r.d.reachable ? " stale" : ""),
            onclick: () => {
              ui.membersOpen = false;
              if (r.person) return startChat(r.name);
              const f = findSession(r.name);
              ui.filter = f ? f.session.addr || r.name : r.addr || r.name;
              ui.conversation = null;
              go("messages");
            },
          },
          r.d ? presence(r.name, r.d) : avatar(r.name),
          h("span", { class: "g-mp-text" }, h("span", { class: "name-line" }, h("span", { class: "g-mp-name truncate" }, r.name), r.person && humanTag()), h("span", { class: "g-mp-doing truncate" }, r.person ? "Person" : r.s ? r.s.doing || agentName(r.s.agent) : "Not running")),
          r.posted > 0 && h("span", { class: "g-mp-posted" }, r.posted === 1 ? "1 post" : r.posted + " posts"),
        ),
      ),
    ]),
  );
}
document.addEventListener("click", () => {
  if (ui.membersOpen) {
    ui.membersOpen = false;
    render();
  }
});

function topbar() {
  let meta = null;
  if (ui.conn === "down" && !ui.restarting) {
    meta = h("span", { class: "device-state" }, h("span", { class: "dot warn" }), "Disconnected");
  }
  // On a phone the sidebar, which carries the update notice, isn't shown: a dot on Settings, whose
  // Version section has the same action, stands for it.
  const update = updateInfo();
  return h(
    "header",
    { class: "topbar" },
    h("span", { class: "topbar-brand brand" }, NAME),
    h("h1", null, TITLES[ui.view]),
    h(
      "div",
      { class: "mobile-nav segmented", role: "group", "aria-label": "Views" },
      h(
        "button",
        { "aria-pressed": isMyFilter() ? "true" : "false", "data-key": "m-you", "aria-label": unreadCount() ? "You, " + unreadCount() + " unread" : "You", onclick: openYours },
        "You",
        unreadCount() > 0 && h("span", { class: "m-unread" }, unreadCount()),
      ),
      VIEWS.map((v) =>
        h(
          "button",
          {
            "aria-pressed": ui.view === v && !(v === "messages" && isMyFilter()) ? "true" : "false",
            "data-key": "m-" + v,
            title: v === "settings" && update ? update.text : null,
            onclick: () => {
              if (v === "messages") {
                ui.filter = null;
                ui.conversation = null;
              }
              go(v);
            },
          },
          v === "messages" ? "All" : TITLES[v],
          v === "settings" && update && h("span", { class: "nav-dot update-" + update.kind, "aria-label": update.text }),
        ),
      ),
    ),
    meta && h("div", { class: "topbar-meta" }, meta),
  );
}

function page() {
  if (ui.view === "sessions") return sessionsPage();
  if (ui.view === "recent") return recentPage();
  if (ui.view === "settings") return settingsPage();
  return conversationsPage();
}

// Tags (#198), under an open session's name. Each chip says where the tag came from; a human can
// take one off or add one. The change goes to the session's own device, which applies it over what
// the session set, so on another device it shows once that device has read it.
const TAG_SOURCES = { human: "Set by a human", derived: "Worked out by its daemon" };

// In the details (inDetails), a model: tag is left out: the Agent row shows the model.
function tagsLine(s, inDetails = false) {
  const tags = (Array.isArray(s.tags) ? s.tags : []).filter((t) => !(inDetails && typeof t?.name === "string" && t.name.startsWith("model:")));
  const addr = s.addr || s.name;
  const key = "t-" + addr;
  const busy = ui.tagging.has(key);
  // The row opens the session's messages; nothing in here should.
  const stop = (e) => e.stopPropagation();
  return h(
    "div",
    { class: "session-tags", onclick: stop, onkeydown: stop },
    tags.map((t) =>
      h(
        "span",
        {
          class: "badge tag badge-outline",
          title: TAG_SOURCES[t.src] || "Set by the session",
        },
        t.name,
        h(
          "button",
          {
            class: "tag-remove",
            "aria-label": "Remove " + t.name,
            title: "Remove " + t.name,
            "data-key": key + "-rm-" + t.name,
            disabled: busy,
            onclick: () => changeTags(addr, key, [], [t.name]),
          },
          "×",
        ),
      ),
    ),
    ui.tagInput === key
      ? tagField(addr, key)
      : h(
          "button",
          {
            class: "btn btn-ghost btn-xs",
            "data-key": key + "-add",
            disabled: busy,
            onclick: () => {
              ui.tagInput = key;
              delete ui.rowErrors[key];
              render();
              root.querySelector(`[data-key="${CSS.escape(key + "-field")}"]`)?.focus();
            },
          },
          "+ Tag",
        ),
    ui.rowErrors[key] && h("span", { class: "row-error" }, ui.rowErrors[key]),
  );
}

function tagField(addr, key) {
  const close = () => {
    ui.tagInput = null;
    render();
  };
  const input = h("input", {
    type: "text",
    placeholder: "reviewing, model:opus-5.5",
    "aria-label": "Tags to add",
    "data-key": key + "-field",
    autocomplete: "off",
    spellcheck: "false",
    onkeydown: (e) => {
      if (e.key === "Escape") {
        e.preventDefault();
        close();
      } else if (e.key === "Enter") {
        e.preventDefault();
        const add = e.target.value.split(/[\s,]+/).filter(Boolean);
        ui.tagInput = null;
        if (add.length > 0) changeTags(addr, key, add, []);
        else render();
      }
    },
    onblur: (e) => {
      if (ui.tagInput === key && !e.target.value.trim()) close();
    },
  });
  return h("span", { class: "field" }, input);
}

async function changeTags(addr, key, add, remove) {
  ui.tagging.add(key);
  delete ui.rowErrors[key];
  render();
  try {
    await api("/api/tags", { method: "POST", body: JSON.stringify({ addr, add, remove }) });
  } catch (e) {
    if (e instanceof AuthError) return deny(e.token);
    ui.rowErrors[key] = e.message;
  }
  ui.tagging.delete(key);
  render();
  poll();
}

// Tags on the session lists (#270): on each row and card, in the sidebar's Tags group, which
// filters every list of sessions to one tag, and in the tag menu, which puts tags on one session
// or on several picked on All sessions, and makes new ones. Tags with a meaning of their own show
// as that meaning instead: a model: tag beside the agent, do-not-disturb as the red dot, hidden as
// the quiet fold, human on a person (#267).
const OWN_MEANING = new Set([HIDDEN, "do-not-disturb", "human"]);

function sortsBy(name) {
  return typeof name === "string" && !name.startsWith("model:") && !OWN_MEANING.has(name);
}

// internal/tags's rule, so the menu can say so before asking.
function validTag(name) {
  return name.length <= 40 && /^[a-z0-9][a-z0-9._:\/-]*$/.test(name) && name !== "model:";
}

// A change this page asked for, by address and then tag, until the state it polls agrees: another
// device's sessions carry it only once that device has read the change, and until then the chip
// shows as waiting. A wish stands until its request is done (done, the time) and a state asked for
// after that agrees: an older state can't, since it may not hold an earlier change still on its way
// (#271's review). Two minutes after its request is done the state wins, so a device that never
// applies it (one from before tags, or one that is down) doesn't leave a chip that isn't there.
const tagWanted = new Map();
const TAG_WAIT_MS = 2 * 60000;

// A session's tags as the lists show them: { names, waiting }.
function tagsOf(s) {
  const have = new Set((s.tags || []).filter((t) => t && typeof t.name === "string").map((t) => t.name));
  const waiting = new Set();
  const w = s.addr && tagWanted.get(s.addr);
  if (w) {
    for (const [name, x] of w) {
      if (x.done && ((have.has(name) === x.on && stateAskedAt > x.done) || Date.now() - x.done > TAG_WAIT_MS)) {
        w.delete(name);
        continue;
      }
      x.on ? have.add(name) : have.delete(name);
      waiting.add(name);
    }
    if (!w.size) tagWanted.delete(s.addr);
  }
  return { names: [...have].filter(sortsBy).sort(), waiting };
}

function hasTag(s, name) {
  return tagsOf(s).names.includes(name);
}

function shownSessions(list) {
  return ui.tagFilter ? list.filter((s) => hasTag(s, ui.tagFilter)) : list;
}

function sessionByAddr(addr) {
  for (const d of ui.state?.devices || []) for (const s of d.sessions || []) if (s.addr === addr) return s;
  return null;
}

// Every tag on a running session, with how many sessions have it.
function tagCounts() {
  const counts = new Map();
  for (const d of ui.state?.devices || []) {
    for (const s of d.sessions || []) for (const n of tagsOf(s).names) counts.set(n, (counts.get(n) || 0) + 1);
  }
  return counts;
}

// A session's tags as small chips, the one the lists are filtered by first; past max, a count of
// the rest. In a sidebar row they share a line with the branch, and those that don't fit drop off
// whole, so the line's tooltip names them all.
function tagChips(s, max = Infinity, line = false) {
  const { names, waiting } = tagsOf(s);
  if (!names.length) return null;
  if (ui.tagFilter && names.includes(ui.tagFilter)) names.sort((a, b) => (b === ui.tagFilter) - (a === ui.tagFilter));
  const shown = names.slice(0, max);
  return h(
    "span",
    { class: "stags", title: line ? names.join(", ") : null },
    shown.map((n) =>
      h("span", { class: "stag" + (n === ui.tagFilter ? " on" : "") + (waiting.has(n) ? " waiting" : ""), title: waiting.has(n) ? n + ": waiting for its device" : n }, n),
    ),
    names.length > shown.length && h("span", { class: "stag-more", title: names.slice(max).join(", ") }, "+" + (names.length - shown.length)),
  );
}

// The Tag menu in All sessions' bar: the tags in use, each with how many sessions have it.
function tagMenuButton() {
  const counts = tagCounts();
  if (ui.tagFilter && !counts.has(ui.tagFilter)) ui.tagFilter = null;
  if (!counts.size) return null;
  const items = [["", "All", null], ...[...counts].sort((a, b) => a[0].localeCompare(b[0])).map(([n, c]) => [n, n, c])];
  return menuButton("fx-tag", "Tag", items, ui.tagFilter || "", (v) => {
    ui.tagFilter = v || null;
    render();
  });
}

function openTagMenu(addrs, anchor) {
  const r = anchor.getBoundingClientRect();
  // Below the button, or above it when there is more room there than the menu's 20rem.
  const up = window.innerHeight - r.bottom < 340 && r.top > window.innerHeight - r.bottom;
  ui.tagMenu = { addrs, x: Math.max(8, Math.min(r.left, window.innerWidth - 264)), top: up ? null : r.bottom + 4, bottom: up ? window.innerHeight - r.top + 4 : null, q: "", err: "", known: new Set() };
  render();
  root.querySelector('[data-key="tagmenu-q"]')?.focus();
}

// The tag menu: each tag in use, checked when every session it is open on has it, and a field that
// finds one or makes a new one. A click puts the tag on all of them, or takes it off all of them.
function tagMenu() {
  const m = ui.tagMenu;
  const targets = m.addrs.map(sessionByAddr).filter(Boolean);
  if (!targets.length) {
    ui.tagMenu = null;
    return null;
  }
  const close = () => {
    ui.tagMenu = null;
    render();
  };
  const q = m.q.trim().toLowerCase();
  // A tag stays listed until the menu closes, so taking it off its last session doesn't take the
  // row from under the pointer.
  for (const n of tagCounts().keys()) m.known.add(n);
  for (const n of ui.madeTags) m.known.add(n);
  const known = m.known;
  const rows = [...known].filter((n) => !q || n.includes(q)).sort();
  const make = q && !known.has(q);
  const toggle = (n) => {
    const on = !targets.every((s) => hasTag(s, n));
    tagSessions(targets, n, on);
  };
  const create = () => {
    if (!validTag(q)) {
      m.err = "Lowercase letters, digits and . _ : / -, up to 40";
      return render();
    }
    // These have their own controls (Hide, the conversation header, the session's model), and the
    // menu, which lists only tags to sort by, couldn't show them as on to take them off again.
    if (!sortsBy(q)) {
      m.err = q + " has a meaning of its own; set it under the session's name";
      return render();
    }
    ui.madeTags.add(q); // sortsBy(q) holds, from above
    m.q = "";
    tagSessions(targets, q, true);
  };
  // Placed through the CSSOM: the page's CSP refuses style attributes.
  const menu = h(
    "div",
    { class: "tag-menu", role: "dialog", "aria-label": "Tags" },
    h("div", { class: "tag-menu-head" }, targets.length === 1 ? targets[0].name : targets.length + " sessions"),
    h(
      "label",
      { class: "tag-menu-q" },
      icon("search"),
      h("input", {
        type: "text",
        value: m.q,
        placeholder: "Find or make a tag",
        autocomplete: "off",
        spellcheck: "false",
        "aria-label": "Find or make a tag",
        "data-key": "tagmenu-q",
        oninput: (e) => {
          m.q = e.target.value;
          m.err = "";
          render();
        },
        onkeydown: (e) => {
          if (e.key !== "Enter") return;
          e.preventDefault();
          if (make) create();
          else if (rows.includes(q)) toggle(q);
          else if (rows.length === 1) toggle(rows[0]);
        },
      }),
    ),
    h(
      "div",
      { class: "tag-menu-list" },
      rows.map((n) => {
        const have = targets.filter((s) => hasTag(s, n)).length;
        const all = have === targets.length;
        return h(
          "button",
          {
            class: "tag-menu-row",
            role: "menuitemcheckbox",
            "aria-checked": all ? "true" : have ? "mixed" : "false",
            "data-key": "tm-" + n,
            onclick: () => toggle(n),
          },
          h("span", { class: "tm-box" + (all ? " on" : have ? " some" : "") }, all && icon("check")),
          h("span", { class: "tm-name truncate" }, n),
          targets.length > 1 && have > 0 && !all && h("span", { class: "tm-n" }, have + " of " + targets.length),
        );
      }),
      make &&
        h(
          "button",
          { class: "tag-menu-row tm-make", "data-key": "tm-make", onclick: create },
          h("span", { class: "tm-box tm-plus" }, "+"),
          h("span", { class: "tm-name truncate" }, "Make “" + q + "”"),
        ),
      !rows.length && !make && h("p", { class: "tm-none" }, "No tags yet"),
    ),
    m.err && h("p", { class: "tm-err" }, m.err),
  );
  menu.style.left = m.x + "px";
  if (m.top != null) menu.style.top = m.top + "px";
  else menu.style.bottom = m.bottom + "px";
  return h(
    "div",
    {
      class: "menu-scrim",
      onclick: (e) => {
        if (e.target === e.currentTarget) close();
      },
      onkeydown: (e) => {
        if (e.key === "Escape") {
          e.preventDefault();
          close();
        }
      },
    },
    menu,
  );
}

// Puts a tag on, or takes it off, each of the sessions that needs the change: one request each,
// to the session's own device. The lists show the change at once, as waiting until it lands.
// A session's changes go one after another, in the order they were clicked: the daemon times a
// change when it gets it, so two in flight at once could land the other way round.
const tagQueues = new Map();

function queueTagChange(addr, fn) {
  const run = (tagQueues.get(addr) || Promise.resolve()).then(fn);
  const tail = run.catch(() => {});
  tagQueues.set(addr, tail);
  tail.then(() => {
    if (tagQueues.get(addr) === tail) tagQueues.delete(addr);
  });
  return run;
}

async function tagSessions(targets, name, on) {
  const todo = targets.filter((s) => hasTag(s, name) !== on);
  const wishes = new Map();
  for (const s of todo) {
    if (!tagWanted.has(s.addr)) tagWanted.set(s.addr, new Map());
    const wish = { on, done: 0 };
    tagWanted.get(s.addr).set(name, wish);
    wishes.set(s.addr, wish);
  }
  if (ui.tagMenu) ui.tagMenu.err = "";
  render();
  const failed = [];
  let denied = null;
  await Promise.all(
    todo.map(async (s) => {
      try {
        await queueTagChange(s.addr, () =>
          api("/api/tags", { method: "POST", body: JSON.stringify({ addr: s.addr, add: on ? [name] : [], remove: on ? [] : [name] }) }),
        );
        wishes.get(s.addr).done = Date.now();
      } catch (e) {
        // A later click on the same tag has its own wish; only this one's is dropped.
        const w = tagWanted.get(s.addr);
        if (w?.get(name) === wishes.get(s.addr)) w.delete(name);
        if (e instanceof AuthError) denied = e;
        else failed.push({ s, msg: e.message });
      }
    }),
  );
  if (denied) return deny(denied.token);
  if (failed.length) {
    const msg = "Couldn't " + (on ? "tag " : "untag ") + failed.map((f) => f.s.name).join(", ") + ": " + failed[0].msg;
    if (ui.tagMenu) ui.tagMenu.err = msg;
    else ui.notice = msg;
  }
  render();
  poll();
}

// Messages.

// A session's messages: those it sent, and those it was sent, to it, its device or everyone. The
// filter is the session's address, which stays put when its name lands after it was chosen. Once
// it has a name, any address now called by that name is the same conversation: one that came back
// under a new session ID keeps its name.
function visibleMessages() {
  const f = ui.filter;
  if (!f) return ui.messages;
  if (isMyFilter()) return myMessages();
  const name = nameOf(f, f);
  const slash = f.indexOf("/");
  const device = slash > 0 ? f.slice(0, slash) : findSession(name)?.device.id;
  const is = (addr, shown) => (addr ? addr === f : shown === name) || (name !== f && nameOf(addr, shown) === name);
  return ui.messages.filter(
    (m) =>
      is(m.fromAddr, m.from) ||
      (m.toKind === "session" && is(m.toAddr, m.to)) ||
      (m.toKind === "device" && m.to === device) ||
      m.toKind === "all",
  );
}

// Your messages: direct ones, and the group chats you have posted in, whole.
function myMessages() {
  const mine = myName();
  if (!mine) return [];
  const groups = new Set();
  for (const m of ui.messages) if (m.toKind !== "session" && fromName(m) === mine) groups.add(conversationOf(m).key);
  return ui.messages.filter((m) => (m.toKind === "session" ? fromName(m) === mine || toName(m) === mine : groups.has(conversationOf(m).key)));
}

function noMessages() {
  return ui.filter ? "No messages with " + nameOf(ui.filter, ui.filter) : "No messages";
}

// Who sent a message and when, as far as the page knows.
function messageDetail(m) {
  const at = when(m.at);
  const sender = findSession(fromName(m));
  return [sender && [agentName(sender.session.agent), sender.device.id].filter(Boolean).join(" on "), at && stamp(at)]
    .filter(Boolean)
    .join(" · ");
}

// Conversations: two sessions' messages to each other, a device's, and everyone's. There is no
// thread on the bus, so this is all a conversation can be. They are keyed by name, not address, so
// a session that came back under a new session ID stays in the same one.
function conversationOf(m) {
  if (m.toKind === "all") return { key: "all", kind: "all", title: "Everyone" };
  if (m.toKind === "device") return { key: "device:" + m.to, kind: "device", title: m.to };
  const pair = [...new Set([fromName(m), toName(m)])].sort();
  return { key: "dm:" + JSON.stringify(pair), kind: "dm", title: pair.join(" & "), pair };
}

// Newest conversation first, and each one's messages oldest first. The page holds messages as they
// arrived, device by device, so a reply can be held ahead of what it answers; sortAt, from the
// senders' clocks, puts them back in order.
function conversations() {
  const byKey = new Map();
  for (const m of visibleMessages()) {
    const c = conversationOf(m);
    if (!byKey.has(c.key)) byKey.set(c.key, { ...c, messages: [] });
    byKey.get(c.key).messages.push(m);
  }
  // A conversation just begun has no messages yet, and is at the top while it is open.
  const fresh = draftConversation();
  if (fresh && (!ui.filter || isMyFilter()) && !byKey.has(fresh.key)) byKey.set(fresh.key, fresh);
  // So is one whose only message hasn't gone: nothing else lists it, and a message that failed has
  // to stay reachable, with its text and its Try again, after the person looks at something else.
  if (!ui.filter || isMyFilter()) {
    for (const item of ui.outbox) if (!byKey.has(item.key)) byKey.set(item.key, { ...item.conversation, messages: [] });
  }
  const list = [...byKey.values()];
  for (const c of list) c.messages.sort((a, b) => a.sortAt - b.sortAt);
  const latest = (c) => (c.messages.length ? c.messages[c.messages.length - 1].sortAt : Infinity);
  return list.sort((a, b) => latest(b) - latest(a) || 0); // two with no messages subtract to NaN
}

function dmKey(a, b) {
  return "dm:" + JSON.stringify([...new Set([a, b])].sort());
}

function draftConversation() {
  if (ui.restored?.key === ui.conversation) return { ...ui.restored, messages: [] };
  if (!ui.draftTo) return null;
  const pair = [...new Set([selfName(), ui.draftTo])].sort();
  const key = dmKey(selfName(), ui.draftTo);
  return ui.conversation === key ? { key, kind: "dm", title: pair.join(" & "), pair, messages: [] } : null;
}

// The same name always gets the same color. The classes are in style.css: the page's content
// policy doesn't allow an inline style.
function avatar(name) {
  let n = 0;
  for (const ch of name) n = (n * 31 + ch.codePointAt(0)) % 997;
  return h("span", { class: isPerson(name) ? "avatar avatar-person" : "avatar avatar-" + (n % 5), "aria-hidden": "true" }, (name[0] || "?").toUpperCase());
}

// A group chat is named for who it reaches, and its face is its members'.
function groupName(c) {
  if (c.kind === "all") return "Everyone";
  const d = (ui.state?.devices || []).find((x) => x.id === c.title);
  return "Everyone on " + (!d ? host(c.title) : d.self ? "this device" : deviceLabel(d));
}

function groupHost(c) {
  return c.kind === "device" ? host(c.title) : "";
}

function groupSummary(c) {
  const rows = groupMembers(c);
  const on = rows.filter((r) => r.d && sessionState(r.d, r.s) === "on").length;
  return { rows, on };
}

// Up to four members' faces in a square, the last showing how many more there are.
function groupIcon(c) {
  const { rows } = groupSummary(c);
  const shown = rows.length > 4 ? rows.slice(0, 3) : rows.slice(0, 4);
  return h(
    "span",
    { class: "g-grid g-grid-" + Math.min(rows.length, 4), "aria-hidden": "true" },
    shown.map((r) => avatar(r.name)),
    rows.length > 4 && h("span", { class: "avatar g-more" }, "+" + (rows.length - 3)),
  );
}

function conversationIcon(c) {
  if (c.kind !== "dm") return groupIcon(c);
  if (c.pair.length === 1) return dotted(c.pair[0]);
  return h("span", { class: "avatar-pair", "aria-hidden": "true" }, c.pair.map(avatar));
}

// Under a conversation between two others' name: each one's state, a dot and its word.
function pairStates(c) {
  return h(
    "div",
    { class: "chat-about pair-states" },
    c.pair.map((n) => {
      const { st, word } = presenceOf(n);
      return h("span", { class: "pair-state" }, stateDot(st, "pres pres-" + st), h("span", { class: "truncate" }, n + " · " + word));
    }),
  );
}

let chatMoved = false; // the open conversation has just changed key, and is still the one being read

function conversationsPage() {
  const list = conversations();
  // On a wide page a conversation is always open, and stays open while others get newer. On a
  // narrow one the list and a conversation take turns, so the list is where it starts.
  // A name landing changes a conversation's key, so one that has gone is looked for by a message
  // it held before settling for the newest.
  if (ui.conversation && !list.some((c) => c.key === ui.conversation)) {
    ui.conversation = list.find((c) => c.messages.some((m) => m.id === ui.conversationAnchor))?.key || null;
    chatMoved = ui.conversation != null;
  }
  if (!ui.conversation && list.length && !NARROW.matches) ui.conversation = list[0].key;
  // A search or a tab keeps what is open among what the list shows: the first of them, if the open
  // one isn't, and nothing if nothing is. On a phone the list is all there is meanwhile.
  const view = listView(holdOrder(list), list);
  const shown = [...view.rows, ...view.older];
  const narrowed = !!view.q.trim() || view.kind !== "all";
  let open = list.find((c) => c.key === ui.conversation);
  if (narrowed && !NARROW.matches && !shown.includes(open) && shown.length) {
    open = shown[0];
    ui.conversation = open.key;
  }
  ui.conversationAnchor = open?.messages[open.messages.length - 1]?.id;
  if (ui.newMark && ui.newMark.key !== ui.conversation) ui.newMark = null;
  if (!isMyFilter()) ui.lp = null;
  return h(
    "div",
    { class: "page chat-page" },
    // A picked session keeps its header, and so its tags, before it has any messages.
    conversationsBar(view),
    list.length === 0 && !ui.filter
      ? h("div", { class: "blank" }, noMessages())
      : h(
          "div",
          { class: "chat" + (open ? " chat-open" : "") },
          conversationList(list, view),
          open ? conversationPane(open) : NARROW.matches ? null : h("section", { class: "chat-pane chat-pane-empty", "aria-hidden": "true" }),
        ),
  );
}

// Conversations' bar: whose conversations they are and how many, the tabs, and the search. On one
// session's conversations, a way to message it; come from All sessions, a way back.
function conversationsBar(view) {
  const { name, kind, count } = view;
  const f = name && findSession(name);
  const settled = name && !f && settledSessions().find((r) => r.name === name);
  return pageBar({
    lead: ui.backTo && h("button", { class: "g-back", "data-key": "g-back", onclick: () => history.back() }, icon("arrowLeft"), "All sessions"),
    title: isMyFilter() ? "Your messages" : name || "All conversations",
    titleTip: f ? f.session.doing : null,
    meta: settled ? "Settled · " + ago(settled.last || 0) : view.total > 0 || view.q.trim() ? countLine(view) : null,
    tabs:
      view.tabs &&
      barTabs(
        "Which conversations",
        [
          ["all", "All", count("all"), "kind-all"],
          ["dm", "Direct", count("dm"), "kind-dm"],
          ["group", "Group", count("group"), "kind-group"],
        ],
        kind,
        (id) => {
          ui.convKind = id;
          render();
        },
      ),
    right: [
      f && h("button", { class: "bar-menu", "data-key": "head-msg", onclick: () => startChat(name) }, icon("message"), "Message"),
      (view.total > 0 || view.q.trim()) &&
        barSearch("cq", "Search conversations", ui.cq, (v) => {
          ui.cq = v;
          render();
        }),
    ],
  });
}

// What the list shows, and the counts that describe it. A conversation counts once it has a
// message, or one on its way; settled sessions' conversations count, folded or not, so the header,
// the tabs and the sidebar all count the same list. A search counts what it finds.
function listView(list, all = list) {
  const name = ui.filter && !isMyFilter() ? nameOf(ui.filter, ui.filter) : "";
  const q = (ui.cq || "").trim().toLowerCase();
  let kind = ui.convKind || "all";
  const hit = (c) => !q || (otherIn(c) || (c.kind === "dm" ? c.title : groupName(c))).toLowerCase().includes(q) || c.messages.some((m) => (m.subject + " " + (m.body || "")).toLowerCase().includes(q));
  const ofKind = (c, k) => k === "all" || (k === "dm") === (c.kind === "dm");
  // Direct messages with a settled session fold away at the end, unless searching or on one session.
  const old = (c) => !q && !name && c.kind === "dm" && c.pair.some((n) => isSettled(n));
  const both = (k) => all.some((c) => isCounted(c) && ofKind(c, k));
  if (!(both("dm") && both("group"))) kind = ui.convKind = "all";
  const shown = list.filter((c) => hit(c) && ofKind(c, kind));
  const counted = all.filter(isCounted);
  const kinds = (k) => counted.filter((c) => ofKind(c, k)).length;
  return {
    q: ui.cq || "",
    kind,
    name,
    rows: shown.filter((c) => !old(c)),
    older: shown.filter(old),
    total: counted.length,
    count: (k) => counted.filter((c) => hit(c) && ofKind(c, k)).length,
    // Tabs only where there are both kinds to tell apart.
    tabs: kinds("dm") > 0 && kinds("group") > 0,
  };
}

// The list holds still under the reader. While the pointer is over it, a row in it has focus, or
// it is scrolled down, it keeps the order it had when that began: conversations that get newer
// stay where they are, ones that start don't appear, and a pill at its top counts them. Clicking
// the pill, or leaving the list (pointer and focus gone, scrolled to the top), puts it in order.
// The open conversation is always there, and its new messages show in the thread as they come.
function listBusy() {
  return !!ui.listHover || !!document.activeElement?.matches?.(".chat-list .chat-row, .chat-list .g-sec-toggle") || (ui.chatListTop || 0) > 8;
}

function listSnapshot(list) {
  return { scope: ui.view + " " + (ui.filter || ""), order: list.map((c) => c.key), latest: new Map(list.map((c) => [c.key, lastId(c)])) };
}

function lastId(c) {
  return c.messages[c.messages.length - 1]?.id || "";
}

function holdOrder(list) {
  const scope = ui.view + " " + (ui.filter || "");
  ui.heldNew = 0;
  if (ui.held?.scope !== scope) ui.held = null;
  if (!listBusy()) {
    ui.held = null;
    ui.lastShown = listSnapshot(list);
    return list;
  }
  if (!ui.held) ui.held = ui.lastShown?.scope === scope ? ui.lastShown : listSnapshot(list);
  const at = new Map(ui.held.order.map((k, i) => [k, i]));
  ui.heldNew = list.filter((c) => c.key !== ui.conversation && (!at.has(c.key) || ui.held.latest.get(c.key) !== lastId(c))).length;
  return list.filter((c) => at.has(c.key) || c.key === ui.conversation).sort((a, b) => (at.get(a.key) ?? -1) - (at.get(b.key) ?? -1));
}

function releaseList() {
  if (ui.held && !listBusy()) {
    ui.held = null;
    if (ui.view === "messages") render();
  }
}

document.addEventListener("pointermove", (e) => {
  const over = !!e.target?.closest?.(".chat-list");
  if (over === !!ui.listHover) return;
  ui.listHover = over;
  if (!over) releaseList();
});
document.documentElement.addEventListener("pointerleave", () => {
  ui.listHover = false;
  releaseList();
});
document.addEventListener("focusout", () => setTimeout(releaseList, 0));

function isCounted(c) {
  return c.messages.length > 0 || pendingIn(c).length > 0;
}

function countLine(view) {
  if (view.q.trim()) return "Showing " + view.count(view.kind) + " of " + plural(view.total, "conversation");
  if (view.kind === "dm") return plural(view.count("dm"), "direct conversation");
  if (view.kind === "group") return plural(view.count("group"), "group conversation");
  return plural(view.total, "conversation");
}

// A search that finds nothing in the list's tab says so in the list, with a way back.
function noMatch(view) {
  const what = view.kind === "dm" ? "No direct conversations" : view.kind === "group" ? "No group conversations" : "No conversations";
  return h(
    "div",
    { class: "g-nomatch" },
    h("p", null, what + " match \u201c" + view.q.trim() + "\u201d"),
    h(
      "button",
      {
        class: "btn btn-outline btn-xs",
        "data-key": "clear-cq",
        onclick: () => {
          ui.cq = "";
          render();
          root.querySelector('[data-key="cq"]')?.focus();
        },
      },
      "Clear search",
    ),
  );
}

// A title whose names wrap as whole names: a line may break between them, not at a hyphen in one.
function wholeNames(text) {
  return String(text)
    .split(/(\s+)/)
    .map((t) => (t.includes("-") ? h("span", { class: "nw" }, t) : t));
}

// A direct conversation you are in is "You & solar-wren", wherever it is listed or open.
function dmTitle(c) {
  const self = selfName();
  if (c.kind !== "dm" || !c.pair.includes(self) || c.pair.length < 2) return null;
  return "You & " + c.pair.find((n) => n !== self);
}

// With a session picked, a chat with it is called by the other session's name.
function otherIn(c) {
  if (c.kind !== "dm") return null;
  const self = ui.filter ? nameOf(ui.filter, ui.filter) : myName();
  return self && c.pair.includes(self) ? c.pair.find((n) => n !== self) || self : null;
}

// The middle column: whose conversations they are, a search, then direct messages and group chats
// in one list. Direct messages with a settled session fold away at the end.
// One list, newest first, so a busy group chat is never below every quiet DM. The tabs narrow it to
// direct messages or group chats. The rows are one stop for Tab, and the arrow keys, Home and End
// move between them.
function conversationList(list, view) {
  const { name, rows, older, kind, count } = view;
  const q = view.q.trim();
  return h(
    "nav",
    {
      class: "chat-list g-list",
      "aria-label": "Conversations",
      onscroll: (e) => {
        ui.chatListTop = e.target.scrollTop;
        if (ui.chatListTop <= 8) releaseList();
      },
      onkeydown: chatListKey,
    },
    isMyFilter() && view.total > 0 && !ui.lp &&
      h(
        "button",
        {
          class: "g-new-row",
          "data-key": "new-row",
          "aria-expanded": "false",
          onclick: () => {
            ui.lp = { q: "", index: 0 };
            render();
            root.querySelector('[data-key="lp-q"]')?.focus();
          },
        },
        h("span", { class: "g-new-icon", "aria-hidden": "true" }, icon("compose")),
        "New message",
      ),
    isMyFilter() && (ui.lp || (!view.total && !q)) ? listPicker(view.total > 0) : listRows(view, rows, older, kind, count, q),
  );
}

// The rows.
function listRows(view, rows, older, kind, count, q) {
  return [
    // Over the rows, taking no room, so it moves nothing under the pointer.
    ui.heldNew > 0 &&
      h(
        "div",
        { class: "list-pill-wrap" },
        h(
          "button",
          {
            class: "list-pill",
            "data-key": "list-pill",
            title: "Put the list in order",
            onclick: () => {
              ui.held = null;
              ui.lastShown = null;
              ui.chatListTop = 0;
              render();
            },
          },
          icon("arrowUp"),
          ui.heldNew + " new",
        ),
      ),
    rows.map(conversationRow),
    older.length > 0 &&
      h(
        "button",
        {
          class: "g-sec g-sec-toggle",
          "aria-expanded": ui.showOlder ? "true" : "false",
          "data-key": "older",
          onclick: () => {
            ui.showOlder = !ui.showOlder;
            render();
          },
        },
        h("span", null, icon(ui.showOlder ? "chevronDown" : "chevronRight", "settled-caret"), "With settled sessions"),
        h("span", { class: "g-sec-n" }, older.length),
      ),
    ui.showOlder && older.map(conversationRow),
    // A search or a tab with nothing in it is said in the list, where the rows would be.
    !rows.length && !older.length && (q ? noMatch(view) : h("p", { class: "g-none" }, kind === "dm" ? "No direct messages" : kind === "group" ? "No group chats" : "No conversations")),
  ];
}

function chatItems() {
  return [...root.querySelectorAll(".chat-list .chat-row, .chat-list .g-sec-toggle")];
}

// The list's one tabbable row: the focused one, else the open conversation's, else the first.
function chatRoving() {
  const items = chatItems();
  if (!items.length) return;
  const want = items.find((el) => el === document.activeElement) || items.find((el) => el.getAttribute("aria-current") === "true") || items[0];
  for (const el of items) el.tabIndex = el === want ? 0 : -1;
}

function chatListKey(e) {
  if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(e.key) || e.altKey || e.ctrlKey || e.metaKey) return;
  const items = chatItems();
  const i = items.indexOf(e.target);
  if (i < 0) return;
  e.preventDefault();
  const next = items[Math.max(0, Math.min(items.length - 1, { ArrowDown: i + 1, ArrowUp: i - 1, Home: 0, End: items.length - 1 }[e.key]))];
  for (const el of items) el.tabIndex = el === next ? 0 : -1;
  next.focus();
  next.scrollIntoView({ block: "nearest" });
  // A row reached opens, as in Linear's lists: the focused row and the open one are the same.
  // Enter then goes on to the composer.
  if (next.classList.contains("chat-row") && next.getAttribute("aria-current") !== "true") {
    ui.conversation = next.dataset.key.slice(2);
    ui.conversationAnchor = null;
    render();
  }
}

function conversationRow(c) {
  const last = c.messages[c.messages.length - 1];
  const at = last && when(last.at);
  const o = otherIn(c);
  const self = ui.filter ? nameOf(ui.filter, ui.filter) : myName();
  const unread = unreadIn(c);
  const failed = pendingIn(c).find((item) => item.status === "failed");
  const preview = failed ? "Not sent: " + failed.text : !last ? "New conversation" : (o && fromName(last) === o ? "" : (o && fromName(last) === self) || fromName(last) === myName() ? "You: " : fromName(last) + ": ") + last.subject;
  return h(
    "button",
    {
      class: "chat-row" + (unread ? " chat-row-unread" : ""),
      "aria-current": c.key === ui.conversation ? "true" : null,
      "data-key": "c-" + c.key,
      onclick: () => {
        ui.conversation = c.key;
        ui.conversationAnchor = c.messages[c.messages.length - 1]?.id; // the key can be out of date
        render();
        focusComposer();
      },
    },
    // One 20px face: the other party's, the first of two others', or a group's glyph.
    c.kind !== "dm" ? h("span", { class: "avatar avatar-group", "aria-hidden": "true" }, icon("users")) : avatar(o || c.pair[0]),
    c.kind !== "dm"
      ? [h("span", { class: "chat-row-title", title: c.title }, wholeNames(groupName(c))), h("span", { class: "g-row-count" }, groupSummary(c).rows.length)]
      : h("span", { class: "chat-row-title", title: c.title }, wholeNames(dmTitle(c) || o || c.title)),
    h("span", { class: "chat-row-preview truncate" }, preview),
    unread > 0 && h("span", { class: "unread-badge", "aria-label": unread + " unread" }, unread),
    at && agoEl("span", "chat-row-time", at),
  );
}

function conversationPane(c) {
  if (ui.relayFor && ui.relayFor !== c.key) ui.relayFor = null;
  const senders = new Set(c.messages.map(fromName));
  const o = otherIn(c);
  const of = o && findSession(o);
  const gs = c.kind !== "dm" ? groupSummary(c) : null;
  const op = o && findPerson(o);
  const about = gs
    ? [groupHost(c), plural(gs.rows.length, "member"), gs.on + " active"].filter(Boolean).join(" · ")
    : o
      ? isPerson(o)
        ? op
          ? personOnline(op)
            ? "On " + NAME + " now"
            : deviceLabel(op.device)
          : ""
        : of
          ? of.session.doing || agentName(of.session.agent)
          : "Not running"
      : "";
  return h(
    "section",
    { class: "chat-pane", "aria-label": o || c.title },
    h(
      "header",
      { class: "chat-head" },
      h(
        "button",
        {
          class: "chat-back",
          "aria-label": "All conversations",
          "data-key": "chat-back",
          onclick: () => {
            ui.conversation = null;
            ui.draftTo = null;
            render();
          },
        },
        icon("arrowLeft"),
      ),
      o ? dotted(o) : conversationIcon(c),
      h(
        "div",
        { class: "chat-head-text" },
        h("div", { class: "name-line" }, h("h2", { class: "chat-title truncate", title: c.title }, gs ? groupName(c) : dmTitle(c) || o || c.title), o && isPerson(o) && humanTag()),
        about && h("div", { class: "chat-about truncate", title: [...senders].join(", ") }, about),
        c.kind === "dm" && !o && c.pair.length > 1 && pairStates(c),
      ),
      of && h("span", { class: "chat-state" }, stateDot(sessionState(of.device, of.session), "chat-state-dot"), stateWord(of.device, of.session)),
      c.kind !== "dm" && membersButton(c),
    ),
    h("div", { class: "chat-thread", "data-conversation": c.key }, c.messages.length || pendingIn(c).length ? chatMessages(c) : chatIntro(o)),
    waitLine(c),
    paneFoot(c),
  );
}

// An empty conversation: the header above says who it is with and how they are.
function chatIntro(name) {
  if (!name) return null;
  return h("div", { class: "chat-intro" }, h("p", { class: "chat-intro-meta" }, "No messages yet. Write below to start."));
}

// A sender's messages run together under one header, as long as they come close together.
const RUN_MS = 7 * 60 * 1000;

function chatMessages(c) {
  const out = [];
  let prev = null;
  let prevDay = null;
  for (const m of c.messages) {
    const at = when(m.at);
    const day = at && new Date(at).toDateString();
    if (day && day !== prevDay) {
      out.push(h("div", { class: "chat-day" }, h("span", null, dayLabel(at))));
      prev = null;
      prevDay = day;
    }
    const from = fromName(m);
    const head = !prev || fromName(prev) !== from || !at || at - (when(prev.at) || 0) > RUN_MS;
    if (ui.newMark?.key === c.key && ui.newMark.id === m.id) out.push(h("div", { class: "chat-new" }, h("span", null, "New")));
    out.push(chatMessage(m, from, at, head, prev || c.messages[c.messages.indexOf(m) - 1]));
    prev = m;
  }
  for (const item of pendingIn(c)) {
    const head = !prev || fromName(prev) !== myName() || item.status === "failed";
    out.push(pendingMessage(item, head));
    prev = { from: myName(), fromAddr: me()?.addr };
  }
  return out;
}

// What a person wrote, whole, from the subject and body the bus carries it as (web.SplitText).
function personText(m) {
  const subject = m.subject || "";
  const body = m.body || "";
  if (subject.endsWith("…") && body.startsWith(subject.slice(0, -1))) return body;
  return body ? subject + "\n" + body : subject;
}

// A message's text: a person's as they wrote it, an agent's with its subject over its body. A
// reply's subject that is only "Re: " and the subject before it says nothing the thread doesn't.
function messageText(m, prev) {
  if (m.fromPerson) return h("div", { class: "chat-msg-body rich" }, rich(personText(m)));
  const echo = !!m.body && !!prev?.subject && /^re:\s*/i.test(m.subject) && bareSubject(m.subject) === bareSubject(prev.subject);
  return [m.subject && !echo && h("div", { class: "chat-msg-subject" }, m.subject), m.body && h("div", { class: "chat-msg-body rich" }, rich(m.body))];
}

function bareSubject(s) {
  return (s || "").replace(/^(re:\s*)+/i, "").trim();
}

function pendingMessage(item, head) {
  const failed = item.status === "failed";
  return h(
    "article",
    { class: "chat-msg" + (head ? " chat-msg-first" : "") + (failed ? " chat-msg-failed" : " chat-msg-sending"), "data-cid": item.cid },
    head ? avatar(myName()) : h("span", { class: "avatar-gap" }),
    h(
      "div",
      { class: "chat-msg-main" },
      head && h("div", { class: "chat-msg-head" }, h("span", { class: "chat-msg-from chat-msg-you truncate" }, myName()), h("span", { class: "chat-msg-via" }, "you")),
      h("div", { class: "chat-msg-body rich" }, rich(item.text)),
      failed
        ? h(
            "div",
            { class: "msg-failed", role: "alert" },
            h("span", { class: "msg-failed-why" }, item.error),
            h("button", { class: "msg-action", "data-key": "retry-" + item.cid, onclick: () => deliver(item) }, icon("retry"), "Try again"),
            h("button", { class: "msg-action", "data-key": "edit-" + item.cid, onclick: () => editFailed(item) }, icon("pencil"), "Edit"),
          )
        : item.status === "sending" && h("div", { class: "msg-sending" }, "Sending…"),
    ),
  );
}

function dayLabel(t) {
  const today = new Date();
  if (new Date(t).toDateString() === today.toDateString()) return "Today";
  today.setDate(today.getDate() - 1);
  if (new Date(t).toDateString() === today.toDateString()) return "Yesterday";
  // Within the week, its day's name; before that, the date.
  today.setDate(today.getDate() - 5);
  today.setHours(0, 0, 0, 0);
  if (t >= today.getTime()) return new Date(t).toLocaleDateString(undefined, { weekday: "long" });
  return new Date(t).toLocaleDateString(undefined, { year: "numeric", month: "long", day: "numeric" });
}

const FLASH_MS = 1600;

function chatMessage(m, from, at, head, before) {
  const sender = findSession(from);
  const mine = from === myName();
  const copy = m.fromPerson ? personText(m) : (m.subject ? m.subject + "\n\n" : "") + (m.body || "");
  const since = ui.flash?.id === m.id ? Date.now() - ui.flash.at : -1;
  if (since >= FLASH_MS) ui.flash = null;
  const el = h(
    "article",
    { class: "chat-msg" + (head ? " chat-msg-first" : "") + (since >= 0 && since < FLASH_MS ? " chat-msg-flash" : ""), "data-id": m.id, title: head ? null : messageDetail(m) },
    head ? avatar(from) : h("span", { class: "avatar-gap" }),
    h(
      "div",
      { class: "chat-msg-main" },
      head &&
        h(
          "div",
          { class: "chat-msg-head" },
          h("span", { class: "chat-msg-from truncate" + (mine ? " chat-msg-you" : "") }, from),
          mine && h("span", { class: "chat-msg-via" }, "you"),
          !mine && m.fromPerson && humanTag(),
          sender && h("span", { class: "chat-msg-via truncate" }, [agentName(sender.session.agent), sender.device.self ? null : sender.device.id].filter(Boolean).join(" · ")),
          h("span", { class: "chat-msg-time", title: messageDetail(m) }, at && new Date(at).toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" })),
        ),
      messageText(m, before),
    ),
    h(
      "div",
      { class: "msg-tools" },
      h(
        "button",
        {
          title: "Copy",
          "aria-label": "Copy message",
          onclick: () => navigator.clipboard?.writeText(copy),
        },
        icon("copy"),
      ),
    ),
  );
  if (since >= 0 && since < FLASH_MS) el.style.setProperty("animation-delay", -since + "ms");
  return el;
}

// Message bodies are Markdown-ish text from agents. A small subset becomes structure: paragraphs,
// lists, fenced code, inline code, bold and https links. All of it reaches the page as text nodes.
function rich(text, plain = false) {
  const out = [];
  const lines = text.replace(/\r\n/g, "\n").split("\n");
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (/^\s*```/.test(line)) {
      const code = [];
      i++;
      while (i < lines.length && !/^\s*```/.test(lines[i])) code.push(lines[i++]);
      i++;
      out.push(h("pre", null, h("code", null, code.join("\n"))));
      continue;
    }
    const bullet = /^\s*[-*•] +/;
    const num = /^\s*\d+[.)] +/;
    if (bullet.test(line) || num.test(line)) {
      const re = bullet.test(line) ? bullet : num;
      const items = [];
      while (i < lines.length && lines[i].trim()) {
        if (re.test(lines[i])) items.push(lines[i].replace(re, ""));
        else if (items.length) items[items.length - 1] += " " + lines[i].trim();
        else break;
        i++;
      }
      out.push(h(re === bullet ? "ul" : "ol", null, items.map((t) => h("li", null, inline(t, plain)))));
      continue;
    }
    if (!line.trim()) {
      i++;
      continue;
    }
    const para = [];
    while (i < lines.length && lines[i].trim() && !/^\s*```/.test(lines[i]) && !bullet.test(lines[i]) && !num.test(lines[i])) para.push(lines[i++]);
    out.push(h("p", null, para.map((l, k) => [k ? h("br") : null, inline(l, plain)])));
  }
  return out;
}

function inline(t, plain = false) {
  const out = [];
  const re = /(`[^`]+`)|(\*\*[^*]+\*\*)|(https:\/\/[^\s)<>]+[^\s)<>.,;:!?'"])/g;
  let last = 0;
  for (let m; (m = re.exec(t)); ) {
    if (m.index > last) out.push(t.slice(last, m.index));
    if (m[1]) out.push(h("code", null, m[1].slice(1, -1)));
    else if (m[2]) out.push(h("strong", null, m[2].slice(2, -2)));
    else out.push(plain ? m[3] : h("a", { href: m[3], target: "_blank", rel: "noopener noreferrer" }, m[3]));
    last = re.lastIndex;
  }
  if (last < t.length) out.push(t.slice(last));
  return out;
}

let toEnd = false; // the person just sent something: show it, wherever they had scrolled to

// A render builds the panes anew. The list keeps its place. So does a conversation being read
// further up; one that is open at its end, or just opened, shows its newest message.
function chatScroll() {
  const thread = root.querySelector(".chat-thread");
  return {
    key: thread?.dataset.conversation,
    top: thread ? thread.scrollTop : 0,
    atEnd: !thread || thread.scrollHeight - thread.scrollTop - thread.clientHeight < 48,
  };
}

function restoreChatScroll(was) {
  const list = root.querySelector(".chat-list");
  const thread = root.querySelector(".chat-thread");
  if (list) list.scrollTop = ui.chatListTop;
  const same = chatMoved || thread?.dataset.conversation === was.key;
  chatMoved = false;
  if (thread) thread.scrollTop = same && !was.atEnd && !toEnd ? was.top : thread.scrollHeight;
  toEnd = false;
}

// A person's account (#242).

const personNames = new Set(); // every name known to be a person's, running or not

function me() {
  return ui.state?.account?.name ? ui.state.account : null;
}

function myName() {
  return me()?.name || "";
}

function isPerson(name) {
  return personNames.has(name) || (!!name && name === myName());
}

// Your messages can be read, and a conversation begun, before you have a name: the name is asked for
// when you first send. Until then the page calls you this, in its filter and its conversation keys.
const YOU = "@you";

function selfName() {
  return myName() || YOU;
}

function isMyFilter() {
  return ui.view === "messages" && !!ui.filter && (ui.filter === YOU || (!!me() && ui.filter === me().addr));
}

function people() {
  const out = [];
  for (const d of ui.state?.devices || []) if (d.person) out.push({ ...d.person, device: d });
  return out;
}

function findPerson(name) {
  return people().find((p) => p.name === name) || null;
}

// A person counts as here while a page they can see is calling: it does so every 3 s, and a
// presence that says so can take as long as a heartbeat to arrive.
function personOnline(p) {
  const t = when(p.lastActive);
  return p.device.reachable && !!t && Date.now() - t < 2.5 * 60000;
}

// Unread: what the daemon has down as unread, less what this page has just opened, of the messages
// it holds.
function unreadIds() {
  return (ui.state?.account?.unread || []).filter((id) => ui.ids.has(id) && !ui.readLocal.has(id));
}

function unreadCount() {
  return unreadIds().length;
}

function unreadIn(c) {
  const ids = new Set(unreadIds());
  return ids.size ? c.messages.filter((m) => ids.has(m.id)).length : 0;
}

function yourConversationCount() {
  return new Set([...myMessages().map((m) => conversationOf(m).key), ...ui.outbox.map((item) => item.key)]).size;
}

// Opening a conversation marks it read, if someone is looking: a hidden page or a window in the
// background reads nothing. What was unread when they came to it stays marked "New" until they
// leave. What arrives while they are reading it is just the next message.
function markOpenRead() {
  const looking = ui.view === "messages" && !!ui.conversation && !document.hidden && document.hasFocus();
  const arriving = looking && ui.reading !== ui.conversation;
  ui.reading = looking ? ui.conversation : null;
  if (!looking) return;
  const unread = new Set(unreadIds());
  if (!unread.size) return;
  const ids = ui.messages
    .filter((m) => unread.has(m.id) && conversationOf(m).key === ui.conversation)
    .sort((a, b) => a.sortAt - b.sortAt)
    .map((m) => m.id);
  if (!ids.length) return;
  if (arriving) ui.newMark = { key: ui.conversation, id: ids[0] };
  for (const id of ids) ui.readLocal.add(id);
  api("/api/read", { method: "POST", body: JSON.stringify({ ids }) }).catch((e) => {
    if (e instanceof AuthError) deny(e.token);
    else for (const id of ids) ui.readLocal.delete(id);
  });
  render();
}

function openYours() {
  if (!ui.state) return;
  ui.filter = me() ? me().addr : YOU;
  ui.conversation = null;
  ui.draftTo = null;
  go("messages");
  setView("messages");
}

// Open the conversation with someone, whether or not it has any messages yet.
function startChat(name) {
  if (!ui.state) return;
  if (name === myName()) return openYours();
  ui.lp = null;
  ui.filter = me() ? me().addr : YOU;
  ui.draftTo = name;
  ui.conversation = dmKey(selfName(), name);
  go("messages");
  setView("messages"); // the hash changes later, and the composer has to be there to focus now
  focusComposer();
}

// The name is asked for in a dialog, or, when it is wanted to send something, in the composer that
// is sending it (inline: that composer's textarea), so nothing covers what is being read.
function askForName(then, inline = null) {
  const a = ui.state?.account;
  ui.setup = { name: a?.suggested || "", error: "", busy: false, then, inline };
  render();
  const field = root.querySelector('[data-key="setup-name"]');
  field?.focus();
  field?.select();
}

async function submitName() {
  const setup = ui.setup;
  if (!setup || setup.busy) return;
  setup.busy = true;
  setup.error = "";
  render();
  try {
    const was = selfName();
    const account = await api("/api/account", { method: "PUT", body: JSON.stringify({ name: setup.name.trim() }) });
    ui.state.account = { ...ui.state.account, ...account };
    moveToName(was, account.name); // asked for a new name, they may have drafts under the old one
    if (ui.filter === YOU) ui.filter = account.addr;
    ui.setup = null;
    poll();
    setup.then?.();
    render();
  } catch (e) {
    if (e instanceof AuthError) return deny(e.token);
    setup.busy = false;
    setup.error = e.message;
    render();
    root.querySelector('[data-key="setup-name"]')?.focus();
  }
}

function dialog(label, close, ...kids) {
  return h(
    "div",
    {
      class: "scrim",
      onclick: (e) => {
        if (e.target === e.currentTarget) close();
      },
      onkeydown: (e) => {
        if (e.key === "Escape") {
          e.preventDefault();
          close();
        }
      },
    },
    h("div", { class: "dialog", role: "dialog", "aria-modal": "true", "aria-label": label }, kids),
  );
}

function setupDialog() {
  const s = ui.setup;
  const taken = me()?.taken || "";
  const close = () => {
    ui.setup = null;
    render();
  };
  return dialog(
    taken ? "Choose a new name" : "Choose your name",
    close,
    h("h2", { class: "dialog-title" }, taken ? "Choose a new name" : "Choose your name"),
    h(
      "p",
      { class: "dialog-line" },
      taken ? takenLine(me()) : "Agents see it, and send to it.",
    ),
    h(
      "form",
      {
        class: "setup-form",
        onsubmit: (e) => {
          e.preventDefault();
          submitName();
        },
      },
      nameField(s),
      s.error && h("p", { class: "dialog-error", role: "alert" }, s.error),
      h(
        "div",
        { class: "dialog-actions" },
        h("button", { type: "button", class: "btn btn-ghost", onclick: close }, "Cancel"),
        h("button", { type: "submit", class: "btn btn-primary", disabled: s.busy }, s.busy ? "Saving…" : "Continue"),
      ),
    ),
  );
}

function nameField(s) {
  return h(
    "div",
    { class: "setup-row" },
    h("span", { class: "setup-face" }, faceOf(s.name)),
    h(
      "span",
      { class: "field setup-field" + (s.error ? " field-bad" : "") },
      h("input", {
        type: "text",
        value: s.name,
        maxlength: 32,
        placeholder: "ada-lovelace",
        autocomplete: "off",
        autocapitalize: "none",
        spellcheck: "false",
        "aria-label": "Your name",
        "aria-invalid": s.error ? "true" : null,
        "data-key": "setup-name",
        oninput: (e) => {
          s.name = e.target.value.toLowerCase().replace(/[\s_]+/g, "-");
          s.error = "";
          if (e.target.value !== s.name) e.target.value = s.name;
          root.querySelector(".setup-face")?.replaceChildren(faceOf(s.name));
          root.querySelector(".dialog-error")?.remove();
          root.querySelector(".setup-field")?.classList.remove("field-bad");
        },
      }),
    ),
  );
}

// Sending before there is a name: the name is asked for over the composer, the draft stays where
// it is, and Send sends it once the name is saved.
function nameAsk() {
  const s = ui.setup;
  const close = () => {
    ui.setup = null;
    render();
    focusComposer();
  };
  return h(
    "form",
    {
      class: "name-ask",
      "aria-label": "Choose your name to send",
      onsubmit: (e) => {
        e.preventDefault();
        submitName();
      },
      onkeydown: (e) => {
        if (e.key !== "Escape") return;
        e.preventDefault();
        close();
      },
    },
    me()?.taken
      ? h("p", { class: "name-ask-title" }, "Choose a new name to send", h("span", { class: "name-ask-line" }, takenLine(me())))
      : h("p", { class: "name-ask-title" }, "Choose your name to send", h("span", { class: "name-ask-line" }, "Agents see it, and send to it.")),
    h(
      "div",
      { class: "name-ask-row" },
      nameField(s),
      h("button", { type: "submit", class: "btn btn-primary", "data-key": "setup-send", disabled: s.busy }, s.busy ? "Saving…" : "Send"),
      h("button", { type: "button", class: "btn btn-ghost", "data-key": "setup-cancel", onclick: close }, "Cancel"),
    ),
    s.error && h("p", { class: "dialog-error", role: "alert" }, s.error),
  );
}

// The face a name would have: a person's, whatever is typed so far.
function faceOf(name) {
  return h("span", { class: "avatar avatar-person", "aria-hidden": "true" }, (name[0] || "?").toUpperCase());
}

// Everyone a person can start a conversation with: people, then sessions, those at work first.
function reachable() {
  const rows = [];
  for (const p of people()) {
    if (p.name !== myName()) rows.push({ name: p.name, d: p.device, person: p, line: PERSON_TIPS[personOnline(p) ? "on" : "off"], rank: personOnline(p) ? 0 : 1, at: when(p.lastActive) || 0 });
  }
  for (const d of devices()) {
    for (const s of d.sessions) {
      if (isHidden(s)) continue;
      const st = sessionState(d, s);
      // One rule for every row's subtitle: its state in words, then what it is doing.
      rows.push({ name: s.name, d, s, line: [stateWord(d, s), s.doing].filter(Boolean).join(" · "), rank: st === "on" ? 0 : st === "idle" ? 1 : st === "dnd" ? 2 : 3, at: when(s.lastActive) || when(s.started) || 0 });
    }
  }
  return rows.sort((a, b) => a.rank - b.rank || b.at - a.at);
}

function matches(r, q) {
  if (!q) return true;
  return [r.name, r.line, r.s?.branch, r.s?.agent, r.d.owner, r.d.id].some((x) => (x || "").toLowerCase().includes(q));
}

// One of the people and sessions in the picker: an option of its listbox, reached with the arrow
// keys from the search over it.
function personRow(r, cur, onclick, id) {
  return h(
    "button",
    { class: "pick-row" + (cur ? " current" : ""), role: "option", id, tabindex: "-1", "aria-selected": cur ? "true" : "false", "data-pick": r.name, onclick },
    presence(r.name, r.d),
    h(
      "span",
      { class: "pick-text" },
      h("span", { class: "name-line" }, h("span", { class: "pick-name truncate" }, r.name), r.person && humanTag()),
      h("span", { class: "pick-line truncate" }, r.line),
    ),
  );
}

// Who you can start a conversation with, in the list column: all of it while you have no
// conversations, and under "New message" once you do (closable). Typing narrows it, the arrow keys
// move along it, and Enter or a click opens the conversation beside it.
function listPicker(closable) {
  const p = (ui.lp ||= { q: "", index: 0 });
  const close = () => {
    ui.lp = null;
    render();
    root.querySelector('[data-key="new-row"]')?.focus();
  };
  const all = reachable();
  const q = p.q.trim().toLowerCase();
  const rows = all.filter((r) => matches(r, q));
  p.index = Math.min(p.index, Math.max(rows.length - 1, 0));
  const move = (to) => {
    p.index = Math.max(0, Math.min(rows.length - 1, to));
    render();
    root.querySelector(".lp .pick-row.current")?.scrollIntoView({ block: "nearest" });
  };
  return h(
    "div",
    { class: "lp" },
    h(
      "div",
      { class: "lp-head" },
      h("h3", { class: "lp-title", id: "lp-title" }, all.length ? "Who do you want to talk to?" : "Nobody is here yet."),
      closable && h("button", { class: "icon-btn", title: "Close", "aria-label": "Close", "data-key": "lp-close", onclick: close }, icon("x")),
    ),
    all.length > 0 &&
      h(
        "label",
        { class: "g-search" },
        icon("search"),
        h("input", {
          type: "text",
          value: p.q || null,
          placeholder: "Search people and sessions",
          autocomplete: "off",
          spellcheck: "false",
          role: "combobox",
          "aria-expanded": "true",
          "aria-controls": "lp-list",
          "aria-autocomplete": "list",
          "aria-activedescendant": rows.length ? "lp-o-" + p.index : null,
          "aria-labelledby": "lp-title",
          "data-key": "lp-q",
          oninput: (e) => {
            p.q = e.target.value;
            p.index = 0;
            render();
          },
          onkeydown: (e) => {
            const step = { ArrowDown: 1, ArrowUp: -1 }[e.key];
            if (step) {
              e.preventDefault();
              move(p.index + step);
            } else if (e.key === "Enter" && rows[p.index]) {
              e.preventDefault();
              startChat(rows[p.index].name);
            } else if (e.key === "Escape" && closable) {
              e.preventDefault();
              close();
            }
          },
        }),
      ),
    all.length > 0 &&
      h(
        "div",
        { class: "pick-list lp-list", role: "listbox", id: "lp-list", "aria-labelledby": "lp-title" },
        rows.length ? rows.map((r, i) => personRow(r, i === p.index, () => startChat(r.name), "lp-o-" + i)) : h("p", { class: "pick-none" }, "Nobody matches."),
      ),
  );
}

// The composer. Each conversation keeps one textarea element across renders, so a draft, its caret
// and its undo history outlast the rebuilds that polling causes.
const composers = new Map();
let composing = false; // an input method is partway through a character

function composerFor(key, send, onchange) {
  let el = composers.get(key);
  if (!el) {
    el = document.createElement("textarea");
    el.rows = 1;
    el.className = "composer-input";
    el.dataset.key = "composer-" + key;
    el.dataset.composer = key;
    el.addEventListener("compositionstart", () => (composing = true));
    el.addEventListener("compositionend", () => (composing = false));
    composers.set(key, el);
  }
  // Set anew each time: who it sends to can change while the element lasts.
  el.oninput = () => {
    fit(el);
    onchange();
  };
  el.onkeydown = (e) => {
    // On a phone Enter is a new line, and the button sends.
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing && !NARROW.matches) {
      e.preventDefault();
      send();
    }
  };
  return el;
}

function fit(el) {
  el.style.height = "auto";
  el.style.height = Math.min(el.scrollHeight + 2, Math.round(window.innerHeight * 0.4)) + "px";
}

function fitComposers() {
  for (const el of root.querySelectorAll(".composer-input")) fit(el);
}

function focusComposer() {
  if (NARROW.matches) return; // a phone's keyboard shouldn't jump up on opening a conversation
  root.querySelector(".composer-input")?.focus({ preventScroll: true });
}

function pendingIn(c) {
  return ui.outbox.filter((item) => item.key === c.key);
}

// Who a message written in this conversation goes to; null if it isn't yours to write in.
function targetOf(c) {
  if (c.kind === "all") return { to: "*", label: "everyone", count: sessionCount() };
  if (c.kind === "device") {
    const d = (ui.state?.devices || []).find((x) => x.id === c.title);
    return { to: c.title, label: groupName(c).replace(/^Everyone/, "everyone"), count: d ? d.sessions.length : 0 };
  }
  if (!c.pair.includes(selfName())) return null;
  const other = c.pair.find((n) => n !== selfName()) || selfName();
  return { to: other, label: other, count: 1 };
}

// Under a conversation: somewhere to write, or, under one between two others, a way to write to
// either of them.
function paneFoot(c) {
  const target = targetOf(c);
  if (target) return composer(c, target);
  const running = c.pair.filter((n) => n !== myName() && (findSession(n) || findPerson(n)));
  return running.length ? relayBar(c, running) : null;
}

// A send button says which composer it sends, so a name chosen on first sending can press it again.
function sendButton(key, el, onclick) {
  return h("button", { class: "composer-send", title: "Send", "aria-label": "Send", disabled: !el.value.trim(), "data-key": "send-" + key, "data-for": key, onclick }, icon("arrowUp"));
}

function composerChange(key, el) {
  if (ui.armed === key) {
    ui.armed = null;
    root.querySelector(".composer-warn")?.remove();
  }
  const btn = root.querySelector(`button[data-for="${CSS.escape(key)}"]`);
  if (btn) btn.disabled = !el.value.trim();
}

function composer(c, target, onSent) {
  const key = c.key;
  const big = target.to === "*" || (c.kind === "device" && target.count > 1);
  const trySend = () => {
    const item = sendFrom(c, target, el, big);
    if (item && onSent) onSent(item);
  };
  const el = composerFor(key, trySend, () => composerChange(key, el));
  el.placeholder = "Write to " + target.label + "…";
  el.setAttribute("aria-label", "Write to " + target.label);
  return h(
    "div",
    { class: "composer-wrap" },
    ui.setup?.inline === el && nameAsk(),
    ui.armed === key && h("p", { class: "composer-warn", role: "alert" }, "This reaches " + plural(target.count, "session") + ". Send again to go ahead."),
    h("div", { class: "composer" }, el, sendButton(key, el, trySend)),
  );
}

// Sending with no name yet asks for one in the composer; once it is saved, the send button that
// asked is pressed again, in a page that now has the name.
function needName(el) {
  if (me() && !me().taken) return false;
  askForName(() => {
    render();
    root.querySelector(`button[data-for="${CSS.escape(el.dataset.composer)}"]`)?.click();
  }, el);
  return true;
}

// A conversation between two sessions is theirs: a person can't write in it. The composer under
// it writes to one of them, privately, in the person's own conversation with it, and says so on a
// line inside its top edge, then where the message went. Who it writes to is worked out when the
// thread is opened, the last of the two to speak, and holds through new messages until the person
// switches or opens another thread.
function relayBar(c, running) {
  const key = "relay:" + c.key;
  if (ui.relayFor !== c.key || !running.includes(ui.relayTo.get(c.key))) {
    const last = [...c.messages].reverse().find((m) => running.includes(fromName(m)));
    ui.relayTo.set(c.key, last ? fromName(last) : running[0]);
    ui.relayFor = c.key;
  }
  const to = ui.relayTo.get(c.key);
  const other = running.find((n) => n !== to);
  const send = () => relaySend(c, to, el);
  const el = composerFor(key, send, () => {
    composerChange(key, el);
    // Writing the next message: the line goes back to saying where this one will go.
    if (el.value.trim() && root.querySelector(".relay-sent")) {
      ui.relayed.delete(c.key);
      render();
    }
  });
  el.placeholder = "Write to " + to + "…";
  el.setAttribute("aria-label", "Write to " + to + ", privately, in your conversation with it");
  const switchTo = () => {
    ui.relayTo.set(c.key, other);
    ui.relayed.delete(c.key);
    render();
    el.focus({ preventScroll: true });
  };
  return h(
    "div",
    { class: "composer-wrap" },
    ui.setup?.inline === el && nameAsk(),
    h("div", { class: "composer composer-relay" }, relayLine(c, to, other, el, switchTo), el, sendButton(key, el, send)),
  );
}

function relaySend(c, to, el) {
  const text = el.value.trim();
  if (!text || needName(el)) return;
  const key = dmKey(myName(), to);
  const pair = [...new Set([myName(), to])].sort();
  const item = { cid: Date.now() + "-" + Math.random().toString(36).slice(2, 8), key, conversation: { key, kind: "dm", title: pair.join(" & "), pair }, to, text, status: "sending" };
  ui.outbox.push(item);
  ui.notes.delete(key);
  ui.relayed.set(c.key, { cid: item.cid, to, key });
  el.value = "";
  deliver(item);
  el.focus({ preventScroll: true });
}

// Inside the composer's top edge: who a message written here goes to, before it is sent, and where
// it went after.
function relayLine(c, to, other, el, switchTo) {
  const sent = ui.relayed.get(c.key);
  const item = sent && ui.outbox.find((x) => x.cid === sent.cid);
  const pending = item && item.status !== "sent"; // sending, or failed and waiting to be tried again
  if (!sent || (!pending && (sent.to !== to || el.value.trim()))) {
    return h(
      "p",
      { class: "relay-line" },
      "To " + to + ", privately",
      other && [h("span", { class: "relay-sep", "aria-hidden": "true" }, "·"), h("button", { class: "relay-open", "data-key": "relay-switch", onclick: switchTo }, "switch to " + other)],
    );
  }
  if (item?.status === "failed") {
    return h(
      "p",
      { class: "relay-line relay-failed", role: "alert" },
      "Not sent to " + sent.to + ": " + item.error,
      h("button", { class: "msg-action", "data-key": "relay-retry", onclick: () => deliver(item) }, icon("retry"), "Try again"),
    );
  }
  if (item?.status === "sending") return h("p", { class: "relay-line", role: "status" }, "Sending to " + sent.to + "…");
  return h(
    "p",
    { class: "relay-line relay-sent", role: "status" },
    icon("check"),
    "Sent to your conversation with " + sent.to,
    h("span", { class: "relay-sep", "aria-hidden": "true" }, "·"),
    h("button", { class: "relay-open", "data-key": "relay-open", onclick: () => startChat(sent.to) }, "Open"),
  );
}

function sendFrom(c, target, el, big) {
  const text = el.value.trim();
  if (!text || needName(el)) return;
  if (big && ui.armed !== c.key) {
    ui.armed = c.key;
    render();
    return;
  }
  ui.armed = null;
  const { messages, ...conversation } = c; // not the messages: this can outlive them
  const item = { cid: Date.now() + "-" + Math.random().toString(36).slice(2, 8), key: c.key, conversation, to: target.to, text, status: "sending" };
  ui.outbox.push(item);
  ui.notes.delete(c.key);
  el.value = "";
  toEnd = true;
  deliver(item);
  if (!NARROW.matches) el.focus({ preventScroll: true });
  return item;
}

async function deliver(item) {
  item.status = "sending";
  item.error = "";
  render();
  try {
    const sent = await api("/api/send", { method: "POST", body: JSON.stringify({ to: item.to, text: item.text }) });
    item.id = sent.id;
    if (sent.note) ui.notes.set(item.key, { id: sent.id, text: sent.note });
    else ui.notes.delete(item.key);
    item.status = "sent";
    clearTimeout(timer);
    timer = setTimeout(poll, 100); // what was sent is among the messages by now
  } catch (e) {
    if (e instanceof AuthError) return deny(e.token);
    item.status = "failed";
    item.error = e.message.charAt(0).toUpperCase() + e.message.slice(1); // the daemon writes for agents, in lower case
  }
  render();
}

function editFailed(item) {
  ui.outbox = ui.outbox.filter((x) => x !== item);
  // It may have been all that listed its conversation, which the text is going back into.
  ui.restored = item.conversation;
  const el = composers.get(item.key);
  if (el) el.value = el.value ? item.text + "\n" + el.value : item.text;
  render();
  focusComposer();
}

// Above the composer: what has become of your last message, as far as can be known. Nothing here
// claims the agent has read it, which the daemon can't tell (#26). It lasts until a reply, or an hour.
function waitLine(c) {
  const target = targetOf(c);
  if (!target || c.kind !== "dm" || target.to === myName()) return null;
  const f = findSession(target.to);
  const last = c.messages[c.messages.length - 1];
  const pending = pendingIn(c).some((x) => x.status !== "failed");
  const mineLast = !!last && fromName(last) === myName();
  const waiting = pending || (mineLast && Date.now() - (when(last.at) || 0) < 3600000);
  const note = ui.notes.get(c.key);
  if (note && mineLast && last.id === note.id) return h("p", { class: "wait-line" }, note.text);
  if (isPerson(target.to)) {
    const p = findPerson(target.to);
    return !waiting || (p && personOnline(p)) ? null : h("p", { class: "wait-line" }, target.to + " will see this next time they open " + NAME + ".");
  }
  if (!f) return h("p", { class: "wait-line wait-warn" }, target.to + " isn't running, so it may never read this.");
  const st = sessionState(f.device, f.session);
  if (st === "off") return h("p", { class: "wait-line wait-warn" }, target.to + "'s device is offline. It gets this when it's back.");
  if (st === "dnd" && !onDnd(f.session)) return h("p", { class: "wait-line wait-warn" }, "Wake-ups are off for " + target.to + ", so your message won't wake it. It waits in its inbox.");
  if (st === "dnd") return h("p", { class: "wait-line wait-warn" }, target.to + " is on do not disturb. Your message waits in its inbox.");
  // Before sending to an idle session: what sending does. A person's message wakes a session unless
  // its device's wake-ups say Never, which only this device's settings can tell.
  if (!waiting && st === "idle") {
    const idle = target.to + " is " + stateWord(f.device, f.session).toLowerCase();
    return h("p", { class: "wait-line" }, f.device.self ? idle + "; your message wakes it" : idle + "; your message wakes it unless wake-ups are off on " + deviceLabel(f.device));
  }
  if (!waiting) return null;
  const active = when(f.session.lastActive);
  const sentAt = mineLast ? when(last.at) || 0 : Date.now();
  if (active && Date.now() - active < 90000 && active >= sentAt - 5000) {
    return h("p", { class: "wait-line wait-working" }, h("span", { class: "dots", "aria-hidden": "true" }, h("i"), h("i"), h("i")), target.to + " is working");
  }
  return pending ? null : h("p", { class: "wait-line" }, "Waiting for " + target.to);
}

// Settings.

const WAKE_CHOICES = [
  ["always", "Always"],
  ["within", "Within"],
  ["never", "Never"],
];

// Minutes being typed, by control, until the field is left: a render meanwhile (another row's save
// coming back) keeps them.
const minuteDrafts = new Map();

// What a wake choice comes to, in words: "Within 3 min".
function wakeWords(w) {
  const def = ui.settings?.defaultMinutes || 3;
  if (!w || w.when === "within") return "Within " + (w?.minutes || def) + " min";
  return w.when === "always" ? "Always" : "Never";
}

// A segmented control, one stop for Tab with the arrow keys moving along it, as in All sessions.
// A setting that follows the device's (resolved: what that comes to) fills no segment: Default has
// only an outline, and what it comes to follows the control in plain words. Only a setting of the
// session's own is filled.
function segmented(key, choices, current, pick, resolved) {
  const inherited = current === "default" && !!resolved;
  const seg = h(
    "div",
    { class: "segmented" + (inherited ? " inherited" : ""), role: "group", onkeydown: segKeys },
    choices.map(([v, label]) =>
      h(
        "button",
        {
          "aria-pressed": current === v ? "true" : "false",
          tabindex: current === v ? "0" : "-1",
          "data-key": key + "-" + v,
          onclick: () => {
            if (v !== current) pick(v);
          },
        },
        label,
      ),
    ),
  );
  return inherited ? [seg, h("span", { class: "seg-from" }, resolved + ", from the device default")] : seg;
}

function wakeControl(key, wake, onChange, withDefault) {
  const def = ui.settings.defaultMinutes || 3;
  const choices = withDefault ? [["default", "Default"], ...WAKE_CHOICES] : WAKE_CHOICES;
  const current = wake ? wake.when : "default";
  const seg = segmented(
    key,
    choices,
    current,
    (v) => {
      minuteDrafts.delete(key);
      delete ui.rowErrors[key];
      onChange(v === "default" ? null : { when: v, minutes: v === "within" ? wake?.minutes || 0 : 0 });
    },
    withDefault && wakeWords(ui.settings.wake),
  );
  if (current !== "within") return [seg];
  // Checked and saved when the field is left (or on Enter), not while it is being typed in. Empty
  // is the device's default.
  const commit = (e) => {
    if (!e.target.isConnected) return; // taken out by a render, not left
    const raw = e.target.value.trim();
    const n = raw === "" ? 0 : Number(raw);
    if (!Number.isInteger(n) || n < 0 || n > 1440 || (raw !== "" && n === 0)) {
      ui.rowErrors[key] = "Whole number from 1 to 1440, or empty for " + def + ".";
      render(); // the draft stays in the field, to be put right
      return;
    }
    minuteDrafts.delete(key);
    const had = ui.rowErrors[key];
    delete ui.rowErrors[key];
    if (n !== (wake.minutes || 0)) onChange({ when: "within", minutes: n });
    else if (had) render();
  };
  const input = h("input", {
    type: "text",
    inputmode: "numeric",
    maxlength: 4,
    placeholder: "default " + def,
    "aria-label": "Minutes idle, empty for the default of " + def,
    "aria-invalid": ui.rowErrors[key] ? "true" : null,
    "data-key": key + "-minutes",
    value: minuteDrafts.get(key) ?? (wake.minutes ? String(wake.minutes) : null),
    oninput: (e) => minuteDrafts.set(key, e.target.value),
    onblur: commit,
    onkeydown: (e) => {
      if (e.key === "Enter") {
        e.preventDefault();
        commit(e);
      }
    },
  });
  return [seg, h("label", { class: "min-wrap" }, h("span", { class: "field min-field" + (ui.rowErrors[key] ? " field-bad" : "") }, input), h("span", { class: "field-unit", "aria-hidden": "true" }, "min"))];
}

const BUSY_CHOICES = [
  ["subject", "Subject"],
  ["whole", "Whole message"],
];

function busyControl(key, busy, onChange, withDefault) {
  const choices = withDefault ? [["default", "Default"], ...BUSY_CHOICES] : BUSY_CHOICES;
  const current = busy || "default";
  const resolved = withDefault && Object.fromEntries(BUSY_CHOICES)[ui.settings.busy || "subject"];
  return segmented(key, choices, current, (v) => onChange(v === "default" ? null : v), resolved);
}

// Settings go to the daemon one request at a time, in order: each PUT is the whole settings, so
// one that overtook an older one would be undone by it. Loads queue behind saves too. The page
// shows the newest choice while it is on its way, and only the answer to the newest request is
// shown; if that fails, the page goes back to what the daemon last confirmed.
let settingsQueue = Promise.resolve();
let settingsSeq = 0; // the newest save asked for
let confirmed = null; // the settings as the daemon last gave them

function queueSettings(job) {
  const token = ui.token;
  settingsQueue = settingsQueue
    .then(() => (token === ui.token ? job() : null)) // a new token has started over
    .catch((e) => console.error(e));
}

// Each save says how it went beside the control that made it (key): "Saving…" once it has taken
// 300ms, "Saved" for a moment once the daemon has it, or why it failed, under that row.
const saveNotes = new Map(); // a row's key → { seq, state: "pending" | "saving" | "saved", at }

function saveSettings(next, key) {
  const seq = ++settingsSeq;
  ui.settings = next;
  if (key) {
    delete ui.rowErrors[key];
    saveNotes.set(key, { seq, state: "pending" });
    setTimeout(() => {
      const n = saveNotes.get(key);
      if (n?.seq === seq && n.state === "pending") {
        n.state = "saving";
        render();
      }
    }, 300);
  }
  render();
  queueSettings(async () => {
    const note = key && saveNotes.get(key);
    try {
      const saved = await api("/api/settings", { method: "PUT", body: JSON.stringify(next) });
      confirmed = saved;
      if (seq === settingsSeq) {
        ui.settings = saved;
        ui.settingsError = "";
      }
      if (note?.seq === seq) {
        note.state = "saved";
        note.at = Date.now();
        setTimeout(() => {
          if (saveNotes.get(key) === note) saveNotes.delete(key);
          render();
        }, SAVED_MS);
      }
    } catch (e) {
      if (e instanceof AuthError) return deny(e.token);
      if (seq === settingsSeq) ui.settings = confirmed || ui.settings;
      if (note?.seq === seq) saveNotes.delete(key);
      if (key) ui.rowErrors[key] = "Not saved: " + e.message;
      else if (seq === settingsSeq) ui.settingsError = e.message;
    }
    render();
  });
}

const SAVED_MS = 1500;

function saveNote(key) {
  const n = saveNotes.get(key);
  if (!n || n.state === "pending") return h("span", { class: "save-note", "aria-live": "polite" });
  if (n.state === "saving") return h("span", { class: "save-note", "aria-live": "polite" }, "Saving…");
  // A render partway through keeps the fade where it had got to.
  const el = h("span", { class: "save-note saved", "aria-live": "polite" }, icon("check"), "Saved");
  el.style.setProperty("animation-delay", -(Date.now() - n.at) + "ms");
  return el;
}

// A row: its title and what it means, its control, and under the control what went wrong with it.
// A row that can be saved (saves) keeps a place beside its control for how the save went.
function settingsRow(key, title, description, control, extraClass = "", saves = false) {
  return h(
    "div",
    { class: "row" },
    h(
      "div",
      { class: "row-grid " + extraClass + (description ? " has-desc" : "") },
      h("div", { class: "row-text" }, h("div", { class: "row-title" }, title), description && h("p", { class: "row-description" }, description)),
      h(
        "div",
        { class: "row-side" },
        h("div", { class: "row-control" }, saves && saveNote(key), control),
        ui.rowErrors[key] && h("p", { class: "row-error", role: "alert" }, ui.rowErrors[key]),
      ),
    ),
  );
}

// One wording, wherever a name someone else chose first is asked about again.
function takenLine(a) {
  return "The person on " + host(a.taken) + " chose \u201c" + a.name + "\u201d first. Choose another name; until you do, you can't send.";
}

function accountSection() {
  const a = me();
  const suggested = ui.state?.account?.suggested || "";
  // A saved name is the field's value. Without one, or with one taken, the field starts empty, and
  // the suggestion is its placeholder and a line under it, until it is used.
  const value = ui.accountDraft ?? (a && !a.taken ? a.name : "");
  const unchanged = (v) => !v.trim() || (!!a && !a.taken && v.trim() === a.name);
  const offer = (!a || a.taken) && suggested && value.trim() !== suggested;
  const use = () => {
    ui.accountDraft = suggested;
    render();
    const f = root.querySelector('[data-key="account-name"]');
    f?.focus();
    f?.select();
  };
  return h(
    "section",
    { class: "section" },
    h("div", { class: "section-head" }, h("h2", { class: "section-title" }, "Your account")),
    h(
      "div",
      { class: "group" },
      settingsRow(
        "account",
        "Your name",
        a?.taken ? h("span", { class: "taken-line" }, takenLine(a)) : "Agents see it, and send to it.",
        // The field and its button on a line, and the suggestion under them.
        h(
          "div",
          { class: "account-ctl" },
          h(
            "span",
            { class: "account-line" },
            h(
              "span",
              { class: "field account-field" },
              h("input", {
                type: "text",
                value,
                maxlength: 32,
                autocomplete: "off",
                spellcheck: "false",
                "aria-label": "Your name",
                "data-key": "account-name",
                placeholder: suggested || "ada-lovelace",
                oninput: (e) => {
                  ui.accountDraft = e.target.value;
                  const btn = root.querySelector('[data-key="account-save"]');
                  if (btn) btn.disabled = unchanged(e.target.value);
                },
                onkeydown: (e) => {
                  if (e.key === "Enter" && !unchanged(e.target.value)) {
                    e.preventDefault();
                    saveAccount(e.target.value.trim());
                  }
                },
              }),
            ),
            h(
              "button",
              { class: "btn btn-outline btn-xs", "data-key": "account-save", disabled: unchanged(value) || ui.accountSaving, onclick: (e) => saveAccount(e.currentTarget.parentElement.querySelector("input").value.trim()) },
              ui.accountSaving ? "Saving…" : !a ? "Choose" : a.taken ? "Save" : "Rename",
            ),
          ),
          offer && h("p", { class: "account-suggest" }, "Suggested: ", h("b", null, suggested), h("button", { class: "btn btn-ghost btn-xs", "data-key": "account-use", onclick: use }, "Use it")),
        ),
      ),
    ),
  );
}

// A direct conversation's key has the person's name in it, so a new name moves what is unsent to the
// conversation's new key. Left under the old one, it would be out of reach, and would hold up a reload.
function moveToName(was, name) {
  if (!was || was === name) return;
  const rekey = (key) => {
    if (!key.startsWith("dm:")) return key;
    const pair = JSON.parse(key.slice(3));
    return pair.includes(was) ? dmKey(name, pair.find((n) => n !== was) || name) : key;
  };
  for (const [key, el] of [...composers]) {
    const moved = rekey(key);
    if (moved === key) continue;
    composers.delete(key);
    composers.set(moved, el);
    el.dataset.key = "composer-" + moved;
    el.dataset.composer = moved;
  }
  for (const item of ui.outbox) {
    const moved = rekey(item.key);
    if (moved === item.key) continue;
    const pair = JSON.parse(moved.slice(3));
    item.key = moved;
    item.conversation = { ...item.conversation, key: moved, pair, title: pair.join(" & ") };
  }
  // A conversation begun before there was a name is the same one now.
  if (was === YOU && ui.conversation) ui.conversation = rekey(ui.conversation);
  ui.restored = null;
}

async function saveAccount(name) {
  delete ui.rowErrors.account;
  ui.accountSaving = true;
  render();
  try {
    const account = await api("/api/account", { method: "PUT", body: JSON.stringify({ name }) });
    const was = myName();
    if (ui.state) ui.state.account = { ...ui.state.account, ...account };
    moveToName(was, account.name);
    ui.accountDraft = null;
    poll();
  } catch (e) {
    if (e instanceof AuthError) return deny(e.token);
    ui.rowErrors.account = e.message;
  }
  ui.accountSaving = false;
  render();
}


// Under a session's name in Settings: what runs it, how it is, and its branch where there's room.
function sessionLine(d, s) {
  const text = [agentName(s.agent), d && stateWord(d, s), s.branch].filter(Boolean).join(" · ");
  return h("span", { class: "set-sess-line", title: text }, text);
}

function settingsPage() {
  const sections = [ui.state ? accountSection() : null];
  const self = devices().find((d) => d.self);
  // Until the settings have come, and if they couldn't, their sections say so rather than vanish.
  if (!ui.settings) {
    const waiting = (title) =>
      h(
        "section",
        { class: "section" },
        h("div", { class: "section-head" }, h("h2", { class: "section-title" }, title)),
        h(
          "div",
          { class: "group" },
          ui.settingsError
            ? h(
                "div",
                { class: "row-empty set-load" },
                h("span", { role: "alert" }, "Couldn't load these settings."),
                h(
                  "button",
                  {
                    class: "btn btn-outline btn-xs",
                    "data-key": "settings-retry-" + title,
                    onclick: () => {
                      ui.settingsError = "";
                      render();
                      loadSettings();
                    },
                  },
                  "Retry",
                ),
              )
            : h("div", { class: "row-empty", role: "status" }, "Loading…"),
        ),
      );
    sections.push(waiting("Wake-ups"), waiting("Busy sessions"));
  }
  if (ui.settings) {
    const set = ui.settings;
    const local = self?.sessions || [];
    const overrides = set.sessionWake || {};
    // The sessions' own rows sit under the device's, indented, after a "Per session" label.
    const perSession = (list) => list.length > 0 && [h("div", { class: "set-per" }, "Per session"), list.map((r) => (r.classList.add("set-sess"), r))];
    const rows = [
      settingsRow(
        "wake",
        h("span", { class: "set-senior" }, "Device default"),
        "Whether a message from another session may wake an idle session. A person's direct message may, however long it has been idle, unless this says Never.",
        wakeControl("wake", set.wake, (w) => saveSettings({ ...ui.settings, wake: w || { when: "within" } }, "wake"), false),
        "",
        true,
      ),
      perSession(
        [...local]
          .sort((a, b) => a.name.localeCompare(b.name))
          .map((s) =>
            settingsRow(
              "wake-" + s.name,
              s.name,
              sessionLine(self, s),
              wakeControl(
                "wake-" + s.name,
                overrides[s.name] || null,
                (w) => {
                  // From the settings as they are now: minutes are saved on leaving the field, which
                  // can come after another row's choice.
                  const next = { ...(ui.settings.sessionWake || {}) };
                  if (w) next[s.name] = w;
                  else delete next[s.name];
                  saveSettings({ ...ui.settings, sessionWake: next }, "wake-" + s.name);
                },
                true,
              ),
              "",
              true,
            ),
          ),
      ),
    ];
    const busyOverrides = set.sessionBusy || {};
    const busyRows = [
      settingsRow(
        "busy",
        h("span", { class: "set-senior" }, "Device default"),
        "What a busy Claude Code session sees when a message arrives.",
        busyControl("busy", set.busy || "subject", (b) => saveSettings({ ...ui.settings, busy: b }, "busy"), false),
        "",
        true,
      ),
      // Only Claude Code sessions: the busy notice comes from Murmur's hook, which only Claude Code
      // runs (internal/daemon/serve.go).
      perSession(
        [...local]
          .filter((s) => s.agent === "claude-code")
          .sort((a, b) => a.name.localeCompare(b.name))
          .map((s) =>
            settingsRow(
              "busy-" + s.name,
              s.name,
              sessionLine(self, s),
              busyControl(
                "busy-" + s.name,
                busyOverrides[s.name] || null,
                (b) => {
                  const next = { ...(ui.settings.sessionBusy || {}) };
                  if (b) next[s.name] = b;
                  else delete next[s.name];
                  saveSettings({ ...ui.settings, sessionBusy: next }, "busy-" + s.name);
                },
                true,
              ),
              "",
              true,
            ),
          ),
      ),
    ];
    sections.push(
      h(
        "section",
        { class: "section" },
        h("div", { class: "section-head" }, h("h2", { class: "section-title" }, "Wake-ups")),
        h("div", { class: "group" }, rows),
      ),
      h(
        "section",
        { class: "section" },
        h("div", { class: "section-head" }, h("h2", { class: "section-title" }, "Busy sessions")),
        // Why only some sessions are here: the notice comes from Murmur's hook (serve.go), which
        // only Claude Code runs; the rest are handed a message as a turn, or read it from the inbox.
        h("p", { class: "set-note set-section-note" }, "Claude Code sessions only, where Murmur's hook runs; other sessions get a message as their next turn or read it from their inbox."),
        h("div", { class: "group" }, busyRows),
        ui.settingsError && h("p", { class: "row-error section-head", role: "alert" }, ui.settingsError),
      ),
    );
  }

  const mutes = ui.state?.mutes || [];
  sections.push(
    h(
      "section",
      { class: "section" },
      h("div", { class: "section-head" }, h("h2", { class: "section-title" }, "Mutes")),
      h(
        "div",
        { class: "group" },
        mutes.length === 0
          ? h("div", { class: "row-empty" }, "None")
          : mutes.map((m) => {
              const key = "mute-" + m.by + "-" + m.muted;
              const at = when(m.at);
              return settingsRow(
                key,
                h(
                  "span",
                  { class: "mute-line" },
                  icon("bellOff"),
                  h("span", { class: "truncate" }, m.by),
                  h("span", { class: "verb" }, "muted"),
                  h("span", { class: "truncate" }, m.muted),
                ),
                at && agoEl("span", "", at),
                !m.canUnmute && h("span", { class: "set-note" }, "Set on " + (findSession(m.by) ? deviceLabel(findSession(m.by).device) : "another device") + "; only it can unmute"),
                m.canUnmute &&
                  h(
                    "button",
                    {
                      class: "btn btn-outline btn-xs",
                      "data-key": key,
                      disabled: ui.muting.has(key),
                      onclick: () => unmute(m, key),
                    },
                    "Unmute",
                  ),
                "keep-inline",
              );
            }),
      ),
    ),
  );

  sections.push(versionSection());
  return h("div", { class: "framed" }, pageBar({ title: "Settings", meta: self && "For " + (self.host || host(self.id)) + ", this device" }), h("div", { class: "page set-page" }, sections));
}

async function unmute(m, key) {
  ui.muting.add(key);
  delete ui.rowErrors[key];
  render();
  try {
    await api("/api/unmute", { method: "POST", body: JSON.stringify({ by: m.by, muted: m.muted }) });
    ui.state.mutes = ui.state.mutes.filter((x) => !(x.by === m.by && x.muted === m.muted));
  } catch (e) {
    if (e instanceof AuthError) return deny(e.token);
    ui.rowErrors[key] = e.message;
  }
  ui.muting.delete(key);
  render();
}

// Navigation and polling.

function go(view) {
  if (location.hash !== "#" + view) location.hash = view;
  else setView(view);
}

// A view is its name, and for All sessions the open session too: sessions/<name>.
function setView(view) {
  const [v, open] = view.split("/");
  const next = VIEWS.includes(v) ? v : "messages";
  // Leaving All sessions (to a conversation, say) remembers where the table and the details were,
  // so Back to the same session finds them as they were.
  if (ui.view === "sessions" && next !== "sessions") ui.fleetMemo = { sel: ui.fleetSel || null, page: content?.scrollTop || 0, pane: root.querySelector(".fx-pane-in")?.scrollTop || 0 };
  if (ui.view === "recent" && next !== "recent") ui.recentMark = undefined;
  if (next !== "messages") ui.backTo = false;
  if (next === "recent" && ui.view !== "recent") ui.recentHeldAt = ui.recentShownAt = null;
  ui.view = next;
  if (ui.view === "settings") loadSettings();
  if (ui.view !== "sessions") return render();
  const want = fleetFromHash(open);
  const memo = ui.fleetMemo;
  ui.fleetMemo = null;
  fleetSetSel(want);
  if (memo && memo.sel === want && content) {
    content.scrollTop = memo.page;
    const pane = root.querySelector(".fx-pane-in");
    if (pane) pane.scrollTop = memo.pane;
  }
  fleetFocusBack();
}

function loadSettings() {
  const seq = settingsSeq;
  queueSettings(async () => {
    try {
      const got = await api("/api/settings");
      confirmed = got;
      if (seq === settingsSeq) {
        ui.settings = got; // unless a save was asked for since, which the page shows instead
        ui.settingsError = "";
      }
    } catch (e) {
      if (e instanceof AuthError) return deny(e.token);
      if (seq === settingsSeq) ui.settingsError = e.message;
    }
    render();
  });
}

// deny gives up on a token the server refused, unless a newer one has replaced it meanwhile.
function deny(token) {
  if (token !== ui.token) return;
  ui.conn = "denied";
  sessionStorage.removeItem(TOKEN_KEY);
  render();
}

let arrivedAt = 0; // the sort time of the message before

let arrivals = []; // messages fetched since the last poll, to be announced

async function fetchMessages() {
  for (let pages = 0; pages < 20; pages++) {
    const r = await api("/api/messages?cursor=" + encodeURIComponent(ui.cursor));
    let added = 0;
    for (const m of r.messages || []) {
      learn(m.fromAddr, m.from);
      if (m.toKind === "session") learn(m.toAddr, m.to);
      if (m.fromPerson) personNames.add(m.from);
      if (m.toPerson) personNames.add(m.to);
      if (ui.ids.has(m.id)) continue;
      ui.ids.add(m.id);
      // A clock can be wrong. A message with no time goes where it arrived, and one from the future
      // counts as arriving now, so it can't hold its conversation at the top of the list or stay
      // below the replies to it.
      arrivedAt = m.sortAt = Math.min(when(m.at) || arrivedAt, Date.now());
      ui.messages.push(m);
      arrivals.push(m);
      added++;
    }
    const moved = r.cursor !== ui.cursor;
    ui.cursor = r.cursor || ui.cursor;
    if (!added || !moved) return;
  }
}

// Drops the oldest of what is over each limit, so agents talking among themselves can't push out a
// person's conversations or the unread messages in them. It needs the account, so it runs once the
// state has come.
function trimMessages() {
  const addr = me()?.addr;
  const own = (m) => !!addr && (m.fromAddr === addr || m.toAddr === addr);
  const ownCount = ui.messages.filter(own).length;
  let overOwn = ownCount - MAX_OWN_MESSAGES;
  let overRest = ui.messages.length - ownCount - MAX_MESSAGES;
  if (overOwn <= 0 && overRest <= 0) return;
  ui.messages = ui.messages.filter((m) => {
    const mine = own(m);
    if ((mine ? overOwn : overRest) <= 0) return true;
    if (mine) overOwn--;
    else overRest--;
    ui.ids.delete(m.id);
    return false;
  });
}

// What came, in words, where the reader is: in the open conversation by who sent it; in the list
// and on Recent as their pills count it while they hold it back, else by who sent it.
function announceArrivals(ms) {
  const news = ms.filter((m) => fromName(m) !== myName() && !(me() && m.fromAddr === me().addr));
  const from = (list) => {
    const names = new Set(list.map(fromName));
    if (names.size > 1) return plural(list.length, "new message");
    return (list.length === 1 ? "New message" : list.length + " new messages") + " from " + [...names][0];
  };
  if (!news.length) return;
  if (ui.view === "recent") return announce(ui.recentHeldAt != null ? plural(news.length, "new message") : from(news));
  if (ui.view !== "messages") return;
  const of = new Map();
  for (const c of conversations()) for (const m of c.messages) of.set(m.id, c.key);
  const here = news.filter((m) => of.get(m.id) === ui.conversation);
  const there = news.filter((m) => of.has(m.id) && of.get(m.id) !== ui.conversation);
  const said = [];
  if (here.length) said.push(from(here));
  if (there.length) {
    const n = new Set(there.map((m) => of.get(m.id))).size;
    said.push(ui.heldNew > 0 ? plural(ui.heldNew, "new conversation") : n === 1 ? from(there) : n + " conversations with new messages");
  }
  if (said.length) announce(said.join(". "));
}

let last = "";
let timer;
let inflight = false;
let polled = false;
let scrolledAt = 0;
let stateAskedAt = 0; // when the state in hand was asked for

function hasUnsent() {
  return ui.outbox.length > 0 || [...composers.values()].some((el) => el.value.trim());
}

async function poll() {
  if (inflight) return;
  inflight = true;
  clearTimeout(timer);
  try {
    const asked = Date.now();
    const [state] = await Promise.all([api("/api/state"), fetchMessages()]);
    // Another daemon is serving: it restarted onto an update, or a reinstall of the same version,
    // and serves this page at the same address and token. Its page may differ too, so load that.
    const was = ui.state?.version;
    const now = state.version;
    if (was && now && ((was.daemon && now.daemon && was.daemon !== now.daemon) || (was.running && now.running && was.running !== now.running))) {
      ui.reloadDue = true;
    }
    // It waits for what someone is writing, and for a message that failed: a reload would lose both.
    // This page goes on working with the new daemon until then.
    if (ui.reloadDue) {
      if (!hasUnsent()) {
        location.reload();
        return;
      }
      ui.restarting = 0;
    }
    for (const d of state.devices || []) {
      for (const s of d.sessions || []) learn(s.addr, s.name);
      if (d.person) {
        learn(d.person.addr, d.person.name);
        personNames.add(d.person.name);
      }
    }
    ui.state = state;
    stateAskedAt = asked;
    trimMessages();
    for (const id of ui.readLocal) if (!state.account?.unread?.includes(id)) ui.readLocal.delete(id);
    ui.outbox = ui.outbox.filter((item) => !(item.id && ui.ids.has(item.id)));
    ui.conn = "ok";
    ui.okAt = Date.now();
  } catch (e) {
    inflight = false;
    if (e instanceof AuthError) return deny(e.token);
    ui.conn = "down";
  }
  inflight = false;
  // Rebuild only when something changed, and leave alone a minutes field being typed in, text being
  // selected, and a pane being scrolled: a rebuild would drop the selection or stop the scroll.
  const sig = JSON.stringify([ui.state, ui.cursor, ui.conn, ui.restarting > 0, restartStuck(), ui.outbox.length, Math.floor(Date.now() / 15000)]);
  // A composer keeps its own element, so what is being typed there survives a rebuild, unless an
  // input method is partway through a character. On a phone it wouldn't: refocusing it isn't a tap,
  // so the keyboard would drop. There the thread catches up when the composer loses focus.
  const inComposer = document.activeElement?.tagName === "TEXTAREA";
  // All sessions' search box is exempt: a rebuild keeps its caret, and holding the table still
  // while it has focus would leave the rows stale.
  const typing = (document.activeElement?.tagName === "INPUT" && document.activeElement.dataset.key !== "fx-q") || composing || (inComposer && NARROW.matches);
  const selecting = document.getSelection()?.isCollapsed === false;
  const scrolling = Date.now() - scrolledAt < 1000 || ui.fxDragging;
  if (sig !== last && !typing && !selecting && !scrolling) {
    last = sig;
    render();
    if (ui.fleetFocusPending) fleetFocusBack();
  }
  if (polled && arrivals.length) announceArrivals(arrivals);
  arrivals = [];
  // The first state starts the PR lookups; ask again soon, so their answers show without a wait.
  const first = !polled;
  polled = true;
  timer = setTimeout(poll, document.hidden ? HIDDEN_POLL_MS : first ? 1200 : POLL_MS);
}

// A fresh link pasted into a tab already showing the page changes only the fragment, so the
// page takes the new token here and starts over with it.
function start() {
  const token = takeToken();
  if (token !== ui.token) {
    Object.assign(ui, { token, conn: "connecting", state: null, messages: [], cursor: "", settings: null, conversation: null, chatListTop: 0, draftTo: null, restored: null, outbox: [], setup: null, picker: null, newMark: null, armed: null });
    ui.ids = new Set();
    ui.notes = new Map();
    ui.readLocal = new Set();
    composers.clear();
    personNames.clear();
    named.clear();
    minuteDrafts.clear();
    confirmed = null;
    arrivedAt = 0;
    last = "";
    polled = false;
  }
  const [v, open] = location.hash.slice(1).split("/");
  ui.view = VIEWS.includes(v) ? v : "messages";
  if (ui.view === "sessions") ui.fleetSel = fleetFromHash(open);
  render();
  fleetFocusBack();
  if (!ui.token) return;
  loadSettings(); // the wake-up settings say which of this device's sessions show red
  poll();
}

window.addEventListener("hashchange", () => {
  if (location.hash.includes("token=")) start();
  else setView(location.hash.slice(1));
});
document.addEventListener("visibilitychange", () => {
  if (!document.hidden && ui.token && ui.conn !== "denied") poll();
});
window.addEventListener("focus", () => {
  if (ui.token && ui.conn !== "denied") markOpenRead();
});
window.addEventListener("blur", markOpenRead); // nobody is looking now, so what comes next is new
document.addEventListener("scroll", () => (scrolledAt = Date.now()), { capture: true, passive: true });
// Recent lets in what it held once the reader is back at the top.
document.addEventListener(
  "scroll",
  (e) => {
    if (ui.view === "recent" && ui.recentHeldAt != null && e.target === content && content.scrollTop <= 8) render();
  },
  { capture: true, passive: true },
);
NARROW.addEventListener("change", render);
setInterval(tick, 10000);
start();
