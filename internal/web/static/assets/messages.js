// The Messages view: conversations and their list, chat messages and receipts, and the composer.

import { icon } from "./icons.js";
import { h, when, ago, stamp, agoEl, plural, root } from "./dom.js";
import {
  NAME, POLL_MS, NARROW, ui, devices, nameOf, fromName, toName, findSession, sessionCount, me, myName,
  primaryOffer, isPerson, findPerson, personOnline, unreadIds,
} from "./state.js";
import { onlyMine, setOnlyMine } from "./preferences.js";
import { AuthError, api } from "./api.js";
import { deliver, queueSettings, saveSettings, deny, poll } from "./sync.js";
import { announce, render, go, starRow } from "./render.js";
import { personBadges } from "./sidebar.js";
import {
  modelOf, sessionState, dotted, statusDot, prFor, prBadge, presence, settledSessions, isSettled,
  deviceLabel, host, stateWord, emptyState,
} from "./sessions.js";
import { groupMembers, inGroup, liveGroups, membersButton, tagsLine, profileLine } from "./tags.js";
import {
  attached, pictureType, fileSize, filesOnly, messageFiles, fileList, sendsFiles, askToSend, turnOnSending,
  attach, detach, pickFiles, REPO_FILES_OFF,
} from "./files.js";
import {
  startGroup, startChat, askForName, newMessageButton, newGroupButton, openPicker, startPane,
} from "./picker.js";
import { showTipNow } from "./tips.js";

export {
  messageDetail, conversationOf, groupConversation, conversations, dmKey, avatar, nameButton, personFace,
  deviceColor, deviceOfName, conversationsPage, personText, messageText, dayLabel, clockTime, copyText,
  chatScroll, restoreChatScroll, markOpenRead, composers, composing, fitComposers, focusComposer, deviceMode,
  MODE_HELP, modeNote, setMode, namesOf, headMeta,
};

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

