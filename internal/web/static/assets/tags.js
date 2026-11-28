// Tags: group chats and their members, a session's tags, the tag menu, the profile card, and the
// queues that change tags.

import { icon } from "./icons.js";
import { h, ago, plural, root } from "./dom.js";
import {
  ui, devices, fromName, toName, findSession, HIDDEN, me, myName, isPerson, findPerson, personOnline,
} from "./state.js";
import { tagsFolded, setTagsFolded } from "./preferences.js";
import { AuthError, api } from "./api.js";
import { deliver, deny, stateAskedAt, poll } from "./sync.js";
import { render, go } from "./render.js";
import { humanTag, personBadges } from "./sidebar.js";
import {
  STATE_TIPS, PERSON_TIPS, modelOf, sessionState, deviceDot, prFor, prBadge, presence, settledSessions,
  deviceLabel, host, stateWord,
} from "./sessions.js";
import { dmKey, avatar, deviceColor, deviceOfName } from "./messages.js";
import { startChat, askForName } from "./picker.js";
import { openFullView } from "./profile.js";

export {
  groupMembers, inGroup, liveGroups, isGroupTag, membersButton, tagsLine, profileLine, validTag, tagWanted,
  tagsOf, shownSessions, sessionByAddr, tagChips, tagsGroup, tagButton, openTagMenu, closeTagMenu, tagMenu,
  closeProfile, placeProfile, profileCard, tagQueues, groupJoins, groupMisses, tagSessions, forgetJoins,
};

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
      h("span", { class: "g-pile" }, pile.map((r) => avatar(r.name, false))),
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
          h("span", { class: "g-mp-text" }, h("span", { class: "name-line" }, h("span", { class: "g-mp-name truncate", "data-profile": r.name }, r.name), r.person && humanTag()), h("span", { class: "g-mp-doing truncate" }, r.person ? "Person" : r.s ? r.s.doing || r.s.agent || "" : "Not running")),
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

