// The ask to star Murmur on GitHub (#974).

import { h } from "./dom.js";
import { icon } from "./icons.js";
import { api } from "./api.js";

export { askForStarAfter };

// Modelled on T3 Code's: a small card in a corner, asked once and never again. It waits until the
// person has seen Murmur work, a message between two agents arriving while the page is open (the
// history the page loads with doesn't count, so it never comes on first open), and then until they
// have left the page alone for 30 s, so it never lands mid-task.
//
// The daemon keeps that it asked (starAsked, in the page's preferences), so the desktop app and a
// browser on this device ask once between them. It is saved before the card shows, and the card
// shows only if that worked: whatever happens next, a reload, a click or nothing, there is no
// second ask. Murmur can't tell whether someone starred without their GitHub login, so it doesn't
// try. Nothing of this goes to the bus.
const REPO = "https://github.com/a-Fig/murmur";
const IDLE_MS = 30000;
const ACTIVITY = ["pointerdown", "keydown", "wheel", "focus"];
let waiting = false;

// askForStarAfter is given the messages each poll brought after the first.
function askForStarAfter(arrived) {
  if (waiting || !arrived.some((m) => !m.fromPerson && !m.toPerson)) return;
  waiting = true;
  whenIdle().then(ask);
}

// whenIdle resolves once 30 s pass with the page in front and nothing pressed, scrolled or focused.
function whenIdle() {
  return new Promise((resolve) => {
    let timer = 0;
    const wait = () => {
      clearTimeout(timer);
      timer = setTimeout(() => {
        if (document.hidden || !document.hasFocus()) return; // the next event starts the wait over
        for (const e of ACTIVITY) window.removeEventListener(e, wait, true);
        resolve();
      }, IDLE_MS);
    };
    for (const e of ACTIVITY) window.addEventListener(e, wait, { capture: true, passive: true });
    wait();
  });
}

async function ask() {
  try {
    // Read again, not from the page's load: the app or another tab may have asked since.
    if ((await api("/api/preferences")).starAsked) return;
    await api("/api/preferences", { method: "PATCH", body: JSON.stringify({ starAsked: true }) });
  } catch {
    return; // not saved, so not shown: it would come back on every load
  }
  show();
}

function show() {
  const close = () => {
    card.classList.remove("on");
    setTimeout(() => card.remove(), 200);
  };
  const card = h(
    "section",
    { class: "star-ask", role: "status", "aria-labelledby": "star-ask-title", onkeydown: (e) => e.key === "Escape" && close() },
    icon("star", "star-ask-icon"),
    h(
      "div",
      { class: "star-ask-body" },
      h("h2", { id: "star-ask-title" }, "Enjoying Murmur?"),
      h("p", {}, "A star on GitHub helps other teams find it."),
      h(
        "div",
        { class: "star-ask-actions" },
        h("button", { type: "button", class: "btn btn-sm btn-ghost", onclick: close }, "No thanks"),
        h("a", { class: "btn btn-sm btn-primary", href: REPO, target: "_blank", rel: "noopener noreferrer", onclick: close }, "Star on GitHub"),
      ),
    ),
  );
  // Outside #root, as the tooltip is, so a render leaves it alone. As a status it is read out
  // without taking the focus.
  document.body.append(card);
  requestAnimationFrame(() => card.classList.add("on"));
}
