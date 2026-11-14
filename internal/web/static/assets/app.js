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

// The page's look, chosen in Settings and kept in this browser: black like T3 Code, soft greys like
// a chat app, light, or the website's blue, butter and cream. style.css keys each one on <html data-theme>.
const THEME_KEY = NAME + ":theme";
const THEMES = [
  ["dark", "Dark"],
  ["dim", "Dim"],
  ["light", "Light"],
  ["murmur", "Murmur"],
];
let theme = "dark";
try {
  const t = localStorage.getItem(THEME_KEY);
  if (THEMES.some(([id]) => id === t)) theme = t;
} catch {}
document.documentElement.dataset.theme = theme;

// An easter egg (#499), after the old Discord joke about light mode: going from a dark theme to
// Light flashes the screen white. It happens the first time in this browser and one time in twenty
// after that, so it stays a surprise. Someone who asked for reduced motion never gets it.
const FLASH_KEY = NAME + ":flashbanged";
const FLASH_ODDS = 0.05;
function flashbang() {
  if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;
  let seen = false;
  try {
    seen = localStorage.getItem(FLASH_KEY) !== null;
    localStorage.setItem(FLASH_KEY, "1");
  } catch {
    seen = true; // nowhere to remember the first time, so every time takes its chances
  }
  if (seen && Math.random() >= FLASH_ODDS) return;
  document.querySelector(".flashbang")?.remove();
  const flash = h("div", { class: "flashbang", "aria-hidden": "true" });
  flash.addEventListener("animationend", () => flash.remove());
  document.body.append(flash);
}

function setTheme(t) {
  if (t === "light" && (theme === "dark" || theme === "dim")) flashbang();
  theme = t;
  document.documentElement.dataset.theme = t;
  try {
    localStorage.setItem(THEME_KEY, t);
  } catch {}
  render();
}

const ui = {
  view: "messages",
  token: null,
  conn: "connecting", // connecting, ok, down, denied
  state: null,
  messages: [], // oldest first
  ids: new Set(),
  cursor: "",
  // What sessions did other than send messages (#448): the daemon's, oldest first, and GitHub's.
  acts: [],
  actIds: new Set(),
  actCursor: "",
  github: [],
  recentKind: "all", // Recent shows all of it, or only "messages" or "activity"
  recentWho: "", // or only one session's, by name
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
  setup: null, // the name being chosen: { name, error, busy, then }, then what to do once there is one
  picker: null, // the new-message list: { q, index, group }, group set while it is making a group chat
  draftTo: null, // who a conversation with no messages yet is with
  draftGroup: null, // the tag of a group chat just made here, open before anyone has written in it
  restored: null, // the conversation a failed message's text just went back into
  outbox: [], // what the person sent that isn't among the messages yet, or that failed
  notes: new Map(), // a conversation's key → what the daemon said of the last message sent in it
  readLocal: new Set(), // messages marked read that the state doesn't know of yet
  reading: null, // the conversation someone was last seen looking at, null if nobody is looking
  newMark: null, // the open conversation's first unread message: { key, id }
  armed: null, // the group chat whose next send goes: its key
  repoMenu: null, // the repo picker, open: { x, top, repos, opening, err } (#348)
  drawer: false, // on a phone, whether the sidebar's drawer is open
  openReceipts: new Set(), // the person's messages whose receipt lists are open, by ID (#26)
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
    const view = parts.find((p) => VIEWS.includes(p)) || "messages";
    history.replaceState(null, "", location.pathname + location.search + "#" + view);
  }
  return sessionStorage.getItem(TOKEN_KEY);
}

// A page opened from another repo's page (#348) arrives with a one-time key, not the token, and
// trades it with its own daemon for the token. The key is spent either way, so it leaves the
// address bar first. If the trade fails, the tab keeps whatever token it already had here.
async function takeKey() {
  const parts = location.hash.slice(1).split("&");
  const k = parts.find((p) => p.startsWith("key="));
  if (!k) return;
  const view = parts.find((p) => VIEWS.includes(p)) || "messages";
  history.replaceState(null, "", location.pathname + location.search + "#" + view);
  // The theme comes along: this browser keeps it per page, and each repo's page is a page of its own.
  const t = parts.find((p) => p.startsWith("theme="))?.slice(6);
  if (THEMES.some(([id]) => id === t) && t !== theme) {
    theme = t;
    document.documentElement.dataset.theme = t;
    try {
      localStorage.setItem(THEME_KEY, t);
    } catch {}
  }
  try {
    const res = await fetch("/api/key", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ key: k.slice(4) }), cache: "no-store" });
    const got = res.ok ? await res.json() : null;
    if (got?.token) sessionStorage.setItem(TOKEN_KEY, got.token);
  } catch {}
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
    else if (k.startsWith("on")) {
      el.addEventListener(k.slice(2), v);
      (el.listeners ||= []).push([k.slice(2), v]); // for graft, which moves them to the node kept
    } else el.setAttribute(k, v === true ? "" : String(v));
  }
  for (const kid of kids.flat(Infinity)) {
    if (kid == null || kid === false || kid === "") continue;
    // The focused node, if it outlasts renders (a composer), stays in the page; a mark takes its
    // place in the new tree, for graft.
    el.append(kid === held.el ? held.mark : kid instanceof Node ? kid : String(kid));
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

// An element whose text is a relative time, kept current by the ticker.
function agoEl(tag, cls, t, prefix = "") {
  return h(tag, { class: cls, "data-ago": t, "data-prefix": prefix, title: stamp(t) }, prefix + ago(t));
}

function tick() {
  for (const el of document.querySelectorAll("[data-ago]")) {
    el.textContent = el.dataset.prefix + ago(Number(el.dataset.ago));
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
let content; // the scrolling pane, kept across renders so its scroll position is too
const held = { el: null, mark: null }; // the focused element while a render builds around it

// One polite live region for the page, which renders never rebuild: a region made anew each render
// would be read out every time, or never. What it says comes in a batch, a moment after the change,
// the region is cleared first so the same words can be said again, and it empties a while after, so
// moving it says nothing. It lives at the end of <body>, or inside an open aria-modal dialog, since
// Safari with VoiceOver hides everything outside one.
const live = h("div", { class: "sr-only", role: "status", "aria-live": "polite", "aria-atomic": "true" });
document.body.append(live);
let liveQueue = [];
let liveTimer = 0;
function announce(text) {
  if (!text) return;
  if (!liveQueue.length) live.textContent = "";
  liveQueue.push(text);
  clearTimeout(liveTimer);
  liveTimer = setTimeout(() => {
    live.textContent = liveQueue.join(" ");
    liveQueue = [];
    liveTimer = setTimeout(() => (live.textContent = ""), 7000);
  }, 150);
}

function placeLive() {
  const home = root.querySelector('[aria-modal="true"]') || document.body;
  if (live.parentNode !== home) home.append(live);
}

// What a render shows that is marked data-announce (an error, a warning, a lost connection) is
// said once, when it first shows, rather than each time a poll rebuilds it.
let announced = new Set();
function announceShown() {
  const now = new Set();
  for (const el of root.querySelectorAll("[data-announce]")) {
    const text = el.dataset.announce || el.textContent;
    now.add(text);
    if (!announced.has(text)) announce(text);
  }
  announced = now;
}

// A render builds the page anew. The focused element is kept, with the elements it sits in, and
// everything around them is replaced: moved or rebuilt, it would lose focus, and a screen reader
// would read it out again on every poll. Its counterpart in the new tree is the element with its
// data-key, or else its place among the focusable elements of its message. A match by place counts
// only if the message holds as many of them as before and the counterpart is the same kind of
// control with the same name, so a poll can never move the focus to a different control.
function twinOf(el, kids) {
  if (held.mark.parentNode) return held.mark;
  const find = (sel) => {
    for (const k of kids) {
      if (k.matches(sel)) return k;
      const f = k.querySelector(sel);
      if (f) return f;
    }
    return null;
  };
  const by = (n) => (n.dataset.key ? `[data-key="${CSS.escape(n.dataset.key)}"]` : n.dataset.id ? `[data-id="${CSS.escape(n.dataset.id)}"]` : n.dataset.cid ? `[data-cid="${CSS.escape(n.dataset.cid)}"]` : null);
  if (by(el)) return find(by(el));
  const anchor = el.parentElement?.closest("[data-id], [data-cid]");
  if (!anchor) return null;
  const focusable = (n) => [...n.querySelectorAll("button, a[href], input, textarea, [tabindex]")];
  const was = focusable(anchor);
  const twinAnchor = find(by(anchor));
  const now = twinAnchor ? focusable(twinAnchor) : [];
  const twin = now.length === was.length ? now[was.indexOf(el)] : null;
  const same = (a, b) => a.tagName === b.tagName && a.className === b.className && a.getAttribute("aria-label") === b.getAttribute("aria-label") && a.getAttribute("href") === b.getAttribute("href") && a.textContent === b.textContent;
  return twin && same(el, twin) ? twin : null;
}

function graft(parent, kids, el, twin) {
  const chain = [];
  for (let n = el; n && n !== parent; n = n.parentNode) chain.unshift(n);
  const twins = [];
  for (let n = twin; n; n = n.parentNode) {
    twins.unshift(n);
    if (kids.includes(n)) break;
  }
  if (chain.length !== twins.length || !kids.includes(twins[0])) return false;
  if (chain.some((n, i) => twins[i] !== held.mark && n.tagName !== twins[i].tagName)) return false;
  let list = kids;
  for (let i = 0; i < chain.length; i++) {
    const keep = chain[i];
    const at = list.indexOf(twins[i]);
    for (const n of [...parent.childNodes]) if (n !== keep) n.remove();
    keep.before(...list.slice(0, at));
    keep.after(...list.slice(at + 1));
    if (twins[i] === held.mark) break; // the element itself, which outlasts renders
    adopt(keep, twins[i]);
    if (i === chain.length - 1) keep.replaceChildren(...twins[i].childNodes);
    else list = [...twins[i].childNodes];
    parent = keep;
  }
  return true;
}

// Gives a kept element its counterpart's attributes and listeners.
function adopt(old, neu) {
  for (const a of [...old.attributes]) {
    if (neu.hasAttribute(a.name)) continue;
    old.removeAttribute(a.name);
    // A field whose render gave it a value, and now gives none, shows none: once typed in, it
    // would go on showing what was typed (idle minutes back to inherited after a save failed). A
    // field that never had a value attribute is a draft the render leaves alone.
    if (a.name === "value" && "value" in old) old.value = "";
  }
  for (const a of neu.attributes) {
    if (a.name === "style") old.style.cssText = neu.style.cssText; // the CSP takes styles only through the CSSOM
    else if (old.getAttribute(a.name) !== a.value) {
      // A field that has been typed in ignores its value attribute, so a render that changes it
      // (the tag menu's field, cleared once a tag is made) sets what it shows too.
      if (a.name === "value" && old.getAttribute("value") !== a.value && "value" in old && old.value !== a.value) old.value = a.value;
      old.setAttribute(a.name, a.value);
    }
  }
  for (const [type, fn] of old.listeners || []) old.removeEventListener(type, fn);
  for (const [type, fn] of neu.listeners || []) old.addEventListener(type, fn);
  old.listeners = neu.listeners;
}

function render() {
  if (ui.conn === "denied" || !ui.token) {
    root.replaceChildren(
      h(
        "div",
        { class: "gate" },
        h("img", { class: "gate-mark", src: "/assets/favicon.svg", alt: "" }),
        h("p", { class: "gate-title" }, NAME),
        h("p", { class: "gate-line" }, "This page needs its link again. Run " + COMMAND + " ui to open it."),
      ),
    );
    content = null;
    placeLive();
    return;
  }
  const focused = root.contains(document.activeElement) ? document.activeElement : null;
  const keep = document.activeElement?.dataset?.key;
  const caret = caretOf(document.activeElement);
  const scroll = content ? content.scrollTop : 0;
  const chat = chatScroll();
  const side = root.querySelector(".sidebar-content, .mini-scroll")?.scrollTop || 0;
  // With the focus in the pane, the new pane is built apart, and graft keeps the old one.
  if (!content || content.contains(focused)) content = h("div", { class: "content" });
  held.el = focused;
  held.mark = focused && h("span");
  let kids;
  let gone = null; // the key of what opened a tag menu that closed itself
  try {
    const inner = page(); // before the top bar, which counts what this settles
    content.className = "content" + (inner.classList.contains("chat-page") ? " content-chat" : "");
    content.replaceChildren(inner);
    // On a phone the sidebar is a drawer over the page, opened from the top bar. A dialog, the tag
    // menu or the repo menu holds the keyboard: the page behind it is inert until it closes.
    const drawer = NARROW.matches && ui.drawer;
    // A tag menu whose sessions have all gone closes here, before the page is made inert for it.
    if (ui.tagMenu && !ui.tagMenu.addrs.some(sessionByAddr)) {
      gone = ui.tagMenu.from;
      ui.tagMenu = null;
    }
    const modal = !!(ui.setup || ui.picker || ui.tagMenu || ui.repoMenu);
    kids = [
      h(
        "div",
        { class: "app" + (drawer ? " drawer-open" : ""), inert: modal ? true : null },
        // Past the sidebar's many rows to the page, for the keyboard (WCAG 2.4.1).
        !NARROW.matches && h("button", { class: "skip-link", "data-key": "skip", onclick: () => root.querySelector("main")?.focus() }, "Skip to content"),
        sidebar(),
        drawer && h("div", { class: "drawer-scrim", "aria-hidden": "true", onclick: () => setDrawer(false) }),
        h(
          "main",
          { class: "main", tabindex: -1, "data-key": "main", inert: drawer ? true : null },
          showTopbar() ? topbar() : h("h1", { class: "sr-only" }, viewTitle()),
          content,
        ),
      ),
      ui.setup && setupDialog(),
      ui.picker && pickerDialog(),
      ui.tagMenu && tagMenu(),
      ui.repoMenu && repoMenu(),
    ].filter(Boolean);
  } finally {
    held.el = null;
  }
  const twin = focused && twinOf(focused, kids);
  if (twin && graft(root, kids, focused, twin)) {
    content = root.querySelector(".content") || content;
  } else {
    // The page changed around it too much to keep it: put the focus back on its counterpart if
    // its key finds one, or else on the pane it was in, rather than on some other control.
    if (twin && twin === held.mark) held.mark.replaceWith(focused);
    const inMain = !!focused?.closest("main");
    root.replaceChildren(...kids);
    const el = keep ? root.querySelector(`[data-key="${CSS.escape(keep)}"]`) : null;
    if (el?.isConnected) {
      el.focus({ preventScroll: true });
      restoreCaret(el, caret);
    } else if (focused && inMain) {
      root.querySelector("main")?.focus({ preventScroll: true });
    }
  }
  // The focus was in that menu, and went with it: back to what opened it, as closing it would do,
  // or to the page when that went too (the Tag button of a session that has gone).
  if (gone && (!document.activeElement || document.activeElement === document.body)) {
    root.querySelector(`[data-key="${CSS.escape(gone)}"]`)?.focus({ preventScroll: true });
    if (document.activeElement === document.body) root.querySelector("main")?.focus({ preventScroll: true });
  }
  content.scrollTop = scroll;
  const sideNow = root.querySelector(".sidebar-content, .mini-scroll");
  if (sideNow) sideNow.scrollTop = side;
  restoreChatScroll(chat);
  fitComposers();
  placeLive();
  announceShown();
  document.title = (unreadCount() ? "(" + unreadCount() + ") " : "") + NAME;
  queueMicrotask(markOpenRead);
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

// A row in the sidebar's nav, as T3 Code's: an icon, a label, and a count or badge at the end.
function navRow(key, iconName, label, current, onclick, end) {
  return h(
    "button",
    { class: "menu-button", "aria-current": current ? "page" : null, "data-key": key, onclick },
    icon(iconName),
    h("span", { class: "label" }, label),
    end,
  );
}

// The sidebar is the sessions themselves, under their devices, as T3 Code's are threads under their
// projects: your messages, every session, every conversation and the newest messages at the top,
// Settings at the foot. It folds to a strip of faces, and remembers that. On a phone it is a drawer.
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

function setDrawer(open) {
  ui.drawer = open;
  render();
  if (open) root.querySelector(".sidebar .menu-button")?.focus({ preventScroll: true });
  else root.querySelector('[data-key="open-sidebar"]')?.focus({ preventScroll: true });
}

// Picking a session shows its conversations.
function pickSession(key) {
  ui.filter = key;
  ui.conversation = null;
  go("messages");
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
    connBadge(true),
    h(
      "div",
      { class: "mini-scroll" },
      miniYou(),
      top("sessions", "users", "Sessions", ui.view === "sessions"),
      top("messages", "message", "Conversations", ui.view === "messages" && !ui.filter),
      top("recent", "clock", "Recent", ui.view === "recent"),
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
          h("div", { class: "mini-dev", title: deviceLabel(d) + " · " + plural(live.length, "session") }, deviceDot(d)),
          live.map((s) => {
            const key = s.addr || s.name;
            return h(
              "button",
              {
                class: "mini-face" + (ui.view === "messages" && ui.filter === key ? " current" : ""),
                title: s.name + (s.doing ? ": " + s.doing : ""),
                "aria-label": s.name,
                "data-key": "mini-s-" + s.name,
                onclick: () => pickSession(key),
              },
              presence(s.name, d),
            );
          }),
        ];
      }),
    ),
    miniUpdate(),
    h("button", { class: "mini-btn mini-foot" + (ui.view === "settings" ? " current" : ""), title: "Settings", "aria-label": "Settings", "data-key": "mini-settings", onclick: () => go("settings") }, icon("settings")),
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

// Whether the page can reach its daemon. Nothing shows while it can.
function connBadge(small) {
  if (ui.conn !== "down" || ui.restarting) return null;
  const tip = "Can't reach the daemon. The page keeps trying; " + COMMAND + " ui starts it again.";
  return small
    ? h("span", { class: "mini-conn", role: "img", title: tip, "aria-label": "Disconnected", "data-announce": tip }, h("span", { class: "dot warn" }))
    : h("span", { class: "conn-badge", title: tip, "data-announce": tip }, h("span", { class: "dot warn" }), "Disconnected");
}

function sidebar() {
  if (mini && !NARROW.matches) return miniSidebar();
  const devs = devices();
  const allCount = allConversations().length;
  const live = devs.reduce((n, d) => n + liveSessions(d).length, 0);
  return h(
    "aside",
    { class: "sidebar", "data-app-sidebar": true, "aria-label": NAME, id: "sidebar" },
    h(
      "div",
      { class: "sidebar-header" },
      h("img", { class: "brand-mark", src: "/assets/favicon.svg", alt: "" }),
      repoButton(),
      connBadge(false),
      NARROW.matches
        ? h("button", { class: "icon-btn", title: "Close", "aria-label": "Close the sidebar", "data-key": "close-sidebar", onclick: () => setDrawer(false) }, icon("panel"))
        : h("button", { class: "icon-btn", title: "Hide the sidebar", "aria-label": "Hide the sidebar", "data-key": "fold", onclick: () => setMini(true) }, icon("panel")),
    ),
    h(
      "div",
      { class: "sidebar-content" },
      h(
        "nav",
        { class: "sidebar-group g-top", "aria-label": "Views" },
        yourRow(),
        navRow("top-sessions", "users", "Sessions", ui.view === "sessions", () => ((ui.filter = null), go("sessions")), h("span", { class: "menu-count" }, live)),
        navRow("all", "message", "Conversations", ui.view === "messages" && !ui.filter, () => ((ui.filter = null), (ui.conversation = null), go("messages")), h("span", { class: "menu-count" }, allCount)),
        navRow("top-recent", "clock", "Recent", ui.view === "recent", () => ((ui.filter = null), go("recent"))),
      ),
      ui.notice && h("button", { class: "side-notice", title: "Dismiss", "data-key": "notice", "data-announce": "", onclick: () => ((ui.notice = ""), render()) }, ui.notice),
      peopleGroup(),
      tagsGroup(),
      devs.map((d) => deviceGroup(d)),
    ),
    sidebarFoot(),
  );
}

// A device, like a project in T3 Code's sidebar: a header that folds it, then its sessions. A tag
// filter (#270) leaves out the sessions without the tag, and a device left with none.
function deviceGroup(d) {
  ui.closedDevices = ui.closedDevices || new Set();
  const closed = ui.closedDevices.has(d.id);
  const live = shownSessions(liveSessions(d));
  if (ui.tagFilter && !live.length && !shownSessions(quietSessions(d)).length) return null;
  return h(
    "div",
    { class: "sidebar-group dev-group" + (d.reachable ? "" : " stale") },
    h(
      "button",
      {
        class: "device-link",
        "aria-expanded": closed ? "false" : "true",
        "data-key": "dev-" + d.id,
        title: d.reachable ? d.id : d.id + ", last seen " + ago(when(d.lastSeen) || 0),
        onclick: () => {
          closed ? ui.closedDevices.delete(d.id) : ui.closedDevices.add(d.id);
          render();
        },
      },
      icon("chevronRight", "dev-chev"),
      h("span", { class: "label" }, h("span", { class: "dev-name truncate" }, deviceLabel(d)), h("span", { class: "dev-host truncate" }, host(d.id))),
      d.self ? h("span", { class: "dev-you" }, "you") : !d.reachable && h("span", { class: "dev-you" }, "offline"),
      h("span", { class: "menu-count" }, live.length),
    ),
    !closed && [
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
                  title: [s.name, s.doing, s.branch && "on " + s.branch, tagsOf(s).names.join(", ")].filter(Boolean).join(" · "),
                  onclick: () => pickSession(key),
                },
                ...sideSessionBody(d, s),
              ),
              h("span", { class: "side-actions" }, s.addr && tagButton(s), messageButton(s.name), s.addr && hideButton(s)),
            );
          }),
        ),
      quietFold(d),
      !ui.tagFilter && settledFold(d.id),
    ],
  );
}

