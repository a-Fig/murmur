// Tooltips, drawn by the page.

import { h, root } from "./dom.js";

export { refreshTip, showTipNow };

// Tooltips, drawn by the page rather than the browser: a browser's title waits about a second and
// can't be styled. The page's tip is only for what a person can't already read: a button that is
// only an icon, a state dot, a time (its tip has the full date), and "+N" more tags. Rows, messages,
// labelled buttons and badges keep the browser's title, which waits long enough not to get in the
// way. The exception is a choice in a segmented control, whose tip says what it does before it is picked. While the page's tip shows, the title moves to data-tip-title so the browser's own doesn't
// show too, and it comes back after, so assistive tech still reads it.
//
// The tip shows 150 ms after the pointer settles. Once one is showing, the next shows at once, so
// sliding along a row of buttons reads each. Keyboard focus shows it too. The tip lives outside
// #root, so a render doesn't take it away.
// While an element's title is borrowed, the element points at the tip with aria-describedby, so a
// screen reader still reads the title as its description.
// It is a tooltip only while it shows: an empty one would be a tooltip with no name (WCAG 4.1.2).
// An element marked data-tip-lines, with its lines in a tipLines property, shows one of them,
// picked each time its tip opens and never the one before; it keeps that line while it stays open,
// through renders. With
// data-tip-align="end" the tip lines up with the element's right edge, for one at the top right.
const tip = h("div", { class: "tip", id: "page-tip" });
document.body.append(tip);
let tipFor = null; // the element whose tip shows, or is about to
let tipTimer = 0;
let tipHiddenAt = 0;
let tipShown = false;
let tipBy = ""; // what opened the tip: "hover", "focus" or "tap"
let pointerX = 0;
let pointerY = 0;
let picked = ""; // the line a tipLines tip shows while it is open
let pickedBefore = -1;

function tipText(el) {
  if (el.tipLines) return picked;
  return el.dataset.tip || el.dataset.tipTitle || el.getAttribute("title") || "";
}

function pickLine(el) {
  if (!el.tipLines) return;
  const others = el.tipLines.map((_, i) => i).filter((i) => i !== pickedBefore);
  pickedBefore = others[Math.floor(Math.random() * others.length)];
  // A line is text, or a function that draws it, for a line with a picture in it. It is drawn once,
  // so a render doesn't restart the picture's motion.
  const line = el.tipLines[pickedBefore] || "";
  picked = typeof line === "function" ? line() : line;
}

let filled = "";
function fillTip(content) {
  if (content === filled) return;
  filled = content;
  tip.replaceChildren(...[content].flat());
}

// Takes an element's title for the page's tip, and gives it back.
function borrowTitle(el) {
  const t = el.getAttribute("title");
  if (t) {
    el.dataset.tipTitle = t;
    el.removeAttribute("title");
  }
  fillTip(tipText(el));
  if (!el.hasAttribute("aria-describedby") && !el.dataset.tipLabelled) {
    el.setAttribute("aria-describedby", tip.id);
    el.dataset.tipDescribed = "1";
  }
}
function returnTitle(el) {
  if (el?.dataset.tipTitle) {
    el.setAttribute("title", el.dataset.tipTitle);
    delete el.dataset.tipTitle;
  }
  if (el?.dataset.tipDescribed) {
    el.removeAttribute("aria-describedby");
    delete el.dataset.tipDescribed;
  }
}

function showTip(el, atPointer) {
  clearTimeout(tipTimer);
  if (!el.isConnected) return hideTip();
  const text = tipText(el);
  if (!text) return hideTip();
  fillTip(text);
  tip.setAttribute("role", "tooltip");
  tip.classList.add("on");
  tipShown = true;
  const r = el.getBoundingClientRect();
  const t = tip.getBoundingClientRect();
  const mid = atPointer && r.width > 120 ? pointerX : r.left + r.width / 2;
  const left = el.dataset.tipAlign === "end" ? r.right - t.width : mid - t.width / 2;
  const x = Math.max(6, Math.min(left, window.innerWidth - t.width - 6));
  const above = r.top - t.height - 6 >= 6;
  tip.style.left = x + "px";
  tip.style.top = (above ? r.top - t.height - 6 : r.bottom + 6) + "px";
  tip.dataset.side = above ? "top" : "bottom";
}

function hideTip() {
  clearTimeout(tipTimer);
  if (tipShown) tipHiddenAt = Date.now();
  tipShown = false;
  returnTitle(tipFor);
  tipFor = null;
  tipBy = "";
  tip.classList.remove("on");
  tip.removeAttribute("role");
}

