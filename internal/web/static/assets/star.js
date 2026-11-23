// The ask to star Murmur on GitHub (#974).

import { h, when } from "./dom.js";
import { icon } from "./icons.js";
import { ui } from "./state.js";
import { api } from "./api.js";

export { askForStarAfter };

// Modelled on T3 Code's: a small card in a corner, asked once and never again. It waits until the
// person has seen Murmur work, a message between two agents arriving while the page is open, and
// then until they have left the page alone, in front, for 30 s, so it never lands mid-task. The
// history the page loads with doesn't count, nor history the daemon reads from the bus late (#138),
// so it never comes on first open.
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

// askForStarAfter is given the messages each poll brought after the first, and when that first came.
// A minute is allowed for a sender's clock, as when they are read out.
function askForStarAfter(arrived, loadedAt) {
  const live = (m) => (when(m.at) || Infinity) >= loadedAt - 60000;
  if (waiting || !arrived.some((m) => !m.fromPerson && !m.toPerson && live(m))) return;
  waiting = true;
  whenIdle().then(ask);
}

// whenIdle resolves once 30 s pass with the page in front and nothing pressed, scrolled or focused.
// The desktop app's window can be behind the editor and still count as shown and focused, so the
// app's own word on that (appFront) counts too.
function whenIdle() {
  return new Promise((resolve) => {
    let timer = 0;
    const wait = () => {
      clearTimeout(timer);
      timer = setTimeout(() => {
        if (document.hidden || ui.appFront === false || !document.hasFocus()) return; // the next event starts the wait over
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
    // Read again, not from the page's load: the app or another tab may have asked since. The save
    // goes to the daemon that answered, not one the page switched to meanwhile.
    const token = ui.token;
    if ((await api("/api/preferences")).starAsked || ui.token !== token) return;
    await api("/api/preferences", { method: "PATCH", body: JSON.stringify({ starAsked: true }) });
  } catch {
    return; // not saved, so not shown: it would come back on every load
  }
  show();
}

// The card is T3 Code's toast (its GitHubStarPrompt, drawn by coss-ui's toast on Base UI), copied:
// the same words, top right under the top bar, a glass card that slides in from the right, a round
// close button on its corner, Dismiss and Star on GitHub, and a swipe right or up that dismisses it.
// Every way it closes is a no for good, as there; starAsked was saved before it showed.
const SWIPE_PX = 40; // Base UI's swipe threshold

function show() {
  let closed = false;
  const close = (toward = "right") => {
    if (closed) return;
    closed = true;
    card.dataset.leaving = toward;
    card.style.removeProperty("--swipe-x");
    card.style.removeProperty("--swipe-y");
    card.classList.remove("on", "swiping");
    setTimeout(() => card.remove(), 500);
  };
  const card = h(
    "section",
    { class: "star-ask", role: "status", "aria-labelledby": "star-ask-title", onkeydown: (e) => e.key === "Escape" && close() },
    h("button", { type: "button", class: "star-ask-close", "aria-label": "Dismiss notification", onclick: () => close() }, icon("x")),
    h(
      "div",
      { class: "star-ask-content" },
      h(
        "div",
        { class: "star-ask-text" },
        h("div", { class: "star-ask-head" }, h("span", { class: "star-ask-icon" }, icon("star")), h("h2", { id: "star-ask-title" }, "Enjoying Murmur?")),
        h("p", {}, "Give us a star on GitHub to let us know!"),
      ),
      h(
        "div",
        { class: "star-ask-actions" },
        h("button", { type: "button", class: "btn btn-xs star-ask-dismiss", onclick: () => close() }, "Dismiss"),
        h("a", { class: "btn btn-xs btn-primary", href: REPO, target: "_blank", rel: "noopener noreferrer", onclick: () => close() }, "Star on GitHub"),
      ),
    ),
  );
  swipeToClose(card, close);
  // Outside #root, as the tooltip is, so a render leaves it alone. As a status it is read out
  // without taking the focus.
  document.body.append(card);
  card.getBoundingClientRect(); // lay it out off to the right first, so it slides in
  card.classList.add("on");
}

// A drag right or up past 40 px dismisses the card that way; a drag the other way gives a little
// and springs back, as Base UI's toasts do.
function swipeToClose(card, close) {
  let from = null;
  card.addEventListener("pointerdown", (e) => {
    if (e.button !== 0 || e.target.closest("a, button")) return;
    from = { x: e.clientX, y: e.clientY, id: e.pointerId };
    card.setPointerCapture(e.pointerId);
  });
  card.addEventListener("pointermove", (e) => {
    if (!from || e.pointerId !== from.id) return;
    const dx = e.clientX - from.x;
    const dy = e.clientY - from.y;
    card.classList.add("swiping");
    card.style.setProperty("--swipe-x", (dx > 0 ? dx : dx / 4) + "px");
    card.style.setProperty("--swipe-y", (dy < 0 ? dy : dy / 4) + "px");
  });
  const end = (e) => {
    if (!from || e.pointerId !== from.id) return;
    const dx = e.clientX - from.x;
    const dy = e.clientY - from.y;
    from = null;
    if (dx > SWIPE_PX) return close("right");
    if (dy < -SWIPE_PX) return close("up");
    card.classList.remove("swiping");
    card.style.removeProperty("--swipe-x");
    card.style.removeProperty("--swipe-y");
  };
  card.addEventListener("pointerup", end);
  card.addEventListener("pointercancel", end);
}
