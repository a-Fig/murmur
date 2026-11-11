// The v3 design of the mur ui page, built from the Figma file's "mur ui v3" screens: a page of its
// own beside the T3-derived one at /, on the same /api. Every agent gets a cat from
// /assets/cats/, picked from its name the way the Figma file picks them, so an agent keeps its cat
// wherever it shows up.
import { icon } from "../icons.js";

const NS = "http://www.w3.org/2000/svg";
const TOKEN_KEY = "murmur.v3.token";
const VIEWS = ["messages", "sessions", "settings"];
let token = "";
const root = document.getElementById("root");

// ------------------------------------------------------------------------------- DOM helpers.

function h(tag, props = {}, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (v == null || v === false) continue;
    if (k === "class") el.className = v;
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (k === "value") el.value = v;
    else el.setAttribute(k, v === true ? "" : String(v));
  }
  for (const kid of kids.flat(Infinity)) {
    if (kid == null || kid === false) continue;
    el.append(kid instanceof Node ? kid : document.createTextNode(String(kid)));
  }
  return el;
}

// Icons the shared set lacks.
function glyph(paths, className = "") {
  const svg = document.createElementNS(NS, "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("fill", "none");
  svg.setAttribute("stroke", "currentColor");
  svg.setAttribute("stroke-width", "1.75");
  svg.setAttribute("stroke-linecap", "round");
  svg.setAttribute("stroke-linejoin", "round");
  svg.setAttribute("aria-hidden", "true");
  svg.setAttribute("class", ("icon " + className).trim());
  for (const d of paths) {
    const p = document.createElementNS(NS, "path");
    p.setAttribute("d", d);
    svg.appendChild(p);
  }
  return svg;
}
const lockup = () => `/assets/v3/lockup-outline-${ui.theme === "dark" ? "cream" : "blue"}.svg`;
const botIcon = () => glyph(["M12 8V4H8", "M4 8h16v12H4z", "M2 14h2", "M20 14h2", "M15 13v2", "M9 13v2"]);
const xIcon = () => glyph(["M18 6 6 18", "m6 6 12 12"]);
const lockIcon = () => glyph(["M7 11V7a5 5 0 0 1 10 0v4", "M5 11h14v10H5z"]);

// ------------------------------------------------------------------------------- Cats.

// The cats in the Figma file's "Cat Avatar — gallery" (2026-11-11): 54 of them, no Gold. A name
// hashes (FNV-1a, then a mix so neighbouring names spread out) to one of them, so an agent keeps
// its cat everywhere. /assets/cats holds exactly these files.
const CATS = [
  "butter-plain-solid", "butter-mustache-patch", "butter-glasses-stripes", "butter-sleepy-solid", "butter-bow-patch",
  "butter-round-glasses-stripes", "butter-tears-solid", "butter-smile-patch", "butter-back-stripes", "cream-plain-patch",
  "cream-mustache-stripes", "cream-glasses-solid", "cream-sleepy-patch", "cream-bow-stripes", "cream-round-glasses-solid",
  "cream-tears-patch", "cream-smile-stripes", "cream-back-solid", "blue-plain-stripes", "blue-mustache-solid",
  "blue-glasses-patch", "blue-sleepy-stripes", "blue-bow-solid", "blue-round-glasses-patch", "blue-smile-solid",
  "blue-back-patch", "navy-plain-solid", "navy-mustache-patch", "navy-glasses-stripes", "navy-sleepy-solid",
  "navy-bow-patch", "navy-round-glasses-stripes", "navy-smile-patch", "navy-back-stripes", "sky-plain-patch",
  "sky-mustache-stripes", "sky-glasses-solid", "sky-sleepy-patch", "sky-bow-stripes", "sky-round-glasses-solid",
  "sky-tears-patch", "sky-smile-stripes", "sky-back-solid", "sky-round-glasses-stripes", "ginger-plain-solid",
  "ginger-mustache-patch", "ginger-glasses-stripes", "ginger-sleepy-solid", "ginger-bow-patch", "ginger-round-glasses-stripes",
  "ginger-tears-solid", "ginger-smile-patch", "ginger-back-stripes", "ginger-tears-stripes",
];
export function catOf(name) {
  let x = 0x811c9dc5;
  for (const ch of String(name)) x = Math.imul(x ^ ch.charCodeAt(0), 0x01000193) >>> 0;
  x ^= x >>> 16;
  x = Math.imul(x, 0x7feb352d) >>> 0;
  x ^= x >>> 15;
  x = Math.imul(x, 0x846ca68b) >>> 0;
  x ^= x >>> 16;
  return CATS[(x >>> 0) % CATS.length];
}

function initials(name) {
  return String(name || "?").slice(0, 1).toUpperCase();
}

function avatar(who, size = "", online = false) {
  const cls = ("av " + size).trim();
  let el;
  if (who.kind === "all") {
    el = h("span", { class: cls + " av-everyone" }, icon("broadcast"));
  } else if (who.kind === "person") {
    el = h("span", { class: cls + " av-person" + (who.me ? " me" : "") }, initials(who.name));
  } else {
    el = h("span", { class: cls }, h("img", { src: `/assets/cats/${catOf(who.name)}.svg`, alt: "" }));
  }
  if (online) el.append(h("span", { class: "dot" }));
  return el;
}

// ------------------------------------------------------------------------------- API.

// The token arrives once, in the URL fragment, which never reaches a server or a Referer. It is
// kept for this tab only and taken out of the address bar at once, so it isn't left in history or
// bookmarks. The rest of the fragment (the view, the conversation) stays.
function hashWithout(...names) {
  return location.hash.slice(1).split("&").filter((p) => p && !names.some((n) => p.startsWith(n + "="))).join("&");
}
function takeToken() {
  const tok = location.hash.slice(1).split("&").find((p) => p.startsWith("token="));
  if (tok) {
    let value = tok.slice(6);
    try {
      value = decodeURIComponent(value);
    } catch {}
    try {
      sessionStorage.setItem(TOKEN_KEY, value);
    } catch {}
    history.replaceState(null, "", location.pathname + location.search + "#" + (hashWithout("token") || "messages"));
    return value;
  }
  try {
    return sessionStorage.getItem(TOKEN_KEY) || "";
  } catch {
    return "";
  }
}

// A page opened from another repo's page arrives with a one-time key, not the token, and trades it
// with its own daemon. The key is spent either way, so it leaves the address bar first.
async function takeKey() {
  const k = location.hash.slice(1).split("&").find((p) => p.startsWith("key="));
  if (!k) return;
  history.replaceState(null, "", location.pathname + location.search + "#" + (hashWithout("key", "theme") || "messages"));
  try {
    const res = await fetch("/api/key", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ key: k.slice(4) }), cache: "no-store" });
    const got = res.ok ? await res.json() : null;
    if (got?.token) sessionStorage.setItem(TOKEN_KEY, got.token);
  } catch {}
}

async function api(path, opts = {}) {
  const headers = { Authorization: "Bearer " + token };
  if (opts.body) headers["Content-Type"] = "application/json";
  // A tab nobody can see still polls, but mustn't keep the daemon running for good.
  if (document.hidden) headers["X-Page-Hidden"] = "1";
  const res = await fetch(path, { ...opts, headers, cache: "no-store" });
  if (res.status === 401) throw Object.assign(new Error("unauthorized"), { status: 401 });
  if (!res.ok) {
    let msg = res.statusText;
    try {
      msg = (await res.json()).error || msg;
    } catch {}
    throw new Error(msg);
  }
  return res.status === 204 ? null : res.json();
}

// ------------------------------------------------------------------------------- State.