// The foot says who you are here, once you are someone, and carries the update notice.
function sidebarFoot() {
  const a = me();
  const update = updateItem();
  return h(
    "div",
    { class: "sidebar-foot" },
    update,
    a
      ? h(
          "div",
          { class: "foot-account" },
          h(
            "button",
            { class: "account-chip", title: "Your account", "data-key": "account-chip", onclick: () => go("settings") },
            avatar(a.name),
            h("span", { class: "account-name truncate" }, a.name),
          ),
          h("button", { class: "icon-btn", title: "Settings", "aria-label": "Settings", "aria-current": ui.view === "settings" ? "page" : null, "data-key": "nav-settings", onclick: () => go("settings") }, icon("settings")),
        )
      : navRow("nav-settings", "settings", "Settings", ui.view === "settings", () => go("settings")),
  );
}

// Your messages, at the top of the sidebar, with what is unread. With no account yet, it is where
// you get one; with a name someone else claimed first (#268), where you choose another.
function yourRow() {
  const n = unreadCount();
  const taken = !!me()?.taken;
  return navRow(
    "top-you",
    "user",
    "Your messages",
    isMyFilter(),
    openYours,
    taken
      ? h("span", { class: "name-taken", title: "Someone else chose your name first. Choose a new one to send again." }, "New name")
      : n > 0 && h("span", { class: "unread-badge", "aria-label": n + " unread" }, n),
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
    { class: "sidebar-group dev-group" },
    h("div", { class: "group-label" }, h("span", { class: "label" }, "People"), h("span", { class: "menu-count" }, others.length)),
    h(
      "ul",
      { class: "sidebar-menu side-sessions" },
      others.map((p) =>
        h(
          "li",
          { class: "side-li" },
          h(
            "button",
            { class: "side-session", "data-key": "person-" + p.name, title: "Message " + p.name, onclick: () => startChat(p.name) },
            presence(p.name, p.device),
            h(
              "span",
              { class: "side-session-text" },
              h("span", { class: "name-line" }, h("span", { class: "side-session-name truncate" }, p.name), humanTag()),
              h("span", { class: "side-session-doing truncate" }, personOnline(p) ? "On " + NAME + " now" : deviceLabel(p.device)),
            ),
          ),
        ),
      ),
    ),
  );
}

function messageButton(name) {
  return h(
    "button",
    {
      class: "side-msg",
      "data-tip": "Message",
      "aria-label": "Message " + name,
      "data-key": "msg-" + name,
      onclick: (e) => {
        e.stopPropagation();
        startChat(name);
      },
    },
    icon("message"),
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
  const kids = [updateIcon(info), h("span", { class: "label" }, info.text)];
  return info.act
    ? h("button", { class: cls, title: info.title, "data-key": "update", onclick: info.act }, kids)
    : h("div", { class: cls, title: info.title, "data-announce": info.text + ". " + info.title }, kids);
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
  const row = settingsRow("version", NAME + " " + v.running, description, control);
  if (v.latest && !v.auto && !v.ready && !ui.restarting && v.install) {
    row.append(
      h(
        "div",
        { class: "install-line" },
        h("code", null, v.install),
        h(
          "button",
          { class: "btn btn-ghost btn-xs", title: "Copy", "aria-label": "Copy the command", "data-key": "copy-install", onclick: () => copyText(v.install) },
          icon("copy"),
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
// What each state means, for a dot's tooltip.
const STATE_TIPS = {
  on: "Active in the last five minutes",
  idle: "Running and reachable, but quiet",
  dnd: "Do not disturb: messages wait in its inbox, with no turn and no notice, until it reads its inbox or the tag comes off",
  off: "Its device can't be reached",
};

const PERSON_TIPS = { on: "On " + NAME + " now", off: "Not on " + NAME + " now" };

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

function hideButton(s) {
  const hidden = isHidden(s);
  return h(
    "button",
    {
      class: "side-hide",
      "data-tip": hidden ? "Show again" : "Hide until you show it again",
      "aria-label": hidden ? "Show " + s.name + " again" : "Hide " + s.name,
      "data-key": "hide-" + s.name,
      onclick: () => setHidden(s, !hidden),
    },
    icon(hidden ? "eye" : "eyeOff"),
  );
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
              presence(s.name, d),
              h("span", { class: "settled-name truncate" }, s.name),
              tagChips(s, 2, true),
              h("span", { class: "settled-when" }, isHidden(s) ? "hidden" : t ? ago(t).replace(" ago", "") : ""),
            ),
            s.addr && tagButton(s),
            isHidden(s) && hideButton(s),
          );
        }),
      ),
  );
}

// A face with its presence dot inside it, so it still fits the two-face pair of a DM.
function dotted(name) {
  const a = avatar(name);
  const p = findPerson(name);
  const f = findSession(name);
  const st = p ? (personOnline(p) ? "on" : "off") : f ? sessionState(f.device, f.session) : "off";
  a.classList.add("dotted");
  a.append(h("span", { class: "pres pres-" + st, title: name + ": " + (isPerson(name) ? PERSON_TIPS[st] : STATE_TIPS[st]) }));
  return a;
}

// A device: grey when unreachable, green when any of its sessions is active, else yellow.
function deviceState(d) {
  if (!d || !d.reachable) return "off";
  return d.sessions.some((s) => sessionState(d, s) === "on") ? "on" : "idle";
}
const DEVICE_WORDS = { on: "Online, a session is active", idle: "Online, all its sessions idle", off: "Unreachable" };

function deviceDot(d, cls = "") {
  const st = deviceState(d);
  return h("span", { class: ("dev-dot pres-" + st + " " + cls).trim(), title: DEVICE_WORDS[st] });
}

// A state on its own, where a row has no room for a face: T3 Code's thread status dot.
function statusDot(name, d) {
  const p = findPerson(name);
  const f = p ? null : findSession(name);
  const st = p ? (personOnline(p) ? "on" : "off") : sessionState(f ? f.device : d, f ? f.session : null);
  const tip = p ? PERSON_TIPS[st] : STATE_TIPS[st];
  return h("span", { class: "status-dot pres-" + st, title: tip, role: "img", "aria-label": p ? tip : STATE_WORDS[st] });
}

// A pull request for a branch, as gh found it, with a link to it when it is on GitHub.
function prFor(s) {
  const prs = ui.state?.prs || {};
  return s.branch && Object.hasOwn(prs, s.branch) ? prs[s.branch] : null;
}

function prBadge(pr, link, key) {
  const label = (pr.draft ? "Draft pull request #" : "Pull request #") + pr.number;
  const kids = [icon(pr.draft ? "prDraft" : "pr"), "#" + pr.number];
  const cls = "g-pr" + (pr.draft ? " draft" : "");
  if (link && typeof pr.url === "string" && pr.url.startsWith("https://github.com/")) {
    return h("a", { class: cls, href: pr.url, target: "_blank", rel: "noopener noreferrer", title: label + " on GitHub", "data-key": key, onclick: (e) => e.stopPropagation() }, kids);
  }
  return h("span", { class: cls, title: label }, kids);
}

function presence(name, d) {
  const p = findPerson(name);
  if (p) {
    const st = personOnline(p) ? "on" : "off";
    return h("span", { class: "pres-wrap" }, avatar(name), h("span", { class: "pres pres-" + st, title: PERSON_TIPS[st] }));
  }
  const f = findSession(name);
  const st = sessionState(f ? f.device : d, f ? f.session : null);
  return h("span", { class: "pres-wrap" }, avatar(name), h("span", { class: "pres pres-" + st, title: STATE_TIPS[st] }));
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
    avatar(r.name),
    h("span", { class: "settled-name truncate" }, r.name),
    h("span", { class: "settled-when" }, ago(r.last).replace(" ago", "")),
  );
}

// Who a device belongs to, for its heading; its host name goes beside it.
function deviceLabel(d) {
  return d.self ? "This device" : d.nick || d.owner || d.id;
}

// A device ID ends in four hex digits that tell two clones on one machine apart.
function host(id) {
  return String(id).replace(/-[0-9a-f]{4}$/, "");
}

function allConversations() {
  const keep = ui.filter;
  ui.filter = null;
  const list = conversations();
  ui.filter = keep;
  return list;
}

// A session in the sidebar, like a thread in T3 Code's: its state, its name and tags over what it's
// doing, and its pull request.
function sideSessionBody(d, s) {
  const pr = prFor(s);
  return [
    presence(s.name, d),
    h(
      "span",
      { class: "side-session-text" },
      h("span", { class: "name-line" }, h("span", { class: "side-session-name truncate" }, s.name), tagChips(s, 2, true)),
      h("span", { class: "side-session-doing truncate" }, s.doing || s.agent || ""),
    ),
    pr && prBadge(pr, false),
  ];
}

function stateWord(d, s) {
  const st = sessionState(d, s);
  const t = when(s.lastActive);
  if (st === "on") return "Active now";
  if (st === "idle") return t ? "Idle " + ago(t).replace(" ago", "") : "Idle";
  return STATE_WORDS[st];
}

// Every session, in a group under its device as T3 Code's settings rows are; tabs show the quiet and
// the settled ones, and Select picks sessions to tag together (#270).
function sessionsPage() {
  if (!ui.state) return h("div", { class: "g-page" });
  const devs = devices();
  const total = devs.reduce((n, d) => n + shownSessions(liveSessions(d)).length, 0);
  const quiet = devs.reduce((n, d) => n + shownSessions(quietSessions(d)).length, 0);
  const tab = ui.sessTab || "running";
  // What a selection can hold: the sessions in front of the person, so a filter or a tab leaves
  // none picked out of sight.
  const pickable = tab === "settled" ? [] : devs.flatMap((d) => shownSessions(tab === "quiet" ? quietSessions(d) : liveSessions(d))).filter((s) => s.addr);
  const inView = new Set(pickable.map((s) => s.addr));
  for (const a of ui.selected) if (!inView.has(a)) ui.selected.delete(a);
  if (tab === "settled") ui.selecting = false;
  const sub = ui.tagFilter ? plural(total, "session") + " tagged " + ui.tagFilter : plural(total, "session") + " running on " + plural(devs.length, "device");
  return h(
    "div",
    { class: "g-page" },
    h(
      "div",
      { class: "g-toolbar" },
      h("p", { class: "g-toolbar-meta" }, sub),
      h(
        "div",
        { class: "g-head-tools" },
        tab !== "settled" &&
          h(
            "button",
            {
              class: "btn btn-outline btn-xs g-select-btn",
              "aria-pressed": ui.selecting ? "true" : "false",
              "data-key": "select",
              onclick: () => {
                ui.selecting = !ui.selecting;
                ui.selected.clear();
                render();
              },
            },
            ui.selecting ? "Done" : "Select",
          ),
        h(
          "div",
          { class: "segmented", role: "tablist", "aria-label": "Which sessions" },
          [
            ["running", "Running", total],
            ["quiet", "Quiet", quiet],
            ["settled", "Settled", ui.tagFilter ? 0 : settledSessions().length],
          ].map(([id, label, n]) =>
            h(
              "button",
              {
                role: "tab",
                "aria-selected": tab === id ? "true" : "false",
                "data-key": "tab-" + id,
                onclick: () => {
                  ui.sessTab = id;
                  render();
                },
              },
              label,
              h("span", { class: "g-tab-n" }, n),
            ),
          ),
        ),
      ),
    ),
    ui.selecting && selectBar(pickable),
    tab === "settled"
      ? ui.tagFilter
        ? emptyState("tag", "No settled session is tagged " + ui.tagFilter, null)
        : settledPage(devs)
      : tab === "quiet"
        ? quietPage(devs)
        : devs.map((d) => {
            const live = shownSessions(liveSessions(d));
            if (ui.tagFilter && !live.length) return null;
            return deviceSection(
              d,
              d.reachable ? plural(live.length, "session") : "Offline · seen " + ago(when(d.lastSeen) || 0),
              live.length === 0 ? h("div", { class: "row-empty" }, "No sessions") : live.map((s) => sessionCard(d, s)),
            );
          }),
    ui.tagFilter && tab === "running" && total === 0 && emptyState("tag", "No running session is tagged " + ui.tagFilter, null),
  );
}

