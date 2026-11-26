// Files with a message: attaching them, fetching and showing them, and the picture viewer.

import { icon } from "./icons.js";
import { h, plural, root } from "./dom.js";
import { NARROW, ui } from "./state.js";
import { AuthError, api } from "./api.js";
import { settingsSaving, settingsAt, saveSettings, deny, tokenEra, stateAskedAt } from "./sync.js";
import { render } from "./render.js";
import { personText, composers } from "./messages.js";
import { refocus } from "./picker.js";

export {
  fetched, attached, pictureType, fileSize, trimFetched, filesOnly, followFileSwitches, FILE_SWITCHES,
  messageFiles, fileList, closeViewer, viewerDialog, sendsFiles, askToSend, turnOnSending, attach, detach,
  pickFiles, REPO_FILES_OFF,
};

// Files with a message (#570, docs/design/files.md).

// What the daemon takes with one message (internal/bus/files.go).
const MAX_FILES = 8;
const MAX_FILE = 10 << 20;
const MAX_FILES_TOTAL = 20 << 20;
// What the page shows as a picture, by the end of its name. Not SVG, which is a document.
const PICTURES = { png: "image/png", jpg: "image/jpeg", jpeg: "image/jpeg", gif: "image/gif", webp: "image/webp" };
const FETCHED_KEEP = 96 << 20; // how many bytes of what was fetched the page holds on to
const FETCH_AGAIN = 30000; // how long before a picture that couldn't be fetched is asked for again
// How many files are asked for at once. A browser gives a page six connections, and each of these
// can wait on a fetch from the remote while the polls and a send need theirs.
const FETCH_AT_ONCE = 2;
let fetchSlots = FETCH_AT_ONCE;
const fetchWaits = [];

async function inFetchSlot(run) {
  if (fetchSlots) fetchSlots--;
  else await new Promise((go) => fetchWaits.push(go));
  try {
    return await run();
  } finally {
    const next = fetchWaits.shift();
    if (next) next();
    else fetchSlots++;
  }
}

// A message's files as the page has them, by "id:place": { state, url, size, why, at, done }, where
// state is "loading", "here", "gone" (it can't be had: a 404) or "failed" (not just now).
// A picture's entry keeps the elements that show it, so the renders polling causes don't load it
// again. A file the person attached is an entry too, from the moment it is attached.
const fetched = new Map();
const attached = new Map(); // a conversation's key → the files its next message takes

function pictureType(name) {
  return PICTURES[name.slice(name.lastIndexOf(".") + 1).toLowerCase()];
}

function fileSize(n) {
  if (n < 1024) return plural(n, "byte");
  if (n < 1 << 20) return Math.round(n / 1024) + " KB";
  return (n / (1 << 20)).toFixed(1) + " MB";
}

// Asks the daemon for one file of a message, once. A file that isn't on this machine is fetched
// from the remote first, which takes a moment. again asks once more for one that wasn't had.
function fetchFile(id, i, name, again) {
  const key = id + ":" + i;
  let f = fetched.get(key);
  const retry = f && (f.state === "failed" || f.state === "gone") && (again || (f.state === "failed" && Date.now() - f.at > FETCH_AGAIN));
  if (f && !retry) return f;
  f = { state: "loading" };
  fetched.set(key, f);
  f.done = inFetchSlot(async () => (await api("/api/file?id=" + encodeURIComponent(id) + "&i=" + i, { raw: true })).blob())
    .then((blob) => {
      if (f.dropped) return; // Receive files went off while it came
      // The daemon sends every file as bytes to save; a picture is shown as the kind its name says.
      const type = pictureType(name);
      f.url = URL.createObjectURL(type ? new Blob([blob], { type }) : blob);
      f.size = blob.size;
      f.state = "here";
      trimFetched(f);
    })
    .catch((e) => {
      if (e instanceof AuthError) {
        fetched.delete(key);
        return deny(e.token);
      }
      f.state = e.status === 404 ? "gone" : "failed";
      f.why = e.message; // it starts with the file's name, which stays as it is
      f.at = Date.now();
    })
    .then(render);
  return f;
}

// Lets go of the oldest of what was fetched, past FETCHED_KEEP, except what the page is showing.
function trimFetched(keep) {
  let total = 0;
  for (const f of fetched.values()) total += f.size || 0;
  for (const [key, f] of fetched) {
    if (total <= FETCHED_KEEP) break;
    if (f === keep || f.state !== "here" || f.el?.isConnected || ui.viewer?.file === f) continue;
    URL.revokeObjectURL(f.url);
    fetched.delete(key);
    total -= f.size;
  }
}

function saveFile(name, url) {
  const a = h("a", { href: url, download: name, hidden: true });
  document.body.append(a);
  a.click();
  a.remove();
}

