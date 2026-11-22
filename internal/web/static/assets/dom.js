// Building elements and formatting what they show. h makes elements from tags, attributes and text
// only, so nothing from the bus can become markup.

export { h, when, ago, stamp, agoEl, tick, plural, root, held };

// DOM.

function h(tag, props, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (v == null || v === false) continue;
    if (k === "class") el.className = v;
    else if (k.startsWith("on")) {
      el.addEventListener(k.slice(2), v);
      (el.listeners ||= []).push([k.slice(2), v]); // for graft, which moves them to the node kept
    } else el.setAttribute(k, v === true ? "" : String(v));
  }
  for (const kid of kids.flat(Infinity)) {
    if (kid == null || kid === false || kid === "") continue;
    // The focused node, if it outlasts renders (a composer), stays in the page; a mark takes its
    // place in the new tree, for graft.
    el.append(kid === held.el ? held.mark : kid instanceof Node ? kid : String(kid));
  }
  return el;
}

// Time.

function when(s) {
  if (!s) return null;
  const t = Date.parse(s);
  return Number.isNaN(t) || t < Date.UTC(2000, 0, 1) ? null : t;
}

function ago(t) {
  const s = Math.max(0, (Date.now() - t) / 1000);
  if (s < 45) return "now";
  if (s < 3600) return Math.max(1, Math.round(s / 60)) + "m ago";
  if (s < 86400) return Math.floor(s / 3600) + "h ago";
  return Math.floor(s / 86400) + "d ago";
}

function stamp(t) {
  return new Date(t).toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
    second: "2-digit",
  });
}

// An element whose text is a relative time, kept current by the ticker.
function agoEl(tag, cls, t, prefix = "") {
  return h(tag, { class: cls, "data-ago": t, "data-prefix": prefix, title: stamp(t) }, prefix + ago(t));
}

function tick() {
  for (const el of document.querySelectorAll("[data-ago]")) {
    el.textContent = el.dataset.prefix + ago(Number(el.dataset.ago));
  }
}

function plural(n, word) {
  return n + " " + word + (n === 1 ? "" : "s");
}

// Shell.

const root = document.getElementById("root");

const held = { el: null, mark: null }; // the focused element while a render builds around it