const ui = {
  state: null,
  messages: [],
  cursor: "",
  pending: [],
  repos: null,
  view: "messages", // messages | sessions | settings
  conv: "all", // conversation key
  profile: null, // { kind: "agent" | "person", name }
  tab: "all", // all | direct | mine
  query: "",
  collapsed: load("v3.collapsed") === "1",
  themePref: load("v3.theme") || "system", // light | dark | system
  theme: "light", // what themePref comes to now
  settings: undefined, // the daemon's settings, once loaded
  perSession: false,
  addingTag: "", // the agent whose tag is being typed
  sessionsFilter: "running", // running | quiet | settled
  sessionsTag: "",
  sessionsQuery: "",
  folded: {},
  menu: null, // "repo" | "members"
  dialog: false,
  error: "",
  drafts: {}, // conversation key → what is typed there
};

function load(k) {
  try {
    return localStorage.getItem(k);
  } catch {
    return null;
  }
}
function save(k, v) {
  try {
    localStorage.setItem(k, v);
  } catch {}
}

function me() {
  return ui.state?.account?.name || "you";
}
function sessions() {
  return (ui.state?.devices || []).flatMap((d) => (d.sessions || []).map((s) => ({ ...s, device: d })));
}
function people() {
  const out = [];
  for (const d of ui.state?.devices || []) {
    const name = d.person?.name || d.owner;
    if (name && !out.some((p) => p.name === name)) out.push({ name, device: d, me: name === me(), lastActive: d.person?.lastActive || d.lastSeen });
  }
  return out;
}
function others() {
  return people().filter((p) => !p.me);
}
function sessionNamed(name) {
  return sessions().find((s) => s.name === name);
}
function isPerson(name) {
  return people().some((p) => p.name === name);
}
function whoOf(name) {
  if (name === "*" || name === "Everyone") return { kind: "all", name: "Everyone" };
  if (isPerson(name)) return { kind: "person", name, me: name === me() };
  return { kind: "agent", name };
}
function online(name) {
  const s = sessionNamed(name);
  if (s) return s.device.reachable && Date.now() - Date.parse(s.lastActive) < 30 * 60000;
  const p = people().find((x) => x.name === name);
  return !!p && p.device.reachable;
}

const fromName = (m) => m.from;
const toName = (m) => (m.toKind === "all" ? "*" : m.to);
function conversationOf(m) {
  if (m.toKind === "all") return { key: "all", kind: "all", title: "Everyone" };
  if (m.toKind === "device") return { key: "device:" + m.to, kind: "device", title: m.to };
  const pair = [...new Set([fromName(m), toName(m)])].sort();
  return { key: "dm:" + JSON.stringify(pair), kind: "dm", title: pair.join(" & "), pair };
}
function allMessages() {
  return [...ui.messages, ...ui.pending].sort((a, b) => Date.parse(a.at) - Date.parse(b.at));
}
function conversations() {
  const map = new Map();
  map.set("all", { key: "all", kind: "all", title: "Everyone", messages: [] });
  for (const m of allMessages()) {
    const c = conversationOf(m);
    if (!map.has(c.key)) map.set(c.key, { ...c, messages: [] });
    map.get(c.key).messages.push(m);
  }
  return [...map.values()];
}
function lastAt(c) {
  const m = c.messages[c.messages.length - 1];
  return m ? Date.parse(m.at) : 0;
}
function mine(c) {
  return c.kind === "all" || (c.pair || []).includes(me());
}
function pairOf(key) {
  try {
    const pair = JSON.parse(key.slice(3));
    return Array.isArray(pair) && pair.length && pair.every((n) => typeof n === "string") ? pair : null;
  } catch {
    return null;
  }
}
function safeConv(key) {
  if (key === "all" || key.startsWith("device:")) return key;
  return key.startsWith("dm:") && pairOf(key) ? key : "all";
}
function dmKey(a, b) {
  return "dm:" + JSON.stringify([...new Set([a, b])].sort());
}

// ------------------------------------------------------------------------------- Time.

function ago(iso) {
  const t = Date.parse(iso);
  if (!t || t < 86400000) return "a while";
  const s = Math.max(0, (Date.now() - t) / 1000);
  if (s < 60) return "now";
  if (s < 3600) return Math.floor(s / 60) + "m";
  if (s < 86400) return Math.floor(s / 3600) + "h";
  return Math.floor(s / 86400) + "d";
}
function clock(iso) {
  return new Date(iso).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
}
function dayLabel(iso) {
  const d = new Date(iso);
  const today = new Date();
  const y = new Date(Date.now() - 86400000);
  if (d.toDateString() === today.toDateString()) return "Today";
  if (d.toDateString() === y.toDateString()) return "Yesterday";
  return d.toLocaleDateString([], { weekday: "long", month: "short", day: "numeric" });
}

// ------------------------------------------------------------------------------- Routing.

function go(patch) {
  if (patch.view === "settings" && ui.view !== "settings") loadSettings();
  Object.assign(ui, { menu: null }, patch);
  const parts = [ui.view];
  if (ui.view === "messages" && ui.conv !== "all") parts.push("c=" + encodeURIComponent(ui.conv));
  if (ui.profile) parts.push("p=" + ui.profile.kind + ":" + encodeURIComponent(ui.profile.name));
  history.replaceState(null, "", "#" + parts.join("&"));
  render();
}
function readHash() {
  const q = new URLSearchParams(location.hash.slice(1));
  const first = location.hash.slice(1).split("&")[0];
  if (VIEWS.includes(first)) ui.view = first;
  if (q.get("c")) ui.conv = safeConv(q.get("c"));
  const p = q.get("p");
  if (p) {
    const [kind, ...rest] = p.split(":");
    ui.profile = { kind, name: rest.join(":") };
  }
}

// ------------------------------------------------------------------------------- Sidebar.

function sidebar() {
  if (ui.collapsed) return rail();
  const s = ui.state;
  const unread = (s.account?.unread || []).length;
  const devices = (s.devices || []).filter((d) => (d.sessions || []).length);
  return h(
    "aside",
    { class: "sidebar" },
    h(
      "div",
      { class: "side-top" },
      h(
        "div",
        { class: "brand" },
        h("img", { src: lockup(), alt: "Murmur" }),
        h("button", { class: "icon-btn", title: "Collapse sidebar", "aria-label": "Collapse sidebar", onclick: () => setCollapsed(true) }, icon("panel")),
      ),
      h(
        "button",
        { class: "repo-btn", "aria-expanded": ui.menu === "repo" ? "true" : "false", onclick: (e) => { e.stopPropagation(); toggleRepoMenu(); } },
        h("span", { class: "run" }),
        h("span", { class: "name truncate" }, s.repo),
        ui.repos && h("span", { class: "n" }, `${ui.repos.length} repo${ui.repos.length === 1 ? "" : "s"}`),
        icon("chevronDown"),
      ),
    ),
    h(
      "nav",
      { class: "nav" },
      navItem("messages", icon("message"), "Messages", unread ? unread + " new" : null),
      navItem("sessions", icon("users"), "Sessions", sessions().length),
    ),
    h(
      "div",
      { class: "roster" },
      h("div", { class: "group-head" }, "People", h("span", { class: "n" }, others().length)),
      others().map((p) => personRow(p)),
      h("div", { class: "group-head" }, "Agents", h("span", { class: "n" }, sessions().length)),
      devices.map((d) => {
        const open = ui.folded[d.id] == null ? d.reachable : !ui.folded[d.id];
        const n = d.sessions.length;
        return [
          h(
            "button",
            { class: "device-head", "aria-expanded": String(open), onclick: () => { ui.folded[d.id] = open; render(); } },
            h("span", { class: "fold" + (open ? " open" : "") }, icon("chevronRight")),
            h("b", {}, d.self ? "This device" : d.owner || d.host),
            h("span", { class: "host truncate" }, d.host),
            h("span", { class: "n" }, d.self ? `you · ${n}` : d.reachable ? n : `offline · ${n}`),
          ),
          open && d.sessions.map((x) => agentRow(x, d)),
        ];
      }),
    ),
    h(
      "div",
      { class: "account" },
      h(
        "button",
        { class: "account-card" + (ui.view === "settings" ? " on" : ""), onclick: () => go({ view: "settings", profile: null }) },
        avatar({ kind: "person", name: me(), me: true }, "", true),
        h("span", { class: "text" }, h("span", { class: "name" }, me()), h("span", { class: "sub" }, "you · human")),
        icon("settings"),
      ),
    ),
  );
}

