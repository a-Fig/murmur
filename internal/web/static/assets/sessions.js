// The Sessions view: a session's state, hiding and quiet sessions, settled sessions, rows and cards.

import { icon } from "./icons.js";
import { h, when, ago, plural } from "./dom.js";
import {
  NAME, ui, devices, namesLearned, nameOf, fromName, toName, findSession, HIDDEN, isPerson, findPerson,
  personOnline,
} from "./state.js";
import { AuthError, api } from "./api.js";
import { deny } from "./sync.js";
import { render, go } from "./render.js";
import { pickSession } from "./sidebar.js";
import { shownSessions, tagChips, tagButton, openTagMenu } from "./tags.js";
import { conversations, avatar } from "./messages.js";
import { startChat } from "./picker.js";

export {
  STATE_TIPS, PERSON_TIPS, modelOf, sessionState, isHidden, liveSessions, quietSessions, hideButton,
  quietFold, dotted, deviceDot, statusDot, prFor, prBadge, presence, settledFold, settledSessions, isSettled,
  deviceLabel, host, allConversations, sideSessionBody, stateWord, sessionsPage, emptyState,
};

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
              tagChips(s, 1),
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
  const a = avatar(name, false);
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
      h("span", { class: "name-line" }, h("span", { class: "side-session-name truncate", "data-profile": s.name }, s.name), tagChips(s, 1)),
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
          h("span", { class: "s-name truncate", "data-profile": s.name }, s.name),
          tagChips(s, 2),
          // The hook never reported after a tool call, so the session isn't told of messages while it
          // works (#225). It sits in the name line: the meta columns are a fixed grid, and an extra
          // cell there shifted chats, state and Message one column over (#546).
          s.hookOff && h("span", { class: "badge badge-warning", title: "Isn't told of messages while it works" }, "hook not running"),
          h("span", { class: "s-agent truncate" }, [s.agent, modelOf(s)].filter(Boolean).join(" · ")),
        ),
        h("span", { class: "s-doing truncate" + (s.doing ? "" : " none") }, s.doing || "No status set"),
      ),
    ),
    h(
      "span",
      { class: "s-meta" },
      h("span", { class: "s-col-branch" + (s.branch === "main" || s.branch === "master" ? " default" : "") }, s.branch && h("span", { class: "g-branch", title: "Branch " + s.branch }, icon("branch"), h("span", { class: "truncate" }, s.branch))),
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
function emptyState(iconName, title, line, action = null) {
  return h("div", { class: "empty" }, h("span", { class: "empty-ico" }, icon(iconName)), h("p", { class: "empty-title" }, title), line && h("p", { class: "empty-line" }, line), action);
}
