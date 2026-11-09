// The page. Plain DOM, no framework and no build step: the daemon embeds these files as they
// are. Everything from the bus is untrusted text, so it only ever reaches the page as text nodes.
import { icon } from "./icons.js";
import { demo } from "./demo.js";
import { brief } from "./brief.js";
import { swarm } from "./swarm.js";
import { makeBusy } from "./busy.js";

// #183: ?demo=busy grows the mock to the problem's scale, 4 people and 16 agents.
const BUSY = !new URLSearchParams(location.search).has("repo") && new URLSearchParams(location.search).get("demo") === "busy";
if (BUSY) makeBusy(demo, swarm);

const NAME = document.querySelector('meta[name="app-name"]').content;
// Draft for #179: "a" is today's page with sessions in the sidebar; "b" takes Slack's sidebar and
// search and Discord's member list; "c" takes WhatsApp's chat list and bubbles.
const VARIANT = document.documentElement.dataset.variant || "a";
// E and G share Discord's dark look; G lays it out as sessions | conversations | chat.
const DARKISH = VARIANT === "e" || VARIANT === "g";
const TOKEN_KEY = NAME + ":token";
const POLL_MS = 3000;
const HIDDEN_POLL_MS = 15000;
const MAX_MESSAGES = 2000;
const VIEWS = ["overview", "catchup", "mine", "decisions", "timeline", "board", "graph", "office", "briefing", "sessions", "messages", "recent", "fleet", "settings"];
// The ways to look at the swarm (#183), as tabs on one page.
// Fleet v1 (#183): the one swarm view left of the drafts.
const SWARM_VIEWS = [["fleet", "Fleet"]];
const TITLES = { sessions: "Sessions", messages: "Messages", recent: "Recent", fleet: "Fleet audit", briefing: "Briefing", overview: "Overview", catchup: "Since you looked", mine: "Your agents", decisions: "Decisions", timeline: "Timeline", board: "Board", graph: "Who talks to whom", office: "Office", settings: "Settings" };
const MODE_KEY = NAME + ":messages";
const NARROW = window.matchMedia("(max-width: 767px)"); // style.css's mobile width
// Below this width a rail beside Fleet's table would squeeze the table (to about 665px at 1440,
// where "Working on" gets 210px and a "Needs a look" row runs four lines), so a selected session
// takes the page instead. fleet.css stacks at the same width.

const ui = {
  view: "sessions",
  token: null,
  conn: "connecting", // connecting, ok, down, denied
  state: null,
  messages: [], // oldest first
  ids: new Set(),
  cursor: "",
  settings: null,
  settingsError: "",
  rowErrors: {},
  expanded: new Set(),
  filter: null, // a session name
  messageMode: localStorage.getItem(MODE_KEY) === "recent" ? "recent" : "conversations",
  conversation: null, // the open conversation's key
  conversationAnchor: null, // a message in it
  chatListTop: 0,
  muting: new Set(),
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
    const view = parts.find((p) => VIEWS.includes(p)) || "sessions";
    history.replaceState(null, "", location.pathname + location.search + "#" + view);
  }
  return sessionStorage.getItem(TOKEN_KEY) || "draft";
}

// Mock data unless the address names a real repo (?repo=NAME, through serve.py).
const DEMO = !new URLSearchParams(location.search).has("repo");
let demoSent = false;