// A message of files alone, sent from a page, has their names for its text: the daemon gave it
// them, since a message needs a subject. The files say as much.
function filesOnly(m) {
  return !!m.files?.length && m.fromPerson && personText(m) === m.files.map((f) => f.name).join(", ");
}

// The file switches as the daemon has them, changed here or on another page. Receive files off
// drops what this page fetched from other devices, and their files aren't shown (#765).
function followFileSwitches(state) {
  const set = ui.settings;
  if (!set) return;
  if (settingsSaving || stateAskedAt < settingsAt) {
    // A save of this page's is on its way, or this state was asked for before one was answered:
    // the page's are newer.
    for (const k of FILE_SWITCHES) state[k] = !!set[k];
    return;
  }
  for (const k of FILE_SWITCHES) if (!!set[k] !== !!state[k]) ui.settings = { ...ui.settings, [k]: !!state[k] };
  const was = receiving;
  receiving = !!state.receiveFiles;
  // Messages the page has came without their files while it was off: ask again for theirs.
  if (receiving && was === false) refetchFiles(tokenEra);
  if (receiving) return;
  for (const [key, f] of fetched) {
    if (!f.remote) continue;
    fetched.delete(key);
    f.dropped = true; // one still on its way is let go of when it comes
    if (f.url) URL.revokeObjectURL(f.url);
    if (ui.viewer?.file === f) ui.viewer = null;
  }
}

let receiving = null; // the person's Receive files, as the page last followed it

// The four file switches (#765): the agents' Send files and Receive files, and the person's own on
// this page (Tyler, 2026-11-16).
const FILE_SWITCHES = ["sendFiles", "receiveFiles", "agentsSendOff", "agentsReceiveOff"];

// refetchFiles gives the messages the page has the files the daemon now shows for them.
async function refetchFiles(era) {
  const byId = new Map(ui.messages.map((m) => [m.id, m]));
  let cursor = "";
  try {
    for (let pages = 0; pages < 20; pages++) {
      const r = await api("/api/messages?cursor=" + encodeURIComponent(cursor));
      if (era !== tokenEra) return;
      for (const m of r.messages || []) if (m.files?.length && byId.has(m.id)) byId.get(m.id).files = m.files;
      if (!r.messages?.length || !r.cursor || r.cursor === cursor) break;
      cursor = r.cursor;
    }
  } catch (e) {
    if (e instanceof AuthError) return deny(e.token);
  }
  render();
}

// fromElsewhere says message m came from another device, whose files Receive files governs.
function fromElsewhere(m) {
  return !!m.fromAddr && !!ui.state?.device && !m.fromAddr.startsWith(ui.state.device + "/");
}

// Under a message's text: its pictures, shown, and its other files, each a button that saves it.
function messageFiles(m) {
  if (!m.files?.length) return null;
  if (fromElsewhere(m) && !ui.settings?.receiveFiles) return null;
  const remote = fromElsewhere(m);
  return fileList(m.id, m.files, (i, load, again) => {
    const f = load ? fetchFile(m.id, i, m.files[i].name, again) : fetched.get(m.id + ":" + i);
    if (f) f.remote = remote;
    return f;
  });
}

// fileAt gives the entry of the file in a place: as it is, or, with load, fetched if it wasn't.
function fileList(id, files, fileAt) {
  return h(
    "div",
    { class: "msg-files" },
    files.map((f, i) => {
      const key = "file-" + id + "-" + i;
      const picture = !!pictureType(f.name);
      const got = fileAt(i, picture); // a picture is fetched to be shown, anything else when it is asked for
      const state = got?.state;
      if (picture && state === "here") {
        got.el ||= h("img", { class: "msg-picture-img", src: got.url, alt: "" });
        return h("button", { class: "msg-picture", title: f.name, "aria-label": "Open " + f.name, "data-key": key, onclick: () => openViewer(got, f, key) }, got.el);
      }
      if (picture && state === "loading") return h("div", { class: "msg-picture msg-picture-wait", role: "img", "aria-label": f.name + ", loading" });
      const missing = state === "gone" || state === "failed";
      const open = () => {
        if (fileAt(i)?.state === "loading") return;
        const file = fileAt(i, true, true);
        render();
        file.done.then(() => file.state === "here" && !file.dropped && !picture && saveFile(f.name, file.url));
      };
      return h(
        "button",
        { class: "msg-file" + (missing ? " msg-file-missing" : ""), title: missing ? got.why : "Save " + f.name, "aria-busy": state === "loading" ? "true" : null, "data-key": key, onclick: open },
        icon(state === "here" ? "download" : "file"),
        h("span", { class: "msg-file-name truncate" }, f.name),
        h("span", { class: "msg-file-size" }, state === "loading" ? "Fetching…" : state === "gone" ? "Not here" : state === "failed" ? "Couldn't load" : fileSize(f.size)),
      );
    }),
  );
}