function navItem(view, ic, label, extra) {
  const on = ui.view === view && !ui.profile;
  return h(
    "button",
    { class: "nav-item" + (on ? " on" : ""), onclick: () => go({ view, profile: null }) },
    ic,
    label,
    extra && h("span", { class: "count" }, extra),
  );
}

function personRow(p) {
  const on = ui.profile?.kind === "person" && ui.profile.name === p.name;
  return h(
    "button",
    { class: "row" + (on ? " on" : ""), onclick: () => openProfile("person", p.name) },
    avatar({ kind: "person", name: p.name, me: p.me }, "", p.device.reachable),
    h(
      "span",
      { class: "text" },
      h("span", { class: "name truncate" }, p.me ? p.name + " (you)" : p.name),
      h("span", { class: "meta" }, icon("user"), h("span", { class: "truncate" }, "human · " + (p.device.reachable ? "active " + ago(p.lastActive) + " ago" : "away"))),
    ),
  );
}

function agentRow(x, d) {
  const on = ui.profile?.kind === "agent" && ui.profile.name === x.name;
  const pr = prFor(x);
  return h(
    "button",
    { class: "row" + (on ? " on" : "") + (d.reachable ? "" : " offline"), onclick: () => openProfile("agent", x.name) },
    avatar({ kind: "agent", name: x.name }, "", online(x.name)),
    h(
      "span",
      { class: "text" },
      h("span", { class: "name truncate" }, x.name),
      h("span", { class: "sub truncate" }, x.doing || x.agent),
      h(
        "span",
        { class: "meta" },
        icon("branch"),
        h("span", { class: "truncate" }, x.branch || "no branch"),
        pr && h("span", { class: "chip pr" }, "#" + pr.number),
      ),
    ),
  );
}

function prFor(x) {
  const prs = ui.state?.prs || {};
  return prs[x.branch] || (x.committed || []).map((b) => prs[b]).find(Boolean) || null;
}

function rail() {
  const unread = (ui.state.account?.unread || []).length;
  const btn = (title, on, onclick, ...kids) =>
    h("button", { class: "rail-btn" + (on ? " on" : ""), title, "aria-label": title, onclick }, ...kids);
  return h(
    "aside",
    { class: "rail" },
    h("button", { class: "rail-mark", title: "Expand sidebar", "aria-label": "Expand sidebar", onclick: () => setCollapsed(false) }, h("img", { src: "/assets/v3/mark-blue.svg", alt: "" })),
    btn("Messages", ui.view === "messages" && !ui.profile, () => go({ view: "messages", profile: null }), icon("message"), unread ? h("span", { class: "badge" }, unread) : null),
    btn("Sessions", ui.view === "sessions", () => go({ view: "sessions", profile: null }), icon("users")),
    h("div", { class: "rule" }),
    h(
      "div",
      { class: "rail-faces" },
      others().map((p) => btn(p.name, ui.profile?.name === p.name, () => openProfile("person", p.name), avatar({ kind: "person", name: p.name }, "", p.device.reachable))),
      others().length > 0 && h("div", { class: "rule" }),
      sessions().map((x) => btn(x.name, ui.profile?.name === x.name, () => openProfile("agent", x.name), avatar({ kind: "agent", name: x.name }, "", online(x.name)))),
    ),
    btn("Settings", ui.view === "settings", () => go({ view: "settings", profile: null }), icon("settings")),
    btn("You: " + me(), false, () => go({ view: "settings", profile: null }), avatar({ kind: "person", name: me(), me: true }, "", true)),
  );
}

function setCollapsed(v) {
  ui.collapsed = v;
  save("v3.collapsed", v ? "1" : "0");
  render();
}

function openProfile(kind, name) {
  const latest = conversations()
    .filter((c) => (c.pair || []).includes(name))
    .sort((a, b) => lastAt(b) - lastAt(a))[0];
  go({ view: "messages", profile: { kind, name }, conv: latest ? latest.key : dmKey(me(), name), query: "" });
}

// Whose side a conversation row is told from: the open profile's, else yours.
function viewer() {
  return ui.profile?.name || me();
}

// ------------------------------------------------------------------------------- Repo menu.

async function toggleRepoMenu() {
  if (ui.menu === "repo") return go({});
  ui.menu = "repo";
  render();
  if (!ui.repos) {
    try {
      ui.repos = (await api("/api/repos")).repos || [];
    } catch {
      ui.repos = [];
    }
    if (ui.menu === "repo") render();
  }
}

function repoMenu() {
  const repos = ui.repos;
  return h(
    "div",
    { class: "popover repo-menu", onclick: (e) => e.stopPropagation() },
    h("div", { class: "menu-title" }, "Repos on this machine"),
    repos == null
      ? h("div", { class: "hint" }, "Loading…")
      : repos.map((r) =>
          h(
            "button",
            { class: "item" + (r.current ? " on" : ""), onclick: () => openRepo(r) },
            h("span", { class: r.running || r.current ? "run" : "stopped" }),
            h("span", { class: "text" }, h("span", { class: "name truncate" }, r.name), h("span", { class: "sub truncate" }, r.path + (r.running || r.current ? "" : " · not running, opening starts it"))),
            r.current && icon("check"),
          ),
        ),
    h("div", { class: "rule" }),
    h("div", { class: "hint" }, "Add a repo: run ", h("code", {}, "mur join"), " in it"),
  );
}