// The person's messages: direct ones, and the group chats they have posted in, whole, with the one you just
// opened, so a group that already has a history shows it before you write in it. The conversations
// they make are the ones the "only yours" toggle leaves.
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
  if (fresh && !ui.filter && !byKey.has(fresh.key)) byKey.set(fresh.key, fresh);
  // So is one whose only message hasn't gone: nothing else lists it, and a message that failed has
  // to stay reachable, with its text and its Try again, after the person looks at something else.
  if (!ui.filter) {
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

// A face is filled with the color of the device it is on (#560), so a stack of faces says whose
// machines they are. A session's shows a ghost; a person's, their initial (#579, Tyler). The
// classes are in style.css: the page's content policy doesn't allow an inline style.
// A face opens its name's profile card when clicked (#689), unless card is false: where the face
// is part of a control that does something else (the account chip, a group's pile of faces).
function avatar(name, card = true) {
  const color = "avatar dev-fill " + deviceColor(deviceOfName(name));
  const face = isPerson(name) ? personFace(name, color) : ghostFace(name, color);
  if (card && name) face.dataset.profile = name;
  return face;
}

// A name the page shows opens the same card as its face (#778, Tyler): where the name stands on its
// own, as this button; inside a control, as a span with data-profile, which the capture listener
// above takes ahead of the control. Names in controls that pick (the pickers, sessions being picked
// to tag) and in a conversation's row keep the control's action.
function nameButton(name, cls, text = name) {
  return h("button", { type: "button", class: "name-ref " + cls, "data-profile": name, "aria-haspopup": "dialog" }, text);
}

// A name the page knows, written in a message, as a session's two words or in backticks.
function knownName(t) {
  return !!(findSession(t) || findPerson(t) || t === myName());
}

// Plain text from a message, with each name the page knows (quiet-otter) made a nameButton.
function withNames(t, out) {
  // Not part of a path, an address or a longer word: what comes before is checked by hand, since a
  // lookbehind is a parse error in Safari before 16.4, and that would stop the whole page.
  const re = /[a-z0-9]+(?:-[a-z0-9]+)+(?![\w/\\-]|[.:]\w)/g;
  let last = 0;
  for (let m; (m = re.exec(t)); ) {
    if ((m.index > 0 && /[\w./:\\-]/.test(t[m.index - 1])) || !knownName(m[0])) continue;
    if (m.index > last) out.push(t.slice(last, m.index));
    out.push(nameButton(m[0], "rich-name"));
    last = re.lastIndex;
  }
  if (last < t.length) out.push(t.slice(last));
}

function personFace(name, cls) {
  return h("span", { class: cls + " avatar-person", "aria-hidden": "true" }, (name[0] || "?").toUpperCase());
}

// A session's ghost (#579): its name picks one of 16 ghosts and one of 15 accessories or none, so
// it wears the same one everywhere. The files are drawn by scripts/ghosts.py, which these lists
// follow; a ghost has no tile of its own, so it sits on its device's color.
const GHOSTS = ["murmur", "hush", "whisper", "mumble", "chatter", "babble", "rumor", "echo", "chirp", "coo", "hum", "psst", "sigh", "jabber", "natter", "buzz"];
const GHOST_ACCESSORIES = ["", "bow", "glasses", "specs", "shades", "bubble", "headset", "beanie", "party", "crown", "halo", "flower", "sprout", "tophat", "cat", "propeller"];

function ghostOf(name) {
  let n = 2166136261; // FNV-1a
  for (const ch of name) n = Math.imul(n ^ ch.codePointAt(0), 16777619) >>> 0;
  return { ghost: GHOSTS[n % 16], accessory: GHOST_ACCESSORIES[(n >>> 4) % 16] };
}

function ghostFace(name, cls) {
  const { ghost, accessory } = ghostOf(name);
  return h(
    "span",
    { class: cls + " avatar-ghost", "aria-hidden": "true" },
    h("img", { src: "/assets/ghosts/ghost-" + ghost + ".svg", alt: "", draggable: "false" }),
    accessory && h("img", { src: "/assets/ghosts/acc-" + accessory + ".svg", alt: "", draggable: "false" }),
  );
}

// A device's color: one of twelve hues, the same on its faces and by its name in the sidebar. Its
// person may pick it (#573); the daemon settles who has which, so every page shows the same. Offline,
// or not in presence, it is grey.
function deviceColor(d) {
  if (!d || !d.reachable) return "dev-off";
  return "dev-c" + (d.color || 0);
}

// The device a session or a person is on, by name, or null if none in presence has it. Built once
// per state, since every face asks.
let devOfNames = null;
let devOfState = null;
function deviceOfName(name) {
  if (devOfState !== ui.state) {
    devOfState = ui.state;
    devOfNames = new Map();
    for (const d of ui.state?.devices || []) {
      for (const s of d.sessions || []) devOfNames.set(s.name, d);
      if (d.person) devOfNames.set(d.person.name, d);
    }
  }
  return devOfNames?.get(name) || null;
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
    shown.map((r) => avatar(r.name, false)),
    rows.length > 4 && h("span", { class: "avatar g-more" }, "+" + (rows.length - 3)),
  );
}

function conversationIcon(c) {
  if (c.kind !== "dm") return groupIcon(c);
  if (c.pair.length === 1) return dotted(c.pair[0]);
  return h("span", { class: "avatar-pair" }, c.pair.map(dotted));
}

let chatMoved = false; // the open conversation has just changed key, and is still the one being read

// The toggle in the list leaves only the conversations you are in: your DMs, the group chats you
// have written in, and one you have just begun or have a message waiting in. It needs an account,
// and narrows all conversations, not a picked session's, which are about that session.
// The open conversation stays, as one opened from Recent has to; turning the toggle on closes one
// that isn't yours (mineToggle), so the pane never shows what the list has left out.
function yourKeys() {
  return new Set([...myMessages().map((m) => conversationOf(m).key), ...ui.outbox.map((item) => item.key)]);
}

