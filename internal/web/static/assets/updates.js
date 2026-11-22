// Settings' Updates row (#1045): how Murmur updates itself on this machine, which the daemon keeps
// in the global git config, and going back to stable from a prerelease that misbehaves.

import { h } from "./dom.js";
import { ui } from "./state.js";
import { AuthError, api } from "./api.js";
import { deny } from "./sync.js";
import { render } from "./render.js";
import { settingsRow } from "./settings.js";

export { loadUpdates, updatesRow };

const CHOICES = [
  ["off", "Off", "Nothing installs on its own; Settings shows the command for a new release"],
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
    if (e.status === 404) ui.updates = { unavailable: true };
    else if (seq === updatesSeq) {
      ui.rowErrors.updates = e.message;
      if (prev) ui.updates = prev;
    }
  }
  ui.updatesBusy = false;
  render();
}

function loadUpdates() {
  if (!ui.updatesBusy) updatesCall("/api/updates");
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
          title: "Install the latest stable release in place of this one, and hold updates until a newer release than this one is out",
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
        h("button", { "aria-pressed": u.choice === v ? "true" : "false", "data-tip": tip, "data-key": "updates-" + v, onclick: () => u.choice !== v && setUpdates(v) }, label),
      ),
    ),
  );
  return settingsRow("updates", "Updates", notes || null, control);
}
