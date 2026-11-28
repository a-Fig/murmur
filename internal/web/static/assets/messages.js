// The Messages view: conversations and their list, and the open conversation's pane. A message
// itself is drawn in chat.js, a face in faces.js, and the composer is composer.js.

import { icon } from "./icons.js";
import { h, when, ago, stamp, agoEl, plural, root } from "./dom.js";
import {
  NAME, POLL_MS, NARROW, ui, devices, nameOf, fromName, toName, findSession, me, myName,
  primaryOffer, isPerson, findPerson, personOnline, unreadIds,
} from "./state.js";
import { onlyMine, setOnlyMine } from "./preferences.js";
import { AuthError, api } from "./api.js";
import { deny } from "./sync.js";
import { render, go, starRow } from "./render.js";
import { personBadges } from "./sidebar.js";
import {
  modelOf, sessionState, dotted, prFor, prBadge, presence, settledSessions, isSettled, deviceLabel,
  host, stateWord, emptyState,
} from "./sessions.js";
import { groupMembers, inGroup, liveGroups, membersButton, tagsLine, profileLine } from "./tags.js";
import { startChat, askForName, newMessageButton, newGroupButton, openPicker, startPane } from "./picker.js";
import { avatar } from "./faces.js";
import { chatMessages } from "./chat.js";
import { focusComposer, pendingIn, paneFoot, waitLine } from "./composer.js";

export {
  messageDetail, conversationOf, groupConversation, conversations, dmKey, groupName,
  conversationsPage, scrollToEnd, chatScroll, restoreChatScroll, markOpenRead,
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

let toEnd = false; // the person just sent something: show it, wherever they had scrolled to

// A send shows the conversation's end on the next render (composer.js).
function scrollToEnd() {
  toEnd = true;
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
