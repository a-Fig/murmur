// Faces: a session's ghost or a person's initial, filled with their device's color, and the names
// that open a profile card.

import { h } from "./dom.js";
import { ui, findSession, myName, isPerson, findPerson } from "./state.js";

export { avatar, nameButton, knownName, withNames, personFace, deviceColor, deviceOfName };

// A face is filled with the color of the device it is on (#560), so a stack of faces says whose
// machines they are. A session's shows a ghost; a person's, their initial (#579, Tyler). The
// classes are in style.css: the page's content policy doesn't allow an inline style.
// A face opens its name's profile card when clicked (#689), unless card is false: where the face
// is part of a control that does something else (the account chip, a group's pile of faces).
function avatar(name, card = true) {
  const color = "avatar dev-fill " + deviceColor(deviceOfName(name));
  const face = isPerson(name) ? personFace(name, color) : ghostFace(name, color);
  if (card && name) face.dataset.profile = name;
  return face;
}

// A name the page shows opens the same card as its face (#778, Tyler): where the name stands on its
// own, as this button; inside a control, as a span with data-profile, which the capture listener
// above takes ahead of the control. Names in controls that pick (the pickers, sessions being picked
// to tag) and in a conversation's row keep the control's action.
function nameButton(name, cls, text = name) {
  return h("button", { type: "button", class: "name-ref " + cls, "data-profile": name, "aria-haspopup": "dialog" }, text);
}

// A name the page knows, written in a message, as a session's two words or in backticks.
function knownName(t) {
  return !!(findSession(t) || findPerson(t) || t === myName());
}

// Plain text from a message, with each name the page knows (quiet-otter) made a nameButton.
function withNames(t, out) {
  // Not part of a path, an address or a longer word: what comes before is checked by hand, since a
  // lookbehind is a parse error in Safari before 16.4, and that would stop the whole page.
  const re = /[a-z0-9]+(?:-[a-z0-9]+)+(?![\w/\\-]|[.:]\w)/g;
  let last = 0;
  for (let m; (m = re.exec(t)); ) {
    if ((m.index > 0 && /[\w./:\\-]/.test(t[m.index - 1])) || !knownName(m[0])) continue;
    if (m.index > last) out.push(t.slice(last, m.index));
    out.push(nameButton(m[0], "rich-name"));
    last = re.lastIndex;
  }
  if (last < t.length) out.push(t.slice(last));
}

function personFace(name, cls) {
  return h("span", { class: cls + " avatar-person", "aria-hidden": "true" }, (name[0] || "?").toUpperCase());
}

// A session's ghost (#579): its name picks one of 16 ghosts and one of 15 accessories or none, so
// it wears the same one everywhere. The files are drawn by scripts/ghosts.py, which these lists
// follow; a ghost has no tile of its own, so it sits on its device's color.
const GHOSTS = ["murmur", "hush", "whisper", "mumble", "chatter", "babble", "rumor", "echo", "chirp", "coo", "hum", "psst", "sigh", "jabber", "natter", "buzz"];
const GHOST_ACCESSORIES = ["", "bow", "glasses", "specs", "shades", "bubble", "headset", "beanie", "party", "crown", "halo", "flower", "sprout", "tophat", "cat", "propeller"];

function ghostOf(name) {
  let n = 2166136261; // FNV-1a
  for (const ch of name) n = Math.imul(n ^ ch.codePointAt(0), 16777619) >>> 0;
  return { ghost: GHOSTS[n % 16], accessory: GHOST_ACCESSORIES[(n >>> 4) % 16] };
}

function ghostFace(name, cls) {
  const { ghost, accessory } = ghostOf(name);
  return h(
    "span",
    { class: cls + " avatar-ghost", "aria-hidden": "true" },
    h("img", { src: "/assets/ghosts/ghost-" + ghost + ".svg", alt: "", draggable: "false" }),
    accessory && h("img", { src: "/assets/ghosts/acc-" + accessory + ".svg", alt: "", draggable: "false" }),
  );
}

// A device's color: one of twelve hues, the same on its faces and by its name in the sidebar. Its
// person may pick it (#573); the daemon settles who has which, so every page shows the same. Offline,
// or not in presence, it is grey.
function deviceColor(d) {
  if (!d || !d.reachable) return "dev-off";
  return "dev-c" + (d.color || 0);
}

// The device a session or a person is on, by name, or null if none in presence has it. Built once
// per state, since every face asks.
let devOfNames = null;
let devOfState = null;
function deviceOfName(name) {
  if (devOfState !== ui.state) {
    devOfState = ui.state;
    devOfNames = new Map();
    for (const d of ui.state?.devices || []) {
      for (const s of d.sessions || []) devOfNames.set(s.name, d);
      if (d.person) devOfNames.set(d.person.name, d);
    }
  }
  return devOfNames?.get(name) || null;
}
