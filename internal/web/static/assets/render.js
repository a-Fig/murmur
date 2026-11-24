// The shell: render rebuilds the page and keeps what is focused, scrolled and announced; going from
// view to view; the pane's header.

import { icon } from "./icons.js";
import { h, root, held } from "./dom.js";
import { NAME, COMMAND, VIEWS, TITLES, NARROW, ui, nameOf, unreadCount } from "./state.js";
import { loadSettings, tellApp } from "./sync.js";
import { setDrawer, connBadge, sidebar, updateInfo, repoMenu } from "./sidebar.js";
import { sessionsPage } from "./sessions.js";
import { recentPage } from "./recent.js";
import { sessionByAddr, tagMenu, placeProfile, profileCard } from "./tags.js";
import { conversationsPage, chatScroll, restoreChatScroll, markOpenRead, fitComposers } from "./messages.js";
import { viewerDialog } from "./files.js";
import { setupDialog, pickerDialog } from "./picker.js";
import { settingsPage } from "./settings.js";
import { loadUpdates } from "./updates.js";
import { refreshTip } from "./tips.js";

export { announce, render, go, setView, starRow };

let content; // the scrolling pane, kept across renders so its scroll position is too

// One polite live region for the page, which renders never rebuild: a region made anew each render
// would be read out every time, or never. What it says comes in a batch, a moment after the change,
// the region is cleared first so the same words can be said again, and it empties a while after, so
// moving it says nothing. It lives at the end of <body>, or inside an open aria-modal dialog, since
// Safari with VoiceOver hides everything outside one.
const live = h("div", { class: "sr-only", role: "status", "aria-live": "polite", "aria-atomic": "true" });
document.body.append(live);
let liveQueue = [];
let liveTimer = 0;
function announce(text) {
  if (!text) return;
  if (!liveQueue.length) live.textContent = "";
  liveQueue.push(text);
  clearTimeout(liveTimer);
  liveTimer = setTimeout(() => {
    live.textContent = liveQueue.join(" ");
    liveQueue = [];
    liveTimer = setTimeout(() => (live.textContent = ""), 7000);
  }, 150);
}

function placeLive() {
  const home = root.querySelector('[aria-modal="true"]') || document.body;
  if (live.parentNode !== home) home.append(live);
}

// What a render shows that is marked data-announce (an error, a warning, a lost connection) is
// said once, when it first shows, rather than each time a poll rebuilds it.
let announced = new Set();
function announceShown() {
  const now = new Set();
  for (const el of root.querySelectorAll("[data-announce]")) {
    const text = el.dataset.announce || el.textContent;
    now.add(text);
    if (!announced.has(text)) announce(text);
  }
  announced = now;
}

// A render builds the page anew. The focused element is kept, with the elements it sits in, and
// everything around them is replaced: moved or rebuilt, it would lose focus, and a screen reader
// would read it out again on every poll. Its counterpart in the new tree is the element with its
// data-key, or else its place among the focusable elements of its message. A match by place counts
// only if the message holds as many of them as before and the counterpart is the same kind of
// control with the same name, so a poll can never move the focus to a different control.
function twinOf(el, kids) {
  if (held.mark.parentNode) return held.mark;
  const find = (sel) => {
    for (const k of kids) {
      if (k.matches(sel)) return k;
      const f = k.querySelector(sel);
      if (f) return f;
    }
    return null;
  };
  const by = (n) => (n.dataset.key ? `[data-key="${CSS.escape(n.dataset.key)}"]` : n.dataset.id ? `[data-id="${CSS.escape(n.dataset.id)}"]` : n.dataset.cid ? `[data-cid="${CSS.escape(n.dataset.cid)}"]` : null);
  if (by(el)) return find(by(el));
  const anchor = el.parentElement?.closest("[data-id], [data-cid]");
  if (!anchor) return null;
  const focusable = (n) => [...n.querySelectorAll("button, a[href], input, textarea, [tabindex]")];
  const was = focusable(anchor);
  const twinAnchor = find(by(anchor));
  const now = twinAnchor ? focusable(twinAnchor) : [];
  const twin = now.length === was.length ? now[was.indexOf(el)] : null;
  const same = (a, b) => a.tagName === b.tagName && a.className === b.className && a.getAttribute("aria-label") === b.getAttribute("aria-label") && a.getAttribute("href") === b.getAttribute("href") && a.textContent === b.textContent;
  return twin && same(el, twin) ? twin : null;
}

function graft(parent, kids, el, twin) {
  const chain = [];
  for (let n = el; n && n !== parent; n = n.parentNode) chain.unshift(n);
  const twins = [];
  for (let n = twin; n; n = n.parentNode) {
    twins.unshift(n);
    if (kids.includes(n)) break;
  }
  if (chain.length !== twins.length || !kids.includes(twins[0])) return false;
  if (chain.some((n, i) => twins[i] !== held.mark && n.tagName !== twins[i].tagName)) return false;
  let list = kids;
  for (let i = 0; i < chain.length; i++) {
    const keep = chain[i];
    const at = list.indexOf(twins[i]);
    for (const n of [...parent.childNodes]) if (n !== keep) n.remove();
    keep.before(...list.slice(0, at));
    keep.after(...list.slice(at + 1));
    if (twins[i] === held.mark) break; // the element itself, which outlasts renders
    adopt(keep, twins[i]);
    if (i === chain.length - 1) keep.replaceChildren(...twins[i].childNodes);
    else list = [...twins[i].childNodes];
    parent = keep;
  }
  return true;
}