function deviceSection(d, aside, rows) {
  return h(
    "section",
    { class: "section g-dev" },
    h(
      "div",
      { class: "section-head" },
      d && deviceDot(d),
      h("h2", { class: "section-title" }, d ? deviceLabel(d) : "Unknown device"),
      d && h("span", { class: "g-dev-host" }, host(d.id)),
      h("span", { class: "section-aside" }, aside),
    ),
    h("div", { class: "group" }, rows),
  );
}

// Picking sessions on All sessions, to tag them together: how many, and the tag menu for them.
function selectBar(pickable) {
  const n = ui.selected.size;
  const all = n > 0 && n === pickable.length;
  return h(
    "div",
    { class: "g-select-bar" },
    h("span", { class: "g-select-n" }, n + " selected"),
    h(
      "button",
      {
        class: "btn btn-outline btn-xs",
        disabled: n === 0,
        "data-key": "select-tag",
        onclick: (e) => openTagMenu([...ui.selected], e.currentTarget),
      },
      icon("tag"),
      "Tag",
    ),
    pickable.length > 0 &&
      h(
        "button",
        {
          class: "btn btn-ghost btn-xs",
          "data-key": "select-all",
          onclick: () => {
            ui.selected = all ? new Set() : new Set(pickable.map((s) => s.addr));
            render();
          },
        },
        all ? "Select none" : "Select all",
      ),
  );
}

function quietPage(devs) {
  const withQuiet = devs.filter((d) => shownSessions(quietSessions(d)).length > 0);
  if (!withQuiet.length) {
    return ui.tagFilter
      ? emptyState("tag", "No quiet session is tagged " + ui.tagFilter, null)
      : emptyState("users", "No quiet sessions", "A session idle for a day, or one someone hid, waits here.");
  }
  return withQuiet.map((d) => {
    const rows = shownSessions(quietSessions(d));
    return deviceSection(d, rows.length + " quiet", rows.map((s) => sessionCard(d, s)));
  });
}

function settledPage(devs) {
  const q = (ui.sq || "").toLowerCase();
  const all = settledSessions().filter((r) => !q || r.name.includes(q) || (r.lastMsg?.subject || "").toLowerCase().includes(q));
  const byDev = new Map();
  for (const r of all) {
    if (!byDev.has(r.device)) byDev.set(r.device, []);
    byDev.get(r.device).push(r);
  }
  return [
    settledSessions().length > 0 &&
      h(
        "label",
        { class: "field g-search g-search-wide" },
        icon("search"),
        h("input", {
          type: "search",
          placeholder: "Search settled sessions",
          "aria-label": "Search settled sessions",
          "data-key": "sq",
          value: ui.sq || null,
          oninput: (e) => {
            ui.sq = e.target.value;
            render();
          },
        }),
      ),
    [...byDev].map(([id, rows]) => {
      const d = devs.find((x) => x.id === id);
      const sec = deviceSection(
        d,
        plural(rows.length, "settled session"),
        rows.map((r) =>
          h(
            "button",
            {
              class: "g-settled",
              "data-key": "gs-" + r.name,
              onclick: () => {
                ui.filter = r.addr || r.name;
                ui.conversation = null;
                go("messages");
              },
            },
            avatar(r.name),
            h("span", { class: "g-settled-name truncate" }, r.name),
            h("span", { class: "g-settled-last truncate" }, r.lastMsg ? r.lastMsg.subject : ""),
            h("span", { class: "g-settled-chats" }, plural(r.peers.size, "chat")),
            h("span", { class: "g-settled-when" }, ago(r.last)),
          ),
        ),
      );
      if (!d) sec.querySelector(".section-title").textContent = id ? host(id) : "Unknown device";
      return sec;
    }),
    all.length === 0 && (q ? h("p", { class: "g-none" }, "Nothing matches.") : emptyState("clock", "No settled sessions", "A session that stopped shows here while its messages are still on the page.")),
  ];
}

// A session as a row: its state, name and model over what it's doing, then its tags, branch, pull
// request and how many conversations it has, and a way to message it. While picking, the row picks
// its session rather than opening it.
function sessionCard(d, s) {
  const pr = prFor(s);
  const keep = ui.filter;
  ui.filter = s.addr || s.name;
  const convs = conversations().filter((c) => c.kind === "dm").length;
  ui.filter = keep;
  const picking = ui.selecting && !!s.addr;
  const picked = picking && ui.selected.has(s.addr);
  return h(
    "div",
    { class: "s-row" + (d.reachable ? "" : " stale") + (picking ? " picking" : "") + (picked ? " picked" : "") },
    h(
      "button",
      {
        class: "s-main",
        "data-key": "card-" + s.name,
        title: picking ? (picked ? "Unpick " : "Pick ") + s.name : "Show " + s.name + "'s conversations",
        "aria-pressed": picking ? (picked ? "true" : "false") : null,
        onclick: () => {
          if (!picking) return pickSession(s.addr || s.name);
          picked ? ui.selected.delete(s.addr) : ui.selected.add(s.addr);
          render();
        },
      },
      picking && h("span", { class: "g-pick" + (picked ? " on" : ""), "aria-hidden": "true" }, picked && icon("check")),
      presence(s.name, d),
      h(
        "span",
        { class: "s-text" },
        h(
          "span",
          { class: "s-line name-line" },
          h("span", { class: "s-name truncate" }, s.name),
          h("span", { class: "s-agent truncate" }, [s.agent, modelOf(s)].filter(Boolean).join(" · ")),
          // The hook never reported after a tool call, so the session isn't told of messages while it
          // works (#225). It sits in the name line: the meta columns are a fixed grid, and an extra
          // cell there shifted chats, state and Message one column over (#546).
          s.hookOff && h("span", { class: "badge badge-warning", title: "Isn't told of messages while it works" }, "hook not running"),
          tagChips(s, 3, true),
        ),
        h("span", { class: "s-doing truncate" + (s.doing ? "" : " none") }, s.doing || "No status set"),
      ),
    ),
    h(
      "span",
      { class: "s-meta" },
      h("span", { class: "s-col-branch" }, s.branch && h("span", { class: "g-branch", title: "Branch " + s.branch }, icon("branch"), h("span", { class: "truncate" }, s.branch))),
      h("span", { class: "s-col-pr" }, pr && prBadge(pr, true, "pr-row-" + s.name)),
      h("span", { class: "s-chats" }, plural(convs, "chat")),
      h("span", { class: "s-col-state" }, h("span", { class: "g-state g-state-" + sessionState(d, s) }, stateWord(d, s))),
      h(
        "span",
        { class: "s-col-act" },
        !picking && h("button", { class: "btn btn-outline btn-xs", "aria-label": "Message " + s.name, "data-key": "card-msg-" + s.name, onclick: () => startChat(s.name) }, icon("message"), h("span", { class: "btn-label" }, "Message")),
      ),
    ),
  );
}

// A view with nothing to show, as T3 Code's empty state: a small icon, a line, and what would fill it.
function emptyState(iconName, title, line) {
  return h("div", { class: "empty" }, h("span", { class: "empty-ico" }, icon(iconName)), h("p", { class: "empty-title" }, title), line && h("p", { class: "empty-line" }, line));
}

// Recent: what agents have been doing, newest first, as much as a render can build quickly (#448).
// A message opens its conversation. Between the messages are the things sessions did: joined or
// left, changed their status or tags, committed, and what GitHub says became of pull requests and
// issues. It is a feed, not a log, so what repeats is one line: a session's status changes close
// together show the last, and its commits to one branch are counted.
const RECENT_MAX = 300;
const RECENT_RUN = 30 * 60 * 1000; // how far apart two of a kind may be and still be one line

// The session a pull request is from: the one on its branch, or that committed to it.
function sessionOnBranch(branch) {
  if (!branch) return "";
  let committed = "";
  for (const d of ui.state?.devices || []) {
    for (const s of d.sessions || []) {
      if (s.branch === branch && branch !== "main" && branch !== "master") return s.name;
      if (!committed && (s.committed || []).includes(branch)) committed = s.name;
    }
  }
  return committed;
}

// Everything Recent can show, as { t, who, group, ... }, in no order.
function recentEntries() {
  const out = [];
  // A group's message is each of its members' as well as its sender's, so a member's filter shows
  // it (#485). Members are the sessions in the group now, by inGroup, as the filter has them.
  const members = new Map();
  const sessions = devices().flatMap((d) => d.sessions);
  for (const g of liveGroups()) members.set(g, sessions.filter((s) => inGroup(s, g)).map((s) => s.name));
  for (const m of ui.messages) {
    const to = m.toKind === "session" ? [toName(m)] : m.toKind === "group" ? members.get(m.to) || [] : [];
    out.push({ t: m.sortAt, group: "messages", m, who: [fromName(m), ...to] });
  }
  // A session's status changes, and its commits to a branch, that follow each other closely are one entry.
  const runs = new Map();
  const acts = [...ui.acts].sort((a, b) => a.t - b.t);
  for (const a of acts) {
    const who = nameOf(a.addr, a.who);
    const key = a.kind === "status" ? "status " + who : a.kind === "commits" ? "commits " + who + " " + (a.branch || "") : "";
    const run = key && runs.get(key);
    if (run && a.t - run.t <= RECENT_RUN) {
      run.t = a.t;
      run.n++;
      run.commits += a.commits || 0;
      run.a = a;
      continue;
    }
    const e = { t: a.t, group: "activity", a, who: [who], n: 1, commits: a.commits || 0 };
    if (key) runs.set(key, e);
    out.push(e);
  }
  for (const g of ui.github) {
    const t = when(g.at);
    if (t) out.push({ t: Math.min(t, Date.now()), group: "activity", g, who: [sessionOnBranch(g.branch)] });
  }
  return out;
}

function recentPage() {
  const all = recentEntries();
  const names = [...new Set(all.flatMap((e) => e.who).filter(Boolean))].sort();
  if (ui.recentWho && !names.includes(ui.recentWho)) names.push(ui.recentWho);
  const shown = all.filter((e) => (ui.recentKind === "all" || e.group === ui.recentKind) && (!ui.recentWho || e.who.includes(ui.recentWho)));
  const list = shown.sort((a, b) => b.t - a.t).slice(0, RECENT_MAX);
  const out = [];
  let day = null;
  for (const e of list) {
    const d = e.t && new Date(e.t).toDateString();
    if (d && d !== day) {
      out.push(h("div", { class: "chat-day" }, h("span", null, dayLabel(e.t))));
      day = d;
    }
    out.push(e.m ? openable(recentItem(e.m, when(e.m.at)), e.m, when(e.m.at)) : e.a ? activityItem(e) : githubItem(e));
  }
  const count = (kind) => all.filter((e) => kind === "all" || e.group === kind).length;
  const filtered = ui.recentKind !== "all" || !!ui.recentWho;
  return h(
    "div",
    { class: "g-page g-recent" },
    // A heading at the top of the column, as on main, so the list has something to start from.
    h(
      "header",
      { class: "g-page-head" },
      h("h1", null, "Recent activity"),
      h("p", null, shown.length > RECENT_MAX ? "Newest " + RECENT_MAX + " of " + shown.length : plural(shown.length, "item") + (filtered ? " shown" : "") + ", newest first"),
    ),
    h(
      "div",
      { class: "g-recent-filters" },
      h(
        "div",
        { class: "segmented", role: "tablist", "aria-label": "What to show" },
        [
          ["all", "All"],
          ["messages", "Messages"],
          ["activity", "Activity"],
        ].map(([id, label]) =>
          h(
            "button",
            {
              role: "tab",
              "aria-selected": ui.recentKind === id ? "true" : "false",
              "data-key": "recent-" + id,
              onclick: () => {
                ui.recentKind = id;
                render();
              },
            },
            label,
            h("span", { class: "g-tab-n" }, count(id)),
          ),
        ),
      ),
      h(
        "label",
        { class: "g-recent-who" },
        h("span", { class: "sr-only" }, "Whose"),
        h(
          "select",
          {
            class: "select",
            "data-key": "recent-who",
            onchange: (ev) => {
              ui.recentWho = ev.target.value;
              render();
            },
          },
          h("option", { value: "", selected: !ui.recentWho }, "Everyone"),
          names.map((n) => h("option", { value: n, selected: ui.recentWho === n }, n)),
        ),
      ),
    ),
    list.length
      ? h("div", { class: "g-feed" }, out)
      : filtered
        ? emptyState("clock", "Nothing matches", "Show everything, or pick another session.")
        : emptyState("clock", "Nothing yet", "Messages between sessions, and what the sessions do, show here as this daemon learns of them."),
  );
}

// One line of activity: an icon where a message has its sender's face, who, what, and when. tone
// colors the icon by what happened (style.css's .g-act-*), and "" leaves it grey.
function activityLine(iconName, tone, t, key, ...what) {
  return h(
    "div",
    { class: tone ? "g-act g-act-" + tone : "g-act", "data-id": key },
    h("span", { class: "g-act-ico" }, icon(iconName)),
    h("p", { class: "g-act-text" }, what),
    h("time", { class: "chat-msg-time", title: t ? new Date(t).toLocaleString() : "" }, clockTime(t)),
  );
}

function actWho(name) {
  return h("span", { class: "g-act-who" }, name);
}

function actBranch(branch) {
  return h("span", { class: "g-act-branch" }, icon("branch"), branch);
}

function activityItem(e) {
  const a = e.a;
  const who = actWho(e.who[0]);
  switch (a.kind) {
    case "started":
      return activityLine("logIn", "", e.t, a.id, who, " joined", a.agent && [" as ", a.agent], a.branch && [" on ", actBranch(a.branch)]);
    case "stopped":
      return activityLine("logOut", "", e.t, a.id, who, " left");
    case "status":
      return activityLine("activity", "status", e.t, a.id, who, " is now ", h("span", { class: "g-act-quote" }, a.doing), e.n > 1 && h("span", { class: "g-act-more" }, " after " + plural(e.n - 1, "earlier update")));
    case "tags": {
      const added = (a.added || []).map((t) => h("span", { class: "g-act-tag" }, t));
      const removed = (a.removed || []).map((t) => h("span", { class: "g-act-tag g-act-tag-off" }, t));
      return activityLine("tag", "", e.t, a.id, who, added.length ? [" is tagged ", added] : null, removed.length ? [added.length ? " and no longer " : " is no longer tagged ", removed] : null);
    }
    case "commits":
      return activityLine(
        "commit",
        "commit",
        e.t,
        a.id,
        who,
        e.commits ? " made " + plural(e.commits, "commit") : " committed",
        a.branch ? [e.commits ? " on " : " to ", actBranch(a.branch)] : null,
        a.subject && h("span", { class: "g-act-more" }, e.commits > 1 ? ", the last: " : ": ", a.subject),
      );
  }
  return activityLine("activity", "", e.t, a.id, who, " ", a.kind);
}

function githubItem(e) {
  const g = e.g;
  const pr = g.kind === "pr";
  const what = pr ? "pull request" : "issue";
  const num = g.url ? h("a", { href: g.url, target: "_blank", rel: "noopener noreferrer" }, "#" + g.number) : "#" + g.number;
  const who = e.who[0] ? actWho(e.who[0]) : g.author ? h("span", { class: "g-act-login" }, g.author) : "Someone";
  const title = h("span", { class: "g-act-more" }, ": ", g.title);
  if (g.what === "opened") return activityLine(pr ? "pr" : "issue", "open", e.t, g.id, who, pr ? " opened pull request " : " filed issue ", num, title);
  // GitHub's list says who opened it, not who merged or closed it.
  // GitHub's colors: a merged pull request and a closed issue are done, a closed pull request is not.
  const merged = g.what === "merged";
  const verb = merged ? " was merged" : " was closed";
  return activityLine(merged ? "merge" : pr ? "prClosed" : "issueClosed", merged || !pr ? "done" : "closed", e.t, g.id, e.who[0] ? [who, "'s " + what + " "] : what[0].toUpperCase() + what.slice(1) + " ", num, verb, title);
}

function recentItem(m, at) {
  const from = fromName(m);
  const c = conversationOf(m);
  const to = m.toKind === "all" ? [icon("broadcast"), "Everyone"] : m.toKind === "group" ? [icon("tag"), m.to] : m.toKind === "device" ? [icon("laptop"), host(m.to)] : [avatar(toName(m)), toName(m)];
  const open = () => {
    ui.filter = null;
    ui.conversation = c.key;
    ui.conversationAnchor = m.id;
    go("messages");
  };
  return h(
    "article",
    { class: "chat-msg chat-msg-face chat-msg-first g-item", "data-id": m.id, tabindex: 0, role: "button", "aria-label": "Open the conversation", onclick: open, onkeydown: (e) => (e.key === "Enter" ? open() : null) },
    avatar(from),
    h(
      "div",
      { class: "chat-msg-main" },
      h(
        "div",
        { class: "chat-msg-head" },
        h("span", { class: "chat-msg-from truncate" }, from),
        m.fromPerson && humanTag(),
        h("span", { class: "g-to" }, icon("arrowRight", "g-to-arrow"), h("span", { class: "g-to-who truncate" }, to)),
        h("span", { class: "chat-msg-time", title: messageDetail(m) }, clockTime(at)),
      ),
      messageText(m),
    ),
  );
}

