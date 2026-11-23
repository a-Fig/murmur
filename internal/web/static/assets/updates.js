// Settings' Updates row (#1045): how Murmur updates itself on this machine, which the daemon keeps
// in the global git config, and going back to stable from a prerelease that misbehaves.

import { h, when, stamp, plural } from "./dom.js";
import { ui } from "./state.js";
import { AuthError, api } from "./api.js";
import { deny } from "./sync.js";
import { render } from "./render.js";
import { settingsRow } from "./settings.js";

export { loadUpdates, updatesRow, daemonNote };

const CHOICES = [
  ["off", "Off", "New releases show here and install only when you ask"],
  ["on", "On", "Stable releases install on their own"],
  ["nightly", "On, nightly", "Prereleases and nightlies install too, as soon as they are out"],
];

let updatesSeq = 0; // the newest request: only its answer is shown

// prev is what the page showed before a change, put back if the change fails.
async function updatesCall(path, init, prev) {
  const seq = ++updatesSeq;
  delete ui.rowErrors.updates;
  try {
    const got = await api(path, init);
    // After a change the daemon checks for a release within seconds, and the polls bring what
    // it found to the banner and About.
    if (seq === updatesSeq) ui.updates = got;
  } catch (e) {
    if (e instanceof AuthError) return deny(e.token);
    // 404: a page with no setting to change, as in tests.
    if (seq !== updatesSeq) {
      // a newer request's answer is the one shown
    } else if (e.status === 404) ui.updates = { unavailable: true };
    else {
      ui.rowErrors.updates = e.message;
      if (prev) ui.updates = prev;
    }
  }
  if (seq === updatesSeq) ui.updatesBusy = false; // an older request's answer doesn't free the choices
  render();
}

function loadUpdates() {
  if (!ui.updatesBusy) updatesCall("/api/updates");
  loadDaemon();
  daemonTimer ||= setInterval(() => ui.view === "settings" && ui.token && !document.hidden && loadDaemon(), DAEMON_EVERY_MS);
}

// About's quiet line (#1114) is what the daemon already says of itself in GET /api/health: the
// protocol it speaks and when it last learned of a release, from the file a check writes. The call
// asks GitHub nothing. It is read when Settings opens and every half minute after, as the polls
// don't carry it.
const DAEMON_EVERY_MS = 30000;
let daemon = null; // { token, protocol, checked }, checked in ms; 0 before any check has found a release
let daemonSeq = 0;
let daemonTimer = 0;

async function loadDaemon() {
  const token = ui.token;
  const seq = ++daemonSeq;
  try {
    const d = (await api("/api/health")).daemon || {};
    if (seq !== daemonSeq || token !== ui.token) return;
    const next = { token, protocol: d.protocol || 0, checked: when(d.updateChecked) || 0 };
    const same = daemon && daemon.token === token && daemon.protocol === next.protocol && checkedAgo(daemon.checked) === checkedAgo(next.checked);
    daemon = next;
    if (!same) render();
  } catch (e) {
    if (e instanceof AuthError) deny(e.token);
    // Otherwise the line shows what it lacks as nothing: a page with no health, as in tests.
  }
}

function checkedAgo(t) {
  if (!t) return "";
  const s = Math.max(0, (Date.now() - t) / 1000);
  if (s < 45) return "just now";
  if (s < 3600) return plural(Math.max(1, Math.floor(s / 60)), "minute") + " ago";
  if (s < 86400) return plural(Math.floor(s / 3600), "hour") + " ago";
  return plural(Math.floor(s / 86400), "day") + " ago";
}

// "Daemon protocol 8. Checked 5 minutes ago." Nothing yet is said of a check that hasn't found a
// release, and of nothing the daemon doesn't tell.
function daemonNote() {
  if (!daemon || daemon.token !== ui.token) return null;
  const parts = [];
  if (daemon.protocol) parts.push("Daemon protocol " + daemon.protocol + ".");
  if (daemon.checked) parts.push(h("span", { title: stamp(daemon.checked) }, "Checked " + checkedAgo(daemon.checked) + "."));
  return parts.length ? h("span", { class: "version-note" }, parts.flatMap((p, i) => (i ? [" ", p] : [p]))) : null;
}

function setUpdates(choice) {
  const prev = ui.updates;
  ui.updates = { ...prev, choice };
  ui.updatesBusy = true;
  render();
  updatesCall("/api/updates", { method: "PUT", body: JSON.stringify({ choice }) }, prev);
}

function backToStable() {
  ui.updatesBusy = true;
  render();
  updatesCall("/api/updates/back", { method: "POST" });
}

// The row: the three choices, what overrides them here, and what a return to stable holds.
function updatesRow() {
  const u = ui.updates;
  if (!u || u.unavailable) return null;
  const notes = [u.why, u.hold].filter(Boolean).join(" ");
  const control = h(
    "div",
    { class: "updates-control" },
    u.canGoBack &&
      h(
        "button",
        {
          class: "btn btn-outline btn-xs",
          title: "Install the latest stable release in place of this one. Later prereleases install only once one is newer than this one; stable releases still come",
          "data-key": "updates-back",
          disabled: ui.updatesBusy,
          onclick: backToStable,
        },
        "Go back to stable",
      ),
    h(
      "div",
      { class: "segmented", role: "group" },
      CHOICES.map(([v, label, tip]) =>
        // One change at a time: each is two git commands, which another's would interleave with.
        h("button", { "aria-pressed": u.choice === v ? "true" : "false", "data-tip": tip, "data-key": "updates-" + v, disabled: ui.updatesBusy, onclick: () => u.choice !== v && setUpdates(v) }, label),
      ),
    ),
  );
  return settingsRow("updates", "Updates", notes || null, control);
}
