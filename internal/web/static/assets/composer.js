// The composer: one textarea per conversation that outlasts renders, the device's mode, sending,
// and editing a message that failed.

import { icon } from "./icons.js";
import { h, when, plural, root } from "./dom.js";
import {
  NAME, NARROW, ui, fromName, findSession, sessionCount, me, myName, primaryOffer, isPerson,
  findPerson, personOnline,
} from "./state.js";
import { AuthError, api } from "./api.js";
import { deliver, queueSettings, saveSettings, deny, poll } from "./sync.js";
import { render, go } from "./render.js";
import { sessionState, statusDot } from "./sessions.js";
import { groupMembers } from "./tags.js";
import {
  attached, pictureType, fileSize, sendsFiles, askToSend, turnOnSending, attach, detach, pickFiles,
  REPO_FILES_OFF,
} from "./files.js";
import { startGroup, startChat, askForName, openPicker } from "./picker.js";
import { groupName, scrollToEnd } from "./messages.js";

export {
  composers, composing, fitComposers, focusComposer, pendingIn, deviceMode, MODE_HELP, modeNote,
  setMode, paneFoot, namesOf, editFailed, waitLine,
};

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
  scrollToEnd();
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