// refreshTip runs after each render. A tip that shows was for an element the render may have
// replaced, and whose text may have changed (a tick that moved on to read): it moves to the element
// with the same data-key and reads its text again, or goes if there is none. A hover tip is for what
// is under the mouse now, which a render may have scrolled something else under.
let renderedAt = 0;
function refreshTip() {
  renderedAt = Date.now();
  if (!tipShown || !tipFor) return;
  const key = tipFor.dataset.key;
  if (tipBy === "hover") {
    const el = tipTarget(document.elementFromPoint(pointerX, pointerY));
    if (!el) return hideTip();
    if (el !== tipFor && !(key && el.dataset.key === key)) return queueTip(el, true, "hover");
    tipFor = el;
    borrowTitle(el);
    return showTip(el, true);
  }
  const el = tipFor.isConnected ? tipFor : key ? root.querySelector(`[data-key="${CSS.escape(key)}"]`) : null;
  if (!el) return hideTip();
  tipFor = el;
  borrowTitle(el); // a render takes off what borrowing put on an element it kept
  placeTip();
}

// placeTip shows the tip again where its element is now, or hides it if that has scrolled out of view.
function placeTip() {
  if (!tipShown || !tipFor) return;
  if (!inView(tipFor)) return hideTip();
  showTip(tipFor, false);
}

// inView says el is on screen: in the window, and in each scrolling element it sits in.
function inView(el) {
  if (!el.isConnected) return false;
  const r = el.getBoundingClientRect();
  if (r.bottom <= 0 || r.top >= window.innerHeight) return false;
  for (let a = el.parentElement; a; a = a.parentElement) {
    if (!/auto|scroll/.test(getComputedStyle(a).overflowY)) continue;
    const box = a.getBoundingClientRect();
    if (r.bottom <= box.top || r.top >= box.bottom) return false;
  }
  return true;
}

// showTipNow shows el's tip at once, for a click, which hides the tip hover brought. A mouse's click
// then keeps it as a hover tip, for what is under the mouse; a tap or a key keeps it until the next.
function showTipNow(el, by) {
  hideTip();
  pickLine(el);
  tipFor = el;
  tipBy = by;
  borrowTitle(el);
  showTip(el, false);
}

function queueTip(el, atPointer, by) {
  if (el === tipFor) return;
  hideTip();
  pickLine(el);
  tipFor = el;
  tipBy = by;
  borrowTitle(el);
  if (Date.now() - tipHiddenAt < 400 || tipShown) return showTip(el, atPointer);
  tipTimer = setTimeout(() => showTip(el, atPointer), 150);
}

// Whether an element with a title gets the page's tip (see above).
const TIP_KINDS = ".pres, .status-dot, .dev-dot, .mini-dev, .dot, .chat-msg-time, [data-ago], .stag-more";
function wantsTip(el) {
  if (el.dataset.tip || el.dataset.tipLines || el.matches(TIP_KINDS)) return true;
  // A button with no words on it: only an icon, or only a face (the folded sidebar's sessions).
  if (!el.matches("button, a")) return false;
  const faces = [...el.querySelectorAll(".avatar")].map((a) => a.textContent).join("");
  return el.textContent.trim() === faces.trim();
}

const TIPPED = "[data-tip], [data-tip-lines], [title], [data-tip-title]";
function tipTarget(node) {
  for (let el = node?.closest?.(TIPPED); el; el = el.parentElement?.closest(TIPPED)) {
    if (wantsTip(el)) return el;
  }
  return null;
}

document.addEventListener("pointerover", (e) => {
  if (e.pointerType === "touch") return;
  // A render that changes what is under a still mouse brings a pointerover the mouse didn't make. It
  // mustn't hide a tip that focus or a tap opened; refreshTip has already moved that one.
  const still = e.clientX === pointerX && e.clientY === pointerY;
  if (still && tipShown && tipBy !== "hover" && Date.now() - renderedAt < 250) return;
  pointerX = e.clientX;
  pointerY = e.clientY;
  const el = tipTarget(e.target);
  el ? queueTip(el, true, "hover") : hideTip();
});
document.addEventListener("pointermove", (e) => ((pointerX = e.clientX), (pointerY = e.clientY)), { passive: true });
document.documentElement.addEventListener("pointerleave", hideTip);
document.addEventListener("focusin", (e) => {
  const el = tipTarget(e.target);
  if (el && e.target.matches(":focus-visible")) queueTip(el, false, "focus");
});
document.addEventListener("focusout", hideTip);
document.addEventListener("pointerdown", hideTip, true);
// Escape also cancels a tip that is still waiting to show.
document.addEventListener("keydown", (e) => e.key === "Escape" && tipFor && hideTip());
// A render puts the thread's scroll back, which fires a scroll; a tip outlives that unless its element
// went out of view. Any other scroll hides it.
document.addEventListener("scroll", () => (Date.now() - renderedAt > 250 ? hideTip() : placeTip()), { capture: true, passive: true });