function onlyYours(list) {
  if (!me() || !onlyMine || ui.filter) return list;
  const keys = yourKeys();
  return list.filter((c) => keys.has(c.key) || c.key === ui.conversation || (!c.messages.length && !c.quiet));
}

function mineToggle() {
  if (!onlyMine && ui.conversation && !yourKeys().has(ui.conversation) && !draftConversation()) ui.conversation = null;
  setOnlyMine(!onlyMine);
}

function conversationsPage() {
  const every = conversations();
  let list = onlyYours(every);
  // On a wide page a conversation is always open, and stays open while others get newer. On a
  // narrow one the list and a conversation take turns, so the list is where it starts.
  // A name landing changes a conversation's key, so one that has gone is looked for by a message
  // it held before settling for the newest.
  // It is looked for among every conversation, as one left open outside the toggle stays open.
  if (ui.conversation && !list.some((c) => c.key === ui.conversation)) {
    ui.conversation = every.find((c) => c.messages.some((m) => m.id === ui.conversationAnchor))?.key || null;
    chatMoved = ui.conversation != null;
    list = onlyYours(every);
  }
  if (!ui.conversation && list.length && !NARROW.matches) ui.conversation = list[0].key;
  const open = list.find((c) => c.key === ui.conversation);
  ui.conversationAnchor = open?.messages[open.messages.length - 1]?.id;
  if (ui.newMark && ui.newMark.key !== ui.conversation) ui.newMark = null;
  const emptyOffer = every.length === 0 && !ui.filter && !me() ? primaryOffer() : null;
  return h(
    "div",
    { class: "page chat-page" },
    // A picked session keeps its header, and so its tags, before it has any messages. With an
    // account, the list stays, with who you could talk to beside it.
    every.length === 0 && !ui.filter && !me()
      ? [
          starRow(),
          emptyState(
            "message",
            "No conversations yet",
            "Messages between sessions show here as this daemon reads them.",
            emptyOffer && h("button", { class: "btn btn-primary btn-sm empty-action", "data-key": "empty-use-offer", onclick: () => askForName(null) }, "Use " + emptyOffer.name + (emptyOffer.nick ? " (" + emptyOffer.nick + ")" : "") + " from " + emptyOffer.repo),
          ),
        ]
      : h(
          "div",
          { class: "chat" + (open ? " chat-open" : "") },
          conversationList(list),
          // None yet, or none of yours: who you could talk to.
          open ? conversationPane(open) : !ui.filter && !NARROW.matches && startPane(),
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
  // narrows it to direct messages or group chats, and the toggle under it to yours (onlyYours).
  const kind = ui.convKind || "all";
  const wanted = (c) => kind === "all" || (kind === "dm") === (c.kind === "dm");
  const canMine = !!me() && !ui.filter;
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
      // A picked session's profile, as #1018 drew it (#1115): the way back, its face and name large
      // with how it is under them, what it is doing, what it runs on, and its tags.
      name
        ? [
            // The way back, and Message across from it: up here it leaves the name its whole row (#1284).
            h(
              "div",
              { class: "g-head-nav" },
              h(
                "button",
                { class: "g-back", "data-key": "head-back", onclick: () => ((ui.filter = null), (ui.conversation = null), go("messages")) },
                icon("chevronRight"),
                "All conversations",
              ),
              f && h("button", { class: "btn btn-outline btn-xs g-head-msg", "data-key": "head-msg", onclick: () => startChat(name) }, icon("message"), "Message"),
            ),
            h(
              "div",
              { class: "g-head-who g-profile" },
              f ? presence(name, f.device) : avatar(name),
              h(
                "div",
                { class: "g-head-id" },
                // Its whole name on one line (#1284): it is who the session is, and this view exists to show it.
                h("h2", null, name),
                // Its profile (#631): how it is now, where it runs, and since when.
                f ? profileLine(f.device, f.session) : h("p", { class: "g-head-status" }, "Settled · " + ago(settledSessions().find((r) => r.name === name)?.last || 0)),
              ),
            ),
            f && f.session.doing && h("p", { class: "g-head-doing" }, f.session.doing),
            f && headMeta(f.session),
            f && tagsLine(f.session),
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
    canMine &&
      h(
        "button",
        {
          class: "g-mine-toggle",
          role: "switch",
          "aria-checked": onlyMine ? "true" : "false",
          "data-key": "only-mine",
          onclick: mineToggle,
        },
        h("span", { class: "tm-box" + (onlyMine ? " on" : "") }, onlyMine && icon("check")),
        h("span", { class: "truncate" }, "Only conversations you're in"),
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
    kind === "group" && !q && !name && h("div", { class: "g-none-action" }, h("button", { class: "btn btn-outline btn-sm", "data-key": "list-new-group", onclick: () => openPicker(true) }, icon("users"), "New group chat")),
  );
}

// What a picked session runs on: its harness and model, its branch, the pull request open from that
// branch as a chip, when it started, and the other branches it has committed to (#631).
function headMeta(s) {
  const started = when(s.started);
  const pr = prFor(s);
  const chip = pr && prBadge(pr, true, "pr-head-" + s.name);
  if (chip) {
    chip.classList.add("g-pr-chip");
    if (pr.draft) chip.append(" draft");
  }
  const others = (s.committed || []).filter((b) => b && b !== s.branch);
  return h(
    "p",
    { class: "g-head-meta" },
    h("span", { class: "truncate" }, [s.agent, modelOf(s)].filter(Boolean).join(" · ")),
    s.branch && h("span", { class: "g-branch", title: "Branch " + s.branch }, icon("branch"), h("span", { class: "truncate" }, s.branch)),
    chip,
    started && agoEl("span", null, started, "Started "),
    others.length > 0 && h("span", { class: "truncate", title: "Also committed to " + others.join(", ") }, "Also on " + others.join(", ")),
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
        o && isPerson(o) && personBadges(o),
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

// What a person wrote, whole, from the subject and body the bus carries it as (web.SplitText). A
// subject that is only the body's start, cut short, is left out, as RepeatsSubject in
// internal/view/view.go leaves it out of the agent's turn and inbox.
function personText(m) {
  const subject = m.subject || "";
  const body = m.body || "";
  if (repeatsSubject(subject, body)) return body;
  return body ? subject + "\n" + body : subject;
}

// repeatsSubject is RepeatsSubject in internal/view/view.go: a subject cut short with "…" or "..."
// that is only the start of the body, compared word by word, as a subject's spaces are collapsed.
function repeatsSubject(subject, body) {
  const end = ["…", "..."].find((e) => subject.endsWith(e));
  if (!end || !body) return false;
  const stem = subject.slice(0, -end.length);
  const words = (s) => s.trim().split(/\s+/).join(" ");
  return body.startsWith(stem) || words(body).startsWith(words(stem));
}

// A message's text: a person's as they wrote it, an agent's with its subject over its body. An
// agent's subject that only repeats the start of its body is left out, as a turn leaves it (#1049).
function messageText(m) {
  if (m.fromPerson) return h("div", { class: "chat-msg-body rich" }, rich(personText(m)));
  const subject = m.subject && !repeatsSubject(m.subject, (m.body || "").trim()) ? m.subject : "";
  return [subject && h("div", { class: "chat-msg-subject" }, subject), m.body && h("div", { class: "chat-msg-body rich" }, rich(m.body))];
}

// A message the person wrote that isn't among the messages yet. It looks written at once, as a
// chat app's does (#613): a clock while its push to the remote is on its way, which took 2.4 to
// 4.7 s from a Windows laptop on 2026-11-15, then the tick for sent. The daemon finishes the push
// if the page closes meanwhile.
function pendingMessage(item, head) {
  const failed = item.status === "failed";
  return h(
    "article",
    { class: "chat-msg chat-msg-own" + (head ? " chat-msg-first" : "") + (failed ? " chat-msg-failed" : ""), "data-cid": item.cid },
    item.text && h("div", { class: "chat-bubble rich" }, rich(item.text)),
    item.files?.length > 0 && fileList("out-" + item.cid, item.files, (i) => item.files[i]),
    failed
      ? h(
          "div",
          { class: "msg-failed" },
          h("span", { class: "msg-failed-why", "data-announce": /^not sent/i.test(item.error) ? item.error : "Not sent: " + item.error }, item.error),
          h("button", { class: "btn btn-ghost btn-xs", "data-key": "retry-" + item.cid, onclick: () => deliver(item) }, icon("retry"), "Try again"),
          h("button", { class: "btn btn-ghost btn-xs", "data-key": "edit-" + item.cid, onclick: () => editFailed(item) }, icon("pencil"), "Edit"),
        )
      : h("div", { class: "chat-msg-meta" }, sendingMark(item)),
  );
}

// The mark under a message on its way: a clock until it is on the remote, then one tick, as
// receiptMark shows it once the message is listed. Its tip promises the message goes out only
// once the daemon has it (item.posted): a new group's message waits here first for its members'
// tags (deliver). A button, as receiptMark's is, so the keyboard and a tap reach the tip.
function sendingMark(item) {
  const sent = item.status === "sent";
  const tip = sent ? "Sent" : item.posted ? "Sending: it goes out even if you close this page" : "Sending";
  return h(
    "button",
    {
      class: "receipt-mark",
      "data-state": sent ? "sent" : "sending",
      "data-key": "sending-" + item.cid,
      "data-tip": tip,
      "data-tip-labelled": "1",
      "aria-label": tip,
      onclick: (e) => showTipNow(e.currentTarget, e.pointerType === "mouse" ? "hover" : "tap"),
    },
    icon(sent ? "check" : "clock"),
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
      !filesOnly(m) && h("div", { class: "chat-bubble rich" }, rich(personText(m))),
      messageFiles(m),
      h("div", { class: "chat-msg-meta" }, time, copyButton(copy), receiptMark(m)),
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
      head && h("div", { class: "chat-msg-head" }, h("button", { type: "button", class: "chat-msg-from truncate", "data-profile": from, "aria-haspopup": "dialog", "data-key": "from-" + m.id }, from), m.fromPerson && personBadges(from, m.fromAddr), via && h("span", { class: "chat-msg-via truncate" }, via), time),
      !filesOnly(m) && messageText(m),
      messageFiles(m),
    ),
    h("div", { class: "msg-tools" }, copyButton(copy)),
  );
}

// How far one of the person's messages got (#26), as a chat app shows it: one tick for sent, two
// for delivered, and two in the accent colour for read (docs/design/receipts.md). The states in
// between, queued and handed over, read as sent, and so does a recipient whose Murmur sends no
// receipts. A message to several shows the lowest of them, so it reads as read only once everyone
// has read it. The tick's tooltip says what its state means, then who has it and since when, and
// nothing else shows them (Tyler, 2026-11-13): on hover, on keyboard focus, and on touch, where
// nothing hovers, on a tap. A message from before receipts, or past the newest 200, shows no mark
// rather than a guess.
const RECEIPT_RANK = { sent: 0, delivered: 1, read: 2 };

function receiptFold(state) {
  return state === "read" || state === "delivered" ? state : "sent";
}

function receiptLabel(state) {
  return state === "read" ? "Read" : state === "delivered" ? "Delivered" : "Sent";
}

// The tooltip's first line: the state, and in a few words what it leaves out, for one recipient or
// for several (Tyler, 2026-11-13: a few words, no sentences). Delivered is knowing a message waits,
// read is having it (#325); sent covers every step before.
const RECEIPT_MEANS = {
  sent: ["Sent", "Sent"],
  delivered: ["Delivered: not read yet", "Delivered: not all read yet"],
  read: ["Read", "Read"],
};

function receiptMeaning(state, several) {
  return RECEIPT_MEANS[state][several ? 1 : 0];
}

// One recipient as the tooltip lists them: their name, and for several their own state, with the
// time they got there if they got past sent.
function receiptEntry(r) {
  const state = receiptFold(r.state);
  const at = state !== "sent" && when(r.at);
  const today = at && new Date(at).toDateString() === new Date().toDateString();
  return { name: r.name, state, time: !at ? "" : today ? clockTime(at) : new Date(at).toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" }) };
}

function receiptLine(e, several) {
  return [e.name, several ? receiptLabel(e.state) : "", e.time].filter(Boolean).join(" · ");
}

function receiptMark(m) {
  const rs = ui.state?.receipts?.[m.id];
  if (!rs) return null;
  const entries = rs.map(receiptEntry).sort((a, b) => RECEIPT_RANK[b.state] - RECEIPT_RANK[a.state] || a.name.localeCompare(b.name));
  const state = entries.reduce((low, e) => (RECEIPT_RANK[e.state] < RECEIPT_RANK[low] ? e.state : low), "read");
  const several = entries.length > 1;
  // A message that found nobody in presence to count (a group with no member online) is sent.
  const lines = entries.length ? [receiptMeaning(state, several), ...entries.map((e) => receiptLine(e, several))] : ["Sent: nobody online"];
  // A button, so the keyboard reaches it and focus shows its tooltip. A tap shows it too: the page's
  // tips follow the mouse, and a touch has none. A tap elsewhere hides it, as any tip is. Its label is
  // the tip's text, so the tip doesn't describe it again (data-tip-labelled).
  return h(
    "button",
    {
      class: "receipt-mark",
      "data-state": entries.length ? state : "sent",
      "data-key": "receipt-" + m.id,
      "data-tip": lines.join("\n"),
      "data-tip-labelled": "1",
      "aria-label": lines.join(". "),
      onclick: (e) => showTipNow(e.currentTarget, e.pointerType === "mouse" ? "hover" : "tap"),
    },
    icon(entries.length && state !== "sent" ? "checks" : "check"),
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
    if (m.index > last) withNames(t.slice(last, m.index), out);
    if (m[1] && knownName(m[1].slice(1, -1))) out.push(nameButton(m[1].slice(1, -1), "rich-name", h("code", null, m[1].slice(1, -1))));
    else if (m[1]) out.push(h("code", null, m[1].slice(1, -1)));
    else if (m[2]) {
      const bold = [];
      withNames(m[2].slice(2, -2), bold);
      out.push(h("strong", null, bold));
    }
    else out.push(h("a", { href: m[3], target: "_blank", rel: "noopener noreferrer" }, m[3]));
    last = re.lastIndex;
  }
  if (last < t.length) withNames(t.slice(last), out);
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
  if (ui.readHeld) {
    // A mark failed a moment ago: the held timer asks again. "New" still shows where they came to.
    if (arriving) render();
    return;
  }
  for (const id of ids) ui.readLocal.add(id);
  api("/api/read", { method: "POST", body: JSON.stringify({ ids }) }).catch((e) => {
    if (e instanceof AuthError) return deny(e.token);
    for (const id of ids) ui.readLocal.delete(id);
    // Unread again, and no poll shows it: a state that hasn't changed rebuilds nothing. The render
    // asks for it at once, so it is held a poll's time first: a daemon that is down or failing
    // would otherwise be asked, and the page redrawn, without pause (#652). One timer at a time,
    // so two failures close together don't end each other's wait early.
    if (!ui.readHeld) {
      ui.readHeld = true;
      setTimeout(() => {
        ui.readHeld = false;
        markOpenRead();
      }, POLL_MS);
    }
    render();
  });
  render();
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
  // A pasted file is attached: a screenshot, most often. What also comes as text, plain and rich
  // both, is pasted as text: a spreadsheet's cells come as a picture too. One of the two isn't
  // enough to go by: a file copied in a file manager brings its name as plain text, and a picture
  // copied in a browser brings an <img> as rich text.
  el.onpaste = (e) => {
    const files = [...(e.clipboardData?.files || [])];
    const types = e.clipboardData?.types || [];
    if (!files.length || (types.includes("text/html") && types.includes("text/plain"))) return;
    e.preventDefault();
    attach(key, files);
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

// The device's mode in this repo (#681): online, dnd (do not disturb) or invisible.
function deviceMode() {
  return ui.state?.mode || "online";
}

// What each mode does: under the Settings row for the one that is on, and as each button's hint.
const MODE_HELP = {
  online: "Your agents here get messages as usual.",
  dnd: "Teammates still see this device and its agents, but only your messages reach them: everyone else's wait until you're back online, and senders are told why.",
  invisible: "Takes this device off " + NAME + " here, as if it were offline. Teammates stop seeing it, your agents here can't see or send anything, and you can't send. Messages wait until you're back online.",
};

// A quiet line in the sidebar while the device isn't online, which leads to Settings.
function modeNote() {
  const mode = deviceMode();
  if (mode === "online") return null;
  return h(
    "button",
    { class: "side-mode", title: "Open Settings", "data-key": "mode-note", onclick: () => go("settings") },
    icon(mode === "invisible" ? "eyeOff" : "bellOff"),
    h("span", null, mode === "invisible" ? "Invisible: your agents here are off " + NAME : "Do not disturb: others' messages to your agents here wait"),
  );
}

// setMode saves the mode, from the daemon's settings if the page hasn't loaded them.
async function setMode(mode) {
  try {
    const base = ui.settings || (await api("/api/settings"));
    if (ui.state) ui.state.mode = mode; // shown at once; the poll after the save has the daemon's
    saveSettings({ ...base, mode }, true);
    queueSettings(async () => poll());
  } catch (e) {
    if (e instanceof AuthError) return deny(e.token);
    ui.notice = e.message;
    render();
  }
}

// Under a conversation: somewhere to write, or, under one between two others, a way to either.
function paneFoot(c) {
  if (deviceMode() === "invisible")
    return h(
      "div",
      { class: "pane-foot" },
      h(
        "div",
        { class: "pane-foot-card" },
        h("span", { class: "pane-foot-note" }, "You're invisible, so nothing is sent from here."),
        h("button", { class: "btn btn-outline btn-sm", "data-key": "foot-online", onclick: () => setMode("online") }, icon("eye"), "Go online"),
      ),
    );
  const target = targetOf(c);
  if (target) return composer(c, target);
  const offer = primaryOffer();
  if (c.kind !== "dm") return h("div", { class: "pane-foot" }, h("div", { class: "pane-foot-card" }, h("span", { class: "pane-foot-note" }, offer ? "Use " + offer.name + (offer.nick ? " (" + offer.nick + ")" : "") + " from " + offer.repo + " to write here." : "Choose a name to write to " + (c.kind === "all" ? "everyone" : groupName(c).replace(/^This device$/, "this device")) + "."), h("button", { class: "btn btn-outline btn-sm", "data-key": "foot-name", onclick: () => askForName(render) }, offer ? "Use " + offer.name + "?" : "Choose a name")));
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
    if (btn) btn.disabled = !el.value.trim() && !attached.has(key);
  });
  el.placeholder = "Message " + target.label;
  el.setAttribute("aria-label", "Message " + target.label);
  const armed = ui.armed === key;
  const files = attached.get(key) || [];
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
      ui.fileNote?.key === key &&
        h(
          "p",
          { class: "composer-warn", "data-announce": "" },
          ui.fileNote.text,
          ui.fileNote.turnOn && h("button", { class: "composer-warn-btn", "data-key": "files-on-" + key, onclick: () => turnOnSending(key) }, "Turn on"),
        ),
      files.length > 0 &&
        h(
          "div",
          { class: "composer-files" },
          files.map((a, i) =>
            h(
              "span",
              { class: "composer-file" },
              pictureType(a.name) ? (a.thumb ||= h("img", { class: "composer-file-thumb", src: a.url, alt: "" })) : icon("file"),
              h("span", { class: "composer-file-name truncate" }, a.name),
              h("span", { class: "composer-file-size" }, fileSize(a.size)),
              h("button", { class: "composer-file-x", title: "Remove", "aria-label": "Remove " + a.name, "data-key": "detach-" + key + "-" + i, onclick: () => detach(key, a) }, icon("x")),
            ),
          ),
        ),
      el,
      h(
        "div",
        { class: "composer-bar" },
        h("button", { class: "composer-attach", title: ui.state?.repoFilesOff ? REPO_FILES_OFF : "Attach files", "aria-label": "Attach files", "data-key": "attach-" + key, onclick: () => (sendsFiles() ? pickFiles(key) : askToSend(key)) }, icon("paperclip")),
        h("button", { class: "composer-chip", title: "Message someone else", "aria-label": "To " + (c.kind === "dm" ? target.to : (c.kind === "all" ? "Everyone" : target.label) + " " + plural(target.count, "session")) + ". Message someone else", "data-key": "composer-to", onclick: () => openPicker() }, ...who, icon("chevronDown", "composer-chip-chev")),
        !NARROW.matches && h("span", { class: "composer-hint", "aria-hidden": "true" }, h("kbd", null, "Enter"), armed ? " again to send" : " to send"),
        h("button", { class: "composer-send", title: "Send", "aria-label": "Send", disabled: !el.value.trim() && !files.length, "data-key": "send-" + key, onclick: trySend }, icon("arrowUp")),
      ),
    ),
  );
}

function sendFrom(c, target, el, big) {
  const text = el.value.trim();
  const files = attached.get(c.key) || [];
  if (!text && !files.length) return;
  if (big && ui.armed !== c.key) {
    ui.armed = c.key;
    render();
    return;
  }
  ui.armed = null;
  const { messages, ...conversation } = c; // not the messages: this can outlive them
  const item = { cid: Date.now() + "-" + Math.random().toString(36).slice(2, 8), key: c.key, conversation, to: target.to, text, files, status: "sending" };
  ui.outbox.push(item);
  ui.notes.delete(c.key);
  attached.delete(c.key);
  ui.fileNote = null;
  el.value = "";
  toEnd = true;
  deliver(item);
  if (!NARROW.matches) el.focus({ preventScroll: true });
}

function namesOf(failed) {
  return [...new Set(failed.map((f) => f.s.name))].join(", ");
}

function editFailed(item) {
  ui.outbox = ui.outbox.filter((x) => x !== item);
  // It may have been all that listed its conversation, which the text is going back into.
  ui.restored = item.conversation;
  const el = composers.get(item.key);
  if (el) el.value = el.value && item.text ? item.text + "\n" + el.value : item.text || el.value;
  if (item.files?.length) attached.set(item.key, [...item.files, ...(attached.get(item.key) || [])]);
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
  // Do not disturb doesn't hold your messages to your own agents (#777).
  if (st === "dnd" && !f.device.self) return h("p", { class: "wait-line wait-warn" }, target.to + " is on do not disturb. Your message waits in its inbox.");
  if (!waiting) return null;
  const active = when(f.session.lastActive);
  const sentAt = mineLast ? when(last.at) || 0 : Date.now();
  if (active && Date.now() - active < 90000 && active >= sentAt - 5000) {
    return h("p", { class: "wait-line wait-working" }, h("span", { class: "dots", "aria-hidden": "true" }, h("i"), h("i"), h("i")), target.to + " is working");
  }
  return pending ? null : h("p", { class: "wait-line" }, "Waiting for " + target.to);
}