// Gives a kept element its counterpart's attributes and listeners.
function adopt(old, neu) {
  for (const a of [...old.attributes]) {
    if (neu.hasAttribute(a.name)) continue;
    old.removeAttribute(a.name);
    // A field whose render gave it a value, and now gives none, shows none: once typed in, it
    // would go on showing what was typed (idle minutes back to inherited after a save failed). A
    // field that never had a value attribute is a draft the render leaves alone.
    if (a.name === "value" && "value" in old) old.value = "";
  }
  for (const a of neu.attributes) {
    if (a.name === "style") old.style.cssText = neu.style.cssText; // the CSP takes styles only through the CSSOM
    else if (old.getAttribute(a.name) !== a.value) {
      // A field that has been typed in ignores its value attribute, so a render that changes it
      // (the tag menu's field, cleared once a tag is made) sets what it shows too.
      if (a.name === "value" && old.getAttribute("value") !== a.value && "value" in old && old.value !== a.value) old.value = a.value;
      old.setAttribute(a.name, a.value);
    }
  }
  for (const [type, fn] of old.listeners || []) old.removeEventListener(type, fn);
  for (const [type, fn] of neu.listeners || []) old.addEventListener(type, fn);
  old.listeners = neu.listeners;
}

function render() {
  if (ui.conn === "denied" || !ui.token) {
    root.replaceChildren(
      h(
        "div",
        { class: "gate" },
        h("img", { class: "gate-mark", src: "/assets/favicon.svg", alt: "" }),
        h("p", { class: "gate-title" }, NAME),
        h("p", { class: "gate-line" }, "This page needs its link again. Run " + COMMAND + " ui to open it."),
      ),
    );
    content = null;
    placeLive();
    return;
  }
  const focused = root.contains(document.activeElement) ? document.activeElement : null;
  const keep = document.activeElement?.dataset?.key;
  const caret = caretOf(document.activeElement);
  const scroll = content ? content.scrollTop : 0;
  const chat = chatScroll();
  const side = root.querySelector(".sidebar-content, .mini-scroll")?.scrollTop || 0;
  // With the focus in the pane, the new pane is built apart, and graft keeps the old one.
  if (!content || content.contains(focused)) content = h("div", { class: "content" });
  held.el = focused;
  held.mark = focused && h("span");
  let kids;
  let gone = null; // the key of what opened a tag menu that closed itself
  try {
    const inner = page(); // before the top bar, which counts what this settles
    content.className = "content" + (inner.classList.contains("chat-page") ? " content-chat" : "");
    content.replaceChildren(inner);
    // On a phone the sidebar is a drawer over the page, opened from the top bar. A dialog, the tag
    // menu or the repo menu holds the keyboard: the page behind it is inert until it closes.
    const drawer = NARROW.matches && ui.drawer;
    // A tag menu whose sessions have all gone closes here, before the page is made inert for it.
    if (ui.tagMenu && !ui.tagMenu.addrs.some(sessionByAddr)) {
      gone = ui.tagMenu.from;
      ui.tagMenu = null;
    }
    const modal = !!(ui.setup || ui.picker || ui.tagMenu || ui.repoMenu || ui.profile || ui.viewer);
    kids = [
      h(
        "div",
        { class: "app" + (drawer ? " drawer-open" : ""), inert: modal ? true : null },
        // Past the sidebar's many rows to the page, for the keyboard (WCAG 2.4.1).
        !NARROW.matches && h("button", { class: "skip-link", "data-key": "skip", onclick: () => root.querySelector("main")?.focus() }, "Skip to content"),
        sidebar(),
        drawer && h("div", { class: "drawer-scrim", "aria-hidden": "true", onclick: () => setDrawer(false) }),
        h(
          "main",
          { class: "main", tabindex: -1, "data-key": "main", inert: drawer ? true : null },
          showTopbar() ? topbar() : h("h1", { class: "sr-only" }, viewTitle()),
          ui.preferenceError && h("p", { class: "row-error section-head", "data-announce": "" }, ui.preferenceError),
          content,
        ),
      ),
      ui.setup && setupDialog(),
      ui.picker && pickerDialog(),
      ui.tagMenu && tagMenu(),
      ui.repoMenu && repoMenu(),
      ui.profile && profileCard(),
      ui.viewer && viewerDialog(),
    ].filter(Boolean);
  } finally {
    held.el = null;
  }
  const twin = focused && twinOf(focused, kids);
  if (twin && graft(root, kids, focused, twin)) {
    content = root.querySelector(".content") || content;
  } else {
    // The page changed around it too much to keep it: put the focus back on its counterpart if
    // its key finds one, or else on the pane it was in, rather than on some other control.
    if (twin && twin === held.mark) held.mark.replaceWith(focused);
    const inMain = !!focused?.closest("main");
    root.replaceChildren(...kids);
    const el = keep ? root.querySelector(`[data-key="${CSS.escape(keep)}"]`) : null;
    if (el?.isConnected) {
      el.focus({ preventScroll: true });
      restoreCaret(el, caret);
    } else if (focused && inMain) {
      root.querySelector("main")?.focus({ preventScroll: true });
    }
  }
  // The focus was in that menu, and went with it: back to what opened it, as closing it would do,
  // or to the page when that went too (the Tag button of a session that has gone).
  if (gone && (!document.activeElement || document.activeElement === document.body)) {
    root.querySelector(`[data-key="${CSS.escape(gone)}"]`)?.focus({ preventScroll: true });
    if (document.activeElement === document.body) root.querySelector("main")?.focus({ preventScroll: true });
  }
  content.scrollTop = scroll;
  const sideNow = root.querySelector(".sidebar-content, .mini-scroll");
  if (sideNow) sideNow.scrollTop = side;
  restoreChatScroll(chat);
  fitComposers();
  placeProfile();
  placeLive();
  announceShown();
  document.title = (unreadCount() ? "(" + unreadCount() + ") " : "") + NAME; // tellApp says the same
  tellApp();
  refreshTip();
  queueMicrotask(markOpenRead);
}