// Under a session's name in its view (#631, #1115): its state and its device, with no dot of its own
// (the avatar beside it carries one, #1188). When it started and the branches it has committed to
// are on the line below, with the one it is on. It wraps rather than cuts off (#1284).
function profileLine(d, s) {
  return h(
    "p",
    { class: "g-head-status" },
    h("span", null, stateWord(d, s)),
    h("span", { title: "Device " + d.id }, d.self ? "This device" : d.nick || d.owner || host(d.id)),
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

// A session's tags as small chips, the one the lists are filtered by first; past max, "+N", whose
// tip names the rest (#631). Each chip is whole: in the sessions list the row's width decides
// whether two show, one or none (style.css), so each count is drawn and the width picks one.
function tagChips(s, max = Infinity) {
  const { names, waiting } = tagsOf(s);
  if (!names.length) return null;
  if (ui.tagFilter && names.includes(ui.tagFilter)) names.sort((a, b) => (b === ui.tagFilter) - (a === ui.tagFilter));
  const more = (from, cls) => names.length > from && h("span", { class: "stag-more " + cls, title: names.slice(from).join(", ") }, "+" + (names.length - from));
  return h(
    "span",
    { class: "stags" },
    names.slice(0, max).map((n) =>
      h("span", { class: "stag" + (n === ui.tagFilter ? " on" : "") + (waiting.has(n) ? " waiting" : ""), title: waiting.has(n) ? n + ": waiting for its device" : n }, n),
    ),
    max >= 2 && more(1, "more-after-1"),
    more(max, "more-after-max"),
  );
}

// The sidebar's Tags: one chip per tag in use, which filters the lists to it. Folded, it keeps only
// the chip of the filter that is on, so that filter can still be seen and turned off.
function tagsGroup() {
  const counts = tagCounts();
  if (ui.tagFilter && !counts.has(ui.tagFilter)) ui.tagFilter = null;
  if (!counts.size) return null;
  const chips = [...counts].filter(([n]) => !tagsFolded || n === ui.tagFilter);
  return h(
    "div",
    { class: "sidebar-group dev-group" },
    h(
      "button",
      {
        class: "group-label group-fold",
        "aria-expanded": tagsFolded ? "false" : "true",
        "data-key": "tags-fold",
        onclick: () => setTagsFolded(!tagsFolded),
      },
      h("span", { class: "label" }, "Tags", icon("chevronRight", "dev-chev")),
      h("span", { class: "menu-count" }, counts.size),
    ),
    chips.length > 0 &&
      h(
        "div",
        { class: "tag-filter" },
        chips
          .sort((a, b) => a[0].localeCompare(b[0]))
          .map(([n, c]) => {
            const on = ui.tagFilter === n;
            return h(
              "button",
              {
                class: "tf" + (on ? " on" : ""),
                "aria-pressed": on ? "true" : "false",
                title: on ? n + ": show every session" : "Show only sessions tagged " + n,
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

// The profile card (#689, Tyler's request): a click on a face or a sender's name opens a card next
// to it, as a chat app shows when someone's name is clicked. It says who the session or person is,
// how it is now, what you have in common, and has a box that sends a direct message without
// leaving the card. Everything on it comes from presence and the messages the page already has.
// The device's colour runs across its top, as a banner.
const CARD_WIDTH = 320;

// A face inside a control (a sidebar row, a session's row) opens the card rather than the control:
// the listener runs in the capture phase, ahead of the control's own.
document.addEventListener(
  "click",
  (e) => {
    const t = e.target.closest?.("[data-profile]");
    // Not where a face is the whole control (the collapsed sidebar's), nor under another dialog (the
    // pickers, whose rows pick), nor while sessions are being picked to tag.
    if (!t || !root.contains(t) || t.closest(".mini-face") || ui.setup || ui.picker || ui.tagMenu || ui.repoMenu) return;
    if (ui.selecting && ui.view === "sessions") return;
    e.preventDefault();
    e.stopPropagation();
    openProfile(t.dataset.profile, t);
  },
  true,
);

// The card opens beside what was clicked: to its right where there is room, else to its left. Its
// height is known only once it is drawn, so placeProfile keeps it on the screen after each render.
// A face clicked inside the card opens that name's card in the same place.
function openProfile(name, anchor) {
  const was = ui.profile;
  const inCard = was && anchor.closest(".profile-card");
  let x, top;
  if (inCard) {
    ({ x, top } = was);
  } else {
    // From a list (the sidebar, the conversations), beside the list rather than over its rows.
    const r = anchor.getBoundingClientRect();
    const right = anchor.closest(".sidebar, .chat-list")?.getBoundingClientRect().right ?? r.right;
    x = right + 8 + CARD_WIDTH <= window.innerWidth - 8 ? right + 8 : Math.max(8, r.left - 8 - CARD_WIDTH);
    top = r.top - 8;
  }
  // Closing gives the focus back to what opened it: the element, or its counterpart by key once a
  // render has replaced it.
  const opener = anchor.closest("button, a, [tabindex]") || anchor;
  const from = inCard ? was.from : opener.closest("[data-key]")?.dataset.key || null;
  const fromEl = inCard ? was.fromEl : opener;
  ui.membersOpen = false; // the card takes the click the members panel would have closed on
  ui.profile = { name, x, top, from, fromEl, draft: "", sent: null, shown: false };
  render();
  focusProfile();
}

function focusProfile() {
  const card = root.querySelector(".profile-card");
  (card?.querySelector('[data-key="pc-msg"]') || card)?.focus({ preventScroll: true }); // your own has no box
}

function closeProfile() {
  const { from, fromEl } = ui.profile || {};
  ui.profile = null;
  render();
  const back = fromEl?.isConnected ? fromEl : from ? root.querySelector(`[data-key="${CSS.escape(from)}"]`) : null;
  back?.focus({ preventScroll: true });
}

function placeProfile() {
  const card = ui.profile && root.querySelector(".profile-card");
  if (!card) return;
  if (ui.profile.shown) card.classList.add("pc-still"); // it pops in once, not on every render
  ui.profile.shown = true;
  card.style.left = Math.max(8, Math.min(ui.profile.x, window.innerWidth - card.offsetWidth - 8)) + "px";
  card.style.top = Math.max(8, Math.min(ui.profile.top, window.innerHeight - card.offsetHeight - 8)) + "px";
}

// The groups you are in with a session: those it has the tag of that you have posted in. A person
// is in a group only by posting in it, as the page's members list has it.
function sharedGroups(s) {
  if (!me() || !s) return [];
  const mine = new Set();
  for (const m of ui.messages) if (m.toKind === "group" && fromName(m) === myName()) mine.add(m.to);
  return tagsOf(s).names.filter((n) => isGroupTag(n) && mine.has(n));
}

// The sessions a person has had direct messages with, newest first.
function talkedWith(name) {
  const out = [];
  for (let i = ui.messages.length - 1; i >= 0 && out.length < 8; i--) {
    const m = ui.messages[i];
    if (m.toKind !== "session") continue;
    const from = fromName(m);
    const to = toName(m);
    const other = from === name ? to : to === name ? from : null;
    if (other && !isPerson(other) && !out.includes(other)) out.push(other);
  }
  return out;
}

// The card's box sends as the composer does, into the direct conversation with the name, which
// lists it. One with no account yet chooses a name first, and the card comes back to send it.
function sendFromCard() {
  const p = ui.profile;
  const text = p.draft.trim();
  if (!text) return;
  if (!me()) {
    ui.profile = null;
    return askForName(() => {
      ui.profile = { ...p, draft: text, sent: null };
      sendFromCard();
      focusProfile();
    });
  }
  const pair = [...new Set([myName(), p.name])].sort();
  const key = dmKey(myName(), p.name);
  const item = { cid: Date.now() + "-" + Math.random().toString(36).slice(2, 8), key, conversation: { key, kind: "dm", title: pair.join(" & "), pair }, to: p.name, text, status: "sending" };
  ui.outbox.push(item);
  p.draft = "";
  p.sent = item;
  // The field keeps its node while it has the focus, so it is emptied here, as the composer's is.
  const field = root.querySelector('[data-key="pc-msg"]');
  if (field) field.value = "";
  deliver(item);
}

function profileCard() {
  const p = ui.profile;
  const name = p.name;
  const f = findSession(name);
  const person = findPerson(name);
  const s = f?.session || null;
  const d = f?.device || person?.device || deviceOfName(name);
  const own = name === myName();
  const human = !!person || own || isPerson(name);
  const st = human ? (own || (person && personOnline(person)) ? "on" : "off") : s ? sessionState(d, s) : "settled";
  const last = !s && !human ? settledSessions().find((r) => r.name === name)?.last : null;
  const word = s ? stateWord(d, s) : human ? PERSON_TIPS[st] : last ? "Stopped " + ago(last) : "Not running";
  // A person's device is named for them, so theirs goes by its machine.
  const where = d ? (d.self ? "This device" : human ? host(d.id) : d.nick || d.owner || host(d.id)) : null;
  const addr = s?.addr || null;
  const tags = s ? tagsOf(s).names.filter((n) => !n.startsWith("model:")) : [];
  const badges = [s?.agent, s && modelOf(s)].filter(Boolean);
  const common = [...sharedGroups(s).map((g) => h("span", { class: "stag" }, g)), s && d?.self && h("span", { class: "pc-common-dev" }, icon("laptop"), "On your device")].filter(Boolean);
  const talked = human ? talkedWith(name) : [];
  const sent = p.sent;
  const section = (label, ...kids) => h("div", { class: "pc-section" }, h("p", { class: "pc-label" }, label), ...kids);
  const face = avatar(name, false);
  face.classList.add("pc-avatar");
  const card = h(
    "div",
    { class: "profile-card " + deviceColor(d), role: "dialog", "aria-modal": "true", "aria-label": name, tabindex: -1, "data-key": "pc-card" },
    h("div", { class: "pc-banner", "aria-hidden": "true" }),
    h(
      "div",
      { class: "pc-head" },
      h("span", { class: "pc-face" }, face, h("span", { class: "pres pres-" + st, title: s ? STATE_TIPS[st] : PERSON_TIPS[st] || "" })),
      h(
        "div",
        { class: "pc-who" },
        h("h2", { class: "pc-name name-line" }, h("span", { class: "truncate" }, name), human && personBadges(name)),
        addr && h("p", { class: "pc-addr truncate", title: addr }, addr),
        h("p", { class: "pc-state truncate" }, word, where && " · " + where),
      ),
    ),
    h("button", { class: "pc-full", "data-key": "pc-full", onclick: () => openFullView(name) }, "Open full view", icon("arrowRight")),
    (badges.length > 0 || tags.length > 0) && h("div", { class: "pc-badges" }, badges.map((b) => h("span", { class: "pc-badge" }, b)), tags.map((n) => h("span", { class: "stag", title: n }, n))),
    s &&
      (s.doing || s.branch) &&
      section(
        "Status",
        s.doing && h("p", { class: "pc-doing" }, s.doing),
        h(
          "div",
          { class: "pc-meta" },
          s.branch && h("span", { class: "g-branch", title: "Branch " + s.branch }, icon("branch"), h("span", { class: "truncate" }, s.branch)),
          prFor(s) && prBadge(prFor(s), true, "pc-pr"),
        ),
      ),
    common.length > 0 && section("In common", h("div", { class: "pc-common" }, common)),
    talked.length > 0 && section("Talked with", h("div", { class: "pc-talked" }, talked.map((n) => h("button", { type: "button", class: "pc-talked-one", title: n, "data-profile": n, "aria-haspopup": "dialog", "data-key": "pc-talked-" + n }, avatar(n, false), h("span", { class: "truncate" }, n))))),
    !own &&
      h(
        "div",
        { class: "pc-send" },
        sent &&
          h(
            "p",
            { class: "pc-sent" + (sent.status === "failed" ? " failed" : "") },
            h("span", { class: "truncate", "data-announce": sent.status === "failed" ? "" : null }, sent.status === "failed" ? "Not sent: " + sent.error : sent.status === "sent" ? "Sent" : "Sending…"),
            h("button", { class: "pc-link", "data-key": "pc-open-dm", onclick: () => ((ui.profile = null), startChat(name)) }, "Open conversation"),
          ),
        h(
          "label",
          { class: "pc-field" },
          h("input", {
            type: "text",
            value: p.draft,
            placeholder: "Message " + name,
            "aria-label": "Message " + name,
            autocomplete: "off",
            "data-key": "pc-msg",
            oninput: (e) => {
              p.draft = e.target.value;
              e.target.closest(".pc-field").querySelector(".pc-go").disabled = !p.draft.trim();
            },
            onkeydown: (e) => {
              if (e.key !== "Enter" || e.shiftKey || e.isComposing) return;
              e.preventDefault();
              sendFromCard();
            },
          }),
          h("button", { class: "pc-go", "aria-label": "Send", disabled: !p.draft.trim(), "data-key": "pc-go", onclick: sendFromCard }, icon("arrowUp")),
        ),
      ),
  );
  return h(
    "div",
    {
      class: "menu-scrim",
      onclick: (e) => {
        if (e.target === e.currentTarget) closeProfile();
      },
      onkeydown: (e) => {
        if (e.key === "Escape") {
          e.preventDefault();
          closeProfile();
        }
      },
    },
    card,
  );
}

// Puts a tag on, or takes it off, each of the sessions that needs the change: one request each,
// to the session's own device. The lists show the change at once, as waiting until it lands.
// A session's changes go one after another, in the order they were clicked: the daemon times a
// change when it gets it, so two in flight at once could land the other way round.
const tagQueues = new Map();
// The adds this page made to each group still on their way, which a message sent to it waits for.
// A tag change that fails resolves its place in tagQueues all the same, so groupMisses keeps each
// session whose add to a group failed, with why, while that is the latest word on it: until the
// session is in the group, by anyone's hand, the page changes its tag there again, or it isn't
// running. One out of the group after that is out for a reason the page doesn't know (#538).
const groupJoins = new Map(); // a group's tag → [Promise]
const groupMisses = new Map(); // a group's tag → Map<addr, { s, msg }>

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
        groupMisses.get(name)?.delete(s.addr);
      } catch (e) {
        // A later click on the same tag has its own wish; only this one's is dropped.
        const w = tagWanted.get(s.addr);
        if (w?.get(name) === wishes.get(s.addr)) w.delete(name);
        if (e instanceof AuthError) {
          denied = e;
        } else {
          failed.push({ s, msg: e.message });
          if (on && isGroupTag(name)) {
            if (!groupMisses.has(name)) groupMisses.set(name, new Map());
            groupMisses.get(name).set(s.addr, { s, msg: e.message });
          }
        }
      }
    }),
  );
  if (on && isGroupTag(name) && todo.length) {
    groupJoins.set(name, [...(groupJoins.get(name) || []), all]);
    all.then(() => {
      const left = (groupJoins.get(name) || []).filter((p) => p !== all);
      left.length ? groupJoins.set(name, left) : groupJoins.delete(name);
    });
  }
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

// Drops each failed add to a group whose session is in it now or no longer running.
function forgetJoins() {
  for (const [group, misses] of groupMisses) {
    for (const addr of [...misses.keys()]) {
      const s = sessionByAddr(addr);
      if (!s || inGroup(s, group)) misses.delete(addr);
    }
    if (!misses.size) groupMisses.delete(group);
  }
}