// A Recent item is a button onto its conversation. Its name is what it shows, in the order it shows
// it (WCAG 2.5.3), Space works as well as Enter, and a link in its text is left to the conversation
// it opens, since a button can't hold another control.
function openable(el, m, at) {
  const to = m.toKind === "all" ? "Everyone" : m.toKind === "group" ? m.to : m.toKind === "device" ? host(m.to) : toName(m);
  el.setAttribute("aria-label", fromName(m) + " to " + to + ", " + clockTime(at) + ": " + (m.subject || ""));
  const space = (e) => {
    if (e.key !== " ") return;
    e.preventDefault();
    e.currentTarget.click();
  };
  el.addEventListener("keydown", space);
  el.listeners.push(["keydown", space]);
  for (const a of el.querySelectorAll("a")) a.tabIndex = -1;
  return el;
}

// Everyone reaches every session in the repo; a device's chat reaches every session on it, and a
// group's every session that has its tag (#184). Anyone who has posted is in it too, running or not.
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
    for (const s of d.sessions) {
      if (c.kind === "group" && !inGroup(s, c.title)) continue;
      rows.push({ name: s.name, d, s, posted: posted.get(s.name) || 0 });
    }
  }
  for (const [name, n] of posted) if (!rows.some((r) => r.name === name)) rows.push({ name, addr: addrs.get(name), d: findPerson(name)?.device || null, s: null, posted: n, person: isPerson(name) });
  return rows;
}

// A session is in a group while it has the group's tag, or the page has asked for it to have it.
function inGroup(s, group) {
  return hasTag(s, group);
}

// The groups some session is in now, by their tags, with those this page has just asked for.
function liveGroups() {
  const out = new Set();
  for (const d of devices()) for (const s of d.sessions) for (const n of tagsOf(s).names) if (isGroupTag(n)) out.add(n);
  return [...out].sort();
}

