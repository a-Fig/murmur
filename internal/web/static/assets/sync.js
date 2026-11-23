// Keeping the page in step with the daemon: polling, taking a new token, the settings in hand and their
// saving, delivering a message, and telling the desktop app. The variables that time a poll and say what
// the page has loaded are assigned only here.

import { when } from "./dom.js";
import {
  NAME, TOKEN_KEY, POLL_MS, HIDDEN_POLL_MS, MAX_MESSAGES, MAX_OWN_MESSAGES, VIEWS, NARROW, ui, devices,
  named, learn, fromName, personNames, me, myName, personOnline, unreadCount,
} from "./state.js";
import { loadPreferences } from "./preferences.js";
import { AuthError, takeToken, takeKey, api } from "./api.js";
import { announce, render } from "./render.js";
import { restartStuck } from "./sidebar.js";
import {
  inGroup, isGroupTag, tagWanted, sessionByAddr, tagQueues, groupJoins, groupMisses, tagSessions,
  forgetJoins,
} from "./tags.js";
import { conversationOf, composers, composing, deviceMode, namesOf } from "./messages.js";
import { fetched, attached, trimFetched, followFileSwitches, FILE_SWITCHES } from "./files.js";
import { askForName } from "./picker.js";
import { minuteEdits, cancelMinutes } from "./settings.js";
import { loadUpdates } from "./updates.js";

export {
  deliver, settingsSaving, settingsAt, queueSettings, saveSettings, loadSettings, deny, tokenEra,
  stateAskedAt, hasUnsent, poll, inApp, tellApp, start,
};

async function deliver(item) {
  item.status = "sending";
  item.error = "";
  item.posted = false;
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
      await Promise.all(joins);
      forgetJoins();
      missed = [...(groupMisses.get(item.to)?.values() || [])].map((f) => ({ s: sessionByAddr(f.s.addr), msg: f.msg }));
      item.missed = missed;
      if (missed.length && !devices().some((d) => d.sessions.some((s) => inGroup(s, item.to)))) {
        throw new Error("not sent: couldn't add " + namesOf(missed) + " to " + item.to + ": " + missed[0].msg);
      }
    }
    // keepalive: a page closed just as it sends still hands the message to the daemon, which then
    // finishes it whatever the page does (#613). A browser refuses keepalive past 64 KiB in flight,
    // and a UTF-16 unit is at most 3 bytes of UTF-8.
    item.posted = true;
    render();
    let body = JSON.stringify({ to: item.to, text: item.text });
    let keepalive = body.length * 3 < 60000;
    if (item.files?.length) {
      // With files it goes as a form, a part for each. Too large for keepalive: closing the page
      // before the upload ends loses the message.
      body = new FormData();
      body.append("to", item.to);
      body.append("text", item.text);
      for (const a of item.files) body.append("file", a.file, a.name);
      keepalive = false;
    }
    const sent = await api("/api/send", { method: "POST", body, keepalive });
    item.id = sent.id;
    // The message's files are these, in this order: the page has them without asking the daemon.
    if (item.files?.length) {
      item.files.forEach((a, i) => fetched.set(sent.id + ":" + i, a));
      trimFetched(); // held to FETCHED_KEEP as fetched files are: a page that only sends would keep them all
    }
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

// Settings go to the daemon one request at a time, in order: each PUT is the whole settings, so
// one that overtook an older one would be undone by it. Loads queue behind saves too. The page
// shows the newest choice while it is on its way, and only the answer to the newest request is
// shown; if that fails, the page goes back to what the daemon last confirmed.
let settingsQueue = Promise.resolve();
let settingsSeq = 0; // the newest save asked for
let confirmed = null; // the settings as the daemon last gave them
let settingsSaving = 0; // saves asked for and not yet answered
let settingsAt = 0; // when the last one was answered

function queueSettings(job) {
  const token = ui.token;
  settingsQueue = settingsQueue
    .then(() => (token === ui.token ? job() : null)) // a new token has started over
    .catch((e) => console.error(e));
}