// A rebuild makes new fields, whose caret starts at one end.
function caretOf(el) {
  try {
    return el && typeof el.selectionStart === "number" ? [el.selectionStart, el.selectionEnd] : null;
  } catch {
    return null;
  }
}

function restoreCaret(el, caret) {
  try {
    if (el && caret) el.setSelectionRange(caret[0], caret[1]);
  } catch {}
}

// The main pane's header, as T3 Code's: slim, with the view's title. A conversation's pane has a
// header of its own, so on a wide page Conversations has none here, and on a phone an open
// conversation's header, with its way back, stands in for it.
function showTopbar() {
  if (ui.view !== "messages") return true;
  return NARROW.matches && !content?.querySelector(".chat-open");
}

function viewTitle() {
  if (ui.view !== "messages") return TITLES[ui.view];
  return ui.filter ? nameOf(ui.filter, ui.filter) : TITLES.messages;
}

function topbar() {
  // On a phone the sidebar, which carries unread messages and the update notice, is behind this
  // button, so a dot on it stands for them.
  const update = updateInfo();
  const flag = unreadCount() > 0 || !!update;
  return h(
    "header",
    { class: "topbar" },
    NARROW.matches &&
      h(
        "button",
        {
          class: "icon-btn topbar-menu",
          title: "Show the sidebar",
          "aria-label": "Show the sidebar" + (unreadCount() ? ", " + unreadCount() + " unread" : "") + (update ? ", " + update.text : ""),
          "aria-controls": "sidebar",
          "aria-expanded": ui.drawer ? "true" : "false",
          "data-key": "open-sidebar",
          onclick: () => setDrawer(true),
        },
        icon("panel"),
        flag && h("span", { class: "nav-dot" + (unreadCount() ? "" : " update-" + update.kind) }),
      ),
    h("h1", { class: "truncate" }, viewTitle()),
    NARROW.matches && connBadge(false),
    (ui.view === "sessions" || ui.view === "recent") && starLink(),
  );
}

// A way to star the repo on GitHub (#1233), at the top right of Sessions, Recent and a Conversations
// view with nothing open: not over a conversation or in Settings (Tyler, 2026-11-24). It is a plain
// link, with no star count, so the page makes no request of its own to GitHub. A phone's header
// has no room for it.
function starLink() {
  if (NARROW.matches) return null;
  return h(
    "a",
    { class: "btn btn-outline star-link", href: "https://github.com/a-Fig/murmur", target: "_blank", rel: "noopener noreferrer", "data-key": "star" },
    icon("github"),
    "Star us on GitHub",
  );
}

// Conversations has no top bar on a wide page, so with nothing open the link gets a row of the top
// bar's height to itself, and lands where the top bar's would.
function starRow() {
  return !NARROW.matches && h("div", { class: "star-row" }, starLink());
}

function page() {
  if (ui.view === "sessions") return sessionsPage();
  if (ui.view === "recent") return recentPage();
  if (ui.view === "settings") return settingsPage();
  return conversationsPage();
}

// Navigation and polling.

function go(view) {
  ui.drawer = false; // going somewhere from the phone's drawer closes it
  if (location.hash !== "#" + view) location.hash = view;
  else setView(view);
}

function setView(view) {
  ui.view = VIEWS.includes(view) ? view : "messages";
  if (ui.view === "settings") {
    loadSettings();
    loadUpdates();
  }
  render();
}