async function api(path, init = {}) {
  if (DEMO) {
    if (path.startsWith("/api/state")) return structuredClone(demo.state);
    if (path.startsWith("/api/messages")) {
      const first = !demoSent;
      demoSent = true;
      return { messages: first ? structuredClone(demo.messages) : [], cursor: "demo" };
    }
    if (path.startsWith("/api/settings")) return init.body ? JSON.parse(init.body) : structuredClone(demo.settings);
    return null;
  }
  const token = ui.token;
  const headers = { Authorization: "Bearer " + token };
  if (init.body) headers["Content-Type"] = "application/json";
  // A tab nobody can see still polls, but mustn't keep the daemon running for good.
  if (document.hidden) headers["X-Page-Hidden"] = "1";
  const repo = new URLSearchParams(location.search).get("repo") || "t4";
  path += (path.includes("?") ? "&" : "?") + "repo=" + encodeURIComponent(repo);
  // A daemon that takes the request and never answers is as down as one that refuses it.
  const res = await fetch(path, { signal: AbortSignal.timeout(8000), ...init, headers, cache: "no-store" });
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

// How long since t, as a duration: "40m", "1h 59m", "2d 3h".
function lasted(t) {
  const m = Math.max(1, Math.round((Date.now() - t) / 60000));
  if (m < 60) return m + "m";
  const hrs = Math.floor(m / 60);
  if (hrs < 24) return hrs + "h" + (m % 60 ? " " + (m % 60) + "m" : "");
  return Math.floor(hrs / 24) + "d" + (hrs % 24 ? " " + (hrs % 24) + "h" : "");
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

// An element whose text is a relative time, kept current by the ticker. A bare one is a
// duration ("for 40m") rather than a time ("40m ago").
// A "just now" one reads "just now" rather than "now" in its first 45 seconds.
function agoEl(tag, cls, t, prefix = "", bare = false, justNow = false) {
  const text = (t) => (bare ? lasted(t) : justNow && ago(t) === "now" ? "just now" : ago(t));
  return h(tag, { class: cls, "data-ago": t, "data-prefix": prefix, "data-bare": bare ? "1" : null, "data-just": justNow ? "1" : null, title: stamp(t) }, prefix + text(t));
}

function tick() {
  for (const el of document.querySelectorAll("[data-ago]")) {
    const t = Number(el.dataset.ago);
    const v = el.dataset.bare ? lasted(t) : ago(t);
    el.textContent = el.dataset.prefix + (el.dataset.just && v === "now" ? "just now" : v);
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

function learn(addr, name) {
  if (addr && name && name !== addr) named.set(addr, name);
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
  const active = document.activeElement;
  const keep = active?.dataset?.key;
  const caret = active && typeof active.selectionStart === "number" ? [active.selectionStart, active.selectionEnd] : null;
  const scroll = content ? content.scrollTop : 0;
  // Fleet's details pane scrolls on its own and keeps its place.
  const paneScroll = root.querySelector(".fx-pane-in")?.scrollTop || 0;
  const chat = chatScroll();
  if (!content) content = h("div", { class: "content" });
  const inner = page(); // before the top bar, which counts what this settles
  content.className = "content" + (inner.classList.contains("chat-page") ? " content-chat" : "");
  content.replaceChildren(inner);
  root.replaceChildren(h("div", { class: "app" }, sidebar(), h("main", { class: "main" }, VARIANT === "g" ? null : topbar(), content)));
  content.scrollTop = scroll;
  fleetFit();
  const pane = root.querySelector(".fx-pane-in");
  if (pane) pane.scrollTop = paneScroll;
  restoreChatScroll(chat);
  const again = keep && root.querySelector(`[data-key="${CSS.escape(keep)}"]`);
  if (again) {
    again.focus({ preventScroll: true });
    if (caret && typeof again.setSelectionRange === "function") again.setSelectionRange(...caret);
  }
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

// Draft for #179: the sidebar is the sessions themselves, under their devices, with each device's
// count. Sessions and Messages stay at the top; Settings moves to the foot. The repo name opens a
// menu of this machine's other Murmur repos, since switching is rare.
// G: the sidebar folds to a strip of faces, and remembers it.
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
      top("sessions", "users", "All sessions", ui.view === "sessions"),
      top("messages", "message", "All conversations", ui.view === "messages" && !ui.filter),
      top("recent", "clock", "Recent messages", ui.view === "recent"),
      devices().map((d) => [
        h("div", { class: "mini-dev", title: (d.self ? "This device" : d.owner || d.id) + " · " + d.sessions.length }, deviceDot(d)),
        d.sessions.map((s) => {
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
      ]),
    ),
    h("button", { class: "mini-btn mini-foot" + (ui.view === "settings" ? " current" : ""), title: "Settings", "aria-label": "Settings", onclick: () => go("settings") }, icon("settings")),
  );
}

function sidebar() {
  if (VARIANT === "g" && mini) return miniSidebar();
  const devs = devices();
  return h(
    "aside",
    { class: "sidebar", "data-app-sidebar": true, "aria-label": NAME },
    h(
      "div",
      { class: "sidebar-header" },
      h(
        "button",
        { class: "repo-button", "aria-haspopup": "menu", "aria-expanded": ui.repoMenu ? "true" : "false", "data-key": "repo", onclick: (e) => { e.stopPropagation(); ui.repoMenu = !ui.repoMenu; render(); } },
        h("span", { class: "brand" }, NAME),
        h("span", { class: "repo-name truncate" }, currentRepo()),
        icon("chevronDown", "repo-chev"),
      ),
      VARIANT === "g" && h("button", { class: "fold-btn", title: "Hide the sidebar", "aria-label": "Hide the sidebar", "data-key": "fold", onclick: () => setMini(true) }, icon("panel")),
      ui.repoMenu && repoMenu(),
    ),
    h(
      "div",
      { class: "sidebar-content" },
      VARIANT === "g"
        ? h("div", { class: "sidebar-group g-top" }, topRow("sessions", "users", "All sessions", ui.state ? sessionCount() : null, ui.state ? fleetNeeds(fleetRows()).size : 0), allButton(), topRow("recent", "clock", "Recent messages", ui.messages.length))
        : h(
            "div",
            { class: "sidebar-group" },
            h(
              "ul",
              { class: "sidebar-menu" },
              navButton("sessions", "users", ui.state ? sessionCount() : null),
              navButton("messages", "message", ui.messages.length || null),
            ),
          ),
      devs.map((d) =>
        h(
          "div",
          { class: "sidebar-group dev-group" },
          h(
            "button",
            {
              class: "device-link" + (d.reachable ? "" : " stale"),
              title: d.reachable ? d.id : d.id + ", last seen " + ago(when(d.lastSeen) || 0),
              "data-key": "dev-" + d.id,
              onclick: () => {
                if (VARIANT !== "b" && VARIANT !== "f") return showDevice(d.id);
                ui.collapsed = ui.collapsed || new Set();
                ui.collapsed.has(d.id) ? ui.collapsed.delete(d.id) : ui.collapsed.add(d.id);
                render();
              },
            },
            VARIANT === "b" || VARIANT === "f" ? icon(ui.collapsed?.has(d.id) ? "chevronRight" : "chevronDown", "sec-caret") : icon("laptop"),
            VARIANT === "g" ? h("span", { class: "label" }, deviceDot(d), d.self ? "This device" : fleetDeviceLabel(d), d.self && h("span", { class: "dev-host" }, deviceName(d))) : h("span", { class: "label" }, d.id),
            d.self ? h("span", { class: "dev-you" }, "you") : !d.reachable && h("span", { class: "dev-you" }, "offline"),
            h("span", { class: "menu-count" }, d.sessions.length),
          ),
          d.sessions.length > 0 &&
            !((VARIANT === "b" || VARIANT === "f") && ui.collapsed?.has(d.id)) &&
            h(
              "ul",
              { class: "sidebar-menu side-sessions" },
              d.sessions.filter((s) => !ui.q || s.name.includes(ui.q.toLowerCase())).map((s) => {
                const key = s.addr || s.name;
                return h(
                  "li",
                  null,
                  h(
                    "button",
                    {
                      class: "side-session",
                      "aria-current": ui.view === "messages" && ui.filter === key ? "page" : null,
                      "data-key": "ss-" + s.name,
                      title: s.doing || s.name,
                      onclick: () => {
                        ui.filter = key;
                        ui.conversation = null;
                        go("messages");
                      },
                    },
                    ...sideSessionBody(d, s),
                  ),
                );
              }),
            ),
        ),
      ),
    ),
    h(
      "div",
      { class: "sidebar-foot" },
      h("ul", { class: "sidebar-menu" }, navButton("settings", "settings", null)),
      DEMO && VARIANT === "g" && h("div", { class: "demo-size fx" }, fxSeg("Demo size", [["small", "9 agents"], ["busy", "16 agents"]], BUSY ? "busy" : "small", (v) => {
        const q = new URLSearchParams(location.search);
        if (v === "busy") q.set("demo", "busy");
        else q.delete("demo");
        location.search = q.toString();
      })),
    ),
  );
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

// A face with its presence dot inside it, so it still fits the two-face pair of a DM.
function dotted(name) {
  const a = avatar(name);
  if (!DARKISH && VARIANT === "a") return a;
  const f = findSession(name);
  const st = f ? sessionState(f.device, f.session) : "off";
  a.classList.add("dotted");
  a.append(h("span", { class: "pres pres-" + st, title: name + ": " + STATE_TIPS[st] }));
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

function presence(name, d) {
  const f = findSession(name);
  const st = sessionState(f ? f.device : d, f ? f.session : null);
  return h("span", { class: "pres-wrap" }, avatar(name), h("span", { class: "pres pres-" + st, title: STATE_TIPS[st] }));
}

// The newest message a session sent or was sent, for WhatsApp's preview line.
function lastFor(name) {
  for (let i = ui.messages.length - 1; i >= 0; i--) {
    const m = ui.messages[i];
    if (fromName(m) === name || (m.toKind === "session" && toName(m) === name)) return m;
  }
  return null;
}

function sideSessionBody(d, s) {
  if (VARIANT === "b" || VARIANT === "f") return [presence(s.name, d), h("span", { class: "side-session-name truncate" }, s.name)];
  if (VARIANT === "g") return gSessionBody(d, s);
  if (VARIANT === "d" || VARIANT === "e")
    return [presence(s.name, d), h("span", { class: "side-session-text" }, h("span", { class: "side-session-name truncate" }, s.name), h("span", { class: "side-session-doing truncate" }, s.doing || s.agent || ""))];
  if (VARIANT === "c") {
    const m = lastFor(s.name);
    const at = m && when(m.at);
    return [
      presence(s.name, d),
      h(
        "span",
        { class: "side-session-text" },
        h("span", { class: "wa-line" }, h("span", { class: "side-session-name truncate" }, s.name), at && agoEl("span", "wa-time", at)),
        h("span", { class: "side-session-doing truncate" }, m ? (fromName(m) === s.name ? "" : fromName(m) + ": ") + m.subject : s.doing || "No messages yet"),
      ),
    ];
  }
  return [
    h("span", { class: "dot dot-sm" + (d.reachable ? " ok" : "") }),
    h("span", { class: "side-session-text" }, h("span", { class: "side-session-name truncate" }, s.name), s.doing && h("span", { class: "side-session-doing truncate" }, s.doing)),
  ];
}

// Discord's member list: every session, under its device, with the device's count.
function memberList() {
  return h(
    "aside",
    { class: "members", "aria-label": "Sessions" },
    devices().map((d) => [
      h("div", { class: "members-head" }, d.id + " — " + d.sessions.length),
      d.sessions.map((s) =>
        h(
          "button",
          {
            class: "member" + (d.reachable ? "" : " stale") + (ui.filter === (s.addr || s.name) ? " current" : ""),
            "data-key": "mem-" + s.name,
            onclick: () => {
              ui.filter = s.addr || s.name;
              ui.conversation = null;
              render();
            },
          },
          presence(s.name, d),
          h("span", { class: "member-text" }, h("span", { class: "member-name truncate" }, s.name), h("span", { class: "member-doing truncate" }, s.doing || s.agent || "")),
        ),
      ),
    ]),
  );
}

// E: the picked session leads the top bar, as a Discord channel's name and topic do.
function topSession() {
  const name = nameOf(ui.filter, ui.filter);
  const f = findSession(name);
  const prs = ui.state?.prs || {};
  const pr = f?.session.branch && Object.hasOwn(prs, f.session.branch) ? prs[f.session.branch] : null;
  return h(
    "div",
    { class: "top-session" },
    f ? presence(name, f.device) : avatar(name),
    h("h1", { class: "truncate" }, name),
    f?.session.doing && h("span", { class: "top-doing truncate", title: f.session.doing }, f.session.doing),
    f?.session.branch && h("span", { class: "top-chip", title: f.session.branch }, icon("branch"), h("span", { class: "truncate" }, f.session.branch)),
    pr && h("span", { class: "top-chip" }, prLink(pr)),
  );
}

function topRow(view, iconName, title, n, need) {
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
    // Waiting questions take the count's place, as unread mail does: the number to act on.
    need ? h("span", { class: "all-need", title: need + (need === 1 ? " needs" : " need") + " a look", "aria-label": need + (need === 1 ? " needs" : " need") + " a look" }, h("span", { class: "side-flag", "aria-hidden": "true" }), need) : n != null && h("span", { class: "all-n" }, n),
  );
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

function settledSessions() {
  const running = new Set();
  for (const d of ui.state?.devices || []) for (const s of d.sessions || []) running.add(s.name);
  const seen = new Map();
  const note = (name, addr, m) => {
    if (!name || running.has(name) || name.includes("/")) return;
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
  return !findSession(name) && settledSessions().some((r) => r.name === name);
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

// G: every conversation, the sidebar's first row.
function allButton() {
  const all = ui.view === "messages" && !ui.filter;
  const count = allConversations().length;
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

// The sidebar is for getting around: one line per session, its face and state and name, as in
// Slack's and Discord's lists; All sessions has the rest. One that owes an answer carries the yellow
// dot that marks it in the table.
function gSessionBody(d, s) {
  const owes = ui.state && fleetNeeds(fleetRows()).has(s.name);
  return [presence(s.name, d), h("span", { class: "side-session-name truncate" }, s.name), owes && h("span", { class: "side-flag", title: "Waiting on an answer from this session", "aria-label": "waiting on an answer" })];
}

// The sidebar's PR chip says "draft" in words, as the table's does (fxPr).
function gPr(pr) {
  return h("span", { class: "g-pr" + (pr.draft ? " draft" : ""), title: (pr.draft ? "Draft pull request #" : "Pull request #") + pr.number }, "#" + pr.number + (pr.draft ? " draft" : ""));
}

function stateWord(d, s) {
  const st = sessionState(d, s);
  const t = when(s.lastActive);
  if (st === "on") return "Active now";
  if (st === "idle") return t ? "Idle " + ago(t).replace(" ago", "") : "Idle";
  return STATE_WORDS[st];
}

function gSessionsPage() {
  if (!ui.state) return h("div", { class: "page" });
  const devs = devices();
  const total = sessionCount();
  return h(
    "div",
    { class: "g-page" },
    h(
      "header",
      { class: "g-page-head g-page-head-row" },
      h("div", null, h("h1", null, "All sessions"), h("p", null, plural(total, "session") + " running on " + plural(devs.length, "device"))),
      h(
        "div",
        { class: "g-tabs", role: "tablist" },
        [
          ["running", "Running", total],
          ["settled", "Settled", settledSessions().length],
        ].map(([id, label, n]) =>
          h(
            "button",
            {
              role: "tab",
              "aria-selected": (ui.sessTab || "running") === id ? "true" : "false",
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
    (ui.sessTab || "running") === "settled" ? settledPage(devs) : devs.map((d) =>
      h(
        "section",
        { class: "g-dev" },
        h(
          "h2",
          { class: "g-dev-head" },
          deviceDot(d),
          h("span", null, d.self ? "This device" : d.owner || d.id),
          h("span", { class: "g-dev-host" }, d.id.replace(/-[0-9a-f]{4}$/, "")),
          h("span", { class: "g-dev-n" }, d.reachable ? plural(d.sessions.length, "session") : "Offline · seen " + ago(when(d.lastSeen) || 0)),
        ),
        d.sessions.length === 0
          ? h("p", { class: "g-none" }, "No sessions")
          : h("div", { class: "g-cards" }, d.sessions.map((s) => sessionCard(d, s))),
      ),
    ),
  );
}

function settledPage(devs) {
  const q = (ui.sq || "").toLowerCase();
  const all = settledSessions().filter((r) => !q || r.name.includes(q) || (r.lastMsg?.subject || "").toLowerCase().includes(q));
  const byDev = new Map();
  for (const r of all) {
    if (!byDev.has(r.device)) byDev.set(r.device, []);
    byDev.get(r.device).push(r);
  }
  const label = (id) => {
    const d = devs.find((x) => x.id === id);
    return d ? (d.self ? "This device" : d.owner || id) : id || "Unknown device";
  };
  return [
    h("p", { class: "g-settled-note" }, "Sessions that have stopped, or that you settled in T3. Their messages are kept; open one to read them."),
    h(
      "label",
      { class: "g-search g-search-wide" },
      icon("search"),
      h("input", {
        type: "search",
        placeholder: "Search settled sessions",
        "data-key": "sq",
        value: ui.sq || null,
        oninput: (e) => {
          ui.sq = e.target.value;
          render();
        },
      }),
    ),
    [...byDev].map(([id, rows]) =>
      h(
        "section",
        { class: "g-dev" },
        h("h2", { class: "g-dev-head" }, h("span", null, label(id)), h("span", { class: "g-dev-host" }, id.replace(/-[0-9a-f]{4}$/, "")), h("span", { class: "g-dev-n" }, plural(rows.length, "settled session"))),
        h(
          "div",
          { class: "g-settled-list" },
          rows.map((r) =>
            h(
              "button",
              {
                class: "g-settled",
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
        ),
      ),
    ),
    all.length === 0 && h("p", { class: "g-none" }, q ? "Nothing matches." : "No settled sessions."),
  ];
}

function sessionCard(d, s) {
  const prs = ui.state.prs || {};
  const pr = s.branch && Object.hasOwn(prs, s.branch) ? prs[s.branch] : null;
  const keep = ui.filter;
  ui.filter = s.addr || s.name;
  const convs = conversations().filter((c) => c.kind === "dm").length;
  ui.filter = keep;
  const st = stateWord(d, s);
  return h(
    "button",
    {
      class: "g-card" + (d.reachable ? "" : " stale"),
      "data-key": "card-" + s.name,
      onclick: () => {
        ui.filter = s.addr || s.name;
        ui.conversation = null;
        go("messages");
      },
    },
    h(
      "span",
      { class: "g-card-top" },
      presence(s.name, d),
      h("span", { class: "g-card-id" }, h("span", { class: "g-card-name truncate" }, s.name), h("span", { class: "g-card-agent truncate" }, [s.agent, modelOf(s)].filter(Boolean).join(" · "))),
      st && h("span", { class: "g-state g-state-" + sessionState(d, s) }, st),
    ),
    h("span", { class: "g-card-doing" + (s.doing ? "" : " none") }, s.doing || "No status set"),
    h(
      "span",
      { class: "g-card-foot" },
      s.branch && h("span", { class: "g-branch truncate" }, icon("branch"), h("span", { class: "truncate" }, s.branch)),
      pr && gPr(pr),
      h("span", { class: "g-card-chats" }, plural(convs, "chat")),
    ),
  );
}

// ---------- Recent: every message, newest first ----------
function gRecentPage() {
  const list = [...ui.messages].sort((a, b) => b.sortAt - a.sortAt).slice(0, 300);
  const out = [];
  let day = null;
  for (const m of list) {
    const at = when(m.at);
    const d = at && new Date(at).toDateString();
    if (d && d !== day) {
      out.push(h("div", { class: "chat-day" }, h("span", null, dayLabel(at))));
      day = d;
    }
    out.push(recentItem(m, at));
  }
  return h(
    "div",
    { class: "g-page g-recent" },
    h("header", { class: "g-page-head" }, h("h1", null, "Recent messages"), h("p", null, plural(ui.messages.length, "message"))),
    list.length ? h("div", { class: "g-feed" }, out) : h("p", { class: "g-none" }, "No messages yet"),
  );
}

function recentItem(m, at) {
  const from = fromName(m);
  const c = conversationOf(m);
  const to = m.toKind === "all" ? [icon("broadcast"), "Everyone"] : m.toKind === "device" ? [icon("laptop"), m.to] : [avatar(toName(m)), toName(m)];
  const open = () => {
    ui.filter = null;
    ui.conversation = c.key;
    ui.conversationAnchor = m.id;
    go("messages");
  };
  return h(
    "article",
    { class: "chat-msg chat-msg-first g-item", "data-id": m.id, tabindex: 0, role: "button", "aria-label": "Open the conversation", onclick: open, onkeydown: (e) => (e.key === "Enter" ? open() : null) },
    avatar(from),
    h(
      "div",
      { class: "chat-msg-main" },
      h(
        "div",
        { class: "chat-msg-head" },
        h("span", { class: "chat-msg-from truncate" }, from),
        h("span", { class: "g-to" }, icon("arrowRight", "g-to-arrow"), h("span", { class: "g-to-who" }, to)),
        h("span", { class: "chat-msg-time", title: messageDetail(m) }, at && new Date(at).toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" })),
      ),
      m.subject && h("div", { class: "chat-msg-subject" }, m.subject),
      m.body && h("div", { class: "chat-msg-body rich" }, rich(m.body)),
    ),
  );
}

// ---------- who is in a group chat ----------
// Everyone reaches every session in the repo; a device's chat reaches every session on it. Anyone
// who has posted is in it too, running or not.
function groupMembers(c) {
  const posted = new Map();
  for (const m of c.messages) posted.set(fromName(m), (posted.get(fromName(m)) || 0) + 1);
  const rows = [];
  for (const d of devices()) {
    if (c.kind === "device" && d.id !== c.title) continue;
    for (const s of d.sessions) rows.push({ name: s.name, d, s, posted: posted.get(s.name) || 0 });
  }
  for (const [name, n] of posted) if (!rows.some((r) => r.name === name)) rows.push({ name, d: null, s: null, posted: n });
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
    const key = r.d ? (r.d.self ? "This device" : r.d.owner || r.d.id) : "Not running";
    if (!groups.has(key)) groups.set(key, { d: r.d, host: r.d ? r.d.id.replace(/-[0-9a-f]{4}$/, "") : "", rows: [] });
    groups.get(key).rows.push(r);
  }
  return h(
    "div",
    { class: "g-members-panel", role: "dialog", "aria-label": "Members", onclick: (e) => e.stopPropagation() },
    h("div", { class: "g-mp-head" }, c.kind === "all" ? "Everyone in this repo" : "Every session on " + c.title.replace(/-[0-9a-f]{4}$/, "")),
    [...groups].map(([title, g]) => [
      h("div", { class: "g-mp-sec" }, g.d && deviceDot(g.d), title, g.host && h("span", null, g.host), h("span", { class: "g-mp-n" }, g.rows.length)),
      g.rows.map((r) =>
        h(
          "button",
          {
            class: "g-mp-row" + (r.d && !r.d.reachable ? " stale" : ""),
            onclick: () => {
              ui.membersOpen = false;
              const f = findSession(r.name);
              ui.filter = f ? f.session.addr || r.name : r.name;
              ui.conversation = null;
              go("messages");
            },
          },
          r.d ? presence(r.name, r.d) : avatar(r.name),
          h("span", { class: "g-mp-text" }, h("span", { class: "g-mp-name truncate" }, r.name), h("span", { class: "g-mp-doing truncate" }, r.s ? r.s.doing || r.s.agent || "" : "Not running")),
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

let repos = null;
function currentRepo() {
  return new URLSearchParams(location.search).get("repo") || "murmur (demo)";
}
function repoMenu() {
  if (!repos) {
    repos = [];
    fetch("/repos").then((r) => r.json()).then((list) => { repos = list; render(); }).catch(() => {});
  }
  return h(
    "div",
    { class: "repo-menu", role: "menu", onclick: (e) => e.stopPropagation() },
    h("div", { class: "repo-menu-label" }, "Repos on this machine"),
    repos.map((r) =>
      h(
        "a",
        { class: "repo-item", role: "menuitem", href: "?repo=" + encodeURIComponent(r.name) + "#sessions", "aria-current": r.name === currentRepo() ? "true" : null },
        icon("folder"),
        h("span", { class: "truncate" }, r.name),
        r.name === currentRepo() && icon("check", "repo-check"),
      ),
    ),
  );
}
document.addEventListener("click", () => { if (ui.repoMenu) { ui.repoMenu = false; render(); } });

function topbar() {
  let meta = "";
  if (ui.conn === "down") {
    meta = h("span", { class: "device-state" }, h("span", { class: "dot warn" }), "Disconnected");
  } else if (ui.view === "sessions" && ui.state) {
    meta = plural(sessionCount(), "session") + " · " + plural(ui.state.devices.length, "device");
  } else if (ui.view === "messages") {
    meta =
      ui.messageMode === "conversations"
        ? plural(conversations().length, "conversation")
        : plural(visibleMessages().length, "message");
  }
  return h(
    "header",
    { class: "topbar" },
    h("span", { class: "topbar-brand brand" }, NAME),
    VARIANT === "e" && ui.view === "messages" && ui.filter ? topSession() : h("h1", null, TITLES[ui.view]),
    (VARIANT === "b" || VARIANT === "f") &&
      h(
        "label",
        { class: "top-search" },
        icon("search"),
        h("input", {
          type: "search",
          placeholder: "Search sessions",
          "data-key": "q",
          value: ui.q || null,
          oninput: (e) => {
            ui.q = e.target.value;
            render();
          },
        }),
      ),
    h(
      "div",
      { class: "mobile-nav segmented", role: "group", "aria-label": "Views" },
      VIEWS.map((v) =>
        h(
          "button",
          { "aria-pressed": ui.view === v ? "true" : "false", "data-key": "m-" + v, onclick: () => go(v) },
          TITLES[v],
        ),
      ),
    ),
    meta && h("div", { class: "topbar-meta" }, meta),
  );
}

function page() {
  // Fleet v1 (#183) replaces the sessions page, at Tyler's suggestion (2026-11-09).
  if (VARIANT === "g" && ui.view === "sessions") return swarmShell("fleet");
  if (VARIANT === "g" && ui.view === "recent") return gRecentPage();
  if (VARIANT === "g" && SWARM_VIEWS.some(([v]) => v === ui.view)) return swarmShell(ui.view);
  if (VARIANT === "g" && ui.view === "briefing") return briefPage();
  if (ui.view === "recent") return messagesPage();
  if (ui.view === "messages") return messagesPage();
  if (ui.view === "settings") return settingsPage();
  return sessionsPage();
}

// Sessions, grouped by device.

function sessionsPage() {
  if (!ui.state) return h("div", { class: "page" });
  const devs = devices();
  if (devs.length === 0) return h("div", { class: "blank" }, "No sessions");
  return h(
    "div",
    { class: "page" },
    devs.map((d) =>
      h(
        "section",
        { class: "section", id: "device-" + d.id },
        h(
          "div",
          { class: "device-head" },
          icon("laptop"),
          h("span", { class: "device-name truncate" }, d.id),
          d.owner && h("span", { class: "device-id truncate" }, d.owner),
          h("span", { class: "device-count" }, plural(d.sessions.length, "session")),
          d.self && h("span", { class: "badge badge-outline" }, "This device"),
          !d.reachable &&
            h(
              "span",
              { class: "device-state" },
              h("span", { class: "dot" }),
              when(d.lastSeen) ? agoEl("span", "", when(d.lastSeen), "Unreachable · seen ") : "Unreachable",
            ),
        ),
        h(
          "div",
          { class: "group" },
          d.sessions.length === 0
            ? h("div", { class: "row-empty" }, "No sessions")
            : d.sessions.map((s) => sessionRow(d, s)),
        ),
      ),
    ),
  );
}

function prLink(pr) {
  const draft = pr.draft;
  const label = "#" + pr.number;
  const props = {
    class: "pr " + (draft ? "pr-draft" : "pr-open"),
    title: (draft ? "Draft pull request " : "Open pull request ") + label,
  };
  const kids = [icon(draft ? "prDraft" : "pr"), draft ? "Draft " + label : label];
  if (!/^https:\/\//.test(pr.url || "")) return h("span", props, kids);
  return h("a", { ...props, href: pr.url, target: "_blank", rel: "noopener noreferrer", onclick: (e) => e.stopPropagation() }, kids);
}

function sessionRow(d, s) {
  const active = when(s.lastActive);
  const started = when(s.started);
  const committed = Array.isArray(s.committed) ? s.committed : [];
  // Own keys only: a branch named constructor or __proto__ would otherwise find Object's.
  const prs = ui.state.prs || {};
  const pr = s.branch && Object.hasOwn(prs, s.branch) ? prs[s.branch] : null;
  const open = () => {
    ui.filter = s.addr || s.name;
    ui.conversation = null;
    ui.chatListTop = 0;
    go("messages");
  };
  return h(
    "div",
    {
      class: "session" + (d.reachable ? "" : " unreachable"),
      tabindex: 0,
      role: "button",
      "data-key": "s-" + s.name,
      "aria-label": s.name + ", messages",
      onclick: open,
      onkeydown: (e) => {
        // Only keys aimed at the row: Enter on the PR link inside it opens the link.
        if (e.target !== e.currentTarget) return;
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          open();
        }
      },
    },
    h(
      "div",
      { class: "session-line" },
      h("span", { class: "dot" + (d.reachable ? " ok" : ""), title: d.reachable ? "Reachable" : "Unreachable" }),
      h("span", { class: "session-name truncate" }, s.name),
      s.agent && h("span", { class: "badge badge-secondary" }, s.agent),
      active && agoEl("span", "session-active", active, "active "),
    ),
    s.doing && h("div", { class: "session-doing truncate", title: s.doing }, s.doing),
    h(
      "div",
      { class: "session-meta" },
      s.branch &&
        h("span", { class: "meta-item", title: s.branch }, icon("branch"), h("span", { class: "truncate" }, s.branch)),
      pr && h("span", { class: "meta-item" }, prLink(pr)),
      // Shown only when short: a longer list would crowd the row, and `lookup` has it whole.
      committed.length > 0 &&
        committed.length <= 3 &&
        h("span", { class: "meta-item", title: "Committed to " + committed.join(", ") }, "committed to " + committed.join(", ")),
      s.worktree &&
        h("span", { class: "meta-item", title: "Worktree" }, icon("folder"), h("span", { class: "truncate" }, s.worktree)),
      started && agoEl("span", "meta-item tabular", started, "started "),
    ),
  );
}

function showDevice(id) {
  if (VARIANT === "g") return;
  if (ui.view !== "sessions") go("sessions");
  requestAnimationFrame(() => document.getElementById("device-" + id)?.scrollIntoView({ block: "start" }));
}

// Messages.

// A session's messages: those it sent, and those it was sent, to it, its device or everyone. The
// filter is the session's address, which stays put when its name lands after it was chosen. Once
// it has a name, any address now called by that name is the same conversation: one that came back
// under a new session ID keeps its name.
function visibleMessages() {
  const f = ui.filter;
  if (!f) return ui.messages;
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

function recipient(m) {
  if (m.toKind === "all") return h("span", { class: "msg-to" }, icon("broadcast"), "Everyone");
  if (m.toKind === "device") return h("span", { class: "msg-to", title: "Every session on " + m.to }, icon("laptop"), h("span", { class: "truncate" }, m.to));
  const to = toName(m);
  return h("span", { class: "msg-to", title: to }, h("span", { class: "truncate" }, to));
}

function sessionFocus() {
  if (!ui.filter) return null;
  const f = findSession(nameOf(ui.filter, ui.filter));
  if (!f) return null;
  return h("div", { class: "session-focus" }, h("div", { class: "group" }, sessionRow(f.device, f.session)));
}

function messagesBar() {
  if (DARKISH) return null;
  const shown = ui.filter && nameOf(ui.filter, ui.filter);
  const focus = sessionFocus();
  const bar = h(
    "div",
    { class: "filter-bar" },
    ui.filter &&
      !focus &&
      h(
        "span",
        { class: "chip" },
        shown,
        h("button", { "aria-label": "Show all messages", "data-key": "unfilter", onclick: () => ((ui.filter = null), render()) }, "×"),
      ),
    h(
      "div",
      { class: "segmented msg-modes", role: "group", "aria-label": "Message view" },
      [
        ["recent", "Recent"],
        ["conversations", "Conversations"],
      ].map(([mode, label]) =>
        h(
          "button",
          {
            "aria-pressed": ui.messageMode === mode ? "true" : "false",
            "data-key": "mode-" + mode,
            onclick: () => {
              ui.messageMode = mode;
              localStorage.setItem(MODE_KEY, mode);
              render();
            },
          },
          label,
        ),
      ),
    ),
  );
  return focus ? [focus, bar] : bar;
}

function messagesPage() {
  if (ui.messageMode === "conversations") return conversationsPage();
  const list = [...visibleMessages()].reverse();
  if (list.length === 0) {
    return h("div", { class: "page" }, messagesBar(), h("div", { class: "blank" }, noMessages()));
  }
  return h(
    "div",
    { class: "page" },
    messagesBar(),
    h(
      "div",
      { class: "group msg-list" },
      list.map((m) => messageRow(m)),
    ),
  );
}

function noMessages() {
  return ui.filter ? "No messages with " + nameOf(ui.filter, ui.filter) : "No messages";
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
  const list = [...byKey.values()];
  for (const c of list) c.messages.sort((a, b) => a.sortAt - b.sortAt);
  const latest = (c) => c.messages[c.messages.length - 1].sortAt;
  return list.sort((a, b) => latest(b) - latest(a));
}

// The same name always gets the same color. The classes are in style.css: the page's content
// policy doesn't allow an inline style.
function avatar(name) {
  let n = 0;
  for (const ch of name) n = (n * 31 + ch.codePointAt(0)) % 997;
  return h("span", { class: "avatar avatar-" + (n % 5), "aria-hidden": "true" }, (name[0] || "?").toUpperCase());
}

// G: a group chat is named for whose it is, and its face is its members'.
function groupName(c) {
  if (c.kind === "all") return "Everyone";
  const d = (ui.state?.devices || []).find((x) => x.id === c.title);
  if (!d) return c.title.replace(/-[0-9a-f]{4}$/, "");
  if (d.self) return "This device";
  const o = d.owner || "";
  return o ? o[0].toUpperCase() + o.slice(1) + "'s " + (devices().filter((x) => x.owner === d.owner).length > 1 ? d.id.replace(/-[0-9a-f]{4}$/, "") : "device") : d.id;
}

function groupHost(c) {
  return c.kind === "device" ? c.title.replace(/-[0-9a-f]{4}$/, "") : "";
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
  if (VARIANT === "g" && c.kind !== "dm") return groupIcon(c);
  if (c.kind === "all") return h("span", { class: "avatar" }, icon("broadcast"));
  if (c.kind === "device") return h("span", { class: "avatar" }, icon("laptop"));
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
  ui.conversationAnchor = open?.messages[open.messages.length - 1].id;
  return h(
    "div",
    { class: "page chat-page" },
    messagesBar(),
    list.length === 0
      ? h("div", { class: "blank" }, noMessages())
      : h(
          "div",
          { class: "chat" + (open ? " chat-open" : "") },
          VARIANT === "g"
            ? gList(list)
            : h(
                "nav",
                { class: "chat-list", "aria-label": "Conversations", onscroll: (e) => (ui.chatListTop = e.target.scrollTop) },
                list.map(conversationRow),
              ),
          open && conversationPane(open),
          VARIANT === "b" && memberList(),
        ),
  );
}

// With a session picked, a chat with it is called by the other session's name.
function otherIn(c) {
  if (!DARKISH || !ui.filter || c.kind !== "dm") return null;
  const me = nameOf(ui.filter, ui.filter);
  return c.pair.includes(me) ? c.pair.find((n) => n !== me) || me : null;
}

// G's middle column: who it's for, a search, then direct messages and group chats apart.
function gList(list) {
  const name = ui.filter && nameOf(ui.filter, ui.filter);
  const f = name && findSession(name);
  const q = (ui.cq || "").toLowerCase();
  const hit = (c) => !q || (otherIn(c) || c.title).toLowerCase().includes(q) || c.messages.some((m) => (m.subject + " " + (m.body || "")).toLowerCase().includes(q));
  const old = (c) => c.kind === "dm" && c.pair.every((n) => isSettled(n));
  const dms = list.filter((c) => c.kind === "dm" && hit(c) && (q || name || !old(c)));
  const older = q || name ? [] : list.filter((c) => old(c));
  const groups = list.filter((c) => c.kind !== "dm" && hit(c));
  const section = (title, rows) =>
    rows.length > 0 && [h("div", { class: "g-sec" }, h("span", null, title), h("span", { class: "g-sec-n" }, rows.length)), rows.map(conversationRow)];
  return h(
    "nav",
    { class: "chat-list g-list", "aria-label": "Conversations", onscroll: (e) => (ui.chatListTop = e.target.scrollTop) },
    h(
      "div",
      { class: "g-head" },
      name
        ? [
            h("div", { class: "g-head-who" }, f ? presence(name, f.device) : avatar(name), h("h2", { class: "truncate" }, name)),
            f && f.session.doing && h("p", { class: "g-head-doing" }, f.session.doing),
            !f && h("p", { class: "g-head-doing" }, "Settled · " + ago(settledSessions().find((r) => r.name === name)?.last || 0)),
          ]
        : [h("h2", null, "All conversations"), h("p", { class: "g-head-doing" }, plural(list.length, "conversation"))],
    ),
    h(
      "label",
      { class: "g-search" },
      icon("search"),
      h("input", {
        type: "search",
        placeholder: "Search conversations",
        "data-key": "cq",
        value: ui.cq || null,
        oninput: (e) => {
          ui.cq = e.target.value;
          render();
        },
      }),
    ),
    section("Direct messages", dms),
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
        h("span", null, icon(ui.showOlder ? "chevronDown" : "chevronRight", "settled-caret"), "Older, between settled sessions"),
        h("span", { class: "g-sec-n" }, older.length),
      ),
    ui.showOlder && older.map(conversationRow),
    section("Group chats", groups),
    !dms.length && !groups.length && h("p", { class: "g-none" }, "Nothing matches."),
  );
}

function conversationRow(c) {
  const last = c.messages[c.messages.length - 1];
  const at = when(last.at);
  const o = otherIn(c);
  const me = ui.filter && nameOf(ui.filter, ui.filter);
  return h(
    "button",
    {
      class: "chat-row",
      "aria-current": c.key === ui.conversation ? "true" : null,
      "data-key": "c-" + c.key,
      onclick: () => {
        ui.conversation = c.key;
        ui.conversationAnchor = c.messages[c.messages.length - 1].id; // the key can be out of date
        render();
      },
    },
    o ? dotted(o) : conversationIcon(c),
    h(
      "span",
      { class: "chat-row-text" },
      h(
        "span",
        { class: "chat-row-line" },
        VARIANT === "g" && c.kind !== "dm"
          ? [
              h("span", { class: "chat-row-title truncate", title: c.title }, groupName(c)),
              h("span", { class: "g-row-count" }, groupSummary(c).rows.length),
            ]
          : h("span", { class: "chat-row-title truncate", title: c.title }, o || c.title),
        at && agoEl("span", "chat-row-time", at),
      ),
      h("span", { class: "chat-row-preview truncate" }, (o && fromName(last) === o ? "" : o && fromName(last) === me ? "You: " : fromName(last) + ": ") + last.subject),
    ),
  );
}

function conversationPane(c) {
  const senders = new Set(c.messages.map(fromName));
  const o = otherIn(c);
  const of = o && findSession(o);
  const gs = VARIANT === "g" && c.kind !== "dm" ? groupSummary(c) : null;
  const about = gs
    ? [c.kind === "all" ? "Every session in this repo" : "Every session on " + groupHost(c), plural(gs.rows.length, "member"), gs.on + " active"].join(" · ")
    : o ? (of ? of.session.doing || of.session.agent || "" : "Not running") : DARKISH ? (c.kind === "all" ? "Every session" : c.kind === "device" ? "Every session on " + c.title : "") : [
    c.kind === "all" && "Every session",
    c.kind === "device" && "Every session on " + c.title,
    plural(c.messages.length, "message"),
    c.kind !== "dm" && plural(senders.size, "sender"),
  ]
    .filter(Boolean)
    .join(" · ");
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
        about && h("div", { class: "chat-about truncate", title: [...senders].join(", ") }, about),
      ),
      VARIANT === "g" && c.kind !== "dm" && membersButton(c),
    ),
    h("div", { class: "chat-thread", "data-conversation": c.key }, chatMessages(c)),
    DARKISH && h("div", { class: "readonly" }, h("div", { class: "readonly-box" }, icon("message"), "Sessions post here with ", h("code", null, "mur send"), ". You're reading along.")),
  );
}

// A sender's messages run together under one header, as long as they come close together.
const RUN_MS = 7 * 60 * 1000;

function chatMessages(c) {
  chatConv = c;
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
    out.push(chatMessage(m, from, at, head));
    prev = m;
  }
  return out;
}

function dayLabel(t) {
  const today = new Date();
  if (new Date(t).toDateString() === today.toDateString()) return "Today";
  today.setDate(today.getDate() - 1);
  if (new Date(t).toDateString() === today.toDateString()) return "Yesterday";
  return new Date(t).toLocaleDateString(undefined, { year: "numeric", month: "long", day: "numeric" });
}

// Ours, for WhatsApp's right side: the picked session's, or with none picked, this device's.
// In a chat between two sessions with none picked, the one on this device is ours; when both are
// (or neither), the second by name, so the two still face each other.
let chatConv = null;
function isOurs(m) {
  if (ui.filter) return fromName(m) === nameOf(ui.filter, ui.filter);
  const self = (ui.state?.devices || []).find((d) => d.self);
  const local = (name) => !!self && ui.messages.some((x) => fromName(x) === name && (x.fromAddr || "").startsWith(self.id + "/"));
  if (chatConv?.kind === "dm" && chatConv.pair.length === 2) {
    const [a, b] = chatConv.pair;
    const right = local(a) === local(b) ? b : local(a) ? a : b;
    return fromName(m) === right;
  }
  return !!self && (m.fromAddr || "").startsWith(self.id + "/");
}

function chatMessage(m, from, at, head) {
  const side = VARIANT === "c" && isOurs(m) ? " ours" : "";
  const sender = findSession(from);
  // In a group chat, who it was for is the conversation, so only a DM's title says nothing new.
  return h(
    "article",
    { class: "chat-msg" + (head ? " chat-msg-first" : "") + side, "data-id": m.id },
    head ? avatar(from) : h("span", { class: "avatar-gap" }),
    h(
      "div",
      { class: "chat-msg-main" },
      head &&
        h(
          "div",
          { class: "chat-msg-head" },
          h("span", { class: "chat-msg-from truncate" }, from),
          sender && h("span", { class: "chat-msg-via truncate" }, [sender.session.agent, sender.device.self ? null : sender.device.id].filter(Boolean).join(" · ")),
          h("span", { class: "chat-msg-time", title: messageDetail(m) }, at && new Date(at).toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" })),
        ),
      m.subject && h("div", { class: "chat-msg-subject" }, m.subject),
      m.body && h("div", { class: "chat-msg-body rich" }, rich(m.body)),
    ),
    h(
      "div",
      { class: "msg-tools" },
      h(
        "button",
        {
          title: "Copy",
          "aria-label": "Copy message",
          onclick: () => navigator.clipboard?.writeText((m.subject ? m.subject + "\n\n" : "") + (m.body || "")),
        },
        icon("copy"),
      ),
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
  if (thread) thread.scrollTop = same && !was.atEnd ? was.top : thread.scrollHeight;
}

function messageRow(m) {
  const open = ui.expanded.has(m.id);
  const at = when(m.at);
  const from = fromName(m);
  const toggle = () => {
    if (ui.expanded.has(m.id)) ui.expanded.delete(m.id);
    else ui.expanded.add(m.id);
    render();
  };
  const detail = messageDetail(m);
  return h(
    "div",
    { class: "msg", "aria-expanded": open ? "true" : "false" },
    h(
      "button",
      { class: "msg-head", "data-key": "m-" + m.id, "aria-expanded": open ? "true" : "false", onclick: toggle },
      icon("chevronRight", "msg-chev"),
      h("span", { class: "msg-from truncate", title: from }, from),
      icon("arrowRight", "arrow"),
      recipient(m),
      h("span", { class: "msg-subject", title: m.subject }, m.subject),
      at ? agoEl("span", "msg-time", at) : h("span", { class: "msg-time" }),
    ),
    open && detail && h("div", { class: "msg-detail" }, detail),
    open && m.body && h("div", { class: "msg-body rich" }, rich(m.body)),
  );
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
      h("div", null, h("div", { class: "row-title" }, title), description && h("p", { class: "row-description" }, description)),
      h("div", { class: "row-control" }, control),
    ),
    ui.rowErrors[key] && h("p", { class: "row-error" }, ui.rowErrors[key]),
  );
}

function settingsPage() {
  const sections = [];
  if (ui.settings) {
    const set = ui.settings;
    const local = (ui.state?.devices || []).find((d) => d.self)?.sessions || [];
    const overrides = set.sessionWake || {};
    const rows = [
      settingsRow(
        "wake",
        "Device default",
        "Whether a message from another session may wake an idle session.",
        wakeControl("wake", set.wake, (w) => saveSettings({ ...ui.settings, wake: w || { when: "within" } }), false),
      ),
      ...[...local]
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
        ),
    ];
    sections.push(
      h(
        "section",
        { class: "section" },
        h("div", { class: "section-head" }, h("h2", { class: "section-title" }, "Wake-ups")),
        h("div", { class: "group" }, rows),
        ui.settingsError && h("p", { class: "row-error section-head" }, ui.settingsError),
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

  const theme = window.appearance.get();
  if (VARIANT !== "g") sections.push(
    h(
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
            { class: "segmented", role: "group" },
            [
              ["system", "System"],
              ["light", "Light"],
              ["dark", "Dark"],
            ].map(([v, label]) =>
              h(
                "button",
                {
                  "aria-pressed": theme === v ? "true" : "false",
                  "data-key": "theme-" + v,
                  onclick: () => {
                    window.appearance.set(v);
                    render();
                  },
                },
                label,
              ),
            ),
          ),
          "keep-inline",
        ),
      ),
    ),
  );
  if (VARIANT === "g") {
    const self = (ui.state?.devices || []).find((d) => d.self);
    sections.unshift(h("header", { class: "g-page-head g-settings-head" }, h("h1", null, "Settings"), h("p", null, self ? "For this device, " + deviceName(self) : "For this device")));
  }
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
  if (location.hash !== "#" + view) location.hash = view;
  else setView(view);
}

function setView(view) {
  const [v, open] = view.split("/");
  view = v === "fleet" ? "sessions" : v;
  const next = VIEWS.includes(view) ? view : VARIANT === "g" ? "messages" : "sessions";
  // Leaving All sessions (to a conversation, say) remembers where the list and the details were,
  // so Back to the same session finds them as they were.
  if (ui.view === "sessions" && next !== "sessions") ui.fleetMemo = { sel: ui.fleetSel || null, page: content?.scrollTop || 0, pane: root.querySelector(".fx-pane-in")?.scrollTop || 0 };
  ui.view = next;
  if (ui.view === "settings") loadSettings();
  if (ui.view === "sessions") {
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
    return;
  }
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

async function fetchMessages() {
  for (let pages = 0; pages < 20; pages++) {
    const r = await api("/api/messages?cursor=" + encodeURIComponent(ui.cursor));
    let added = 0;
    for (const m of r.messages || []) {
      learn(m.fromAddr, m.from);
      if (m.toKind === "session") learn(m.toAddr, m.to);
      if (ui.ids.has(m.id)) continue;
      ui.ids.add(m.id);
      // A clock can be wrong. A message with no time goes where it arrived, and one from the future
      // counts as arriving now, so it can't hold its conversation at the top of the list or stay
      // below the replies to it.
      arrivedAt = m.sortAt = Math.min(when(m.at) || arrivedAt, Date.now());
      ui.messages.push(m);
      added++;
    }
    if (ui.messages.length > MAX_MESSAGES) {
      for (const m of ui.messages.splice(0, ui.messages.length - MAX_MESSAGES)) ui.ids.delete(m.id);
    }
    const moved = r.cursor !== ui.cursor;
    ui.cursor = r.cursor || ui.cursor;
    if (!added || !moved) return;
  }
}

let last = "";
let timer;
let inflight = false;
let polled = false;
let scrolledAt = 0;
// Text being selected: the pointer is down, or the selection changed in the last 3 seconds. A
// selection left standing after that doesn't hold the page still, or a lead who copied a branch
// name would be looking at a frozen page that seems live.
let pointerHeld = false;
let selectedAt = 0;
let lastConn = "";

async function poll() {
  if (inflight) return;
  inflight = true;
  clearTimeout(timer);
  try {
    const [state] = await Promise.all([api("/api/state"), fetchMessages()]);
    for (const d of state.devices || []) for (const s of d.sessions || []) learn(s.addr, s.name);
    ui.state = state;
    ui.conn = "ok";
    ui.okAt = Date.now();
    ui.fails = 0;
  } catch (e) {
    inflight = false;
    if (e instanceof AuthError) return deny(e.token);
    // One dropped request is noise; two in a row (or none ever answered) is the daemon gone.
    ui.fails = (ui.fails || 0) + 1;
    if (ui.fails >= 2 || !ui.state) ui.conn = "down";
  }
  inflight = false;
  // Rebuild only when something changed, and leave alone a minutes field being typed in, text being
  // selected, and a pane being scrolled: a rebuild would drop the selection or stop the scroll.
  // Fleet's search box is exempt: it keeps its caret across a rebuild (render), and holding the
  // table still while it has focus left the rows stale. The sessions view also rebuilds once a
  // minute, since its "No reply for 40m" waits come from the clock, not the state.
  const minute = VARIANT === "g" && ui.view === "sessions" ? Math.floor(Date.now() / 60000) : 0;
  const sig = JSON.stringify([ui.state, ui.cursor, ui.conn, minute]);
  const typing = document.activeElement?.tagName === "INPUT" && document.activeElement.dataset.key !== "fx-q";
  const selecting = document.getSelection()?.isCollapsed === false && (pointerHeld || Date.now() - selectedAt < 3000);
  const scrolling = Date.now() - scrolledAt < 1000 || ui.fxDragging;
  // Losing or regaining the daemon always shows at once.
  const connMoved = ui.conn !== lastConn;
  if (sig !== last && (connMoved || (!typing && !selecting && !scrolling))) {
    last = sig;
    lastConn = ui.conn;
    render();
    if (ui.fleetFocusPending) fleetFocusBack();
  }
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
    Object.assign(ui, { token, conn: "connecting", state: null, messages: [], cursor: "", settings: null, conversation: null, chatListTop: 0 });
    ui.ids = new Set();
    named.clear();
    for (const key of [...minuteEdits.keys()]) cancelMinutes(key);
    confirmed = null;
    arrivedAt = 0;
    last = "";
    polled = false;
  }
  const [v, open] = location.hash.slice(1).split("/");
  const want = v === "fleet" ? "sessions" : v;
  ui.view = VIEWS.includes(want) ? want : VARIANT === "g" ? "messages" : "sessions";
  if (ui.view === "sessions") ui.fleetSel = fleetFromHash(open);
  render();
  fleetFocusBack();
  if (!ui.token) return;
  if (ui.view === "settings") loadSettings();
  poll();
}

window.addEventListener("hashchange", () => {
  if (location.hash.includes("token=")) start();
  else setView(location.hash.slice(1));
});
document.addEventListener("visibilitychange", () => {
  if (!document.hidden && ui.token && ui.conn !== "denied") poll();
});
document.addEventListener("pointerdown", () => (pointerHeld = true), true);
for (const t of ["pointerup", "pointercancel"]) document.addEventListener(t, () => (pointerHeld = false), true);
document.addEventListener("selectionchange", () => (selectedAt = Date.now()));
document.addEventListener(
  "scroll",
  () => {
    scrolledAt = Date.now();
  },
  { capture: true, passive: true },
);
window.addEventListener("resize", () => fleetFit());

// The toolbar and the table's header stay in view as the table scrolls, under the demo's tab bar
// where there is one; the header sits under the toolbar, whose height changes as it wraps.
function fleetFit() {
  const split = root.querySelector(".fx-split");
  if (!split) return;
  const tabs = root.querySelector(".sv-tabs");
  split.style.setProperty("--fx-top", (tabs ? tabs.offsetHeight : 0) + "px");
  const pane = split.querySelector(".fx-pane");
  split.classList.toggle("overlay", !!pane && split.clientWidth - pane.offsetWidth < 680);
  const bar = split.querySelector(".fx-toolbar");
  if (bar) split.style.setProperty("--fx-tb", bar.offsetHeight + "px");
}
NARROW.addEventListener("change", render);
setInterval(tick, 10000);
start();

// ---------- #183: Fleet audit ----------
// One page to answer "what has each session done, and is it on track?" for a dozen sessions at once:
// a filterable table of running sessions, what needs a human, and each session's timeline. Status
// history, commits and read marks aren't on the bus yet; the mock (demo.history) is what #183's
// proposed events would carry, and a real daemon shows a note instead.
const IDLE_FLAG_MS = 20 * 60000;
const UNREAD_FLAG_MS = 10 * 60000;
const NO_REPLY_FLAG_MS = 15 * 60000;

function fleetHistory() {
  return DEMO ? demo.history : null;
}

function groupOf(s) {
  const t = (s.tags || []).find((x) => x && typeof x.name === "string" && x.name.startsWith("group:"));
  return t ? t.name.slice(6) : "";
}

function fleetRows() {
  const out = [];
  for (const d of devices()) for (const s of d.sessions) out.push({ d, s });
  return out;
}

function isQuestion(m) {
  return /\?/.test((m.subject || "") + " " + (m.body || ""));
}

function replyTo(m) {
  return ui.messages.find((r) => r.re === m.id) || null;
}

// Whether the recipient has written back to the sender since: anything in this conversation,
// or a new message elsewhere that asks nothing. A reply in another thread answers that thread,
// and a new question of its own answers nothing.
function answeredSince(m) {
  const to = toName(m);
  const from = fromName(m);
  const topic = (x) => topicOf(x).toLowerCase();
  return ui.messages.some((r) => r.sortAt > m.sortAt && fromName(r) === to && r.toKind === "session" && toName(r) === from && (topic(r) === topic(m) || (!isReply(r) && !questionIn(r))));
}

// What each session did and was sent, from the history and the messages.
function fleetStats(name) {
  const H = fleetHistory();
  const f = findSession(name);
  const ev = H ? H.events.filter((e) => e.who === name).map((e) => seen(e, f?.device)).filter(Boolean) : [];
  const commits = ev.filter((e) => e.kind === "commit").sort((a, b) => when(a.at) - when(b.at));
  const sent = ui.messages.filter((m) => fromName(m) === name);
  const got = ui.messages.filter((m) => m.toKind === "session" && toName(m) === name);
  const unread = H ? got.filter((m) => !H.reads[m.id]) : [];
  return { ev, commits, sent, got, unread };
}

// Who needs a human, and why. Each alert has a short title, a line of detail, a severity
// (error, warning or neutral) and the session it opens.
function attention(rows) {
  const H = fleetHistory();
  const out = [];
  const now = Date.now();
  const names = new Set(rows.map((r) => r.s.name));
  if (H) {
    for (const m of ui.messages) {
      if (m.toKind !== "session" || !names.has(toName(m))) continue;
      const at = when(m.at);
      if (!at) continue;
      const to = toName(m);
      const f = findSession(to);
      const read = H.reads[m.id];
      if (!read && now - at > UNREAD_FLAG_MS) {
        const why = !f.device.reachable ? "Its device is offline." : onDnd(f.session) ? "It's on do not disturb." : "It hasn't read its inbox.";
        out.push({ kind: "unread", sev: "error", who: to, at, title: [h("b", null, to), " hasn't received a message"], detail: "From " + fromName(m) + ": “" + m.subject + "”. " + why });
      } else if (read && isQuestion(m) && !answeredSince(m) && now - when(read.at) > NO_REPLY_FLAG_MS) {
        out.push({ kind: "noreply", sev: "error", who: to, at: when(read.at), title: [h("b", null, to), " hasn't answered"], detail: fromName(m) + " asked “" + m.subject + "”. Read " + ago(when(read.at)) + "." });
      }
    }
    for (const { d, s } of rows) {
      if (!d.self) continue;
      const tools = H.events.filter((e) => e.who === s.name && e.kind === "tool").sort((a, b) => when(b.at) - when(a.at));
      // The same command, run three times in a row (edits between them don't count), failing each time.
      const cmd = tools.find((t) => t.tool === "Bash");
      const runs = cmd ? tools.filter((t) => t.tool === "Bash" && t.detail === cmd.detail).slice(0, 3) : [];
      if (runs.length === 3 && runs.every((t) => !t.ok)) out.push({ kind: "failing", sev: "error", who: s.name, at: when(runs[0].at), title: [h("b", null, s.name), "'s command keeps failing"], detail: [h("code", null, cmd.detail), " failed three runs in a row."] });
    }
  }
  for (const { d, s } of rows) {
    const t = when(s.lastActive);
    if (d.reachable && !onDnd(s) && t && now - t > IDLE_FLAG_MS) out.push({ kind: "idle", sev: "warning", who: s.name, at: t, title: [h("b", null, s.name), " is idle"], detail: "Quiet for " + ago(t).replace(" ago", "") + (s.doing ? "." : ", with no status set.") });
  }
  if (H) {
    const byBranch = new Map();
    for (const e of H.events) if (e.kind === "commit" && names.has(e.who)) byBranch.set(e.branch, new Set([...(byBranch.get(e.branch) || []), e.who]));
    for (const [branch, who] of byBranch) {
      if (who.size < 2) continue;
      const list = [...who];
      const at = Math.max(...H.events.filter((e) => e.kind === "commit" && e.branch === branch).map((e) => when(e.at)));
      out.push({ kind: "branch", sev: "warning", who: list[0], others: list, at, title: [plural(list.length, "session") + " on ", h("code", null, branch)], detail: list.join(" and ") + " both commit there." });
    }
  }
  const offDevs = new Set();
  for (const { d } of rows) {
    if (d.reachable || offDevs.has(d.id)) continue;
    offDevs.add(d.id);
    const t = when(d.lastSeen);
    out.push({ kind: "offline", sev: "neutral", who: d.sessions[0]?.name, others: d.sessions.map((s) => s.name), at: t, title: [h("b", null, (d.owner ? d.owner + "'s " : "") + d.id.replace(/-[0-9a-f]{4}$/, "")), " is offline"], detail: "Last seen " + ago(t) + ", with " + plural(d.sessions.length, "session") + " on it." });
  }
  const rank = { unread: 0, failing: 1, noreply: 2, offline: 3, branch: 4, idle: 5 };
  return out.sort((a, b) => rank[a.kind] - rank[b.kind] || b.at - a.at);
}

// Group by and the groups shut under it are remembered across reloads, since a lead tends to work
// one way; the state filter and search start fresh.
const FLEET_BY_KEY = NAME + ":fleet-by";
const FLEET_SHUT_KEY = NAME + ":fleet-shut";
// Both sets are read once and written as changes against what storage holds now, so two tabs
// don't overwrite each other; another tab's change arrives through the storage event.
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

// Questions a lead has said nobody is waiting on (an answer the finder misread, or one given out of
// band), by message id and when, in this browser only. Kept two days, past the one a flag lasts.
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
  return ui.fleet;
}

function fleetSub() {
  if (!ui.state) return "";
  const n = attention(fleetRows()).length;
  return n ? plural(n, "thing") + " need a look" : "Nothing needs a look";
}

const FX_STATE = { on: "Active", idle: "Idle", dnd: "Do not disturb", off: "Offline" };

function fxDot(st) {
  return h("span", { class: "fx-dot fx-dot-" + st, "aria-hidden": "true" });
}

function fxSeg(label, options, value, onPick) {
  return h(
    "div",
    { class: "fx-seg", role: "group", "aria-label": label },
    options.map(([v, text, n]) =>
      h(
        "button",
        { "aria-pressed": value === v ? "true" : "false", class: value === v ? "on" : "", "data-key": "fs-" + label + ":" + v, title: Array.isArray(text) ? text.map((x) => (typeof x === "string" ? x : x.textContent || "")).join("").trim() || null : null, onclick: () => onPick(v) },
        text,
        n != null && h("span", { class: "fx-seg-n" + (n === 0 ? " zero" : "") }, n),
      ),
    ),
  );
}

// Fleet v1 (#183) shows only what the daemon serves today: devices, sessions, messages, tags and
// each branch's PR. Tool calls, read marks and other devices' commits aren't on the bus, so
// nothing here depends on them (fleetHistory and the functions above are left for the old draft).
//
// What needs a person: a direct question nobody has answered for 15 minutes. One row per session
// that owes an answer, the oldest question first. Offline and do not disturb explain why a
// question waits rather than being reasons of their own: closed laptops overnight would otherwise
// fill the group every morning. A question stops counting after a day. Idle sessions and two
// sessions on one branch are normal work, so they aren't reasons.
const NO_ANSWER_MS = 15 * 60000;
const GIVE_UP_MS = 24 * 3600000;

// The question a message asks: its subject if that is one and starts the thread, or else the first
// sentence of its body that ends in "?", or else of its subject (send makes the body optional).
// Code blocks and quoted lines ("> ...", usually the question being answered) are left out, since
// their "?"s aren't this message's questions. Inline code and links are set aside while sentences
// are found, so their "?" or "." can't end one, and put back plainly: code without its backticks,
// a link as its text or as what it points at ("#36", a file name). A "?" may sit inside closing
// marks (**bold?**, (aside?), asked: "quoted?"), and a period ends a sentence only before a capital.
function questionIn(m) {
  const subject = (m.subject || "").trim();
  if (/\?\s*$/.test(subject) && !isReply(m)) return cutAtWord(subject, 110);
  return cutAtWord(askedIn(m.body || "") || (isReply(m) ? "" : askedIn(subject)), 110);
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
  const keep = (x) => "\uE000" + (kept.push(x) - 1) + "\uE001";
  // A link says what it points at: "#36" for a pull request or issue, a short SHA for a commit,
  // the last part of its path for anything else (a file, a page), or its host.
  const link = (url) => {
    const [host, ...path] = url.replace(/^https?:\/\//, "").split(/[?#]/)[0].split("/").filter(Boolean);
    const n = url.match(/\/(?:pull|issues)\/(\d+)/);
    const sha = url.match(/\/commit\/([0-9a-f]{7,40})/i);
    return n ? "#" + n[1] : sha ? sha[1].slice(0, 7) : path.length ? safeDecode(path[path.length - 1]) : host;
  };
  const text = raw
    .replace(/```[\s\S]*?```/g, " ")
    .split("\n").filter((line) => !/^\s*>/.test(line))
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
      return mid ? o + inner.replace(/\?/g, "\uE003") + c : all;
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
      .replace(/\uE000(\d+)\uE001/g, (_, i) => kept[i])
      .trim();
  };
  // "?" and "!" end a sentence; "." only before a capital, a digit or an opening mark, so "e.g. the
  // zip", "the U.S. build" and "3 p.m. your time" don't.
  const found = text
    .split(/(?<=[!?][*_)\]"”’]*)\s+|(?<=\.[*_)\]"”’]*)\s+(?=[\p{Lu}\d"“‘(\[*_\uE000])|\n+/u)
    .map((x) => x.trim().replace(/\uE003/g, "?"))
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

// A PR chip; a draft is grey and says "draft" in words, so the grey isn't a code to learn.
function fxPr(pr, href) {
  const props = { class: "fx-pr" + (pr.draft ? " draft" : ""), title: (pr.draft ? "Draft pull request #" : "Pull request #") + pr.number };
  const label = "#" + pr.number + (pr.draft ? " draft" : "");
  return href ? h("a", { ...props, href, target: "_blank", rel: "noreferrer" }, label) : h("span", props, label);
}

// A reply is known by its "Re:" subject; the daemon has no reply field today (#183 proposes one).
// What a message's thread is about: its subject without "Re:". Conversations are keyed on it
// case-blind, so "Re: signing mur.exe" belongs with "Signing mur.exe".
function topicOf(m) {
  return (m.subject || "").replace(/^(re:\s*)+/i, "").trim();
}

function isReply(m) {
  return /^re:/i.test(m.subject || "");
}

function deviceName(d) {
  return d.id.replace(/-[0-9a-f]{4}$/, "");
}

// With all, questions marked "Not waiting" count too (for the details' Undo lines).
function fleetNeeds(rows, all = false) {
  const now = Date.now();
  const by = new Map(rows.map((r) => [r.s.name, r]));
  const out = new Map();
  const asks = new Map();
  for (const m of ui.messages) {
    // A reply that asks something ("Which runner image?") waits like a first message does.
    if (m.toKind !== "session" || !questionIn(m)) continue;
    const to = toName(m);
    const from = fromName(m);
    const at = when(m.at);
    // The asker may have ended since; the question still waits on a running session.
    if (!by.has(to) || to === from || !at || now - at < NO_ANSWER_MS || now - at > GIVE_UP_MS || (!all && Object.hasOwn(fleetDismissed(), m.id)) || answeredSince(m)) continue;
    if (!asks.has(to)) asks.set(to, []);
    asks.get(to).push(m);
  }
  const live = new Set(fleetRows().map((r) => r.s.name));
  for (const [to, list] of asks) {
    list.sort((a, b) => when(a.at) - when(b.at));
    const m = list[0];
    const { d, s } = by.get(to);
    // "More" counts conversations, as the details list them: a nudge in the same thread is one.
    const convs = new Set(list.map((x) => JSON.stringify([fromName(x), topicOf(x).toLowerCase()]))).size;
    const more = convs > 1 ? " And " + plural(convs - 1, "more question") + "." : "";
    // text is a function: a row and the inspector each need their own copy of its nodes. The
    // State column says whether the session is offline or on do not disturb, so this doesn't.
    // The row and the details' flag box say it in the same words.
    // The wait ticks with the page's other times, so the row and its conversation agree.
    const wait = () => agoEl("span", null, when(m.at), " for ", true);
    // When the question isn't the subject (a body's "Fine?"), the subject says what it's about.
    // A subject that holds the question ("Can you review #36? It blocks") adds nothing.
    const about = (x) => (topicOf(x) && !topicOf(x).includes(questionIn(x).replace(/…$/, "")) ? " on “" + cutAtWord(topicOf(x), 60) + "”" : "");
    // An asker that has ended since still counts (its question waits on a running session), and
    // says so.
    const ended = live.has(fromName(m)) ? "" : " (ended)";
    out.set(to, { at: when(m.at), m, msgs: list, text: () => ["No reply to ", h("b", null, fromName(m)), ended, wait(), about(m) + ": “" + questionIn(m) + "”" + more] });
  }
  return out;
}

// While the daemon doesn't answer, the page says so and how old what it shows is, ahead of
// anything else, so a stopped page doesn't read as a quiet team.
function fleetDown() {
  if (ui.conn !== "down") return null;
  return h("span", { class: "fx-down" }, "Can't reach the daemon", ui.okAt ? agoEl("span", null, ui.okAt, " · last updated ", false, true) : " · retrying");
}

function fleetPage() {
  if (!ui.state) return h("div", { class: "fx" }, h("header", { class: "fx-head" }, h("div", null, h("h1", null, "All sessions"), h("p", null, fleetDown() || "Connecting…"))));
  const F = fleetFilter();
  if (!["device", "branch", "group"].includes(F.by)) F.by = "device";
  const all = fleetRows();
  const q = F.q.trim().toLowerCase();
  // Search covers what a row shows: the asker and question it waits on, and the PR too.
  const allNeeds = fleetNeeds(all);
  const prs = ui.state.prs || {};
  const text = (r) => {
    const need = allNeeds.get(r.s.name);
    const pr = r.s.branch && Object.hasOwn(prs, r.s.branch) ? prs[r.s.branch] : null;
    return [r.s.name, FX_STATE[sessionState(r.d, r.s)], r.s.doing, r.s.branch, r.s.agent, r.d.owner, deviceName(r.d), groupOf(r.s), pr && "#" + pr.number + (pr.draft ? " draft" : ""), ...(need ? need.msgs.flatMap((m) => [fromName(m), m.subject, questionIn(m)]) : [])].join(" ");
  };
  const searched = all.filter((r) => !q || text(r).toLowerCase().includes(q));
  const rows = searched.filter((r) => !F.state || sessionState(r.d, r.s) === F.state);
  const needs = fleetNeeds(rows);
  // Open details stay open when their session falls out of the filter (it went idle while you
  // read, say); they close only when the session itself is gone.
  if (ui.fleetSel && !all.some((r) => r.s.name === ui.fleetSel)) {
    ui.fleetSel = null;
    history.replaceState(null, "", fleetHash(null));
  }
  const sel = ui.fleetSel;
  const selNeed = sel ? allNeeds.get(sel) : null;
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
  // Filtered, the need count says how many of them are shown, so it agrees with the "Needs a
  // look" group under it.
  const shownNeeds = needs.size;
  const needNote = !n ? "" : shownNeeds === n ? " · " + n + (n === 1 ? " needs" : " need") + " a look" : " · " + (shownNeeds ? shownNeeds + " of the " : "none of the ") + n + (n === 1 ? " that needs" : " that need") + " a look";
  const sub = (q || F.state) && all.length
    ? ["Showing " + rows.length + " of " + plural(all.length, "session") + needNote, rows.length > 0 && [" ", h("button", { class: "fx-clear", "data-key": "fx-clear", onclick: clear }, "Clear")]]
    : all.length
      ? plural(all.length, "session") + " on " + plural(new Set(all.map((r) => r.d.id)).size, "device") + " · " + (n ? n + (n === 1 ? " needs" : " need") + " a look" : "nothing needs a look")
      : "No sessions running · " + plural(ui.state.devices?.length || 0, "device");

  // The details open in a pane beside the table at every width, as a thread does in Slack: the
  // table narrows (and drops columns, fleet.css) rather than going away.
  return h(
    "div",
    { class: "fx-split" + (sel ? " has-pane" : ""), style: sel ? "--fx-pane-w: " + fleetPaneWidth() + "px" : null },
    h(
      "div",
      { class: "fx" },
      h("header", { class: "fx-head" }, h("div", null, h("h1", null, "All sessions"), h("p", null, fleetDown() ? [fleetDown(), " · ", sub] : sub))),
      h(
        "div",
        { class: "fx-toolbar" },
        h(
          "label",
          { class: "fx-input" },
          icon("search"),
          h("input", {
            type: "search",
            placeholder: "Search sessions",
            "aria-label": "Search sessions",
            value: F.q,
            "data-key": "fx-q",
            oninput: (e) => {
              F.q = e.target.value;
              render();
            },
          }),
          !F.q && h("kbd", { class: "fx-kbd", "aria-hidden": "true" }, "/"),
        ),
        fxSeg("State", [["", "All", searched.length], ...["on", "idle", "dnd", "off"].map((st) => [st, [fxDot(st), h("span", { class: "fx-seg-word" }, FX_STATE[st])], count(st)])], F.state, set("state")),
        h("span", { class: "fx-spacer" }),
        h("span", { class: "fx-group-by" }, h("span", { class: "fx-label" }, "Group by"), fxSeg("Group by", [["device", "Device"], ["branch", "Branch"], ["group", "Job"]], F.by, set("by"))),
      ),
      fleetTable(rows, needs, sel, { q: F.q.trim(), state: F.state, total: all.length, clear }),
    ),
    sel && fleetPane(sel, selNeed),
  );
}

// A device as people say it: its name, with its owner's when the name doesn't already carry it
// ("jun-desktop", "tyler's mac-mini").
function fleetDeviceLabel(d) {
  const dev = deviceName(d);
  return (d.owner && !dev.toLowerCase().includes(d.owner.toLowerCase()) ? d.owner + "'s " : "") + dev;
}

// The table's columns and what each sorts by. A column sorts on its first click in the order a
// person reaches for (names A to Z, Active first, the latest activity first), flips on the second,
// and goes back to the table's own order on the third.
const FX_COLS = [
  ["who", "Session", "name"],
  ["state", "State", "state"],
  ["doing", "Working on", null],
  ["branch", "Branch", "branch"],
  ["last", "Last active", "active"],
];
const FX_STATE_ORDER = { on: 0, idle: 1, dnd: 2, off: 3 };
function fleetSortVal(k, r) {
  if (k === "name") return r.s.name;
  if (k === "state") return FX_STATE_ORDER[sessionState(r.d, r.s)];
  if (k === "branch") return r.s.branch || "\uffff";
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

function fleetTable(rows, needs, sel, empty) {
  const F = fleetFilter();
  const S = F.sort || {};
  const key = (r) => (F.by === "device" ? r.d.id : F.by === "branch" ? r.s.branch || "" : groupOf(r.s));
  // "Needs a look" is oldest first unless a column says otherwise.
  const pinned = rows.filter((r) => needs.has(r.s.name)).sort(S.key ? fleetCompare(S) : (a, b) => needs.get(a.s.name).at - needs.get(b.s.name).at);
  // A fixed order, so groups and rows don't swap places as agents work: this device, then devices
  // by name, offline ones last; branches and jobs by name, with none last; rows by name, or by the
  // column sorted on.
  const rank = (r) => (F.by === "device" ? (r.d.self ? 0 : r.d.reachable ? 1 : 2) : key(r) ? 0 : 1);
  const gname = (r) => (F.by === "device" ? deviceName(r.d) + " " + r.d.id : key(r) || "");
  const within = S.key ? fleetCompare(S) : (a, b) => a.s.name.localeCompare(b.s.name);
  const rest = [...rows].sort((a, b) => rank(a) - rank(b) || gname(a).localeCompare(gname(b)) || within(a, b));
  const buckets = new Map();
  // Which heading each session is under, so closing one whose group is shut can focus the heading.
  ui.fxGroupOf = {};
  for (const r of rest) {
    const k = key(r);
    ui.fxGroupOf[r.s.name] = "fg-" + F.by + ":" + k;
    if (!buckets.has(k)) buckets.set(k, []);
    buckets.get(k).push(r);
  }
  const label = (k, list) => {
    if (F.by === "device") {
      const d = list[0].d;
      return [h("span", { class: "fx-group-name" }, d.self ? "This device" : fleetDeviceLabel(d)), h("span", { class: "fx-group-sub" }, d.self ? deviceName(d) : d.reachable ? "" : "offline · seen " + ago(when(d.lastSeen) || 0))];
    }
    if (F.by === "branch") {
      const pr = k && Object.hasOwn(ui.state.prs || {}, k) ? ui.state.prs[k] : null;
      return [h("span", { class: "fx-group-name mono" }, k || "No branch"), pr && fxPr(pr)];
    }
    return h("span", { class: "fx-group-name" }, k || "No job");
  };
  // Which groups are shut. While a search or state filter is on, a separate set, empty to start,
  // so every match shows, a heading still folds and unfolds, and none of it touches what's shut
  // once the filter clears (that set comes back then).
  const filtering = !!(F.q.trim() || F.state);
  if (!filtering) ui.fxCollapsedFiltered = null;
  const collapsed = filtering ? (ui.fxCollapsedFiltered ||= new Set()) : fleetShut();
  // A heading is a row of one cell across the table.
  const wide = (cls, ...kids) => h("div", { class: "fx-grow", role: "row" }, h("div", { class: "fx-gcell " + cls, role: "cell", "aria-colspan": "5" }, kids));
  const head = h(
    "div",
    { class: "fx-tr fx-th", role: "row" },
    FX_COLS.map(([c, name, k]) => {
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
  );
  let body;
  if (rows.length === 0) {
    const what = empty.state ? FX_STATE[empty.state].toLowerCase() + " sessions" : "sessions";
    body = empty.total
      ? wide(
          "fx-empty",
          h("p", { class: "fx-empty-title" }, empty.q ? "No " + what + " match “" + empty.q + "”" : "No " + what),
          h("button", { class: "fx-btn", "data-key": "fx-empty-clear", onclick: empty.clear }, empty.q && empty.state ? "Clear search and filter" : empty.q ? "Clear search" : "Show all sessions"),
        )
      : wide("fx-empty", h("p", { class: "fx-empty-title" }, "No sessions running"));
  } else {
    body = [
      pinned.length > 0 && [wide("fx-group fx-group-need", h("span", { class: "fx-chev fx-need-dot", "aria-hidden": "true" }, h("span", { class: "fx-flag" })), h("span", { class: "fx-group-name" }, "Needs a look"), h("span", { class: "fx-badge" }, pinned.length)), pinned.map(({ d, s }) => fleetRow(d, s, needs.get(s.name), sel === s.name))],
      [...buckets].map(([k, list]) => {
        const shown = list.filter((r) => !needs.has(r.s.name));
        if (!shown.length) return null;
        const shut = collapsed.has(F.by + ":" + k);
        return [
          wide(
            "",
            h(
              "button",
              {
                class: "fx-group",
                "data-key": "fg-" + F.by + ":" + k,
                "aria-expanded": shut ? "false" : "true",
                onclick: () => {
                  if (filtering) shut ? collapsed.delete(F.by + ":" + k) : collapsed.add(F.by + ":" + k);
                  else fleetShutSet(F.by + ":" + k, !shut);
                  ui.fxUnfolded = shut ? F.by + ":" + k : null;
                  render();
                  ui.fxUnfolded = null;
                },
              },
              icon(shut ? "chevronRight" : "chevronDown", "fx-chev"),
              label(k, list),
              list.some((r) => needs.has(r.s.name)) && h("span", { class: "fx-group-sub" }, list.length - shown.length + " more in Needs a look"),
              h("span", { class: "fx-badge" }, shown.length),
            ),
          ),
          !shut && shown.map(({ d, s }) => {
            const row = fleetRow(d, s, null, sel === s.name);
            if (ui.fxUnfolded === F.by + ":" + k) row.classList.add("enter");
            return row;
          }),
        ];
      }),
    ];
  }
  return h(
    "div",
    { class: "fx-table", role: "table", "aria-label": "Sessions" },
    rows.length > 0 && h("div", { class: "fx-thead", role: "rowgroup" }, head),
    h("div", { class: "fx-tbody", role: "rowgroup" }, body),
  );
}

// How long a session has been running, from its start: "35m", "5h 40m", "2d 3h".
function runningFor(s) {
  const t = when(s.started);
  return t ? lasted(t) : "–";
}

// "8:53 PM" today, "Sat 8:53 PM" within the week, "Nov 2, 8:53 PM" before that.
function sinceWhen(t) {
  const d = new Date(t);
  const time = d.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
  if (d.toDateString() === new Date().toDateString()) return time;
  if (Date.now() - t < 6 * 86400000) return d.toLocaleDateString([], { weekday: "short" }) + " " + time;
  return d.toLocaleDateString([], { month: "short", day: "numeric" }) + ", " + time;
}

// Parts of a line joined by "·" that wraps between parts, never inside one, and never leaves a "·"
// at either end of a line: each part carries its own leading " · " in a fixed-width box, and every
// line is pulled left by that width inside a clipping box (fleet.css .fx-parts), so the one that
// starts a line is hidden. The separators are real text, so a copied line reads "a · b"; the first
// one can't be selected. <wbr> is the only place a line may break, and copies as nothing.
function fxParts(parts) {
  return h("span", { class: "fx-parts-in" }, parts.flatMap((x, i) => [i ? h("wbr") : null, h("span", { class: "fx-part" }, h("span", { class: "fx-sep" + (i ? "" : " lead") }, " · "), x)]));
}

// A row's "No reply…" reason: one line, with all of it in the title; the details list each
// question the session owes.
function fxReason(need) {
  const el = h("p", { class: "fx-reason" }, h("span", { class: "fx-flag", "aria-hidden": "true" }), h("span", { class: "fx-reason-text" }, need.text()));
  el.title = el.textContent;
  return el;
}

function fleetRow(d, s, need, on) {
  const F = fleetFilter();
  const prs = ui.state.prs || {};
  const pr = s.branch && Object.hasOwn(prs, s.branch) ? prs[s.branch] : null;
  const t = when(s.lastActive);
  const state = sessionState(d, s);
  // Under a device's heading the device and person are in the heading; anywhere else the row
  // names the device first, so a cut-short line loses the agent, and the person too unless the
  // device's name already carries it.
  const where = need || F.by !== "device";
  const dev = deviceName(d);
  // Grouped by job, a pinned row sits away from its job's heading, so it names its job.
  const subParts = [where && dev, where && d.owner && !dev.toLowerCase().includes(d.owner.toLowerCase()) && d.owner, s.agent, need && F.by === "group" && groupOf(s) && "job " + groupOf(s)].filter(Boolean);
  return h(
    "div",
    { class: "fx-tr fx-row" + (on ? " on" : "") + (need ? " need" : ""), role: "row" },
    h(
      "div",
      { class: "fx-c fx-c-who", role: "cell" },
      h(
        "div",
        { class: "fx-who" },
        avatar(s.name),
        h(
          "div",
          { class: "fx-who-text" },
          // The name is the row's one control. It covers the whole row (fleet.css), so a click
          // anywhere opens the details, and the keyboard has one stop per row.
          h("button", { class: "fx-open", "data-key": "fl-" + s.name, "aria-expanded": on ? "true" : "false", "aria-controls": ui.fleetSel ? "fx-pane" : null, title: s.name, onclick: () => (on ? fleetCloseAsked() : fleetOpen(s.name)) }, s.name),
          h("span", { class: "fx-muted fx-parts", title: subParts.join(" · ") }, fxParts(subParts)),
          // Where the table is too narrow for Working on, it takes this line instead (fleet.css).
          s.doing && h("span", { class: "fx-muted fx-sub-doing", title: s.doing }, s.doing),
        ),
      ),
      need && fxReason(need),
    ),
    h("div", { class: "fx-c fx-c-state", role: "cell" }, fxDot(state), FX_STATE[state]),
    h("div", { class: "fx-c fx-c-doing", role: "cell" }, s.doing && h("span", { class: "fx-status", title: s.doing }, s.doing)),
    h("div", { class: "fx-c fx-c-branch", role: "cell" }, s.branch && h("span", { class: "mono", title: s.branch }, s.branch), pr && fxPr(pr)),
    h("div", { class: "fx-c fx-c-last", role: "cell" }, t ? agoEl("span", "", t) : "–"),
  );
}

// The open session is in the address, #sessions/<name>, so Back from its conversation, Forward
// and a reload all find it.
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
  root.querySelector(`[data-key="fl-${CSS.escape(name)}"]`)?.closest(".fx-row")?.scrollIntoView({ block: "nearest" });
}
// Closing gives the keyboard back to the row, or to its group's heading if the group is shut.
// Escape does the same.
function fleetClose() {
  const name = ui.fleetSel;
  ui.fleetSel = null;
  render();
  const to = name && (root.querySelector(`[data-key="fl-${CSS.escape(name)}"]`) || (ui.fxGroupOf?.[name] && root.querySelector(`[data-key="${CSS.escape(ui.fxGroupOf[name])}"]`)));
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
  const back = root.querySelector(`[data-key="fl-${CSS.escape(ui.fleetSel)}"]`) || root.querySelector(".fx-pane .fx-icon-btn");
  if (back) back.focus({ preventScroll: true });
  else ui.fleetFocusPending = true;
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
function fleetPane(name, need) {
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
      const right = root.querySelector(".fx-split").getBoundingClientRect().right;
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
  return h("aside", { class: "fx-pane" + (enter ? " enter" : ""), id: "fx-pane", "aria-label": "Session " + name }, grip, h("div", { class: "fx-pane-in" }, fleetInspector(name, need)));
}

document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && e.target?.dataset?.key === "fx-q" && e.target.value) return; // the box clears itself
  // "/" goes to the search box, and ↑/↓ move between rows, as in most lists people use daily.
  if (ui.view === "sessions" && VARIANT === "g" && !e.ctrlKey && !e.metaKey && !e.altKey) {
    const tag = e.target?.tagName;
    if (e.key === "/" && tag !== "INPUT" && tag !== "TEXTAREA") {
      const box = root.querySelector('[data-key="fx-q"]');
      if (box) {
        e.preventDefault();
        box.focus();
        box.select();
        return;
      }
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
    if ((e.key === "ArrowDown" || e.key === "ArrowUp") && e.target?.classList?.contains("fx-open")) {
      const rows = [...root.querySelectorAll(".fx-open")];
      const i = rows.indexOf(e.target);
      if (i === 0 && e.key === "ArrowUp") {
        e.preventDefault();
        root.querySelector('[data-key="fx-q"]')?.focus();
        return;
      }
      const next = rows[i + (e.key === "ArrowDown" ? 1 : -1)];
      if (next) {
        e.preventDefault();
        next.focus();
        next.closest(".fx-row").scrollIntoView({ block: "nearest" });
        // With the details open, they follow the row, as a mail client's reading pane does.
        if (ui.fleetSel) fleetOpen(next.dataset.key.slice(3));
      }
      return;
    }
  }
  if (e.key === "Escape" && !e.defaultPrevented && ui.view === "sessions" && ui.fleetSel) {
    e.preventDefault();
    fleetCloseAsked();
  }
});

// One session: why it needs a look, what it is, and its direct messages. Broadcasts go to
// everyone, so they say nothing about this session and stay out.
function fleetInspector(name, need) {
  const f = findSession(name);
  const d = f.device;
  const s = f.session;
  const state = sessionState(d, s);
  const prs = ui.state.prs || {};
  const pr = s.branch && Object.hasOwn(prs, s.branch) ? prs[s.branch] : null;
  const t = when(s.lastActive);
  const started = when(s.started);
  const seenAt = when(d.lastSeen);
  const agentParts = [s.agent, modelOf(s)].filter(Boolean);
  const meta = [
    ["State", [fxDot(state), FX_STATE[state]]],
    s.doing && ["Working on", s.doing],
    d.owner && ["Person", d.owner],
    // "last seen 47m ago" keeps together, so a narrow line breaks after "offline,".
    ["Device", h("span", null, deviceName(d) + (d.self ? " (this one)" : "") + (d.reachable ? "" : ", offline" + (seenAt ? ", " : "")), !d.reachable && seenAt && h("span", { class: "fx-nowrap" }, "last seen " + ago(seenAt)))],
    // Each part keeps together, so a narrow pane breaks the line at the "·", not inside a model name.
    ["Agent", agentParts.length ? h("span", { class: "fx-parts", title: agentParts.join(" · ") }, fxParts(agentParts)) : "–"],
    ["Branch", s.branch ? [h("span", { class: "mono" }, s.branch), pr && fxPr(pr, pr.url)] : "–"],
    groupOf(s) && ["Job", groupOf(s)],
    ["Running for", started ? runningFor(s) + ", since " + sinceWhen(started) : "–"],
    ["Last active", t ? agoEl("span", "", t) : "–"],
  ].filter(Boolean);
  const owed = need?.msgs || [];
  const unanswered = new Set([...fleetNeeds(fleetRows()).values()].flatMap((n) => n.msgs));
  // One row per conversation (the other session and the subject, a reply's "Re:" aside), since a
  // message and its reply open the same one. Conversations holding a question it owes come first,
  // oldest first; then ones marked "Not waiting"; then its own questions still waiting, oldest
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
  // Conversations marked "Not waiting" whose question would still be waiting: each can be undone.
  const ck = (other, subj) => other + " " + subj.toLowerCase();
  const marked = new Map();
  for (const x of fleetNeeds(fleetRows(), true).get(name)?.msgs || []) {
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
  // "Not waiting" is for a flag that's wrong: the reply was misread, or the answer came some other
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
  // The wait, said once: who has waited longest and for how long. Each question is in the list.
  const asker = need && fromName(need.m);
  const wait =
    need &&
    h(
      "p",
      { class: "fx-insp-wait" },
      h("span", { class: "fx-flag", "aria-hidden": "true" }),
      owing.length > 1
        ? h("span", null, owing.length + " questions waiting · the oldest from ", h("b", null, asker), ", ", agoEl("span", null, need.at, "", true))
        : h("span", null, "No reply to ", h("b", null, asker), agoEl("span", null, need.at, " for ", true)),
    );
  return [
    h(
      "header",
      { class: "fx-insp-head" },
      avatar(name),
      h("h2", { class: "truncate" }, name),
      h("button", { class: "fx-icon-btn", title: "Close (Esc)", "aria-label": "Close the details", "data-key": "fx-close", onclick: () => fleetCloseAsked() }, icon("x")),
    ),
    wait,
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
              c.rank === 0 ? ["From ", h("b", null, c.other), " · waiting for an answer"]
              : c.rank === 1 ? ["To ", h("b", null, c.other), " · no answer yet"]
              : c.isMarked ? ["From ", h("b", null, c.other), " · marked not waiting"]
              : c.msgs.length === 1 ? [out ? "To " : "From ", h("b", null, c.other)]
              : ["With ", h("b", null, c.other), " · " + c.msgs.length + " messages"];
            const k = ck(c.other, c.subj);
            return h(
              "li",
              { class: "fx-dm-item" + (c.rank === 0 ? " wait" : c.isMarked ? " marked" : "") },
              h(
                "button",
                { class: "fx-dm", "data-key": "fd-" + c.other + ":" + c.subj.toLowerCase(), onclick: () => swOpenMessage(m) },
                h("span", { class: "fx-dm-head" }, h("span", null, head), agoEl("span", "fx-dm-time", m.sortAt)),
                h("span", { class: "fx-dm-subj", title: c.subj }, c.subj),
                c.rank < 2 && questionIn(m) !== c.subj && h("span", { class: "fx-dm-q" }, "“" + questionIn(m) + "”"),
              ),
              c.rank === 0 && h("button", { class: "fx-dm-act", "data-key": "fx-nw-" + k, title: "Stop counting this question as waiting, in this browser", onclick: dismiss(c) }, "Not waiting"),
              c.isMarked && h("button", { class: "fx-dm-act", "data-key": "fx-undo-" + k, onclick: undo(c) }, "Undo"),
            );
          }),
        )
      : h("p", { class: "fx-none fx-dms-none" }, "No direct messages"),
    direct.length > 0 &&
      h(
        "button",
        {
          class: "fx-btn",
          "data-key": "fx-convs",
          onclick: () => {
            ui.messageMode = "conversations";
            ui.filter = s.addr || s.name;
            ui.conversation = null;
            go("messages");
          },
        },
        icon("message"),
        "Open " + name + "’s conversations",
      ),
  ];
}

function fleetActivity(name, f) {
  const H = fleetHistory();
  const st = fleetStats(name);
  const items = [];
  for (const e of st.ev) items.push({ at: when(e.at), e });
  for (const m of ui.messages) {
    if (fromName(m) === name) items.push({ at: m.sortAt, m, dir: "out" });
    else if ((m.toKind === "session" && toName(m) === name) || m.toKind === "all" || (m.toKind === "device" && m.to === f.device.id)) items.push({ at: m.sortAt, m, dir: "in" });
  }
  items.sort((a, b) => b.at - a.at);
  // Tool calls (this device only) come in runs; each run is one item that opens to list them.
  const merged = [];
  for (const it of items) {
    const prev = merged[merged.length - 1];
    if (it.e?.kind === "tool") {
      if (prev?.tools) prev.tools.push(it.e);
      else merged.push({ at: it.at, tools: [it.e] });
    } else merged.push(it);
  }
  const T = (n) => new Date(n).toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" });
  const lag = (m, x) => Math.max(1, Math.round((when(x) - when(m.at)) / 60000));
  const open = (m) => () => {
    ui.filter = null;
    ui.conversation = conversationOf(m).key;
    ui.conversationAnchor = m.id;
    go("messages");
  };
  const item = (tone, title, sub, extra, props = {}) => h("li", { class: "fx-act " + tone, ...props }, h("span", { class: "fx-act-dot" }), h("div", { class: "fx-act-body" }, h("div", { class: "fx-act-title" }, title), sub && h("div", { class: "fx-act-sub" }, sub), extra));
  const out = [];
  for (const it of merged) {
    const when_ = T(it.at);
    if (it.tools) {
      const key = name + "@" + it.at;
      const isOpen = ui.fleetTools?.has(key);
      const failed = it.tools.filter((t) => !t.ok).length;
      out.push(
        item(
          failed ? "error" : "",
          h(
            "button",
            {
              class: "fx-disclose",
              "aria-expanded": isOpen ? "true" : "false",
              onclick: () => {
                ui.fleetTools ||= new Set();
                isOpen ? ui.fleetTools.delete(key) : ui.fleetTools.add(key);
                render();
              },
            },
            icon(isOpen ? "chevronDown" : "chevronRight", "fx-chev"),
            plural(it.tools.length, "tool call"),
          ),
          [when_, failed ? h("span", { class: "error" }, " · " + failed + " failed") : null],
          isOpen && h("ul", { class: "fx-tools" }, it.tools.map((t) => h("li", { class: t.ok ? "" : "error" }, h("span", { class: "fx-tool" }, t.tool), h("span", { class: "mono truncate", title: t.detail }, t.detail), h("span", { class: "fx-faint" }, T(when(t.at)))))),
        ),
      );
      continue;
    }
    if (it.e) {
      const e = it.e;
      if (e.kind === "started") out.push(item("", "Started", when_ + (f.session.agent ? " · " + f.session.agent : "")));
      else if (e.kind === "status") out.push(item("", e.doing ? "Set its status" : "Cleared its status", [when_, e.doing && [" · ", h("span", { class: "fx-quote" }, e.doing)]]));
      else if (e.kind === "commit")
        out.push(item("accent", e.subject || h("span", { class: "fx-faint" }, "A commit not pushed yet"), [when_, " · ", h("span", { class: "mono" }, e.sha + " on " + e.branch), f.device.self && !e.pushed && h("span", { class: "warn" }, " · not pushed")]));
      else if (e.kind === "tag") out.push(item("", ["Tagged itself ", h("span", { class: "mono" }, e.tag)], when_));
      else if (e.kind === "offline") out.push(item("error", "Its device stopped answering", when_));
      continue;
    }
    const m = it.m;
    const peer = it.dir === "out" ? (m.toKind === "all" ? "everyone" : m.toKind === "device" ? m.to : toName(m)) : fromName(m);
    const title = it.dir === "out" ? ["To ", h("b", null, peer)] : ["From ", h("b", null, peer), m.toKind === "all" ? " to everyone" : m.toKind === "device" ? " to this device" : ""];
    let status = null;
    if (H && m.toKind === "session") {
      const r = H.reads[m.id];
      const reply = replyTo(m);
      status = r
        ? [h("span", { class: "ok" }, r.how === "turn" ? "Arrived as a turn" : "Read from its inbox"), " " + lag(m, r.at) + "m later", reply ? " · answered " + lag(m, reply.at) + "m later" : isQuestion(m) && !answeredSince(m) ? h("span", { class: "error" }, " · not answered") : null]
        : h("span", { class: "error" }, "Not read · waiting " + ago(when(m.at)).replace(" ago", ""));
    }
    out.push(
      item(
        it.dir === "out" ? "" : "",
        title,
        [when_, " · ", h("span", { class: "fx-quote" }, m.subject)],
        status && h("div", { class: "fx-act-sub" }, status),
        { class: "fx-act fx-act-link", role: "button", tabindex: 0, title: "Open the conversation", onclick: open(m), onkeydown: (ev) => (ev.key === "Enter" ? open(m)() : null) },
      ),
    );
  }
  return h("ol", { class: "fx-activity" }, out);
}

// ---------- #183: what is visible, by where the session runs ----------
// This device's daemon sees its own sessions closely: every hook event (so each tool call and
// whether it failed), the commits it credited with their subjects whether pushed or not, and the
// worktree. None of that leaves the machine. Another device's sessions are seen only through what
// their device publishes on the bus: status, last active, branch, the SHAs and branches of commits
// (subjects once pushed, read from the remote), and messages with read marks.
function seen(e, d) {
  if (!d || d.self) return e;
  if (e.kind === "tool") return null;
  if (e.kind === "commit" && !e.pushed) return { ...e, subject: "" };
  return e;
}

const SEE_WORDS = {
  full: "Full detail: tool calls, commit subjects, worktree. It stays on this device.",
  bus: "What its device publishes: status, commits, messages and read marks. Tool calls and transcripts stay on that device.",
};

function seeBadge(d) {
  const full = d.self;
  return h("span", { class: "fx-tag " + (full ? "blue" : "gray"), title: SEE_WORDS[full ? "full" : "bus"] }, full ? "Full detail" : "Bus only");
}

function lastTool(name) {
  const H = fleetHistory();
  if (!H) return null;
  const t = H.events.filter((e) => e.who === name && e.kind === "tool").sort((a, b) => when(b.at) - when(a.at))[0];
  if (!t) return null;
  return h("span", { class: "fx-last-tool" + (t.ok ? "" : " error"), title: t.tool + ": " + t.detail }, h("span", { class: "mono truncate" }, t.tool + " · " + t.detail), h("span", null, (t.ok ? "" : "failed ") + ago(when(t.at))));
}

// ---------- #183: Briefing ----------
// The page a person opens to understand the fleet: what the agents decided among themselves,
// what each stream of work is for and where it stands, and who nobody can account for. Fleet (the
// table) is for checking on agents; this is for following them.
const BRIEF_ACK_KEY = NAME + ":brief-ack";
function briefAcks() {
  if (!ui.briefAck) {
    try {
      ui.briefAck = new Set(JSON.parse(localStorage.getItem(BRIEF_ACK_KEY) || "[]"));
    } catch {
      ui.briefAck = new Set();
    }
  }
  return ui.briefAck;
}
function briefAck(id, on) {
  const s = briefAcks();
  on ? s.add(id) : s.delete(id);
  try {
    localStorage.setItem(BRIEF_ACK_KEY, JSON.stringify([...s]));
  } catch {}
  render();
}

function briefSub() {
  if (!DEMO) return "Not built yet";
  const n = brief.decisions.filter((d) => when(d.at) >= when(brief.lastLooked) && !briefAcks().has(d.id)).length;
  return n ? plural(n, "decision") + " to read" : "You're caught up";
}

function briefSince() {
  const span = ui.briefSpan || "last";
  if (span === "day") return Date.now() - 24 * 3600000;
  if (span === "week") return Date.now() - 7 * 24 * 3600000;
  return when(brief.lastLooked);
}

function briefPage() {
  if (!ui.state) return h("div", { class: "page" });
  if (!DEMO) return h("div", { class: "fx" }, h("header", { class: "fx-head" }, h("div", null, h("h1", null, "Briefing"), h("p", null, "Needs agents to mark their decisions (#183). Shown with mock data only."))));
  const since = briefSince();
  const acks = briefAcks();
  const decisions = brief.decisions.filter((d) => when(d.at) >= since);
  const unread = decisions.filter((d) => !acks.has(d.id));
  const streams = brief.streams;
  const running = sessionCount();
  const span = ui.briefSpan || "last";
  const streamOf = (id) => streams.find((s) => s.id === id);
  return h(
    "div",
    { class: "fx br" },
    h(
      "header",
      { class: "fx-head" },
      h(
        "div",
        null,
        h("h1", null, "Briefing"),
        h("p", null, span === "last" ? "Since you last looked, " + ago(since) + "." : span === "day" ? "The last 24 hours." : "The last 7 days."),
      ),
      fxSeg(
        "Since",
        [
          ["last", "Since last look"],
          ["day", "Today"],
          ["week", "This week"],
        ],
        span,
        (v) => {
          ui.briefSpan = v;
          render();
        },
      ),
    ),
    h(
      "p",
      { class: "br-lede" },
      plural(running, "agent") + " are working on " + plural(streams.length, "thing") + ". They made ",
      h("b", null, plural(decisions.length, "decision")),
      decisions.filter((d) => d.big).length ? [" (", h("b", null, decisions.filter((d) => d.big).length + (decisions.filter((d) => d.big).length === 1 ? " that changes" : " that change") + " how Murmur works"), ")"] : "",
      " between themselves" + (unread.length ? ", and " + unread.length + " of them you haven't read." : ", and you've read them all."),
      brief.unexplained.length ? [" ", h("b", null, plural(brief.unexplained.length, "agent")), brief.unexplained.length === 1 ? " isn't doing anything anyone can explain." : " aren't doing anything anyone can explain."] : "",
    ),
    h(
      "div",
      { class: "br-main" },
      h(
        "section",
        { class: "br-col" },
        h("div", { class: "br-sec-head" }, h("h2", null, "Decided by agents"), unread.length > 0 && h("span", { class: "fx-badge" }, unread.length + " new")),
        decisions.length
          ? h("ol", { class: "br-decisions" }, decisions.map((d) => briefDecision(d, streamOf(d.stream), acks.has(d.id))))
          : h("div", { class: "fx-empty" }, h("p", { class: "fx-empty-title" }, "Nothing decided since then"), h("p", null, "Agents have only been working, not settling anything between them.")),
      ),
      h(
        "aside",
        { class: "br-col br-side" },
        h("div", { class: "br-sec-head" }, h("h2", null, "What they're working on")),
        h("ul", { class: "br-streams" }, streams.map((s) => briefStream(s, decisions.filter((d) => d.stream === s.id).length))),
        brief.unexplained.length > 0 && [
          h("div", { class: "br-sec-head br-gap" }, h("h2", null, "Nobody can say")),
          h(
            "ul",
            { class: "br-streams" },
            brief.unexplained.map((u) =>
              h(
                "li",
                { class: "br-stream br-unknown" },
                h("div", { class: "br-stream-top" }, h("span", { class: "br-faces" }, avatar(u.who)), h("span", { class: "br-stream-title" }, u.who)),
                h("p", { class: "br-now" }, u.why),
                h("div", { class: "br-actions" }, briefAsk("u-" + u.who, [u.who], "Ask " + u.who + " what it's doing")),
              ),
            ),
          ),
        ],
      ),
    ),
  );
}

function briefFaces(names) {
  return h("span", { class: "br-faces" }, names.slice(0, 4).map((n) => avatar(n)));
}

function briefWho(names) {
  return names.map((n, i) => [i ? (i === names.length - 1 ? " and " : ", ") : "", h("b", null, n)]);
}

// The page can't send yet: asking would post a message from the human to those agents, which needs
// a sender for humans on the bus (#183). The box shows what it would do.
function briefAsk(key, to, label) {
  const open = ui.briefAsking === key;
  if (!open)
    return h(
      "button",
      {
        class: "br-link",
        onclick: () => {
          ui.briefAsking = key;
          render();
        },
      },
      icon("message"),
      label,
    );
  return h(
    "div",
    { class: "br-ask" },
    h("textarea", { rows: 2, placeholder: "Your question or objection", "data-key": "ask-" + key }),
    h(
      "div",
      { class: "br-ask-foot" },
      h("span", { class: "fx-faint" }, "Goes to ", to.join(" and "), " as a message from you."),
      h(
        "button",
        {
          class: "fx-btn ghost",
          onclick: () => {
            ui.briefAsking = null;
            render();
          },
        },
        "Cancel",
      ),
      h("button", { class: "fx-btn primary", disabled: true, title: "The page can't send yet" }, "Send"),
    ),
  );
}

function briefDecision(d, s, read) {
  const src = ui.messages.find((m) => m.subject === d.source);
  const open = () => {
    if (!src) return;
    ui.filter = null;
    ui.conversation = conversationOf(src).key;
    ui.conversationAnchor = src.id;
    go("messages");
  };
  return h(
    "li",
    { class: "br-decision" + (read ? " read" : "") },
    h("span", { class: "br-unread", "aria-label": read ? null : "Not read" }),
    h(
      "div",
      { class: "br-dbody" },
      h("p", { class: "br-what" }, d.what, d.big && h("span", { class: "fx-tag blue br-big" }, "Changes how Murmur works")),
      h("p", { class: "br-why" }, h("span", { class: "br-why-k" }, "Why "), d.why),
      h(
        "div",
        { class: "br-meta" },
        briefFaces(d.by),
        h("span", null, briefWho(d.by), d.broadcast ? " told everyone" : d.by.length > 1 ? " agreed" : " decided"),
        h("span", { class: "br-dot" }, "·"),
        agoEl("span", "", when(d.at)),
        s && [h("span", { class: "br-dot" }, "·"), h("span", { class: "br-chip" }, s.title)],
      ),
      h(
        "div",
        { class: "br-actions" },
        h(
          "button",
          { class: "br-link", onclick: () => briefAck(d.id, !read) },
          icon("check"),
          read ? "Read" : "Mark read",
        ),
        src && h("button", { class: "br-link", onclick: open }, icon("message"), "Read the " + (d.messages > 1 ? d.messages + " messages" : "message")),
        briefAsk(d.id, d.by, "Question this"),
      ),
    ),
  );
}

function briefStream(s, decided) {
  const prs = ui.state.prs || {};
  const tone = { ok: "on", warn: "idle", error: "dnd", off: "off" }[s.tone] || "off";
  return h(
    "li",
    { class: "br-stream" },
    h(
      "div",
      { class: "br-stream-top" },
      fxDot(tone),
      h("span", { class: "br-stream-title" }, s.title),
      h("span", { class: "br-refs" }, s.issue && h("span", { class: "br-ref" }, "#" + s.issue), s.pr && h("span", { class: "fx-pr" + (s.draft ? " draft" : "") }, "PR #" + s.pr)),
    ),
    h("p", { class: "br-now" }, s.now),
    h(
      "div",
      { class: "br-meta" },
      briefFaces(s.who),
      h("span", null, s.who.join(", ")),
      h("span", { class: "br-dot" }, "·"),
      h("span", null, plural(s.commits, "commit")),
      decided > 0 && [h("span", { class: "br-dot" }, "·"), h("span", null, plural(decided, "decision"))],
    ),
  );
}

// ---------- #183: Overview ----------
// Understanding a swarm, not auditing it. Streams of work are the loops you listen in on (mission
// control's voice loops): each shows its goal, what's done, what's happening now and what's next
// (ParallelPilot), and opens to the conversation behind it. The code map shows where each stream
// is working (FASTDash, Palantír). A narrator answers questions in plain words, with its sources
// one click away (Gas Town's Mayor, Slack's recaps).
function swStreamOf(name) {
  return swarm.streams.find((s) => s.who.includes(name)) || null;
}

// A stream's loop: what its agents said and were told since the first of them started.
function swLoop(s) {
  const starts = s.who.map((n) => when(findSession(n)?.session.started)).filter(Boolean);
  const from = starts.length ? Math.min(...starts) : 0;
  return ui.messages.filter((m) => m.sortAt >= from && (s.who.includes(fromName(m)) || (m.toKind === "session" && s.who.includes(toName(m))))).sort((a, b) => a.sortAt - b.sortAt);
}

function swOpenMessage(m) {
  if (!m) return;
  // A message picked from a view opens its conversation, whatever list mode was last used.
  ui.messageMode = "conversations";
  ui.filter = null;
  ui.conversation = conversationOf(m).key;
  ui.conversationAnchor = m.id;
  go("messages");
}

function overviewPage() {
  if (!ui.state) return h("div", { class: "page" });
  if (!DEMO) return h("div", { class: "fx" }, h("header", { class: "fx-head" }, h("div", null, h("h1", null, "Overview"), h("p", null, "Shown with mock data only, for now (#183)."))));
  const people = new Set(devices().map((d) => d.owner).filter(Boolean));
  const agents = sessionCount();
  const stuck = swarm.streams.filter((s) => s.tone === "error" || s.waiting || s.tone === "off").length;
  return h(
    "div",
    { class: "fx sw" },
    h(
      "header",
      { class: "fx-head" },
      h("div", null, h("h1", null, "Overview"), h("p", null, plural(agents, "agent") + " on " + plural(swarm.streams.length, "stream") + " of work, for " + plural(people.size, "person").replace("persons", "people") + ". " + stuck + " of the streams are held up.")),
    ),
    swAsk(),
    h(
      "div",
      { class: "sw-main" },
      h(
        "section",
        { class: "sw-col" },
        h("div", { class: "br-sec-head" }, h("h2", null, "Streams of work"), h("span", { class: "fx-faint sw-hint" }, "Listen to one to hear its agents talk")),
        h("ol", { class: "sw-streams" }, swarm.streams.map((s) => swStream(s))),
        swarm.unknown.length > 0 &&
          h(
            "div",
            { class: "sw-unknown" },
            h("h3", null, "Not part of any stream"),
            swarm.unknown.map((u) => h("div", { class: "sw-unknown-row" }, avatar(u.who), h("div", null, h("b", null, u.who), h("p", null, u.note)))),
          ),
      ),
      h("aside", { class: "sw-col" }, swMap()),
    ),
  );
}

// The narrator: questions in, plain answers out, every answer with its sources.
function swAsk() {
  const qi = ui.swQ;
  const qa = qi != null ? swarm.questions[qi] : null;
  return h(
    "section",
    { class: "sw-ask" + (qa ? " open" : "") },
    h(
      "div",
      { class: "sw-ask-bar" },
      icon("message"),
      h("input", { type: "text", placeholder: "Ask what the agents are doing, or why", "data-key": "sw-ask", value: qa ? qa.q : "" }),
      h("span", { class: "fx-faint sw-ask-by" }, "Answered by a narrator agent that reads the bus"),
    ),
    h(
      "div",
      { class: "sw-chips" },
      swarm.questions.map((x, i) =>
        h(
          "button",
          {
            class: "sw-chip" + (qi === i ? " on" : ""),
            onclick: () => {
              ui.swQ = qi === i ? null : i;
              render();
            },
          },
          x.q,
        ),
      ),
    ),
    qa &&
      h(
        "div",
        { class: "sw-answer" },
        qa.a.map((p) => h("p", null, p)),
        h(
          "div",
          { class: "sw-sources" },
          h("span", { class: "fx-faint" }, "From"),
          qa.sources.map((subj) => {
            const m = ui.messages.find((x) => x.subject === subj);
            return m && h("button", { class: "sw-source", onclick: () => swOpenMessage(m) }, avatar(fromName(m)), h("span", { class: "truncate" }, m.subject));
          }),
        ),
      ),
  );
}

function swStream(s) {
  const listening = ui.swListen === s.id;
  const loop = swLoop(s);
  const last = loop[loop.length - 1];
  const prs = ui.state.prs || {};
  const tone = { ok: "on", warn: "idle", error: "dnd", off: "off" }[s.tone] || "off";
  const doneOpen = ui.swDone === s.id;
  return h(
    "li",
    {
      class: "sw-stream" + (listening ? " listening" : ""),
      onmouseenter: () => {
        if (ui.swHover === s.id) return;
        ui.swHover = s.id;
        swLight();
      },
      onmouseleave: () => {
        ui.swHover = null;
        swLight();
      },
    },
    h(
      "div",
      { class: "sw-top" },
      fxDot(tone),
      h("span", { class: "sw-title" }, s.title),
      s.big && h("span", { class: "fx-tag blue" }, "Changes how Murmur works"),
      h("span", { class: "sw-refs" }, s.issue && h("span", { class: "br-ref" }, "#" + s.issue), s.pr && h("span", { class: "fx-pr" + (s.draft ? " draft" : "") }, "PR #" + s.pr)),
      briefFaces(s.who),
    ),
    h("p", { class: "sw-goal" }, s.goal),
    h(
      "div",
      { class: "sw-dnn" },
      h(
        "div",
        { class: "sw-cell" },
        h("span", { class: "sw-k" }, "Done", h("span", { class: "fx-faint" }, " " + s.done.length)),
        doneOpen
          ? h("ul", { class: "sw-done" }, s.done.map((d) => h("li", null, d)))
          : h("span", null, s.done[s.done.length - 1], s.done.length > 1 && h("button", { class: "sw-more", onclick: () => { ui.swDone = s.id; render(); } }, " +" + (s.done.length - 1))),
      ),
      h("div", { class: "sw-cell now" }, h("span", { class: "sw-k" }, "Now"), h("span", null, s.now)),
      h("div", { class: "sw-cell" }, h("span", { class: "sw-k" }, "Next"), h("span", null, s.next)),
    ),
    s.waiting &&
      h("div", { class: "sw-waiting" }, icon("clock"), h("span", null, h("b", null, s.waiting.who), " is waiting on ", h("b", null, s.waiting.on), " for " + s.waiting.what + ", for " + s.waiting.since + " minutes.")),
    h(
      "div",
      { class: "sw-loopbar" },
      h(
        "button",
        {
          class: "sw-listen" + (listening ? " on" : ""),
          "aria-expanded": listening ? "true" : "false",
          onclick: () => {
            ui.swListen = listening ? null : s.id;
            render();
          },
        },
        icon(listening ? "chevronDown" : "chevronRight"),
        listening ? "Stop listening" : "Listen",
        h("span", { class: "fx-faint" }, " · " + plural(loop.length, "message")),
      ),
      !listening &&
        last &&
        h(
          "button",
          { class: "sw-ticker", title: "Open this conversation", onclick: () => swOpenMessage(last) },
          h("b", null, fromName(last)),
          h("span", { class: "fx-faint" }, " → " + (last.toKind === "all" ? "everyone" : last.toKind === "device" ? "a device" : toName(last)) + ": "),
          h("span", { class: "truncate" }, (last.body || last.subject).split("\n")[0]),
          agoEl("span", "fx-faint sw-ticker-t", when(last.at)),
        ),
    ),
    listening && swTranscript(s, loop),
  );
}

// The loop at full volume: its last messages, oldest first, marking talk with other streams.
function swTranscript(s, loop) {
  const T = (n) => new Date(n).toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" });
  return h(
    "ol",
    { class: "sw-transcript" },
    loop.slice(-10).map((m) => {
      const from = fromName(m);
      const to = m.toKind === "all" ? "everyone" : m.toKind === "device" ? "a device" : toName(m);
      const other = [from, to].map(swStreamOf).find((x) => x && x.id !== s.id);
      return h(
        "li",
        { class: "sw-line", role: "button", tabindex: 0, onclick: () => swOpenMessage(m), onkeydown: (e) => (e.key === "Enter" ? swOpenMessage(m) : null) },
        h("span", { class: "sw-line-t" }, T(m.sortAt)),
        h(
          "div",
          { class: "sw-line-body" },
          h("div", { class: "sw-line-head" }, avatar(from), h("b", null, from), h("span", { class: "fx-faint" }, " → " + to), other && h("span", { class: "sw-cross", title: "Talk between two streams" }, "↔ " + other.title)),
          h("div", { class: "sw-line-subj" }, m.subject),
          m.body && h("div", { class: "sw-line-text" }, m.body.split("\n").filter(Boolean)[0]),
        ),
      );
    }),
  );
}

// Where in the code each stream is working. Hovering a stream lights its areas.
function swMap() {
  const open = ui.swArea;
  return [
    h("div", { class: "br-sec-head" }, h("h2", null, "Where in the code"), h("span", { class: "fx-faint sw-hint" }, "From each branch's changes")),
    h(
      "ul",
      { class: "sw-map" },
      swarm.areas.map((a) => {
        const who = [...new Set(a.files.flatMap(([, w]) => w))];
        const streams = [...new Set(who.map((n) => swStreamOf(n)?.id).filter(Boolean))];
        const crowded = streams.length > 1;
        const isOpen = open === a.path;
        return h(
          "li",
          { class: "sw-area" + (who.length ? "" : " empty") + (crowded ? " crowded" : "") + (isOpen ? " open" : ""), "data-who": who.join(" ") },
          h(
            "button",
            {
              class: "sw-area-head",
              disabled: !who.length,
              onclick: () => {
                ui.swArea = isOpen ? null : a.path;
                render();
              },
            },
            h("span", { class: "mono sw-path" }, a.path),
            crowded && h("span", { class: "sw-crowd" }, plural(streams.length, "stream")),
            who.length ? briefFaces(who) : h("span", { class: "fx-faint sw-none" }, "nobody"),
          ),
          isOpen &&
            h(
              "ul",
              { class: "sw-files" },
              a.files.map(([f, w]) => h("li", { class: w.length > 1 ? "shared" : "" }, h("span", { class: "mono truncate" }, f), h("span", { class: "sw-file-who" }, w.join(", ")))),
            ),
        );
      }),
    ),
    h("p", { class: "fx-faint sw-map-note" }, "Not in the code: ", h("b", null, swarm.unknown.map((u) => u.who).join(", ")), ", which hasn't changed anything we can see."),
  ];
}

// Light the areas of the stream under the pointer, without a full render.
function swLight() {
  const s = swarm.streams.find((x) => x.id === ui.swHover);
  for (const el of document.querySelectorAll(".sw-area")) {
    const who = (el.dataset.who || "").split(" ");
    el.classList.toggle("lit", !!s && s.who.some((n) => who.includes(n)));
    el.classList.toggle("dim", !!s && !s.who.some((n) => who.includes(n)));
  }
}

// ---------- #183: the swarm, six ways ----------
function swarmRow() {
  const el = topRow("fleet", "activity", "Fleet", sessionCount());
  el.setAttribute("aria-current", SWARM_VIEWS.some(([v]) => v === ui.view) ? "page" : "false");
  return el;
}

function swarmTabs(view) {
  if (SWARM_VIEWS.length < 2) return null;
  return h(
    "nav",
    { class: "sv-tabs", "aria-label": "Ways to look at the swarm" },
    SWARM_VIEWS.length > 1 && SWARM_VIEWS.map(([v, label]) => h("button", { class: v === view ? "on" : "", "aria-current": v === view ? "page" : null, "data-key": "sv-" + v, onclick: () => go(v) }, label)),
    DEMO && h("div", { class: "sv-demo fx" }, fxSeg("Demo size", [["small", "9 agents"], ["busy", "16 agents"]], BUSY ? "busy" : "small", (v) => {
      const q = new URLSearchParams(location.search);
      if (v === "busy") q.set("demo", "busy");
      else q.delete("demo");
      location.search = q.toString();
    })),
  );
}

function swarmShell(view) {
  let inner;
  if (view === "overview") inner = overviewPage();
  else if (view === "fleet") inner = fleetPage();
  else if (typeof window.MURVIEWS?.[view] === "function") {
    try {
      inner = window.MURVIEWS[view](window.MUR);
    } catch (e) {
      console.error(e);
      inner = h("div", { class: "fx" }, h("p", { class: "fx-banner" }, "This view failed: " + e.message));
    }
  } else inner = h("div", { class: "fx" }, h("p", { class: "fx-faint" }, "Loading…"));
  return h("div", { class: "sv-shell" }, swarmTabs(view), inner);
}

// What a view in its own file (assets/view-*.js) may use. Views register themselves on
// window.MURVIEWS[name] = (mur) => element, and call mur.render() once loaded.
window.MURVIEWS ||= {};
window.MUR = {
  h, icon, avatar, presence, ago, agoEl, when, stamp, plural, dayLabel,
  devices, findSession, fromName, toName, conversationOf, sessionState, onDnd, deviceDot,
  fxDot, fxSeg, briefFaces, swOpenMessage, swStreamOf, go, render,
  demo, swarm, busy: BUSY,
  get ui() { return ui; },
  get messages() { return ui.messages; },
  get state() { return ui.state; },
  history: () => fleetHistory(),
};
