// The page's constants and its one store, ui, with the lookups over what it holds: devices, names,
// people and unread messages.

import { when } from "./dom.js";

export {
  NAME, COMMAND, TOKEN_KEY, POLL_MS, HIDDEN_POLL_MS, MAX_MESSAGES, MAX_OWN_MESSAGES, VIEWS, TITLES, NARROW,
  ui, devices, named, namesLearned, learn, nameOf, fromName, toName, findSession, sessionCount, HIDDEN,
  personNames, me, myName, isPerson, people, findPerson, personOnline, unreadIds, unreadCount,
};

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
  // What sessions did other than send messages (#448): the daemon's, oldest first, and GitHub's.
  acts: [],
  actIds: new Set(),
  actCursor: "",
  github: [],
  recentKind: "all", // Recent shows all of it, or only "messages" or "activity"
  recentWho: "", // or only one session's, by name
  settings: null,
  settingsError: "",
  preferenceError: "",
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
  profile: null, // { name, x, top, from, draft, sent }: the profile card, open on that name (#689)
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
  readHeld: false, // a mark that failed is waiting a poll's time before it is asked for again (#652)
  reading: null, // the conversation someone was last seen looking at, null if nobody is looking
  newMark: null, // the open conversation's first unread message: { key, id }
  armed: null, // the group chat whose next send goes: its key
  fileNote: null, // why a file wasn't attached: { key, text, turnOn }, key the conversation's; turnOn, what to do once sending is turned on
  viewer: null, // the picture that is open: { file, name, size, from }, from the key of what opened it
  repoMenu: null, // the repo picker, open: { x, top, repos, opening, err } (#348)
  drawer: false, // on a phone, whether the sidebar's drawer is open
};

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

const HIDDEN = "hidden";

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