// A picture, opened: as large as the window lets it be.
function openViewer(file, f, from) {
  ui.viewer = { file, name: f.name, size: f.size, from };
  render();
  root.querySelector('[data-key="viewer-close"]')?.focus();
}

function closeViewer() {
  const from = ui.viewer?.from;
  if (ui.viewer) ui.viewer.file.big = null;
  ui.viewer = null;
  render();
  refocus(from);
}

function viewerDialog() {
  const v = ui.viewer;
  v.file.big ||= h("img", { class: "viewer-img", src: v.file.url, alt: v.name });
  return h(
    "div",
    {
      class: "scrim viewer-scrim",
      onclick: (e) => {
        if (e.target === e.currentTarget) closeViewer();
      },
      onkeydown: (e) => {
        if (e.key === "Escape") {
          e.preventDefault();
          closeViewer();
        }
      },
    },
    h(
      "div",
      { class: "viewer", role: "dialog", "aria-modal": "true", "aria-label": v.name },
      v.file.big,
      h(
        "div",
        { class: "viewer-bar" },
        h("span", { class: "viewer-name truncate" }, v.name),
        h("span", { class: "viewer-size" }, fileSize(v.size)),
        h("button", { class: "btn btn-ghost btn-sm", "data-key": "viewer-save", onclick: () => saveFile(v.name, v.file.url) }, icon("download"), "Save"),
        h("button", { class: "btn btn-outline btn-sm", "data-key": "viewer-close", onclick: closeViewer }, "Close"),
      ),
    ),
  );
}

// Adds files to a conversation's next message, held to what a message takes: the daemon would
// refuse the rest only once they had been uploaded.
// The person's Send files is on, in this repo (#765): off, a file attached, dropped or pasted asks
// them to turn it on there, and goes on once they do (Tyler, 2026-11-16).
const REPO_FILES_OFF = "The mesh owner has turned off file transfers for this repo.";

function sendsFiles() {
  return !ui.state?.repoFilesOff && !!ui.settings?.sendFiles;
}

function askToSend(key, files) {
  ui.fileNote = ui.state?.repoFilesOff
    ? { key, text: REPO_FILES_OFF }
    : { key, text: "Sending files is off for you in this repo.", turnOn: files || "pick" };
  render();
}

// Turns the person's Send files on, then does what asked for it: opens the picker, in the same
// click, or attaches what was dropped or pasted.
function turnOnSending(key) {
  if (ui.state?.repoFilesOff) return askToSend(key);
  const then = ui.fileNote?.turnOn;
  ui.fileNote = null;
  saveSettings({ ...ui.settings, sendFiles: true }, false, "sendFiles");
  if (then === "pick") pickFiles(key);
  else if (then) attach(key, then);
  else render();
}

function attach(key, files) {
  if (!sendsFiles()) return askToSend(key, files);
  const list = attached.get(key) || [];
  let note = "";
  for (const file of files) {
    const total = list.reduce((n, a) => n + a.size, 0);
    if (list.length >= MAX_FILES) note = "A message takes " + MAX_FILES + " files at most.";
    else if (!file.size) note = file.name + " is empty.";
    else if (file.size > MAX_FILE) note = file.name + " is " + fileSize(file.size) + ". A file can be " + (MAX_FILE >> 20) + " MB at most.";
    else if (total + file.size > MAX_FILES_TOTAL) note = "A message's files can come to " + (MAX_FILES_TOTAL >> 20) + " MB at most.";
    else {
      // Shown or saved as a fetched file is: a picture by its name, anything else as bytes,
      // whatever kind the browser took it for.
      const blob = new Blob([file], { type: pictureType(file.name) || "application/octet-stream" });
      list.push({ state: "here", file, name: file.name, size: file.size, url: URL.createObjectURL(blob), done: Promise.resolve() });
    }
  }
  if (list.length) attached.set(key, list);
  ui.fileNote = note ? { key, text: note } : null;
  render();
  if (!NARROW.matches) composers.get(key)?.focus({ preventScroll: true });
}

function detach(key, a) {
  const list = (attached.get(key) || []).filter((x) => x !== a);
  URL.revokeObjectURL(a.url);
  if (list.length) attached.set(key, list);
  else attached.delete(key);
  ui.fileNote = null;
  render();
  composers.get(key)?.focus({ preventScroll: true });
}

function pickFiles(key) {
  const input = h("input", { type: "file", multiple: true });
  input.addEventListener("change", () => attach(key, [...input.files]));
  input.click();
}