// As tags.IsGroup has it: group: and a name, with no slash.
function isGroupTag(name) {
  return name.startsWith("group:") && name.length > 6 && !name.includes("/");
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
        "aria-controls": ui.membersOpen ? "members-panel" : null,
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
    // By device, not by what it is called: two devices may have one nickname or one owner (#190).
    const title = r.person ? "People" : r.d ? deviceLabel(r.d) : "Not running";
    const key = !r.person && r.d ? "device " + r.d.id : title;
    if (!groups.has(key)) groups.set(key, { title, d: r.person ? null : r.d, host: r.d && !r.person ? host(r.d.id) : "", rows: [] });
    groups.get(key).rows.push(r);
  }
  return h(
    "div",
    // A disclosure: the list follows its button, and Escape folds it back onto the button.
    { class: "g-members-panel", id: "members-panel", role: "group", "aria-label": "Members", onclick: (e) => e.stopPropagation() },
    h("div", { class: "g-mp-head" }, c.kind === "all" ? "Everyone in this repo" : c.kind === "group" ? "Every session tagged " + c.title : "Every session on " + host(c.title)),
    [...groups.values()].map((g) => [
      h("div", { class: "g-mp-sec" }, g.d && deviceDot(g.d), g.title, g.host && h("span", null, g.host), h("span", { class: "g-mp-n" }, g.rows.length)),
      g.rows.map((r) =>
        h(
          "button",
          {
            class: "g-mp-row" + (r.d && !r.d.reachable ? " stale" : ""),
            "data-key": "mp-" + r.name,
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
          h("span", { class: "g-mp-text" }, h("span", { class: "name-line" }, h("span", { class: "g-mp-name truncate" }, r.name), r.person && humanTag()), h("span", { class: "g-mp-doing truncate" }, r.person ? "Person" : r.s ? r.s.doing || r.s.agent || "" : "Not running")),
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
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && ui.membersOpen && !e.defaultPrevented) {
    e.preventDefault();
    ui.membersOpen = false;
    render();
    root.querySelector('[data-key="members"]')?.focus();
  }
});

// The main pane's header, as T3 Code's: slim, with the view's title. A conversation's pane has a
// header of its own, so on a wide page Conversations has none here, and on a phone an open
// conversation's header, with its way back, stands in for it.
function showTopbar() {
  if (ui.view !== "messages") return true;
  return NARROW.matches && !content?.querySelector(".chat-open");
}

function viewTitle() {
  if (ui.view !== "messages") return TITLES[ui.view];
  if (isMyFilter()) return "Your messages";
  return ui.filter ? nameOf(ui.filter, ui.filter) : TITLES.messages;
}

function topbar() {
  // On a phone the sidebar, which carries unread messages and the update notice, is behind this
  // button, so a dot on it stands for them.
  const update = updateInfo();
  const flag = unreadCount() > 0 || !!update;
  return h(
    "header",
    { class: "topbar" },
    NARROW.matches &&
      h(
        "button",
        {
          class: "icon-btn topbar-menu",
          title: "Show the sidebar",
          "aria-label": "Show the sidebar" + (unreadCount() ? ", " + unreadCount() + " unread" : "") + (update ? ", " + update.text : ""),
          "aria-controls": "sidebar",
          "aria-expanded": ui.drawer ? "true" : "false",
          "data-key": "open-sidebar",
          onclick: () => setDrawer(true),
        },
        icon("panel"),
        flag && h("span", { class: "nav-dot" + (unreadCount() ? "" : " update-" + update.kind) }),
      ),
    h("h1", { class: "truncate" }, viewTitle()),
    NARROW.matches && connBadge(false),
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

function tagsLine(s) {
  const tags = Array.isArray(s.tags) ? s.tags : [];
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
    ui.rowErrors[key] && h("span", { class: "row-error", "data-announce": "" }, ui.rowErrors[key]),
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

// The sidebar's Tags: one chip per tag in use, which filters the lists to it.
function tagsGroup() {
  const counts = tagCounts();
  if (ui.tagFilter && !counts.has(ui.tagFilter)) ui.tagFilter = null;
  if (!counts.size) return null;
  return h(
    "div",
    { class: "sidebar-group dev-group" },
    h("div", { class: "group-label" }, h("span", { class: "label" }, "Tags"), h("span", { class: "menu-count" }, counts.size)),
    h(
      "div",
      { class: "tag-filter" },
      [...counts]
        .sort((a, b) => a[0].localeCompare(b[0]))
        .map(([n, c]) => {
          const on = ui.tagFilter === n;
          return h(
            "button",
            {
              class: "tf" + (on ? " on" : ""),
              "aria-pressed": on ? "true" : "false",
              title: on ? "Show every session" : "Show only sessions tagged " + n,
              "data-key": "tf-" + n,
              onclick: () => {
                ui.tagFilter = on ? null : n;
                render();
              },
            },
            h("span", { class: "tf-name" }, n),
            h("span", { class: "tf-n" }, c),
          );
        }),
    ),
  );
}

// On a sidebar row, beside Message and Hide.
function tagButton(s) {
  return h(
    "button",
    {
      class: "side-tag",
      "data-tip": "Tag",
      "aria-label": "Tag " + s.name,
      "aria-haspopup": "dialog",
      "data-key": "tag-" + s.name,
      onclick: (e) => {
        e.stopPropagation();
        openTagMenu([s.addr], e.currentTarget);
      },
    },
    icon("tag"),
  );
}

function openTagMenu(addrs, anchor) {
  const r = anchor.getBoundingClientRect();
  // Below the button, or above it when there is more room there than the menu's 20rem.
  const up = window.innerHeight - r.bottom < 340 && r.top > window.innerHeight - r.bottom;
  ui.tagMenu = { addrs, x: Math.max(8, Math.min(r.left, window.innerWidth - 264)), top: up ? null : r.bottom + 4, bottom: up ? window.innerHeight - r.top + 4 : null, q: "", err: "", known: new Set(), from: anchor.dataset.key };
  render();
  root.querySelector('[data-key="tagmenu-q"]')?.focus();
}

// Closing the menu gives the focus back to the button that opened it.
function closeTagMenu() {
  const from = ui.tagMenu?.from;
  ui.tagMenu = null;
  render();
  if (from) root.querySelector(`[data-key="${CSS.escape(from)}"]`)?.focus();
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
  const close = closeTagMenu;
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
    { class: "tag-menu", role: "dialog", "aria-modal": "true", "aria-label": "Tags for " + (targets.length === 1 ? targets[0].name : targets.length + " sessions") },
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
            role: "checkbox", // in a dialog with a search field, not a menu: Tab moves between them
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
    m.err && h("p", { class: "tm-err", "data-announce": "" }, m.err),
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

// The repo picker (#348): this machine's clones that joined, each served by its own daemon. Picking
// one moves this tab to that repo's page, whose daemon is started if it isn't running.
function repoButton() {
  const name = ui.state ? ui.state.repo || NAME : ""; // nothing until the state says, rather than a name that changes
  return h(
    "button",
    {
      class: "repo-btn",
      "aria-haspopup": "menu",
      "aria-expanded": ui.repoMenu ? "true" : "false",
      title: "Switch repo",
      "data-key": "repo",
      onclick: (e) => {
        if (ui.repoMenu) return closeRepoMenu();
        const r = e.currentTarget.getBoundingClientRect();
        ui.repoMenu = { x: r.left, top: r.bottom + 4, repos: null, opening: null, err: "" };
        render();
        focusRepoMenu();
        loadRepos();
      },
    },
    h("span", { class: "repo-name truncate" }, name),
    icon("chevronDown"),
  );
}

// The menu holds the keyboard while it is open (the page behind it is inert): the focus goes to its
// current repo, the arrow keys move it, and closing it gives it back to the button.
function closeRepoMenu() {
  ui.repoMenu = null;
  render();
  root.querySelector('[data-key="repo"]')?.focus();
}

function focusRepoMenu() {
  if (!root.querySelector(".repo-menu")?.contains(document.activeElement)) {
    (root.querySelector(".repo-row.current") || root.querySelector(".repo-row"))?.focus();
  }
}

async function loadRepos() {
  const m = ui.repoMenu;
  try {
    const r = await api("/api/repos");
    if (ui.repoMenu !== m) return;
    m.repos = r.repos || [];
  } catch (e) {
    if (e instanceof AuthError) return deny(e.token);
    if (ui.repoMenu !== m) return;
    m.repos = [];
    m.err = e.message;
  }
  render();
  focusRepoMenu();
}

async function openRepo(r) {
  const m = ui.repoMenu;
  if (r.current) return closeRepoMenu();
  if (m.opening) return;
  // The drafts live in this page only, and the other repo's page is another page.
  if (hasUnsent()) {
    m.err = "Send or clear your draft first";
    return render();
  }
  m.opening = r.id;
  m.err = "";
  render();
  try {
    const { url } = await api("/api/repos/open", { method: "POST", body: JSON.stringify({ id: r.id }) });
    // Starting a daemon can take a while, and the picker may have been closed and a draft begun.
    if (ui.repoMenu !== m || hasUnsent()) {
      if (ui.repoMenu === m) {
        m.opening = null;
        m.err = "Send or clear your draft first";
        render();
      }
      return;
    }
    location.assign(url + "&theme=" + theme);
  } catch (e) {
    if (e instanceof AuthError) return deny(e.token);
    if (ui.repoMenu !== m) return;
    m.opening = null;
    m.err = e.message;
    if (/isn't joined/.test(e.message)) m.repos = (m.repos || []).filter((x) => x.id !== r.id);
    render();
  }
}

function repoMenu() {
  const m = ui.repoMenu;
  const repos = m.repos || [{ id: "", name: ui.state?.repo || NAME, current: true, running: true }];
  // A path tells two clones of one repo apart; one alone goes by its name.
  const named = new Map();
  for (const r of repos) named.set(r.name, (named.get(r.name) || 0) + 1);
  const menu = h(
    "div",
    {
      class: "tag-menu repo-menu",
      role: "menu",
      "aria-label": "Repos",
      onkeydown: (e) => {
        const rows = [...e.currentTarget.querySelectorAll(".repo-row:not(:disabled)")];
        const at = rows.indexOf(document.activeElement);
        const to = { ArrowDown: at + 1, ArrowUp: at - 1, Home: 0, End: rows.length - 1 }[e.key];
        if (to == null || !rows.length) return;
        e.preventDefault();
        rows[(to + rows.length) % rows.length].focus();
      },
    },
    h(
      "div",
      { class: "tag-menu-list" },
      repos.map((r) =>
        h(
          "button",
          {
            class: "tag-menu-row repo-row" + (r.current ? " current" : ""),
            role: "menuitemradio",
            "aria-checked": r.current ? "true" : "false",
            title: r.path || null,
            "data-key": "repo-" + r.id,
            disabled: !!m.opening && m.opening !== r.id,
            onclick: () => openRepo(r),
          },
          h("span", { class: "dot" + (r.running ? " ok" : ""), role: "img", "aria-label": r.running ? "Running" : "Not running" }),
          h("span", { class: "repo-row-text" }, h("span", { class: "tm-name truncate" }, r.name), named.get(r.name) > 1 && r.path && h("span", { class: "repo-path truncate" }, r.path)),
          m.opening === r.id ? icon("rotateCw", "spin") : r.current && icon("check"),
        ),
      ),
    ),
    m.err && h("p", { class: "tm-err", "data-announce": "" }, m.err),
  );
  // Placed through the CSSOM: the page's CSP refuses style attributes.
  menu.style.left = m.x + "px";
  menu.style.top = m.top + "px";
  return h(
    "div",
    {
      class: "menu-scrim",
      onclick: (e) => {
        if (e.target === e.currentTarget) closeRepoMenu();
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
// The adds this page made to each group, which a message sent to it waits for: each resolves to the
// sessions whose add failed, with why. A tag change that fails resolves its place in tagQueues all
// the same. They are kept, and a session counts as missed only while it is still out of the group.
const groupJoins = new Map(); // a group's tag → [Promise<[{ s, msg }]>]

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
  const all = Promise.all(
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
  if (on && isGroupTag(name) && todo.length) groupJoins.set(name, [...(groupJoins.get(name) || []), all.then(() => failed)]);
  await all;
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
  const found = findSession(name);
  const device = slash > 0 ? f.slice(0, slash) : found?.device.id;
  const is = (addr, shown) => (addr ? addr === f : shown === name) || (name !== f && nameOf(addr, shown) === name);
  // A group the session is in now is its conversation too, others' messages in it included (#485).
  return ui.messages.filter(
    (m) =>
      is(m.fromAddr, m.from) ||
      (m.toKind === "session" && is(m.toAddr, m.to)) ||
      (m.toKind === "device" && m.to === device) ||
      (m.toKind === "group" && !!found && inGroup(found.session, m.to)) ||
      m.toKind === "all",
  );
}

// Your messages: direct ones, and the group chats you have posted in, whole, with the one you just
// opened, so a group that already has a history shows it before you write in it.
function myMessages() {
  const mine = myName();
  const groups = new Set(ui.draftGroup ? [groupConversation(ui.draftGroup).key] : []);
  for (const m of ui.messages) if (m.toKind !== "session" && fromName(m) === mine) groups.add(conversationOf(m).key);
  return ui.messages.filter((m) => (m.toKind === "session" ? fromName(m) === mine || toName(m) === mine : groups.has(conversationOf(m).key)));
}

// Who sent a message and when, as far as the page knows.
function messageDetail(m) {
  const at = when(m.at);
  const sender = findSession(fromName(m));
  return [sender && [sender.session.agent, sender.device.id].filter(Boolean).join(" on "), at && stamp(at)]
    .filter(Boolean)
    .join(" · ");
}

// Conversations: two sessions' messages to each other, a device's, and everyone's. There is no
// thread on the bus, so this is all a conversation can be. They are keyed by name, not address, so
// a session that came back under a new session ID stays in the same one.
function conversationOf(m) {
  if (m.toKind === "all") return { key: "all", kind: "all", title: "Everyone" };
  if (m.toKind === "group") return groupConversation(m.to);
  if (m.toKind === "device") return { key: "device:" + m.to, kind: "device", title: m.to };
  const pair = [...new Set([fromName(m), toName(m)])].sort();
  return { key: "dm:" + JSON.stringify(pair), kind: "dm", title: pair.join(" & "), pair };
}

// A group's conversation: what was sent to its tag.
function groupConversation(group) {
  return { key: "tag:" + group, kind: "group", title: group };
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
  // A group with members is listed before anyone writes to it, so a person can be the first; it
  // sits below the conversations that have messages.
  if (!ui.filter) {
    for (const g of liveGroups()) {
      const c = groupConversation(g);
      if (!byKey.has(c.key)) byKey.set(c.key, { ...c, messages: [], quiet: true });
    }
  }
  const list = [...byKey.values()];
  for (const c of list) c.messages.sort((a, b) => a.sortAt - b.sortAt);
  const latest = (c) => (c.messages.length ? c.messages[c.messages.length - 1].sortAt : c.quiet ? 0 : Infinity);
  return list.sort((a, b) => latest(b) - latest(a) || 0); // two with no messages subtract to NaN
}

function dmKey(a, b) {
  return "dm:" + JSON.stringify([...new Set([a, b])].sort());
}

function draftConversation() {
  if (ui.restored?.key === ui.conversation) return { ...ui.restored, messages: [] };
  if (ui.draftGroup && me() && ui.conversation === groupConversation(ui.draftGroup).key) return { ...groupConversation(ui.draftGroup), messages: [] };
  if (!ui.draftTo || !me()) return null;
  const pair = [...new Set([myName(), ui.draftTo])].sort();
  const key = dmKey(myName(), ui.draftTo);
  return ui.conversation === key ? { key, kind: "dm", title: pair.join(" & "), pair, messages: [] } : null;
}

// The same name always gets the same color. The classes are in style.css: the page's content
// policy doesn't allow an inline style.
function avatar(name) {
  let n = 0;
  for (const ch of name) n = (n * 31 + ch.codePointAt(0)) % 997;
  return h("span", { class: isPerson(name) ? "avatar avatar-person" : "avatar avatar-" + (n % 5), "aria-hidden": "true" }, (name[0] || "?").toUpperCase());
}

// A group chat is named for whose it is, and its face is its members'.
function groupName(c) {
  if (c.kind === "all") return "Everyone";
  if (c.kind === "group") return c.title;
  const d = (ui.state?.devices || []).find((x) => x.id === c.title);
  if (!d) return host(c.title);
  if (d.self) return "This device";
  if (d.nick) return d.nick;
  const o = d.owner || "";
  return o ? o[0].toUpperCase() + o.slice(1) + "'s " + (devices().filter((x) => x.owner === d.owner).length > 1 ? host(d.id) : "device") : d.id;
}

function groupHost(c) {
  return c.kind === "device" ? host(c.title) : "";
}

function groupSummary(c) {
  const rows = groupMembers(c);
  // A person has no session, so sessionState would call them idle; they are active while their page is open.
  const on = rows.filter((r) => {
    const p = r.person && findPerson(r.name);
    return p ? personOnline(p) : r.d && sessionState(r.d, r.s) === "on";
  }).length;
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
  return h("span", { class: "avatar-pair" }, c.pair.map(dotted));
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
  const open = list.find((c) => c.key === ui.conversation);
  ui.conversationAnchor = open?.messages[open.messages.length - 1]?.id;
  if (ui.newMark && ui.newMark.key !== ui.conversation) ui.newMark = null;
  return h(
    "div",
    { class: "page chat-page" },
    // A picked session keeps its header, and so its tags, before it has any messages.
    list.length === 0 && !ui.filter
      ? emptyState("message", "No conversations yet", "Messages between sessions show here as this daemon reads them.")
      : h(
          "div",
          { class: "chat" + (open ? " chat-open" : "") },
          conversationList(list),
          open ? conversationPane(open) : isMyFilter() && !NARROW.matches && startPane(),
        ),
  );
}

// With a session picked, a chat with it is called by the other session's name.
function otherIn(c) {
  if (c.kind !== "dm") return null;
  const self = ui.filter ? nameOf(ui.filter, ui.filter) : myName();
  return self && c.pair.includes(self) ? c.pair.find((n) => n !== self) || self : null;
}

// The middle column: whose conversations they are, a search, then direct messages and group chats
// in one list. Direct messages with a settled session fold away at the end.
function conversationList(list) {
  const name = ui.filter && nameOf(ui.filter, ui.filter);
  const f = name && findSession(name);
  const q = (ui.cq || "").toLowerCase();
  const hit = (c) => !q || (otherIn(c) || c.title).toLowerCase().includes(q) || c.messages.some((m) => (m.subject + " " + (m.body || "")).toLowerCase().includes(q));
  // One list, newest first, so a busy group chat is never below every quiet DM. The switch
  // narrows it to direct messages or group chats.
  const kind = ui.convKind || "all";
  const wanted = (c) => kind === "all" || (kind === "dm") === (c.kind === "dm");
  const old = (c) => c.kind === "dm" && c.pair.some((n) => isSettled(n));
  const rows = list.filter((c) => wanted(c) && hit(c) && (q || name || !old(c)));
  const older = q || name || kind === "group" ? [] : list.filter((c) => old(c));
  const count = (k) => list.filter((c) => (k === "all" || (k === "dm") === (c.kind === "dm")) && hit(c) && (q || name || !old(c))).length;
  return h(
    "nav",
    { class: "chat-list g-list", "aria-label": "Conversations", onscroll: (e) => (ui.chatListTop = e.target.scrollTop) },
    h(
      "div",
      { class: "g-head" },
      isMyFilter()
        ? h(
            "div",
            { class: "g-head-row" },
            h("div", { class: "g-head-text" }, h("h2", null, "Your messages"), list.some((c) => c.messages.length) && h("p", { class: "g-head-doing" }, plural(list.filter((c) => c.messages.length).length, "conversation"))),
            h("div", { class: "g-head-actions" }, newGroupButton(), newMessageButton()),
          )
        : name
        ? [
            h(
              "div",
              { class: "g-head-who" },
              f ? presence(name, f.device) : avatar(name),
              h("h2", { class: "truncate" }, name),
              f && h("button", { class: "btn btn-outline btn-xs g-head-msg", "data-key": "head-msg", onclick: () => startChat(name) }, icon("message"), "Message"),
            ),
            f && f.session.doing && h("p", { class: "g-head-doing" }, f.session.doing),
            f &&
              h(
                "p",
                { class: "g-head-meta" },
                h("span", { class: "truncate" }, [f.session.agent, modelOf(f.session)].filter(Boolean).join(" · ")),
                f.session.branch && h("span", { class: "g-branch", title: "Branch " + f.session.branch }, icon("branch"), h("span", { class: "truncate" }, f.session.branch)),
                prFor(f.session) && prBadge(prFor(f.session), true, "pr-head-" + f.session.name),
              ),
            f && tagsLine(f.session),
            !f && h("p", { class: "g-head-doing" }, "Settled · " + ago(settledSessions().find((r) => r.name === name)?.last || 0)),
          ]
        : h(
            "div",
            { class: "g-head-row" },
            h("div", { class: "g-head-text" }, h("h2", null, "All conversations"), h("p", { class: "g-head-doing" }, plural(list.length, "conversation"))),
            h("div", { class: "g-head-actions" }, newGroupButton(), newMessageButton()),
          ),
    ),
    h(
      "label",
      { class: "field g-search" },
      icon("search"),
      h("input", {
        type: "search",
        placeholder: "Search conversations",
        "aria-label": "Search conversations",
        "data-key": "cq",
        value: ui.cq || null,
        oninput: (e) => {
          ui.cq = e.target.value;
          render();
        },
      }),
    ),
    h(
      "div",
      { class: "segmented g-kinds", role: "tablist", "aria-label": "Which conversations" },
      [
        ["all", "All"],
        ["dm", "Direct"],
        ["group", "Group"],
      ].map(([id, label]) =>
        h(
          "button",
          {
            role: "tab",
            "aria-selected": kind === id ? "true" : "false",
            "data-key": "kind-" + id,
            onclick: () => {
              ui.convKind = id;
              render();
            },
          },
          label,
          h("span", { class: "g-tab-n" }, count(id)),
        ),
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
    !rows.length && (q || !older.length) && h("p", { class: "g-none" }, q ? "Nothing matches." : kind === "dm" ? "No direct messages" : kind === "group" ? "No group chats" : "No conversations"),
    kind === "group" && !q && !(name && !isMyFilter()) && h("div", { class: "g-none-action" }, h("button", { class: "btn btn-outline btn-sm", "data-key": "list-new-group", onclick: () => openPicker(true) }, icon("users"), "New group chat")),
  );
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
    o ? dotted(o) : conversationIcon(c),
    h(
      "span",
      { class: "chat-row-text" },
      h(
        "span",
        { class: "chat-row-line" },
        c.kind !== "dm"
          ? [
              h("span", { class: "chat-row-title truncate", title: c.title }, groupName(c)),
              h("span", { class: "g-row-count" }, groupSummary(c).rows.length),
            ]
          : h("span", { class: "chat-row-title truncate", title: c.title }, o || c.title),
        at && agoEl("span", "chat-row-time", at),
      ),
      h(
        "span",
        { class: "chat-row-line" },
        h("span", { class: "chat-row-preview truncate" }, preview),
        unread > 0 && h("span", { class: "unread-badge", "aria-label": unread + " unread" }, unread),
      ),
    ),
  );
}

function conversationPane(c) {
  const senders = new Set(c.messages.map(fromName));
  const o = otherIn(c);
  const of = o && findSession(o);
  const gs = c.kind !== "dm" ? groupSummary(c) : null;
  const op = o && findPerson(o);
  const about = gs
    ? [groupHost(c), gs.on + " active"].filter(Boolean).join(" · ")
    : o
      ? isPerson(o)
        ? op
          ? personOnline(op)
            ? "On " + NAME + " now"
            : deviceLabel(op.device)
          : ""
        : of
          ? of.session.doing || of.session.agent || ""
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
            ui.draftGroup = null;
            render();
          },
        },
        icon("arrowLeft"),
      ),
      o ? dotted(o) : conversationIcon(c),
      h(
        "div",
        { class: "chat-head-text" },
        h("h2", { class: "chat-title truncate", title: c.title }, gs ? groupName(c) : o || c.title),
        o && isPerson(o) && humanTag(),
        of && h("span", { class: "g-state g-state-" + sessionState(of.device, of.session) }, stateWord(of.device, of.session)),
        about && h("span", { class: "chat-about truncate", title: [...senders].join(", ") }, about),
      ),
      of && prFor(of.session) && prBadge(prFor(of.session), true, "pr-chat-" + of.session.name),
      c.kind !== "dm" && membersButton(c),
    ),
    h("div", { class: "chat-thread", "data-conversation": c.key }, c.messages.length || pendingIn(c).length ? chatMessages(c) : chatIntro(o)),
    waitLine(c),
    paneFoot(c),
  );
}

// An empty conversation says who it is with, and what they are up to.
function chatIntro(name) {
  if (!name) return null;
  const f = findSession(name);
  const details = f ? [f.session.agent, modelOf(f.session), f.session.branch].filter(Boolean).join(" · ") : isPerson(name) ? "Person" : "";
  return h(
    "div",
    { class: "chat-intro" },
    h("div", { class: "chat-intro-face" }, avatar(name)),
    h("h3", null, name),
    f?.session.doing && h("p", { class: "chat-intro-doing" }, f.session.doing),
    details && h("p", { class: "chat-intro-meta" }, details),
  );
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
    out.push(chatMessage(m, from, at, head));
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

// A message's text: a person's as they wrote it, an agent's with its subject over its body.
function messageText(m) {
  if (m.fromPerson) return h("div", { class: "chat-msg-body rich" }, rich(personText(m)));
  return [m.subject && h("div", { class: "chat-msg-subject" }, m.subject), m.body && h("div", { class: "chat-msg-body rich" }, rich(m.body))];
}

function pendingMessage(item, head) {
  const failed = item.status === "failed";
  return h(
    "article",
    { class: "chat-msg chat-msg-own" + (head ? " chat-msg-first" : "") + (failed ? " chat-msg-failed" : " chat-msg-sending"), "data-cid": item.cid },
    h("div", { class: "chat-bubble rich" }, rich(item.text)),
    failed
      ? h(
          "div",
          { class: "msg-failed" },
          h("span", { class: "msg-failed-why", "data-announce": /^not sent/i.test(item.error) ? item.error : "Not sent: " + item.error }, item.error),
          h("button", { class: "btn btn-ghost btn-xs", "data-key": "retry-" + item.cid, onclick: () => deliver(item) }, icon("retry"), "Try again"),
          h("button", { class: "btn btn-ghost btn-xs", "data-key": "edit-" + item.cid, onclick: () => editFailed(item) }, icon("pencil"), "Edit"),
        )
      : item.status === "sending" && h("div", { class: "chat-msg-meta msg-sending" }, "Sending…"),
  );
}

function dayLabel(t) {
  const today = new Date();
  if (new Date(t).toDateString() === today.toDateString()) return "Today";
  today.setDate(today.getDate() - 1);
  if (new Date(t).toDateString() === today.toDateString()) return "Yesterday";
  return new Date(t).toLocaleDateString(undefined, { year: "numeric", month: "long", day: "numeric" });
}

function clockTime(at) {
  return at ? new Date(at).toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" }) : "";
}

// Says Copied only once the clipboard has it: writing can fail, as when the page isn't focused.
function copyText(text) {
  const w = navigator.clipboard?.writeText(text);
  if (!w) return announce("Couldn't copy.");
  w.then(
    () => announce("Copied."),
    () => announce("Couldn't copy."),
  );
}

function copyButton(text) {
  return h(
    "button",
    {
      class: "btn btn-ghost btn-icon-xs",
      title: "Copy",
      "aria-label": "Copy message",
      onclick: (e) => {
        copyText(text);
        const b = e.currentTarget;
        b.replaceChildren(icon("check"));
        b.classList.add("copied");
        setTimeout(() => (b.replaceChildren(icon("copy")), b.classList.remove("copied")), 1200);
      },
    },
    icon("copy"),
  );
}

function chatMessage(m, from, at, head) {
  const sender = findSession(from);
  const mine = from === myName();
  const copy = m.fromPerson ? personText(m) : (m.subject ? m.subject + "\n\n" : "") + (m.body || "");
  const time = h("span", { class: "chat-msg-time", title: messageDetail(m) }, clockTime(at));
  if (mine) {
    return h(
      "article",
      { class: "chat-msg chat-msg-own" + (head ? " chat-msg-first" : ""), "data-id": m.id },
      h("div", { class: "chat-bubble rich" }, rich(personText(m))),
      receiptLine(m),
      h("div", { class: "chat-msg-meta" }, time, copyButton(copy)),
    );
  }
  // Everyone else's message has its sender's face beside it, and a run of messages from one sender
  // shows the face once.
  const via = [sender?.session.agent, sender && !sender.device.self ? host(sender.device.id) : null].filter(Boolean).join(" · ");
  return h(
    "article",
    { class: "chat-msg chat-msg-face" + (head ? " chat-msg-first" : ""), "data-id": m.id, title: head ? null : messageDetail(m) },
    head ? avatar(from) : h("span", { class: "avatar-gap" }),
    h(
      "div",
      { class: "chat-msg-main" },
      head && h("div", { class: "chat-msg-head" }, h("span", { class: "chat-msg-from truncate" }, from), m.fromPerson && humanTag(), via && h("span", { class: "chat-msg-via truncate" }, via), time),
      messageText(m),
    ),
    h("div", { class: "msg-tools" }, copyButton(copy)),
  );
}

// How far one of the person's messages got (#26), from the marks its recipients' devices send back
// (docs/VISION.md, "Receipts"): one recipient's state, or, for several, how many have read it, or
// failing that how many it reached. Hovering lists each one; a tap opens the list under it. A
// message from before receipts, or past the newest 200, shows nothing rather than a guess.
function receiptWord(r) {
  switch (r.state) {
    case "read":
      return "Read";
    case "delivered":
      return "Delivered";
    case "handed":
      return "Handed over";
    case "queued":
      return "Queued";
    case "none":
      return "No receipt";
  }
  return "Sent";
}

// The words are VISION's (#325): delivered is knowing a message waits, read is having it.
function receiptDetail(r) {
  const t = when(r.at) ? " at " + clockTime(r.at) : "";
  switch (r.state) {
    case "read":
      return r.name + (r.person ? " opened it" : " has it") + t;
    case "delivered":
      return (r.person ? "Unread in " + r.name + "'s inbox" : r.name + " was told it is waiting") + t;
    case "handed":
      return "Given to " + r.name + " as a turn" + t + ". " + NAME + " can't see the turn start";
    case "queued":
      return "Waiting in " + r.name + "'s inbox" + t;
    case "none":
      return r.name + "'s " + NAME + " is older, and sends no receipts";
  }
  return "Not on " + r.name + "'s device yet";
}

function receiptLine(m) {
  const rs = ui.state?.receipts?.[m.id];
  if (!rs?.length) return null;
  let text;
  if (rs.length === 1) {
    text = receiptWord(rs[0]);
  } else {
    const n = rs.length;
    const read = rs.filter((r) => r.state === "read").length;
    const reached = rs.filter((r) => r.state && r.state !== "none").length;
    const none = rs.filter((r) => r.state === "none").length;
    text = read ? "Read by " + read + " of " + n : reached ? "Reached " + reached + " of " + n : "Sent";
    if (none) text += " · " + none + " no receipt";
  }
  const open = ui.openReceipts.has(m.id);
  const tip = rs.length === 1 ? receiptDetail(rs[0]) : rs.map((r) => r.name + ": " + receiptWord(r).toLowerCase()).join("\n");
  return h(
    "div",
    { class: "receipt" },
    h(
      "button",
      {
        class: "receipt-sum",
        "data-state": rs.length === 1 ? rs[0].state || "sent" : null,
        "data-key": "receipt-" + m.id,
        "data-tip": tip,
        "aria-expanded": String(open),
        onclick: () => {
          open ? ui.openReceipts.delete(m.id) : ui.openReceipts.add(m.id);
          render();
        },
      },
      text,
    ),
    open &&
      h(
        "ul",
        { class: "receipt-list" },
        rs.map((r) => h("li", { "data-state": r.state || "sent" }, rs.length > 1 && h("span", { class: "receipt-name truncate" }, r.name), h("span", { class: "receipt-detail" }, rs.length > 1 ? receiptWord(r) : receiptDetail(r)))),
      ),
  );
}

// Message bodies are Markdown-ish text from agents. A small subset becomes structure: paragraphs,
// lists, fenced code, inline code, bold and https links. All of it reaches the page as text nodes.
function rich(text) {
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
      out.push(h(re === bullet ? "ul" : "ol", null, items.map((t) => h("li", null, inline(t)))));
      continue;
    }
    if (!line.trim()) {
      i++;
      continue;
    }
    const para = [];
    while (i < lines.length && lines[i].trim() && !/^\s*```/.test(lines[i]) && !bullet.test(lines[i]) && !num.test(lines[i])) para.push(lines[i++]);
    out.push(h("p", null, para.map((l, k) => [k ? h("br") : null, inline(l)])));
  }
  return out;
}

function inline(t) {
  const out = [];
  const re = /(`[^`]+`)|(\*\*[^*]+\*\*)|(https:\/\/[^\s)<>]+[^\s)<>.,;:!?'"])/g;
  let last = 0;
  for (let m; (m = re.exec(t)); ) {
    if (m.index > last) out.push(t.slice(last, m.index));
    if (m[1]) out.push(h("code", null, m[1].slice(1, -1)));
    else if (m[2]) out.push(h("strong", null, m[2].slice(2, -2)));
    else out.push(h("a", { href: m[3], target: "_blank", rel: "noopener noreferrer" }, m[3]));
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

function isMyFilter() {
  return !!ui.filter && !!me() && ui.filter === me().addr && ui.view === "messages";
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
  if (!me()) return askForName(openYours);
  ui.filter = me().addr;
  ui.conversation = null;
  ui.draftTo = null;
  ui.draftGroup = null;
  go("messages");
  setView("messages");
}

// Start a group chat with these sessions and open it, ready to write in (#517, #518). A group is a
// tag, so starting one tags them; the group shows at once, its members as waiting until their
// devices have the tag. With no tag given, a group that is just these sessions is opened if there is
// one, and otherwise a new one is named for them.
function startGroup(sessions, tag) {
  if (!ui.state || !sessions.length) return;
  if (!me()) return askForName(() => startGroup(sessions, tag));
  tag = tag || groupOf(sessions) || newGroupTag(sessions.map((s) => s.name));
  ui.picker = null;
  ui.filter = me().addr;
  ui.draftTo = null;
  ui.draftGroup = tag;
  ui.conversation = groupConversation(tag).key;
  go("messages");
  setView("messages");
  focusComposer();
  tagSessions(sessions, tag, true);
}

// The group whose members are exactly these sessions, if there is one.
function groupOf(sessions) {
  const want = new Set(sessions.map((s) => s.addr));
  return liveGroups().find((g) => {
    const members = devices().flatMap((d) => d.sessions.filter((s) => inGroup(s, g)));
    return members.length === want.size && members.every((s) => want.has(s.addr));
  });
}

// A group's tag named for its members, by their whole names while those fit in a tag and by their
// first words after that, numbered if a group already has the name.
function newGroupTag(names) {
  const max = 40 - "group:".length;
  const fit = (b, n) => b.toLowerCase().replace(/[^a-z0-9._:-]/g, "-").slice(0, n).replace(/-+$/, "") || "chat";
  let base = names.join("-");
  if (base.length > max) base = names.map((n) => n.split("-")[0]).join("-");
  const taken = new Set(liveGroups());
  let tag = "group:" + fit(base, max);
  for (let i = 2; taken.has(tag); i++) tag = "group:" + fit(base, max - 1 - String(i).length) + "-" + i;
  return tag;
}

// Open the conversation with someone, whether or not it has any messages yet.
function startChat(name) {
  if (!ui.state) return;
  if (!me()) return askForName(() => startChat(name));
  if (name === myName()) return openYours();
  ui.picker = null;
  ui.filter = me().addr;
  ui.draftTo = name;
  ui.draftGroup = null;
  ui.conversation = dmKey(myName(), name);
  go("messages");
  setView("messages"); // the hash changes later, and the composer has to be there to focus now
  focusComposer();
}

function askForName(then) {
  const a = ui.state?.account;
  ui.setup = { name: (a?.taken && a.name) || a?.suggested || "", error: "", busy: false, then, from: document.activeElement?.dataset?.key || null };
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
    const was = myName();
    const account = await api("/api/account", { method: "PUT", body: JSON.stringify({ name: setup.name.trim() }) });
    ui.state.account = { ...ui.state.account, ...account };
    moveToName(was, account.name); // asked for a new name, they may have drafts under the old one
    ui.setup = null;
    poll();
    setup.then?.();
    render();
    refocus(setup.from);
  } catch (e) {
    if (e instanceof AuthError) return deny(e.token);
    setup.busy = false;
    setup.error = e.message;
    render();
    root.querySelector('[data-key="setup-name"]')?.focus();
  }
}

// A dialog that closes gives the focus back to what opened it, unless what it did moved it on.
function refocus(key) {
  if (key && !root.contains(document.activeElement)) root.querySelector(`[data-key="${CSS.escape(key)}"]`)?.focus();
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
    refocus(s.from);
  };
  return dialog(
    taken ? "Choose a new name" : "Choose your name",
    close,
    h("h2", { class: "dialog-title" }, taken ? "Choose a new name" : "Choose your name"),
    h(
      "p",
      { class: "dialog-line" },
      taken
        ? "The person on " + taken + " chose " + me().name + " first, so messages to that name go to them. Pick another; what was sent to you before still reaches you."
        : "Agents see it, and send to it.",
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
      h(
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
            autocomplete: "nickname",
            autocapitalize: "none",
            spellcheck: "false",
            "aria-label": "Your name",
            "aria-invalid": s.error ? "true" : null,
            "aria-describedby": s.error ? "setup-error" : null,
            "data-key": "setup-name",
            oninput: (e) => {
              s.name = e.target.value.toLowerCase().replace(/[\s_]+/g, "-");
              s.error = "";
              if (e.target.value !== s.name) e.target.value = s.name;
              root.querySelector(".setup-face")?.replaceChildren(faceOf(s.name));
              root.querySelector(".dialog-error")?.remove();
              root.querySelector(".setup-field")?.classList.remove("field-bad");
              e.target.removeAttribute("aria-invalid");
              e.target.removeAttribute("aria-describedby");
            },
          }),
        ),
      ),
      s.error && h("p", { class: "dialog-error", id: "setup-error", role: "alert" }, s.error),
      h(
        "div",
        { class: "dialog-actions" },
        h("button", { type: "button", class: "btn btn-ghost", "data-key": "setup-cancel", onclick: close }, "Cancel"),
        h("button", { type: "submit", class: "btn btn-primary", "data-key": "setup-continue", disabled: s.busy }, s.busy ? "Saving…" : "Continue"),
      ),
    ),
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
    if (p.name !== myName()) rows.push({ name: p.name, d: p.device, person: p, line: personOnline(p) ? "On " + NAME + " now" : "Person", rank: personOnline(p) ? 0 : 1, at: when(p.lastActive) || 0 });
  }
  for (const d of devices()) {
    for (const s of d.sessions) {
      if (isHidden(s)) continue;
      const st = sessionState(d, s);
      rows.push({ name: s.name, d, s, line: s.doing || s.agent || "", rank: st === "on" ? 0 : st === "idle" ? 1 : st === "dnd" ? 2 : 3, at: when(s.lastActive) || when(s.started) || 0 });
    }
  }
  return rows.sort((a, b) => a.rank - b.rank || b.at - a.at);
}

function matches(r, q) {
  if (!q) return true;
  return [r.name, r.line, r.s?.branch, r.s?.agent, r.d.nick, r.d.owner, r.d.id].some((x) => (x || "").toLowerCase().includes(q));
}

// In the new-message list a row is an option of its combobox, picked with the arrow keys while the
// focus stays in the field; elsewhere it is a plain button. Making a group chat, picked is whether
// the row is ticked, which is what a list that picks several marks as selected.
function personRow(r, cur, onclick, option, picked = null) {
  const several = picked !== null;
  return h(
    "button",
    option
      ? { class: "pick-row" + (cur ? " current" : ""), role: "option", id: "pick-" + r.name, tabindex: -1, "aria-selected": (several ? picked : cur) ? "true" : "false", "data-pick": r.name, onclick }
      : { class: "pick-row", "data-pick": r.name, "data-key": "start-" + r.name, onclick },
    several && h("span", { class: "g-pick" + (picked ? " on" : ""), "aria-hidden": "true" }, picked && icon("check")),
    presence(r.name, r.d),
    h(
      "span",
      { class: "pick-text" },
      h("span", { class: "name-line" }, h("span", { class: "pick-name truncate" }, r.name), r.person && humanTag()),
      h("span", { class: "pick-line truncate" }, r.line),
    ),
    h("span", { class: "pick-where truncate" }, r.person ? "" : r.d.self ? "This device" : r.d.nick || r.d.owner || host(r.d.id)),
  );
}

function newMessageButton() {
  return h("button", { class: "icon-btn", title: "New message", "aria-label": "New message", "data-key": "new-message", onclick: () => openPicker() }, icon("compose"));
}

function newGroupButton() {
  return h("button", { class: "icon-btn", title: "New group chat", "aria-label": "New group chat", "data-key": "new-group", onclick: () => openPicker(true) }, icon("users"));
}

// The new-message list, or with group set, the same list making a group chat (#518).
function openPicker(group) {
  if (!me()) return askForName(() => openPicker(group));
  ui.picker = { q: "", index: 0, from: document.activeElement?.dataset?.key || null, group: group ? newGroupDraft() : null };
  render();
  root.querySelector('[data-key="picker-q"]')?.focus();
}

// A group chat being made: the sessions picked, by address in the order picked, and the name typed
// for it, if any (without its group: prefix); until one is typed the field offers one.
function newGroupDraft() {
  return { picked: [], name: "", named: false, err: "" };
}

function pickerDialog() {
  const p = ui.picker;
  const g = p.group;
  const close = () => {
    ui.picker = null;
    render();
    if (p.from) root.querySelector(`[data-key="${CSS.escape(p.from)}"]`)?.focus();
  };
  const q = p.q.trim().toLowerCase();
  // A group's members are sessions: a group is a tag, and a person has none.
  const rows = reachable().filter((r) => (!g || r.s) && matches(r, q));
  p.index = Math.min(p.index, Math.max(rows.length - 1, 0));
  // A tick leaves the focus in the field, on the row ticked, so the keys go on from there.
  const toggle = (r) => {
    const i = g.picked.indexOf(r.s.addr);
    i < 0 ? g.picked.push(r.s.addr) : g.picked.splice(i, 1);
    g.err = "";
    p.index = Math.max(0, rows.indexOf(r));
    render();
    root.querySelector('[data-key="picker-q"]')?.focus();
  };
  const pick = (r) => (g ? toggle(r) : startChat(r.name));
  const mode = (group) => {
    p.group = group ? newGroupDraft() : null;
    p.q = "";
    p.index = 0;
    render();
    root.querySelector('[data-key="picker-q"]')?.focus();
  };
  return dialog(
    g ? "New group chat" : "New message",
    close,
    h(
      "label",
      { class: "pick-search" },
      icon("search"),
      h("input", {
        type: "text",
        value: p.q,
        placeholder: g ? "Add sessions to the group" : "Message a session or a person",
        autocomplete: "off",
        spellcheck: "false",
        role: "combobox",
        "aria-expanded": "true",
        "aria-controls": "pick-list",
        "aria-autocomplete": "list",
        "aria-activedescendant": rows[p.index] ? "pick-" + rows[p.index].name : null,
        "aria-label": g ? "Sessions to add" : "Who to message",
        "data-key": "picker-q",
        oninput: (e) => {
          p.q = e.target.value;
          p.index = 0;
          render();
        },
        onkeydown: (e) => {
          if (e.key === "ArrowDown" || e.key === "ArrowUp") {
            e.preventDefault();
            p.index = Math.max(0, Math.min(rows.length - 1, p.index + (e.key === "ArrowDown" ? 1 : -1)));
            render();
            root.querySelector(".pick-row.current")?.scrollIntoView({ block: "nearest" });
          } else if (e.key === "Enter" && g && (e.metaKey || e.ctrlKey)) {
            e.preventDefault();
            makeGroup();
          } else if (e.key === "Enter" && rows[p.index]) {
            e.preventDefault();
            pick(rows[p.index]);
          }
        },
      }),
    ),
    h(
      "div",
      { class: "pick-list", id: "pick-list", tabindex: -1, role: rows.length ? "listbox" : null, "aria-multiselectable": rows.length && g ? "true" : null, "aria-label": rows.length ? (g ? "Sessions" : "Sessions and people") : null },
      rows.length
        ? rows.map((r, i) => personRow(r, i === p.index, () => pick(r), true, g ? g.picked.includes(r.s.addr) : null))
        : h("p", { class: "pick-none", "data-announce": "" }, g && !q ? "No sessions are running." : "Nobody matches."),
    ),
    g ? groupFoot(g, () => mode(false), makeGroup) : h("div", { class: "pick-foot" }, h("button", { class: "btn btn-ghost btn-sm", "data-key": "pick-group", onclick: () => mode(true) }, icon("users"), "New group chat")),
  );

  function makeGroup() {
    const sessions = g.picked.map(sessionByAddr).filter(Boolean);
    if (!sessions.length) {
      g.err = "Pick at least one session.";
      return render();
    }
    if (!g.name.trim()) return startGroup(sessions); // the group that is just them, or a new one
    const tag = "group:" + g.name.trim();
    if (!validTag(tag) || !isGroupTag(tag)) {
      g.err = "A name is lowercase letters, digits and . _ : -, up to " + (40 - 6) + ".";
      return render();
    }
    startGroup(sessions, tag);
  }
}

// Under the list while it makes a group: the group's name, and the way back to one person.
function groupFoot(g, back, make) {
  const sessions = g.picked.map(sessionByAddr).filter(Boolean);
  const same = sessions.length ? groupOf(sessions) : null;
  const offered = same ? same.slice(6) : sessions.length ? newGroupTag(sessions.map((s) => s.name)).slice(6) : "";
  const tag = "group:" + (g.name.trim() || offered);
  const note = g.err || (tag === same ? tag + " has just these sessions; it opens." : g.named && liveGroups().includes(tag) ? tag + " exists; these sessions join it." : "");
  return h(
    "form",
    {
      class: "pick-group",
      onsubmit: (e) => {
        e.preventDefault();
        make();
      },
    },
    h(
      "label",
      { class: "field pick-group-name" + (g.err ? " field-bad" : "") },
      h("span", { class: "pick-group-prefix", "aria-hidden": "true" }, "group:"),
      h("input", {
        type: "text",
        value: g.named ? g.name : offered,
        placeholder: offered || "name",
        maxlength: 34,
        autocomplete: "off",
        autocapitalize: "none",
        spellcheck: "false",
        "aria-label": "Group name, after group:",
        "aria-invalid": g.err ? "true" : null,
        "data-key": "group-name",
        oninput: (e) => {
          const el = e.target;
          g.name = el.value.toLowerCase().replace(/\s+/g, "-");
          g.named = true;
          g.err = "";
          if (el.value !== g.name) {
            const at = el.selectionStart;
            el.value = g.name;
            el.setSelectionRange(at, at);
          }
          render();
        },
      }),
    ),
    note && h("p", { class: g.err ? "dialog-error" : "pick-group-note", "data-announce": "" }, note),
    h(
      "div",
      { class: "pick-group-actions" },
      h("button", { type: "button", class: "btn btn-ghost btn-sm", "data-key": "pick-back", onclick: back }, "Message one instead"),
      h("button", { type: "submit", class: "btn btn-primary btn-sm", "data-key": "pick-make", disabled: !sessions.length }, sessions.length ? "Start group chat with " + plural(sessions.length, "session") : "Start group chat"),
    ),
  );
}

// With no conversation open in your messages: who you could talk to.
function startPane() {
  const all = reachable();
  const rows = all.slice(0, 8);
  return h(
    "section",
    { class: "chat-pane start-pane", "aria-label": "Start a conversation" },
    h(
      "div",
      { class: "start-inner" },
      h("h2", { class: "start-title" }, rows.length ? "Who do you want to talk to?" : "Nobody is here yet."),
      rows.length > 0 && h("div", { class: "pick-list start-list" }, rows.map((r) => personRow(r, false, () => startChat(r.name)))),
      (all.length > rows.length || rows.length > 1) &&
        h(
          "div",
          { class: "start-more" },
          all.length > rows.length && h("button", { class: "btn btn-ghost", "data-key": "start-more", onclick: () => openPicker() }, "See everyone"),
          rows.length > 1 && h("button", { class: "btn btn-ghost", "data-key": "start-group", onclick: () => openPicker(true) }, icon("users"), "New group chat"),
        ),
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
  if (!me()) return null;
  if (c.kind === "all") return { to: "*", label: "everyone", count: sessionCount() };
  if (c.kind === "group") return { to: c.title, label: c.title, count: groupMembers({ ...c, messages: [] }).length };
  if (c.kind === "device") {
    const d = (ui.state?.devices || []).find((x) => x.id === c.title);
    return { to: c.title, label: groupName(c), count: d ? d.sessions.length : 0 };
  }
  if (!c.pair.includes(myName())) return null;
  const other = c.pair.find((n) => n !== myName()) || myName();
  return { to: other, label: other, count: 1 };
}

// Under a conversation: somewhere to write, or, under one between two others, a way to either.
function paneFoot(c) {
  const target = targetOf(c);
  if (target) return composer(c, target);
  if (c.kind !== "dm") return h("div", { class: "pane-foot" }, h("div", { class: "pane-foot-card" }, h("span", { class: "pane-foot-note" }, "Choose a name to write to " + (c.kind === "all" ? "everyone" : groupName(c).replace(/^This device$/, "this device")) + "."), h("button", { class: "btn btn-outline btn-sm", "data-key": "foot-name", onclick: () => askForName(render) }, "Choose a name")));
  const running = c.pair.filter((n) => findSession(n) || findPerson(n));
  if (!running.length) return null;
  // Two running sessions can be put in a group chat with you: a group is a tag, which a person
  // can't have (#517).
  const both = c.pair.length === 2 && c.pair.every((n) => findSession(n)) ? c.pair.map((n) => findSession(n).session) : null;
  return h(
    "div",
    { class: "pane-foot" },
    h(
      "div",
      { class: "pane-foot-card" },
      h("span", { class: "pane-foot-note" }, "Only " + c.pair.join(" and ") + " write here."),
      both && h("button", { class: "btn btn-primary btn-sm", title: "Start a group chat with both", "data-key": "foot-both", onclick: () => startGroup(both) }, icon("users"), "Message both"),
      running.map((n) => h("button", { class: "btn btn-outline btn-sm", "data-key": "foot-" + n, onclick: () => startChat(n) }, icon("message"), "Message " + n)),
    ),
  );
}

function composer(c, target) {
  const key = c.key;
  const big = target.to === "*" || (c.kind === "device" && target.count > 1);
  const trySend = () => sendFrom(c, target, el, big);
  const el = composerFor(key, trySend, () => {
    if (ui.armed === key) {
      ui.armed = null;
      root.querySelector(".composer-warn")?.remove();
      root.querySelector(".composer-armed")?.classList.remove("composer-armed");
      const hint = root.querySelector(".composer-hint");
      if (hint?.lastChild) hint.lastChild.textContent = " to send";
    }
    const btn = root.querySelector(".composer-send");
    if (btn) btn.disabled = !el.value.trim();
  });
  el.placeholder = "Message " + target.label;
  el.setAttribute("aria-label", "Message " + target.label);
  const armed = ui.armed === key;
  const who =
    c.kind === "dm"
      ? [statusDot(target.to), h("span", { class: "truncate" }, target.to)]
      : [icon(c.kind === "all" ? "broadcast" : c.kind === "group" ? "tag" : "laptop"), h("span", { class: "truncate" }, c.kind === "all" ? "Everyone" : target.label), h("span", { class: "composer-chip-n" }, plural(target.count, "session"))];
  return h(
    "div",
    { class: "composer-wrap" },
    h(
      "div",
      { class: "composer" + (armed ? " composer-armed" : "") },
      armed && h("p", { class: "composer-warn", "data-announce": "" }, "This reaches " + plural(target.count, "session") + ". Send again to go ahead."),
      el,
      h(
        "div",
        { class: "composer-bar" },
        h("button", { class: "composer-chip", title: "Message someone else", "aria-label": "To " + (c.kind === "dm" ? target.to : (c.kind === "all" ? "Everyone" : target.label) + " " + plural(target.count, "session")) + ". Message someone else", "data-key": "composer-to", onclick: () => openPicker() }, ...who, icon("chevronDown", "composer-chip-chev")),
        !NARROW.matches && h("span", { class: "composer-hint", "aria-hidden": "true" }, h("kbd", null, "Enter"), armed ? " again to send" : " to send"),
        h("button", { class: "composer-send", title: "Send", "aria-label": "Send", disabled: !el.value.trim(), "data-key": "send-" + key, onclick: trySend }, icon("arrowUp")),
      ),
    ),
  );
}

function sendFrom(c, target, el, big) {
  const text = el.value.trim();
  if (!text) return;
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
}

async function deliver(item) {
  item.status = "sending";
  item.error = "";
  render();
  try {
    // A group just started here reaches its members only once they have its tag: the tag changes
    // still on their way go to the bus first, so every device reads them before the message.
    // A session whose add failed isn't in the group and won't get it (#535): the message goes to
    // those who are, and the page says who was left out; with nobody in the group, it isn't sent.
    let missed = [];
    if (isGroupTag(item.to)) {
      if (item.missed?.length) tagSessions(item.missed.map((f) => f.s), item.to, true); // Try again tries them again
      const joins = [...(groupJoins.get(item.to) || [])];
      await Promise.all([...tagQueues.values()]);
      const seen = new Set();
      for (const f of (await Promise.all(joins)).flat()) {
        const s = sessionByAddr(f.s.addr) || f.s;
        if (!seen.has(s.addr) && !inGroup(s, item.to)) missed.push({ s, msg: f.msg });
        seen.add(s.addr);
      }
      item.missed = missed;
      if (missed.length && !devices().some((d) => d.sessions.some((s) => inGroup(s, item.to)))) {
        throw new Error("not sent: couldn't add " + namesOf(missed) + " to " + item.to + ": " + missed[0].msg);
      }
    }
    const sent = await api("/api/send", { method: "POST", body: JSON.stringify({ to: item.to, text: item.text }) });
    item.id = sent.id;
    if (missed.length) ui.notes.set(item.key, { id: sent.id, text: "Couldn't add " + namesOf(missed) + " to " + item.to + ", so they didn't get this: " + missed[0].msg, warn: true });
    else if (sent.note) ui.notes.set(item.key, { id: sent.id, text: sent.note });
    else ui.notes.delete(item.key);
    item.status = "sent";
    announce(missed.length ? "Sent, but not to " + namesOf(missed) + "." : "Sent.");
    clearTimeout(timer);
    timer = setTimeout(poll, 100); // what was sent is among the messages by now
  } catch (e) {
    if (e instanceof AuthError) return deny(e.token);
    item.status = "failed";
    item.error = e.message.charAt(0).toUpperCase() + e.message.slice(1); // the daemon writes for agents, in lower case
  }
  render();
}

function namesOf(failed) {
  return [...new Set(failed.map((f) => f.s.name))].join(", ");
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
  if (target && c.kind === "group") {
    // Only the page's own note: the daemon's for a group lists members the page already shows.
    const note = ui.notes.get(c.key);
    const last = c.messages[c.messages.length - 1];
    return note?.warn && last && fromName(last) === myName() && last.id === note.id ? h("p", { class: "wait-line wait-warn" }, note.text) : null;
  }
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
  if (st === "dnd") return h("p", { class: "wait-line wait-warn" }, target.to + " is on do not disturb. Your message waits in its inbox.");
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

// Minutes being typed are saved once the typing pauses. A choice made meanwhile, such as Never,
// cancels them: they belong to the Within it replaced.
const minuteEdits = new Map(); // a control's key → its timer

function cancelMinutes(key) {
  clearTimeout(minuteEdits.get(key));
  minuteEdits.delete(key);
}

function wakeControl(key, wake, onChange, withDefault) {
  const def = ui.settings.defaultMinutes || 3;
  const choices = withDefault ? [["default", "Default"], ...WAKE_CHOICES] : WAKE_CHOICES;
  const current = wake ? wake.when : "default";
  const seg = h(
    "div",
    { class: "segmented", role: "group" },
    choices.map(([v, label]) =>
      h(
        "button",
        {
          "aria-pressed": current === v ? "true" : "false",
          "data-key": key + "-" + v,
          onclick: () => {
            if (v === current) return;
            cancelMinutes(key);
            delete ui.rowErrors[key];
            onChange(v === "default" ? null : { when: v, minutes: v === "within" ? wake?.minutes || 0 : 0 });
          },
        },
        label,
      ),
    ),
  );
  if (current !== "within") return [seg];
  const input = h("input", {
    type: "number",
    min: 1,
    max: 1440,
    step: 1,
    inputmode: "numeric",
    placeholder: String(def),
    "aria-label": "Minutes idle",
    "data-key": key + "-minutes",
    value: wake.minutes ? String(wake.minutes) : null,
    oninput: (e) => {
      cancelMinutes(key);
      const raw = e.target.value.trim();
      const timer = setTimeout(() => {
        minuteEdits.delete(key);
        const n = raw === "" ? 0 : Number(raw);
        if (!Number.isInteger(n) || n < 0 || n > 1440) {
          ui.rowErrors[key] = "Minutes must be a whole number from 1 to 1440.";
          render();
          return;
        }
        delete ui.rowErrors[key];
        onChange({ when: "within", minutes: n });
      }, 600);
      minuteEdits.set(key, timer);
    },
  });
  return [seg, h("span", { class: "field" }, input), h("span", { class: "field-suffix" }, "min")];
}

const BUSY_CHOICES = [
  ["subject", "Subject"],
  ["whole", "Whole message"],
];

function busyControl(key, busy, onChange, withDefault) {
  const choices = withDefault ? [["default", "Default"], ...BUSY_CHOICES] : BUSY_CHOICES;
  const current = busy || "default";
  return h(
    "div",
    { class: "segmented", role: "group" },
    choices.map(([v, label]) =>
      h(
        "button",
        {
          "aria-pressed": current === v ? "true" : "false",
          "data-key": key + "-" + v,
          onclick: () => {
            if (v !== current) onChange(v === "default" ? null : v);
          },
        },
        label,
      ),
    ),
  );
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

function saveSettings(next) {
  const seq = ++settingsSeq;
  ui.settings = next;
  render();
  queueSettings(async () => {
    try {
      const saved = await api("/api/settings", { method: "PUT", body: JSON.stringify(next) });
      confirmed = saved;
      if (seq === settingsSeq) {
        ui.settings = saved;
        ui.settingsError = "";
      }
    } catch (e) {
      if (e instanceof AuthError) return deny(e.token);
      if (seq === settingsSeq) {
        ui.settings = confirmed || ui.settings;
        ui.settingsError = e.message;
      }
    }
    render();
  });
}

function settingsRow(key, title, description, control, extraClass = "") {
  return h(
    "div",
    { class: "row" },
    h(
      "div",
      { class: "row-grid " + extraClass },
      h("div", null, h("div", { class: "row-title", id: "row-" + key }, title), description && h("p", { class: "row-description" }, description)),
      // Named by the row's title, so each of a column of Always, Within and Never says whose it is.
      h("div", { class: "row-control", role: control ? "group" : null, "aria-labelledby": control ? "row-" + key : null }, control),
    ),
    ui.rowErrors[key] && h("p", { class: "row-error", "data-announce": "" }, ui.rowErrors[key]),
  );
}

function accountSection() {
  const a = me();
  const value = ui.accountDraft ?? (a?.name || ui.state?.account?.suggested || "");
  return h(
    "section",
    { class: "section" },
    h("div", { class: "section-head" }, h("h2", { class: "section-title" }, "Your account")),
    h(
      "div",
      { class: "group" },
      settingsRow("account", "Your name", a?.taken ? "The person on " + a.taken + " chose this name first. Choose another; until you do, you can't send." : "Agents see it, and send to it.", [
        h(
          "span",
          { class: "field account-field" },
          h("input", {
            type: "text",
            value,
            maxlength: 32,
            autocomplete: "nickname",
            spellcheck: "false",
            "aria-label": "Your name",
            "aria-invalid": ui.rowErrors.account ? "true" : null,
            "data-key": "account-name",
            oninput: (e) => {
              ui.accountDraft = e.target.value;
            },
            onkeydown: (e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                saveAccount(e.target.value.trim());
              }
            },
          }),
        ),
        h("button", { class: "btn btn-outline btn-xs", "data-key": "account-save", onclick: (e) => saveAccount(e.currentTarget.parentElement.querySelector("input").value.trim()) }, a ? "Save" : "Choose"),
      ]),
      nickRow(),
    ),
  );
}

// This device's nickname (#533), as `mur nickname` sets it: a label teammates see beside the
// device's ID, and no address. This page calls its own device "This device", so the row says the
// nickname is what the others see.
function nickRow() {
  const acct = ui.state?.account;
  const nick = acct?.nick || "";
  const id = ui.state?.device || "";
  const value = ui.nickDraft ?? nick;
  const without = acct?.name ? "Without one, they see your name, " + acct.name + "." : "";
  return settingsRow("nick", "Device nickname", ["What teammates call this device, shown beside its ID" + (id ? ", " + id : "") + ". Only a label: nothing is sent to it.", without].filter(Boolean).join(" "), [
    h(
      "span",
      { class: "field account-field" },
      h("input", {
        type: "text",
        value,
        maxlength: 32,
        placeholder: "None",
        autocomplete: "off",
        autocapitalize: "none",
        spellcheck: "false",
        "aria-label": "Device nickname",
        "aria-invalid": ui.rowErrors.nick ? "true" : null,
        "data-key": "nick-name",
        oninput: (e) => {
          ui.nickDraft = e.target.value;
        },
        onkeydown: (e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            saveNick(e.target.value.trim());
          }
        },
      }),
    ),
    h("button", { class: "btn btn-outline btn-xs", "data-key": "nick-save", onclick: (e) => saveNick(e.currentTarget.parentElement.querySelector("input").value.trim()) }, "Save"),
    nick && h("button", { class: "btn btn-ghost btn-xs", "data-key": "nick-clear", onclick: () => saveNick("") }, "Clear"),
  ]);
}

async function saveNick(nick) {
  delete ui.rowErrors.nick;
  if (nick === (ui.state?.account?.nick || "")) {
    ui.nickDraft = null;
    return render(); // nothing to change
  }
  try {
    const account = await api("/api/nick", { method: "PUT", body: JSON.stringify({ nick }) });
    if (ui.state) ui.state.account = { ...ui.state.account, ...account, nick: account.nick || "" };
    ui.nickDraft = null;
    announce(nick ? "This device is now called " + nick + "." : "Nickname cleared.");
    poll();
  } catch (e) {
    if (e instanceof AuthError) return deny(e.token);
    ui.rowErrors.nick = e.message; // it starts with a nickname, ID or name, all lowercase
  }
  render();
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
  ui.restored = null;
}

async function saveAccount(name) {
  delete ui.rowErrors.account;
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
  render();
}

// Each session's own setting, folded under the device default so a device with many sessions
// doesn't fill the page. The fold says how many sessions there are and how many set their own.
function perSessionFold(key, rows, set) {
  ui.settingsOpen = ui.settingsOpen || new Set();
  if (!rows.length) return [];
  const open = ui.settingsOpen.has(key);
  const head = h(
    "button",
    {
      class: "row row-fold",
      "aria-expanded": open ? "true" : "false",
      "data-key": "fold-" + key,
      onclick: () => {
        open ? ui.settingsOpen.delete(key) : ui.settingsOpen.add(key);
        render();
      },
    },
    icon("chevronRight", "row-fold-chev"),
    h("span", { class: "row-title" }, "Each session"),
    h("span", { class: "row-fold-meta" }, plural(rows.length, "session") + (set ? " · " + set + " set" : "")),
  );
  if (open) for (const r of rows) r.classList.add("row-sub");
  return [head, ...(open ? rows : [])];
}

function themeSection() {
  return h(
    "section",
    { class: "section" },
    h("div", { class: "section-head" }, h("h2", { class: "section-title" }, "Appearance")),
    h(
      "div",
      { class: "group" },
      settingsRow(
        "theme",
        "Theme",
        null,
        h(
          "div",
          { class: "segmented", role: "radiogroup", "aria-label": "Theme" },
          THEMES.map(([id, label]) =>
            // A radio group: one Tab stop, the arrow keys move the choice (WCAG 4.1.2).
            h(
              "button",
              {
                role: "radio",
                "aria-checked": theme === id ? "true" : "false",
                tabindex: theme === id ? 0 : -1,
                "data-key": "theme-" + id,
                onclick: () => setTheme(id),
                onkeydown: (e) => {
                  const step = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }[e.key];
                  if (!step) return;
                  e.preventDefault();
                  const i = THEMES.findIndex(([t]) => t === theme);
                  const next = THEMES[(i + step + THEMES.length) % THEMES.length][0];
                  setTheme(next);
                  root.querySelector(`[data-key="theme-${next}"]`)?.focus();
                },
              },
              label,
            ),
          ),
        ),
      ),
    ),
  );
}

function settingsPage() {
  const sections = [ui.state ? accountSection() : null, themeSection()];
  if (ui.settings) {
    const set = ui.settings;
    const local = (ui.state?.devices || []).find((d) => d.self)?.sessions || [];
    const overrides = set.sessionWake || {};
    const wakeOwn = local.filter((s) => overrides[s.name]).length;
    const rows = [
      settingsRow(
        "wake",
        "Device default",
        "Whether a message from another session may wake an idle session. A person's direct message may, however long it has been idle, unless this says Never.",
        wakeControl("wake", set.wake, (w) => saveSettings({ ...ui.settings, wake: w || { when: "within" } }), false),
      ),
      ...perSessionFold("wake", [...local]
        .sort((a, b) => a.name.localeCompare(b.name))
        .map((s) =>
          settingsRow(
            "wake-" + s.name,
            [s.name, s.agent && h("span", { class: "badge badge-secondary" }, s.agent)],
            null,
            wakeControl(
              "wake-" + s.name,
              overrides[s.name] || null,
              (w) => {
                // From the settings as they are now: a pause in typing minutes can end after
                // another choice was made.
                const next = { ...(ui.settings.sessionWake || {}) };
                if (w) next[s.name] = w;
                else delete next[s.name];
                saveSettings({ ...ui.settings, sessionWake: next });
              },
              true,
            ),
          ),
        ), wakeOwn),
    ];
    const busyOverrides = set.sessionBusy || {};
    const busyOwn = local.filter((s) => busyOverrides[s.name]).length;
    const busyRows = [
      settingsRow(
        "busy",
        "Device default",
        "What a busy session sees when a message arrives.",
        busyControl("busy", set.busy || "subject", (b) => saveSettings({ ...ui.settings, busy: b }), false),
      ),
      ...perSessionFold("busy", [...local]
        .sort((a, b) => a.name.localeCompare(b.name))
        .map((s) =>
          settingsRow(
            "busy-" + s.name,
            [s.name, s.agent && h("span", { class: "badge badge-secondary" }, s.agent)],
            null,
            busyControl(
              "busy-" + s.name,
              busyOverrides[s.name] || null,
              (b) => {
                const next = { ...(ui.settings.sessionBusy || {}) };
                if (b) next[s.name] = b;
                else delete next[s.name];
                saveSettings({ ...ui.settings, sessionBusy: next });
              },
              true,
            ),
          ),
        ), busyOwn),
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
        h("div", { class: "group" }, busyRows),
        ui.settingsError && h("p", { class: "row-error section-head", "data-announce": "" }, ui.settingsError),
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
  return h("div", { class: "page" }, sections);
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
  ui.drawer = false; // going somewhere from the phone's drawer closes it
  if (location.hash !== "#" + view) location.hash = view;
  else setView(view);
}

function setView(view) {
  ui.view = VIEWS.includes(view) ? view : "messages";
  if (ui.view === "settings") loadSettings();
  render();
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

// era is the tokenEra the poll started in: an answer that comes after the page has taken a token
// since is an old daemon's, and is dropped (#523).
async function fetchMessages(era) {
  for (let pages = 0; pages < 20; pages++) {
    const r = await api("/api/messages?cursor=" + encodeURIComponent(ui.cursor));
    if (era !== tokenEra) return;
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

// What sessions did, from the daemon, and what GitHub says of pull requests and issues. A daemon
// from before the feed has no such call, and a failure here leaves the page its messages.
async function fetchActivity(era) {
  try {
    for (let pages = 0; pages < 20; pages++) {
      const r = await api("/api/activity?cursor=" + encodeURIComponent(ui.actCursor));
      if (era !== tokenEra) return; // as in fetchMessages
      let added = 0;
      for (const a of r.activity || []) {
        learn(a.addr, a.who);
        if (ui.actIds.has(a.id)) continue;
        ui.actIds.add(a.id);
        a.t = Math.min(when(a.at) || Date.now(), Date.now());
        ui.acts.push(a);
        added++;
      }
      ui.github = r.github || [];
      const moved = r.cursor !== ui.actCursor;
      ui.actCursor = r.cursor || ui.actCursor;
      if (!added || !moved) break;
    }
    if (ui.acts.length > MAX_ACTS) {
      for (const a of ui.acts.splice(0, ui.acts.length - MAX_ACTS)) ui.actIds.delete(a.id);
    }
  } catch (e) {
    if (e instanceof AuthError) throw e;
  }
}
const MAX_ACTS = 1000;

let last = "";
let timer;
let inflight = false;
// tokenEra counts the tokens the page has taken. A poll compares it, not the token: a page given
// another token and then its first again, all within one answer, would otherwise take a poll from
// before both as current.
let tokenEra = 0;
let polled = false;
let loaded = false; // the messages have been fetched once, so what comes after is new
let loadedAt = 0; // when that first fetch came
let heldIds = null; // see poll
let scrolledAt = 0;
let stateAskedAt = 0; // when the state in hand was asked for

function hasUnsent() {
  return ui.outbox.length > 0 || [...composers.values()].some((el) => el.value.trim());
}

async function poll() {
  if (inflight) return;
  inflight = true;
  clearTimeout(timer);
  const wasConn = ui.conn;
  // The messages in hand before any not yet announced. A poll that brings messages but not the
  // state fails before it announces them, so this outlasts it, and the next poll still does.
  if (!heldIds) heldIds = new Set(ui.messages.map((m) => m.id));
  // A poll that the page took a new token during (start) belongs to the old daemon: it ends without
  // touching the page, and leaves inflight and the timer to the poll start began (#523).
  const era = tokenEra;
  try {
    const asked = Date.now();
    const [state] = await Promise.all([api("/api/state"), fetchMessages(era), fetchActivity(era)]);
    if (era !== tokenEra) return;
    const arrived = loaded ? ui.messages.filter((m) => !heldIds.has(m.id)) : []; // the first fetch brings the history
    heldIds = null;
    if (!loaded) loadedAt = Date.now();
    loaded = true;
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
    // Someone else claimed this name first (#268): ask for another, once per page, unless something
    // is already being asked.
    if (state.account?.taken && !ui.takenAsked && !ui.setup && !ui.picker && !ui.tagMenu) {
      ui.takenAsked = true;
      setTimeout(() => askForName(null), 0);
    }
    trimMessages();
    for (const id of ui.readLocal) if (!state.account?.unread?.includes(id)) ui.readLocal.delete(id);
    ui.outbox = ui.outbox.filter((item) => !(item.id && ui.ids.has(item.id)));
    ui.conn = "ok";
    announceArrivals(arrived);
    if (wasConn === "down") announce("Connected to the daemon again.");
  } catch (e) {
    if (era !== tokenEra) return;
    inflight = false;
    if (e instanceof AuthError) return deny(e.token);
    ui.conn = "down";
  }
  inflight = false;
  // Rebuild only when something changed, and leave alone a minutes field being typed in, text being
  // selected, and a pane being scrolled: a rebuild would drop the selection or stop the scroll.
  const sig = JSON.stringify([ui.state, ui.cursor, ui.actCursor, ui.github, ui.conn, ui.restarting > 0, restartStuck(), ui.outbox.length, Math.floor(Date.now() / 15000)]);
  // A composer keeps its own element, so what is being typed there survives a rebuild, unless an
  // input method is partway through a character. On a phone it wouldn't: refocusing it isn't a tap,
  // so the keyboard would drop. There the thread catches up when the composer loses focus.
  const inComposer = document.activeElement?.tagName === "TEXTAREA";
  const typing = document.activeElement?.tagName === "INPUT" || composing || (inComposer && NARROW.matches);
  const selecting = document.getSelection()?.isCollapsed === false;
  const scrolling = Date.now() - scrolledAt < 1000;
  if (sig !== last && !typing && !selecting && !scrolling) {
    last = sig;
    render();
  }
  // The first state starts the PR lookups; ask again soon, so their answers show without a wait.
  const first = !polled;
  polled = true;
  timer = setTimeout(poll, document.hidden ? HIDDEN_POLL_MS : first ? 1200 : POLL_MS);
}

// New messages are read out, politely: those to the person, and those in the conversation open in
// front of them. Agents talking among themselves anywhere else would be a flood, so they aren't.
// Nor is one in the open conversation that was sent before the page loaded: the daemon read it back
// from the bus late (#138), and it is history, not news. A minute is allowed for a sender's clock.
function announceArrivals(list) {
  if (document.hidden) return;
  const unread = new Set(ui.state?.account?.unread || []);
  const open = ui.view === "messages" && ui.conversation;
  const old = (m) => (when(m.at) || Infinity) < loadedAt - 60000;
  const say = list.filter((m) => fromName(m) !== myName() && (unread.has(m.id) || (open && conversationOf(m).key === ui.conversation && !old(m))));
  if (!say.length) return;
  const m = say[say.length - 1];
  announce(say.length === 1 ? "New message from " + fromName(m) + ": " + m.subject : say.length + " new messages, the newest from " + fromName(m) + ": " + m.subject);
}

// A fresh link pasted into a tab already showing the page changes only the fragment, so the
// page takes the new token here and starts over with it.
async function start() {
  await takeKey();
  const token = takeToken();
  if (token !== ui.token) {
    Object.assign(ui, { token, conn: "connecting", state: null, messages: [], cursor: "", settings: null, conversation: null, chatListTop: 0, draftTo: null, draftGroup: null, restored: null, outbox: [], setup: null, picker: null, newMark: null, armed: null, repoMenu: null });
    // Recent's feed is the old daemon's repo's too, and its cursor means nothing to the new one (#485).
    Object.assign(ui, { acts: [], actCursor: "", github: [], recentWho: "" });
    ui.actIds = new Set();
    ui.ids = new Set();
    ui.notes = new Map();
    ui.readLocal = new Set();
    composers.clear();
    personNames.clear();
    named.clear();
    for (const key of [...minuteEdits.keys()]) cancelMinutes(key);
    confirmed = null;
    arrivedAt = 0;
    heldIds = null;
    last = "";
    polled = false;
    loaded = false;
    // A poll of the old daemon's may still be waiting on it. It will drop what it brings, so the
    // new daemon is asked now rather than once that answer, or its failure, comes (#523).
    inflight = false;
    clearTimeout(timer);
    tokenEra++;
  }
  ui.view = VIEWS.includes(location.hash.slice(1)) ? location.hash.slice(1) : "messages";
  render();
  if (!ui.token) return;
  loadSettings(); // the wake-up settings say which of this device's sessions show red
  poll();
}

// Escape closes the tag menu wherever the focus is, even on the page's body.
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && ui.tagMenu && !e.defaultPrevented) {
    e.preventDefault(); // this Escape was the menu's; the phone's drawer under it stays open
    closeTagMenu();
  } else if (e.key === "Escape" && ui.repoMenu && !e.defaultPrevented) {
    e.preventDefault(); // the drawer under it stays open, as under the tag menu
    closeRepoMenu();
  }
});
window.addEventListener("hashchange", () => {
  if (location.hash.includes("token=") || location.hash.includes("key=")) start();
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
NARROW.addEventListener("change", () => {
  if (!NARROW.matches) ui.drawer = false;
  render();
});
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && !e.defaultPrevented && ui.drawer && !ui.setup && !ui.picker && !ui.tagMenu) setDrawer(false);
});
// Tooltips, drawn by the page rather than the browser: a browser's title waits about a second and
// can't be styled. The page's tip is only for what a person can't already read: a button that is
// only an icon, a state dot, a time (its tip has the full date), and "+N" more tags. Rows, messages,
// labelled buttons and badges keep the browser's title, which waits long enough not to get in the
// way. While the page's tip shows, the title moves to data-tip-title so the browser's own doesn't
// show too, and it comes back after, so assistive tech still reads it.
//
// The tip shows 150 ms after the pointer settles. Once one is showing, the next shows at once, so
// sliding along a row of buttons reads each. Keyboard focus shows it too. The tip lives outside
// #root, so a render doesn't take it away.
// While an element's title is borrowed, the element points at the tip with aria-describedby, so a
// screen reader still reads the title as its description.
// It is a tooltip only while it shows: an empty one would be a tooltip with no name (WCAG 4.1.2).
const tip = h("div", { class: "tip", id: "page-tip" });
document.body.append(tip);
let tipFor = null; // the element whose tip shows, or is about to
let tipTimer = 0;
let tipHiddenAt = 0;
let tipShown = false;
let pointerX = 0;

function tipText(el) {
  return el.dataset.tip || el.dataset.tipTitle || el.getAttribute("title") || "";
}

// Takes an element's title for the page's tip, and gives it back.
function borrowTitle(el) {
  const t = el.getAttribute("title");
  if (t) {
    el.dataset.tipTitle = t;
    el.removeAttribute("title");
  }
  tip.textContent = tipText(el);
  if (!el.hasAttribute("aria-describedby")) {
    el.setAttribute("aria-describedby", tip.id);
    el.dataset.tipDescribed = "1";
  }
}
function returnTitle(el) {
  if (el?.dataset.tipTitle) {
    el.setAttribute("title", el.dataset.tipTitle);
    delete el.dataset.tipTitle;
  }
  if (el?.dataset.tipDescribed) {
    el.removeAttribute("aria-describedby");
    delete el.dataset.tipDescribed;
  }
}

function showTip(el, atPointer) {
  clearTimeout(tipTimer);
  if (!el.isConnected) return hideTip();
  const text = tipText(el);
  if (!text) return hideTip();
  tip.textContent = text;
  tip.setAttribute("role", "tooltip");
  tip.classList.add("on");
  tipShown = true;
  const r = el.getBoundingClientRect();
  const t = tip.getBoundingClientRect();
  const mid = atPointer && r.width > 120 ? pointerX : r.left + r.width / 2;
  const x = Math.max(6, Math.min(mid - t.width / 2, window.innerWidth - t.width - 6));
  const above = r.top - t.height - 6 >= 6;
  tip.style.left = x + "px";
  tip.style.top = (above ? r.top - t.height - 6 : r.bottom + 6) + "px";
  tip.dataset.side = above ? "top" : "bottom";
}

function hideTip() {
  clearTimeout(tipTimer);
  if (tipShown) tipHiddenAt = Date.now();
  tipShown = false;
  returnTitle(tipFor);
  tipFor = null;
  tip.classList.remove("on");
  tip.removeAttribute("role");
}

function queueTip(el, atPointer) {
  if (el === tipFor) return;
  hideTip();
  tipFor = el;
  borrowTitle(el);
  if (Date.now() - tipHiddenAt < 400 || tipShown) return showTip(el, atPointer);
  tipTimer = setTimeout(() => showTip(el, atPointer), 150);
}

// Whether an element with a title gets the page's tip (see above).
const TIP_KINDS = ".pres, .status-dot, .dev-dot, .mini-dev, .dot, .chat-msg-time, [data-ago], .stag-more";
function wantsTip(el) {
  if (el.dataset.tip || el.matches(TIP_KINDS)) return true;
  // A button with no words on it: only an icon, or only a face (the folded sidebar's sessions).
  if (!el.matches("button, a")) return false;
  const faces = [...el.querySelectorAll(".avatar")].map((a) => a.textContent).join("");
  return el.textContent.trim() === faces.trim();
}

function tipTarget(node) {
  for (let el = node?.closest?.("[data-tip], [title], [data-tip-title]"); el; el = el.parentElement?.closest("[data-tip], [title], [data-tip-title]")) {
    if (wantsTip(el)) return el;
  }
  return null;
}

document.addEventListener("pointerover", (e) => {
  if (e.pointerType === "touch") return;
  pointerX = e.clientX;
  const el = tipTarget(e.target);
  el ? queueTip(el, true) : hideTip();
});
document.addEventListener("pointermove", (e) => (pointerX = e.clientX), { passive: true });
document.documentElement.addEventListener("pointerleave", hideTip);
document.addEventListener("focusin", (e) => {
  const el = tipTarget(e.target);
  if (el && e.target.matches(":focus-visible")) queueTip(el, false);
});
document.addEventListener("focusout", hideTip);
document.addEventListener("pointerdown", hideTip, true);
// Escape also cancels a tip that is still waiting to show.
document.addEventListener("keydown", (e) => e.key === "Escape" && tipFor && hideTip());
document.addEventListener("scroll", hideTip, { capture: true, passive: true });

setInterval(tick, 10000);
start();