async function openRepo(r) {
  if (r.current) return go({});
  try {
    const { url } = await api("/api/repos/open", { method: "POST", body: JSON.stringify({ id: r.id }) });
    if (url) location.href = url.replace(/\/(#|$)/, "/assets/v3/page.html$1");
  } catch (e) {
    ui.error = e.message;
    go({});
  }
}

// ------------------------------------------------------------------------------- Conversations card.

function convsCard() {
  const all = conversations();
  const p = ui.profile;
  let list = p ? all.filter((c) => c.kind === "all" || (c.pair || []).includes(p.name)) : all.filter((c) => c.kind !== "all");
  if (ui.tab === "direct") list = list.filter((c) => c.kind === "dm");
  if (ui.tab === "mine") list = list.filter(mine);
  const q = ui.query.trim().toLowerCase();
  if (q) list = list.filter((c) => c.title.toLowerCase().includes(q) || c.messages.some((m) => (m.subject + " " + m.body).toLowerCase().includes(q)));
  list.sort((a, b) => lastAt(b) - lastAt(a));
  const everyone = all.find((c) => c.key === "all");

  return h(
    "section",
    { class: "card convs" },
    p ? profileHead(p) : h(
      "div",
      { class: "convs-head" },
      h("div", { class: "title-row" }, h("h2", {}, "Messages"), h("button", { class: "icon-btn", title: "New message", "aria-label": "New message", onclick: openDialog }, icon("compose"))),
      h("span", { class: "sub" }, `${all.length} conversations` + ((ui.state.account?.unread || []).length ? ` · ${ui.state.account.unread.length} new` : "")),
    ),
    h(
      "label",
      { class: "search" },
      icon("search"),
      h("input", {
        type: "search",
        placeholder: "Search conversations",
        value: ui.query,
        oninput: (e) => { ui.query = e.target.value; renderKeepFocus(e.target); },
      }),
    ),
    h(
      "div",
      { class: "conv-scroll" },
      !p && [
        h("div", { class: "section-label" }, h("span", {}, "Group"), h("span", {}, "pinned")),
        h("div", { class: "list" }, convRow(everyone)),
      ],
      h(
        "div",
        { class: "tabs", role: "tablist" },
        ["all", "direct", "mine"].map((t) =>
          h("button", { role: "tab", class: ui.tab === t ? "on" : "", "aria-selected": String(ui.tab === t), onclick: () => go({ tab: t }) }, t[0].toUpperCase() + t.slice(1)),
        ),
      ),
      list.length
        ? h("div", { class: "list" }, list.map(convRow))
        : h("div", { class: "empty" }, q ? "Nothing matches." : p ? `No chats with ${p.name} yet.` : "No direct messages yet."),
    ),
  );
}

function profileHead(p) {
  const back = h("button", { class: "back-link", onclick: () => go({ profile: null, conv: "all" }) }, icon("chevronRight"), "All conversations");
  const msgBtn = h("button", { class: "outline-btn", onclick: () => go({ conv: dmKey(me(), p.name) }) }, icon("message"), "Message");
  const status = (on, last) =>
    h("div", { class: "status" }, h("span", { class: on ? "on-dot" : "off-dot" }), (on ? "Online" : "Away") + (last ? " · " + ago(last) + " ago" : ""));
  if (p.kind === "person") {
    const person = people().find((x) => x.name === p.name);
    const agents = sessions().filter((x) => x.device.owner === p.name).map((x) => x.name);
    return h(
      "div",
      { class: "profile" },
      back,
      h(
        "div",
        { class: "identity" },
        avatar({ kind: "person", name: p.name, me: p.name === me() }, "lg", person?.device.reachable),
        h("div", { class: "who" }, h("h2", {}, p.name, h("span", { class: "chip human" }, "human")), status(person?.device.reachable, person?.lastActive)),
        p.name !== me() && msgBtn,
      ),
      h(
        "div",
        { class: "meta" },
        icon("laptop"),
        [person?.device.host, agents.length ? `${agents.length} agent${agents.length === 1 ? "" : "s"}: ${agents.join(", ")}` : "no agents"].filter(Boolean).join(" · "),
      ),
    );
  }
  const x = sessionNamed(p.name);
  const pr = x && prFor(x);
  const tags = (x?.tags || []).map((t) => t.name);
  return h(
    "div",
    { class: "profile" },
    back,
    h(
      "div",
      { class: "identity" },
      avatar({ kind: "agent", name: p.name }, "lg", online(p.name)),
      h("div", { class: "who" }, h("h2", { class: "truncate" }, p.name), status(online(p.name), x?.lastActive)),
      msgBtn,
    ),
    x?.doing && h("p", { class: "doing" }, x.doing),
    x &&
      h(
        "div",
        { class: "meta" },
        [x.agent, modelOf(x)].filter(Boolean).join(" · ") + " ·",
        icon("branch"),
        x.branch || "no branch",
        pr && h("span", { class: "pr-chip" }, "#" + pr.number + (pr.draft ? " draft" : "")),
      ),
    x &&
      h(
        "div",
        { class: "tags" },
        tags.map((t) =>
          h("span", { class: "tag" }, t, h("button", { class: "tag-x", title: "Remove " + t, "aria-label": "Remove tag " + t, onclick: () => changeTags(x, [], [t]) }, "×")),
        ),
        ui.addingTag === x.name
          ? h("input", {
              class: "tag-input",
              "aria-label": "New tag",
              placeholder: "tag",
              onkeydown: (e) => {
                if (e.key === "Enter" && e.target.value.trim()) changeTags(x, [e.target.value.trim()], []);
                if (e.key === "Escape") { e.stopPropagation(); ui.addingTag = ""; render(); }
              },
              onblur: () => { ui.addingTag = ""; render(); },
            })
          : h("button", { class: "tag-add", onclick: () => { ui.addingTag = x.name; render(); document.querySelector(".tag-input")?.focus(); } }, "+ Tag"),
      ),
  );
}

async function changeTags(x, add, remove) {
  ui.addingTag = "";
  try {
    await api("/api/tags", { method: "POST", body: JSON.stringify({ addr: x.addr, add, remove }) });
    await pollState();
  } catch (e) {
    ui.error = "Couldn't change tags: " + e.message;
  }
  render();
}

function convRow(c) {
  const last = c.messages[c.messages.length - 1];
  const on = ui.conv === c.key;
  let face;
  let title = c.title;
  if (c.kind === "all") face = avatar({ kind: "all" });
  else if (c.kind === "device") face = avatar({ kind: "person", name: c.title });
  else {
    const others = c.pair.filter((n) => n !== viewer());
    if (c.pair.includes(viewer()) && others.length === 1) {
      face = avatar(whoOf(others[0]), "", online(others[0]));
      title = others[0];
    } else face = avatar(whoOf(c.pair[0]), "", online(c.pair[0]));
  }
  const preview = last ? (last.from === me() ? "you" : last.from) + ": " + (last.subject || last.body) : "Everyone in the repo";
  return h(
    "button",
    { class: "row conv-row" + (c.kind === "all" && !ui.profile ? " pinned" : "") + (on && (c.kind !== "all" || ui.profile) ? " on" : ""), onclick: () => go({ conv: c.key }) },
    face,
    h(
      "span",
      { class: "text" },
      h("span", { class: "line" }, h("span", { class: "name truncate" }, title), last && h("span", { class: "time" }, ago(last.at))),
      h("span", { class: "sub truncate" }, preview),
    ),
  );
}

// ------------------------------------------------------------------------------- Thread card.

function threadCard() {
  const all = conversations();
  let c = all.find((x) => x.key === ui.conv);
  const pair = !c && ui.conv.startsWith("dm:") && pairOf(ui.conv);
  if (pair) c = { key: ui.conv, kind: "dm", title: pair.join(" & "), pair, messages: [] };
  if (!c) c = all[0];
  const canSend = c.kind === "all" || (c.pair || []).includes(me());

  return h(
    "section",
    { class: "card thread" },
    threadHead(c),
    messageList(c),
    h("div", { class: "composer" }, canSend ? composer(c) : readonlyBar(c)),
    ui.menu === "members" && membersPopover(),
  );
}

function threadHead(c) {
  if (c.kind === "all") {
    const n = sessions().length;
    return h(
      "header",
      { class: "thread-head" },
      h("span", { class: "tile" }, icon("broadcast")),
      h(
        "div",
        { class: "title" },
        h("h2", {}, "Everyone"),
        h(
          "div",
          { class: "sub" },
          h(
            "button",
            { class: "counts", "aria-expanded": ui.menu === "members" ? "true" : "false", onclick: (e) => { e.stopPropagation(); go({ menu: ui.menu === "members" ? null : "members" }); } },
            ui.state.repo,
            h("span", { class: "sep" }),
            h("span", { class: "pair-n" }, String(n), botIcon()),
            h("span", { class: "pair-n" }, String(people().length), icon("user")),
          ),
        ),
      ),
    );
  }
  const others = (c.pair || []).filter((n) => n !== me());
  const direct = (c.pair || []).includes(me()) && others.length === 1;
  const face = direct ? avatar(whoOf(others[0]), "", online(others[0])) : h("span", { class: "pair" }, (c.pair || []).map((n) => avatar(whoOf(n), "", online(n))));
  const name = direct ? others[0] : c.title;
  const x = direct && sessionNamed(others[0]);
  const kinds = (c.pair || []).map((n) => (isPerson(n) ? "person" : "agent"));
  const what = kinds.every((k) => k === "agent") ? "between 2 agents" : kinds.every((k) => k === "person") ? "between 2 people" : "a person and their agent";
  return h(
    "header",
    { class: "thread-head" },
    h("span", { class: "head-face" }, face),
    h(
      "div",
      { class: "title" },
      h("h2", {}, name),
      h(
        "div",
        { class: "sub" },
        x ? [icon("branch"), x.branch || "no branch", " · ", x.agent, " · ", online(x.name) ? "online" : "active " + ago(x.lastActive) + " ago"]
          : direct ? "Direct message" : [ui.state.repo, h("span", { class: "sep" }), what, h("span", { class: "sep" }), "read only"],
      ),
    ),
  );
}

function messageList(c) {
  const box = h("div", { class: "messages", "aria-live": "polite", "data-key": c.key });
  if (!c.messages.length) {
    const other = (c.pair || []).find((n) => n !== me());
    const x = other && sessionNamed(other);
    box.append(
      h(
        "div",
        { class: "intro" },
        c.kind === "all" ? avatar({ kind: "all" }, "xl") : avatar(whoOf(other || c.title), "xl"),
        h("h3", {}, c.kind === "all" ? "Everyone" : other || c.title),
        h("p", {}, c.kind === "all" ? "A message here reaches every agent and person in the repo." : x ? x.doing || "No messages yet." : "No messages yet."),
        x && h("div", { class: "meta" }, icon("branch"), x.branch || "no branch", " · ", x.agent),
      ),
    );
    return box;
  }
  let day = "";
  let prev = null;
  for (const m of c.messages) {
    const d = dayLabel(m.at);
    if (d !== day) {
      box.append(h("div", { class: "day" }, d));
      day = d;
      prev = null;
    }
    const cont = prev && prev.from === m.from && Date.parse(m.at) - Date.parse(prev.at) < 5 * 60000;
    box.append(m.from === me() ? ownMessage(m) : otherMessage(m, cont));
    prev = m;
  }
  return box;
}

function otherMessage(m, cont) {
  const x = sessionNamed(m.from);
  const person = !x && people().find((p) => p.name === m.from);
  const meta = x ? `${x.agent} on ${x.device.host} · ${clock(m.at)}` : person ? `human on ${person.device.host} · ${clock(m.at)}` : clock(m.at);
  if (cont) {
    return h("div", { class: "msg cont" }, h("span", { class: "gutter" }), h("div", { class: "body-col" }, m.subject && h("div", { class: "subject" }, m.subject), m.body && h("div", { class: "text" }, m.body)));
  }
  return h(
    "div",
    { class: "msg" },
    h("button", { title: m.from, onclick: () => openProfile(isPerson(m.from) ? "person" : "agent", m.from) }, avatar(whoOf(m.from))),
    h(
      "div",
      { class: "body-col" },
      h("div", { class: "head" }, h("span", { class: "sender" }, m.from), h("span", { class: "when" }, meta)),
      m.subject && h("div", { class: "subject" }, m.subject),
      m.body && h("div", { class: "text" }, m.body),
    ),
  );
}

function ownMessage(m) {
  return h(
    "div",
    { class: "own" + (m.sending ? " sending" : "") + (m.failed ? " failed" : "") },
    h("span", { class: "when" }, m.sending ? "Sending…" : clock(m.at) + " · you"),
    h("div", { class: "bubble" }, m.subject && m.body ? [h("span", { class: "subject" }, m.subject), "\n", m.body] : m.subject || m.body),
    m.failed && h("span", { class: "err" }, "Not sent: " + m.failed),
  );
}

function composer(c) {
  const to = c.kind === "all" ? "*" : c.pair.find((n) => n !== me()) || c.pair[0];
  const label = c.kind === "all" ? "Message everyone" : "Message " + to;
  const ta = h("textarea", {
    rows: "1",
    placeholder: "Type here...",
    "aria-label": label,
    value: ui.drafts[c.key] || "",
    oninput: (e) => {
      ui.drafts[c.key] = e.target.value;
      grow(e.target);
      send.disabled = !ui.drafts[c.key].trim();
    },
    onkeydown: (e) => {
      if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
        e.preventDefault();
        submit(to, c.key);
      }
    },
  });
  const send = h("button", { class: "send", title: "Send", "aria-label": "Send", disabled: !(ui.drafts[c.key] || "").trim(), onclick: () => submit(to, c.key) }, icon("arrowUp"));
  requestAnimationFrame(() => grow(ta));
  return h("div", { class: "pill" }, ta, send);
}

function grow(ta) {
  ta.style.height = "24px";
  ta.style.height = Math.min(ta.scrollHeight, 160) + "px";
}

async function submit(to, key) {
  const text = (ui.drafts[key] || "").trim();
  if (!text) return;
  const local = {
    id: "local-" + Date.now(),
    at: new Date().toISOString(),
    from: me(),
    to,
    toKind: to === "*" ? "all" : "session",
    subject: "",
    body: text,
    sending: true,
  };
  ui.pending.push(local);
  ui.drafts[key] = "";
  render();
  try {
    const sent = await api("/api/send", { method: "POST", body: JSON.stringify({ to, text }) });
    local.sending = false;
    local.sentId = sent?.id || "";
    if (sent?.note) ui.error = sent.note;
    await pollMessages();
  } catch (e) {
    local.sending = false;
    local.failed = e.message;
  }
  render();
  document.querySelector(".pill textarea")?.focus();
}

function readonlyBar(c) {
  if (!c.pair) return h("div", { class: "pill readonly" }, icon("eyeOff"), h("span", { class: "note" }, `A message to every session on ${c.title}. You can read it here.`));
  return h(
    "div",
    { class: "pill readonly" },
    icon("eyeOff"),
    h("span", { class: "note" }, `Only ${c.pair.join(" and ")} write here.`),
    c.pair.map((n) =>
      h("button", { class: "outline-btn", onclick: () => go({ profile: null, conv: dmKey(me(), n) }) }, avatar(whoOf(n), "xs", online(n)), "Message " + n),
    ),
  );
}

function membersPopover() {
  const item = (who, name, sub, onclick, dim) =>
    h("button", { class: "item" + (dim ? " dim" : ""), onclick }, who, h("span", { class: "text" }, h("span", { class: "name truncate" }, name), h("span", { class: "sub truncate" }, sub)));
  const stateText = (x) => {
    const st = stateOf(x);
    if (st === "off") return `offline ${ago(x.device.lastSeen || x.lastActive)} · messages wait until it's back`;
    return { working: "working", dnd: "do not disturb" }[st] || "idle " + ago(x.lastActive);
  };
  return h(
    "div",
    { class: "popover members", role: "dialog", "aria-label": "Everyone in this repo", onclick: (e) => e.stopPropagation() },
    h("div", { class: "menu-title plain" }, "Everyone in this repo"),
    h("div", { class: "sec" }, "People", h("span", {}, "· " + people().length)),
    people().map((p) =>
      item(
        avatar({ kind: "person", name: p.name, me: p.me }, "md2", p.device.reachable),
        p.name,
        p.me ? "you · human" : "human · " + (p.device.reachable ? "active " + ago(p.lastActive) + " ago" : "away"),
        () => openProfile("person", p.name),
      ),
    ),
    (ui.state.devices || [])
      .filter((d) => (d.sessions || []).length)
      .map((d) => [
        h("div", { class: "sec" }, d.self ? "This device" : d.owner || d.host, h("i", {}, d.host), h("span", {}, "· " + d.sessions.length)),
        d.sessions.map((s) => {
          const x = { ...s, device: d };
          return item(avatar({ kind: "agent", name: x.name }, "md2", online(x.name)), x.name, stateText(x), () => openProfile("agent", x.name), !d.reachable);
        }),
      ]),
  );
}

// ------------------------------------------------------------------------------- Sessions page.

// The frame "v3 · Sessions": a filter field, Running / Quiet / Settled, a row of tag chips, then
// each device's sessions as cards with branch, PR, chats and state columns.
const QUIET_AFTER = 24 * 3600000; // idle a day: quiet, out of the way

function phaseOf(x) {
  if (!x.device.reachable) return "settled";
  return Date.now() - Date.parse(x.lastActive) > QUIET_AFTER ? "quiet" : "running";
}

function stateOf(x) {
  if (!x.device.reachable) return "off";
  if ((x.tags || []).some((t) => t.name === "do-not-disturb")) return "dnd";
  return Date.now() - Date.parse(x.lastActive) < 5 * 60000 ? "working" : "idle";
}

function stateBadge(x) {
  const st = stateOf(x);
  const since = ago(st === "off" ? x.device.lastSeen || x.lastActive : x.lastActive);
  const text = { working: "Working", idle: "Idle " + since, dnd: "Do not disturb", off: "Offline " + since }[st];
  return h("span", { class: "state-badge " + st }, text);
}

function modelOf(x) {
  const t = (x.tags || []).find((t) => t.name.startsWith("model:"));
  return t ? t.name.slice(6) : "";
}

function sessionsPage() {
  const all = sessions();
  const counts = { running: 0, quiet: 0, settled: 0 };
  for (const x of all) counts[phaseOf(x)]++;
  const tags = [...new Set(all.flatMap((x) => (x.tags || []).map((t) => t.name)))].sort();
  const q = (ui.sessionsQuery || "").trim().toLowerCase();
  const keep = (x) => {
    const p = phaseOf(x);
    // Running is the everyday view: it keeps devices that dropped off in sight, dimmed.
    if (ui.sessionsFilter === "quiet" ? p !== "quiet" : ui.sessionsFilter === "settled" ? p !== "settled" : p === "quiet") return false;
    if (ui.sessionsTag && !(x.tags || []).some((t) => t.name === ui.sessionsTag)) return false;
    if (q && ![x.name, x.branch, x.doing, x.agent, ...(x.tags || []).map((t) => t.name)].join(" ").toLowerCase().includes(q)) return false;
    return true;
  };
  const devices = (ui.state.devices || [])
    .map((d) => ({ d, list: (d.sessions || []).map((s) => ({ ...s, device: d })).filter(keep) }))
    .filter((g) => g.list.length);
  const ndev = (ui.state.devices || []).length;

  return h(
    "section",
    { class: "card page sessions-page" },
    h(
      "div",
      { class: "sessions-head" },
      h("div", { class: "title" }, h("h1", {}, "Sessions"), h("p", {}, `${counts.running} session${counts.running === 1 ? "" : "s"} running on ${ndev} device${ndev === 1 ? "" : "s"}`)),
      h(
        "label",
        { class: "filter" },
        icon("search"),
        h("input", {
          type: "search",
          placeholder: "Filter by name, branch, tag",
          "aria-label": "Filter sessions",
          value: ui.sessionsQuery || "",
          oninput: (e) => { ui.sessionsQuery = e.target.value; renderKeepFocus(e.target); },
        }),
      ),
      h(
        "div",
        { class: "phase-tabs", role: "tablist" },
        [["running", "Running"], ["quiet", "Quiet"], ["settled", "Settled"]].map(([k, label]) =>
          h(
            "button",
            { role: "tab", class: ui.sessionsFilter === k ? "on" : "", "aria-selected": String(ui.sessionsFilter === k), onclick: () => go({ sessionsFilter: k }) },
            h("b", {}, label),
            h("span", {}, counts[k]),
          ),
        ),
      ),
    ),
    tags.length > 0 &&
      h(
        "div",
        { class: "tag-filter" },
        icon("tag"),
        [["", "All"], ...tags.map((t) => [t, t])].map(([v, label]) =>
          h("button", { class: (ui.sessionsTag || "") === v ? "on" : "", "aria-pressed": String((ui.sessionsTag || "") === v), onclick: () => go({ sessionsTag: v }) }, label),
        ),
      ),
    devices.length
      ? h(
          "div",
          { class: "device-list" },
          devices.map(({ d, list }) =>
            h(
              "div",
              { class: "device" },
              h(
                "div",
                { class: "dev-head" },
                h("span", { class: d.reachable ? "ok" : "off" }),
                h("b", {}, d.self ? "This device" : d.owner || d.host),
                icon("laptop"),
                h("span", {}, d.host),
                h("span", {}, "· " + [d.self && "you", !d.reachable && "offline", list.length].filter(Boolean).join(" · ")),
              ),
              list.map(sessionCard),
            ),
          ),
        )
      : h("div", { class: "empty" }, q || ui.sessionsTag ? "No session matches." : `No ${ui.sessionsFilter} sessions.`),
  );
}

function sessionCard(x) {
  const pr = prFor(x);
  const chats = conversations().filter((c) => (c.pair || []).includes(x.name)).length;
  const meta = [x.agent, modelOf(x)].filter(Boolean).join(" · ");
  return h(
    "button",
    { class: "scard" + (x.device.reachable ? "" : " offline"), onclick: () => openProfile("agent", x.name) },
    avatar({ kind: "agent", name: x.name }, "md", online(x.name)),
    h(
      "span",
      { class: "who" },
      h("span", { class: "top" }, h("span", { class: "name" }, x.name), meta && h("span", { class: "agent" }, meta)),
      h("span", { class: "doing truncate" }, x.doing || ""),
    ),
    h("span", { class: "branch" }, icon("branch"), h("span", { class: "truncate" }, x.branch || "no branch")),
    h("span", { class: "pr" }, pr && h("span", { class: "pr-chip" }, "#" + pr.number + (pr.draft ? " draft" : ""))),
    h("span", { class: "chats" }, icon("message"), `${chats} chat${chats === 1 ? "" : "s"}`),
    h("span", { class: "state" }, stateBadge(x)),
  );
}

// ------------------------------------------------------------------------------- Settings.

// The frame "v3 · Settings": Account, Appearance, Wake-ups, Busy sessions, Version, each a box of
// rows. Wake-ups and busy sessions are this device's daemon settings, saved whole with each PUT,
// one at a time so an older save can't land after a newer one.
const WAKE_OPTIONS = [
  ["always", "Always"],
  ["within", "Within N min of last activity"],
  ["never", "Never"],
];
const BUSY_OPTIONS = [
  ["subject", "Subject"],
  ["whole", "Whole message"],
];

function wakeLabel(w) {
  if (!w) return "Default";
  if (w.when === "within") return `Within ${w.minutes || ui.settings?.defaultMinutes || 3} min of last activity`;
  return w.when === "never" ? "Never" : "Always";
}

let settingsQueue = Promise.resolve();
let settingsSeq = 0;
let confirmed = null; // the settings as the daemon last gave them
function saveSettings(next) {
  const seq = ++settingsSeq;
  ui.settings = next;
  render();
  settingsQueue = settingsQueue.then(async () => {
    try {
      const saved = await api("/api/settings", { method: "PUT", body: JSON.stringify(next) });
      if (saved) confirmed = saved;
      if (seq === settingsSeq && saved) ui.settings = saved;
    } catch (e) {
      if (seq === settingsSeq) {
        ui.settings = confirmed || ui.settings;
        ui.error = "Couldn't save: " + e.message;
      }
    }
    render();
  });
}

async function loadSettings() {
  try {
    ui.settings = confirmed = (await api("/api/settings")) || {};
  } catch {
    ui.settings = null;
  }
  if (ui.view === "settings") render();
}

// A native select, drawn as the frame's white field with a chevron.
function selectField(label, value, options, onChange) {
  const sel = h(
    "select",
    { "aria-label": label, onchange: (e) => onChange(e.target.value) },
    options.map(([v, text]) => {
      const o = h("option", { value: v }, text);
      if (v === value) o.selected = true;
      return o;
    }),
  );
  return h("label", { class: "select" }, sel, icon("chevronDown"));
}

function segmented(label, value, options, onChange) {
  return h(
    "div",
    { class: "seg", role: "group", "aria-label": label },
    options.map(([v, text]) =>
      h("button", { class: value === v ? "on" : "", "aria-pressed": String(value === v), onclick: () => value !== v && onChange(v) }, text),
    ),
  );
}

function srow(label, help, control, extra = "") {
  return h(
    "div",
    { class: "srow" + extra },
    h("div", { class: "text" }, h("div", { class: "label" }, label), help && h("div", { class: "help" }, help)),
    control,
  );
}

function section(title, ...rows) {
  return h("div", { class: "sgroup" }, h("h2", {}, title), h("div", { class: "box" }, rows));
}

function settingsPage() {
  if (ui.settings === undefined) loadSettings();
  const s = ui.state;
  const input = h("input", { value: me(), "aria-label": "Your name", maxlength: "40", onkeydown: (e) => e.key === "Enter" && saveName() });
  const saveName = async () => {
    const name = input.value.trim();
    if (!name || name === me()) return;
    try {
      await api("/api/account", { method: "PUT", body: JSON.stringify({ name }) });
      ui.state.account.name = name;
    } catch (e) {
      ui.error = e.message;
    }
    render();
  };

  const set = ui.settings;
  const local = ((s.devices || []).find((d) => d.self)?.sessions || []).slice().sort((a, b) => a.name.localeCompare(b.name));
  const groups = [
    section(
      "ACCOUNT",
      srow("Your name", `How agents and people see you. Agents reach you at ${s.account?.addr || s.device + "/human"}.`, h("div", { class: "field" }, input, h("button", { class: "primary", onclick: saveName }, "Save"))),
    ),
    section(
      "APPEARANCE",
      srow("Theme", null, segmented("Theme", ui.themePref, [["light", "Light"], ["dark", "Dark"], ["system", "System"]], setTheme)),
    ),
  ];

  if (set) {
    const wake = set.wake || { when: "always" };
    const overrides = set.sessionWake || {};
    const own = local.filter((x) => overrides[x.name]).length;
    const wakeValue = wake.when;
    groups.push(
      section(
        "WAKE-UPS",
        srow(
          "Device default",
          "Whether a message from another session may wake an idle session. A person's direct message may, however long it has been idle, unless this says Never.",
          selectField("Device default", wakeValue, WAKE_OPTIONS.map(([v, t]) => [v, v === wakeValue ? wakeLabel(wake) : v === "within" ? wakeLabel({ when: "within" }) : t]), (v) =>
            saveSettings({ ...set, wake: { when: v, minutes: v === "within" ? wake.minutes || 0 : 0 } }),
          ),
        ),
        srow(
          "Per session",
          local.length
            ? `${local.filter((x) => !overrides[x.name]).map((x) => x.name).join(" and ") || "None"} ${local.length - own === 1 ? "follows" : "follow"} the device default.`
            : "No sessions on this device yet.",
          h(
            "button",
            { class: "select", "aria-expanded": String(!!ui.perSession), onclick: () => { ui.perSession = !ui.perSession; render(); } },
            h("span", {}, own ? `${own} set their own` : `${local.length} use the default`),
            icon("chevronDown"),
          ),
          ui.perSession ? " open" : "",
        ),
        ui.perSession &&
          local.map((x) =>
            srow(
              x.name,
              x.agent,
              selectField("Wake-ups for " + x.name, overrides[x.name]?.when || "default", [["default", "Default"], ...WAKE_OPTIONS.map(([v, t]) => [v, v === "within" ? wakeLabel({ when: "within", minutes: overrides[x.name]?.minutes }) : t])], (v) => {
                const next = { ...overrides };
                if (v === "default") delete next[x.name];
                else next[x.name] = { when: v, minutes: v === "within" ? overrides[x.name]?.minutes || 0 : 0 };
                saveSettings({ ...set, sessionWake: next });
              }),
              " sub",
            ),
          ),
      ),
      section(
        "BUSY SESSIONS",
        srow("Device default", "What a busy session sees when a message arrives.", segmented("Busy sessions", set.busy || "subject", BUSY_OPTIONS, (v) => saveSettings({ ...set, busy: v }))),
      ),
    );
  }

  const v = s.version || {};
  const latest = v.latest && v.latest !== v.running;
  groups.push(
    section(
      "VERSION",
      srow(
        `mur ${v.running || "?"}`,
        `Daemon protocol ${v.daemon || "?"}.`,
        h("div", { class: "status-line" }, h("span", { class: latest ? "dot-warn" : "dot-ok" }), latest ? `${v.latest} available` : "Up to date"),
      ),
    ),
  );

  return h("section", { class: "card page settings-page" }, h("h1", {}, "Settings"), groups);
}

function resolveTheme(pref) {
  if (pref === "light" || pref === "dark") return pref;
  return matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function setTheme(pref) {
  ui.themePref = pref;
  ui.theme = resolveTheme(pref);
  save("v3.theme", pref);
  document.documentElement.dataset.theme = ui.theme;
  render();
}
matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => {
  if (ui.themePref === "system") setTheme("system");
});

// ------------------------------------------------------------------------------- New message dialog.

function openDialog() {
  ui.dialog = true;
  ui.pick = "";
  ui.pickIndex = 0;
  render();
  document.querySelector(".dialog input")?.focus();
}
function closeDialog() {
  ui.dialog = false;
  render();
}

function dialog() {
  const q = (ui.pick || "").trim().toLowerCase();
  const agentSub = (x) => {
    const st = stateOf(x);
    const state = st === "idle" ? "idle " + ago(x.lastActive) : st === "dnd" ? "do not disturb" : st === "off" ? "offline" : "";
    return [x.agent, x.device.host, state].filter(Boolean).join(" · ");
  };
  const nAgents = sessions().length;
  const nPeople = people().length;
  const options = [
    { name: "*", label: "Everyone", sub: `${nAgents} agent${nAgents === 1 ? "" : "s"}, ${nPeople} ${nPeople === 1 ? "person" : "people"} in ${ui.state.repo}`, who: { kind: "all" } },
    ...sessions().map((x) => ({ name: x.name, label: x.name, sub: agentSub(x), long: agentSub(x) + (x.doing ? " · " + x.doing : ""), who: { kind: "agent", name: x.name }, on: online(x.name) })),
    ...others().map((p) => ({ name: p.name, label: p.name, sub: "human · " + p.device.host, who: { kind: "person", name: p.name }, on: p.device.reachable })),
  ];
  const hit = (o) => o.label.toLowerCase().includes(q);
  const matches = q ? options.filter(hit) : [];
  const rest = q ? options.filter((o) => !hit(o)) : options;
  const flat = [...matches, ...rest];
  const idx = Math.min(ui.pickIndex || 0, Math.max(0, flat.length - 1));
  const choose = (o) => {
    ui.dialog = false;
    go({ view: "messages", profile: null, conv: o.name === "*" ? "all" : dmKey(me(), o.name) });
    requestAnimationFrame(() => document.querySelector(".pill textarea")?.focus());
  };
  const input = h("input", {
    value: ui.pick || "",
    "aria-label": "To",
    oninput: (e) => { ui.pick = e.target.value; ui.pickIndex = 0; renderKeepFocus(e.target); },
    onkeydown: (e) => {
      if (e.key === "ArrowDown") { ui.pickIndex = Math.min(idx + 1, flat.length - 1); e.preventDefault(); renderKeepFocus(e.target); }
      else if (e.key === "ArrowUp") { ui.pickIndex = Math.max(idx - 1, 0); e.preventDefault(); renderKeepFocus(e.target); }
      else if (e.key === "Enter" && flat[idx]) { e.preventDefault(); choose(flat[idx]); }
    },
  });
  const row = (o) => {
    const i = flat.indexOf(o);
    return h(
      "button",
      { class: "item" + (i === idx ? " on" : ""), onmouseenter: () => { if (ui.pickIndex !== i) { ui.pickIndex = i; renderKeepFocus(input); } }, onclick: () => choose(o) },
      avatar(o.who, "md2", o.on),
      h("span", { class: "text" }, h("span", { class: "name truncate" }, o.label), h("span", { class: "sub truncate" }, matches.includes(o) && o.long ? o.long : o.sub)),
    );
  };
  return h(
    "div",
    { class: "scrim", onclick: closeDialog },
    h(
      "div",
      { class: "dialog", role: "dialog", "aria-modal": "true", "aria-label": "New message", onclick: (e) => e.stopPropagation() },
      h("div", { class: "title-row" }, h("h2", {}, "New message"), h("button", { class: "close", title: "Close (Esc)", "aria-label": "Close", onclick: closeDialog }, xIcon())),
      h("label", { class: "to" }, h("span", {}, "To:"), input),
      h(
        "div",
        { class: "picks" },
        matches.length > 0 && [h("div", { class: "sec-label" }, "Matches"), matches.map(row)],
        rest.length > 0 && [q && h("div", { class: "sec-label" }, "Everyone else"), rest.map(row)],
      ),
    ),
  );
}

// ------------------------------------------------------------------------------- Render.

function renderKeepFocus(el) {
  const label = el.getAttribute("aria-label");
  const sel = el.tagName.toLowerCase() + (label ? `[aria-label="${CSS.escape(label)}"]` : el.type === "search" ? "[type=search]" : "");
  const pos = el.selectionStart;
  render();
  const next = document.querySelector(sel);
  if (next) {
    next.focus();
    try {
      next.setSelectionRange(pos, pos);
    } catch {}
  }
}

function render() {
  document.documentElement.dataset.theme = ui.theme;
  if (!ui.state) return;
  const main = [];
  if (ui.view === "messages") main.push(convsCard(), threadCard());
  else if (ui.view === "sessions") main.push(sessionsPage());
  else main.push(settingsPage());
  const app = h(
    "div",
    { class: ["app", ui.collapsed && "collapsed", ui.view !== "messages" && "full"].filter(Boolean).join(" ") },
    sidebar(),
    main,
    ui.menu === "repo" && repoMenu(),
  );
  const scroll = document.querySelector(".conv-scroll")?.scrollTop;
  const old = document.querySelector(".messages");
  const sameThread = old && old.dataset.key === ui.conv;
  const atBottom = !old || old.scrollHeight - old.scrollTop - old.clientHeight < 40;
  const oldTop = old?.scrollTop;
  root.replaceChildren(app, ui.dialog ? dialog() : "", ui.error ? h("div", { class: "popover toast", role: "alert" }, ui.error, h("button", { class: "icon-btn", "aria-label": "Dismiss", onclick: () => { ui.error = ""; render(); } }, xIcon())) : "");
  if (scroll != null) {
    const el = document.querySelector(".conv-scroll");
    if (el) el.scrollTop = scroll;
  }
  const box = document.querySelector(".messages");
  if (box) box.scrollTop = !sameThread || atBottom ? box.scrollHeight : oldTop;
  markRead();
}

// What you open is read: the messages to you in the open conversation, while the tab is seen.
const readSent = new Set();
function markRead() {
  if (document.hidden || ui.view !== "messages" || !ui.state) return;
  const unread = new Set(ui.state.account?.unread || []);
  if (!unread.size) return;
  const c = conversations().find((x) => x.key === ui.conv);
  const ids = (c?.messages || []).map((m) => m.id).filter((id) => unread.has(id) && !readSent.has(id));
  if (!ids.length) return;
  for (const id of ids) readSent.add(id);
  api("/api/read", { method: "POST", body: JSON.stringify({ ids }) }).catch(() => {
    for (const id of ids) readSent.delete(id);
  });
}

function gate(text) {
  root.replaceChildren(
    h(
      "div",
      { class: "gate" },
      h("img", { src: lockup(), alt: "Murmur" }),
      h("p", {}, text),
    ),
  );
}

// ------------------------------------------------------------------------------- Polling.

async function pollState() {
  ui.state = await api("/api/state");
}
async function pollMessages() {
  const r = await api("/api/messages" + (ui.cursor ? "?cursor=" + encodeURIComponent(ui.cursor) : ""));
  const seen = new Set(ui.messages.map((m) => m.id));
  for (const m of r.messages || []) {
    if (seen.has(m.id)) continue;
    ui.messages.push(m);
  }
  // The bus echoes a sent message back under the id the send returned; drop the bubble that stood
  // in for it.
  const have = new Set(ui.messages.map((m) => m.id));
  ui.pending = ui.pending.filter((p) => !(p.sentId && have.has(p.sentId)));
  ui.cursor = r.cursor || ui.cursor;
}

function signature() {
  return ui.messages.length + "|" + ui.pending.length + "|" + JSON.stringify(ui.state);
}

async function tick() {
  try {
    const before = signature();
    await Promise.all([pollState(), pollMessages()]);
    if (signature() !== before && !ui.addingTag) {
      const el = document.activeElement;
      if (el && ["INPUT", "TEXTAREA", "SELECT"].includes(el.tagName) && el.getAttribute("aria-label")) renderKeepFocus(el);
      else render();
    }
  } catch (e) {
    if (e.status === 401) return gate("This page's key has expired. Run mur ui again to open a fresh one.");
  }
  setTimeout(tick, 3000);
}

document.addEventListener("keydown", (e) => {
  if (e.key === "Escape") {
    if (ui.dialog) closeDialog();
    else if (ui.menu) go({});
  }
});
document.addEventListener("visibilitychange", () => {
  if (!document.hidden) markRead();
});
document.addEventListener("click", () => {
  if (ui.menu) go({});
});

async function start() {
  ui.theme = resolveTheme(ui.themePref);
  document.documentElement.dataset.theme = ui.theme;
  await takeKey();
  token = takeToken();
  if (!token) return gate("This page needs its key. Run mur ui in the repo to open it.");
  readHash();
  try {
    await Promise.all([pollState(), pollMessages()]);
    api("/api/repos").then((r) => { ui.repos = r.repos || []; render(); }, () => {});
  } catch (e) {
    return gate(e.status === 401 ? "This page's key has expired. Run mur ui again to open a fresh one." : "Can't reach mur: " + e.message);
  }
  render();
  setTimeout(tick, 3000);
}
start();