function saveSettings(next, modeChange = false, filesChange = "") {
  // Each save carries the whole settings: the mode and the file switches come from the daemon's
  // state unless this save changes them, so a page whose settings were loaded before they changed
  // elsewhere can't undo it.
  if (!modeChange && ui.state) next = { ...next, mode: deviceMode() };
  if (ui.state) for (const k of FILE_SWITCHES) if (k !== filesChange) next = { ...next, [k]: !!ui.state[k] };
  // The daemon takes only the switch this save changes, and keeps the others as it has them.
  next = { ...next, filesSet: filesChange ? [filesChange] : [] };
  if (filesChange && ui.state) ui.state[filesChange] = !!next[filesChange]; // as setMode does
  const seq = ++settingsSeq;
  ui.settings = next;
  render();
  settingsSaving++;
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
  settingsQueue = settingsQueue.finally(() => {
    settingsSaving--;
    settingsAt = Date.now();
  });
  if (filesChange) queueSettings(async () => poll()); // the poll after the save has the daemon's
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
  return ui.outbox.length > 0 || attached.size > 0 || [...composers.values()].some((el) => el.value.trim());
}

// The state as far as the page shows it, for poll's signature. This device's lastSeen and its
// person's lastActive move on every poll, so with them in the signature the page rebuilt on every
// poll, with nothing on it changed (#618). Neither kind of time is shown as a time, on any device:
// a lastSeen only for a device that is unreachable, and of a person's lastActive only whether they
// are here now. So a reachable device's lastSeen is left out, and every person's lastActive is
// reduced to here or not.
function shownState(state) {
  if (!state) return state;
  const devices = (state.devices || []).map((d) => ({
    ...d,
    lastSeen: d.reachable ? null : d.lastSeen,
    person: d.person && { ...d.person, lastActive: personOnline({ ...d.person, device: d }) },
  }));
  return { ...state, devices };
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
    followFileSwitches(state);
    forgetJoins();
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
    tellApp(appNotes(arrived));
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
  // A tag chip waits for a state asked for after its change was answered (tagsOf), which can be
  // one that differs in nothing from the state before it: while any waits, each new state counts.
  const sig = JSON.stringify([shownState(ui.state), ui.cursor, ui.actCursor, ui.github, ui.conn, ui.restarting > 0, restartStuck(), ui.outbox.length, Math.floor(Date.now() / 15000), tagWanted.size ? stateAskedAt : 0]);
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

// The desktop app (`mur app`, #23) is this page in a window of its own, which gives it murmurApp.
// The window is told the unread count, for its title and icon, and the messages to the person as
// they arrive, which it shows as notifications when it isn't in front. A browser has no murmurApp.
function inApp() {
  return typeof window.murmurApp === "function";
}

let toldApp = "";
function tellApp(notes = []) {
  if (!inApp()) return;
  const unread = unreadCount();
  const title = (unread ? "(" + unread + ") " : "") + NAME;
  // The window's frame follows the theme's color scheme.
  const dark = getComputedStyle(document.documentElement).colorScheme === "dark";
  const said = [location.host, title, dark].join(" ");
  if (said === toldApp && !notes.length) return;
  toldApp = said;
  const seen = !document.hidden && document.hasFocus();
  window.murmurApp({ host: location.host, title, unread, notes, seen, dark }).catch(() => {});
}

function appNotes(list) {
  if (!inApp()) return [];
  const unread = new Set(ui.state?.account?.unread || []);
  const mine = list.filter((m) => unread.has(m.id) && fromName(m) !== myName());
  if (mine.length > 3) return [{ title: NAME, body: mine.length + " new messages" }];
  return mine.map((m) => ({ title: fromName(m), body: m.subject || "" }));
}

// A fresh link pasted into a tab already showing the page changes only the fragment, so the
// page takes the new token here and starts over with it.
async function start() {
  await takeKey();
  const token = takeToken();
  if (token !== ui.token) {
    Object.assign(ui, { token, conn: "connecting", state: null, messages: [], cursor: "", settings: null, updates: null, conversation: null, chatListTop: 0, draftTo: null, draftGroup: null, restored: null, outbox: [], setup: null, picker: null, newMark: null, armed: null, repoMenu: null });
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
    const era = tokenEra;
    ui.preferenceError = "";
    if (token) await loadPreferences(era);
    if (era !== tokenEra) return;
  }
  ui.view = VIEWS.includes(location.hash.slice(1)) ? location.hash.slice(1) : "messages";
  render();
  if (!ui.token) return;
  loadSettings(); // the wake-up settings say which of this device's sessions show red
  if (ui.view === "settings") loadUpdates();
  poll();
}

document.addEventListener("scroll", () => (scrolledAt = Date.now()), { capture: true, passive: true });
