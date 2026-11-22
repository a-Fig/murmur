// The page. Plain DOM, no framework and no build step: the daemon embeds these files as they
// are. Everything from the bus is untrusted text, so it only ever reaches the page as text nodes.
//
// The page is native ES modules, one file for each part (#1015); each opens with what it holds. This one
// holds what belongs to the whole page, and starts it.

import { tick, root } from "./dom.js";
import { NARROW, ui } from "./state.js";
import { appSaysFront } from "./api.js";
import { poll, start } from "./sync.js";
import { render, setView } from "./render.js";
import { setDrawer, closeRepoMenu } from "./sidebar.js";
import { closeTagMenu, closeProfile } from "./tags.js";
import { markOpenRead } from "./messages.js";
import { closeViewer, attach } from "./files.js";

// A file dropped anywhere on the page goes with the open conversation's next message. Left to the
// browser, a dropped file opens in place of the page.
let dropTimer = 0;
function dragsFiles(e) {
  return [...(e.dataTransfer?.types || [])].includes("Files");
}
function dropsHere() {
  return ui.setup || ui.picker || ui.tagMenu || ui.repoMenu || ui.profile || ui.viewer ? null : root.querySelector(".composer");
}
document.addEventListener("dragover", (e) => {
  if (!dragsFiles(e)) return;
  e.preventDefault();
  const box = dropsHere();
  e.dataTransfer.dropEffect = box ? "copy" : "none";
  if (!box) return;
  // No event says a drag has left the window for good: the mark goes when dragover stops coming.
  box.classList.add("composer-drop");
  clearTimeout(dropTimer);
  dropTimer = setTimeout(() => root.querySelector(".composer-drop")?.classList.remove("composer-drop"), 200);
});
document.addEventListener("drop", (e) => {
  if (!dragsFiles(e)) return;
  e.preventDefault();
  const key = dropsHere()?.querySelector(".composer-input")?.dataset.composer;
  // A folder comes as a file that can't be read.
  const files = [...e.dataTransfer.items].filter((it) => it.kind === "file" && !it.webkitGetAsEntry?.()?.isDirectory).map((it) => it.getAsFile());
  if (key && files.length) attach(key, files.filter(Boolean));
});

// Escape closes the tag menu wherever the focus is, even on the page's body.
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && ui.viewer && !e.defaultPrevented) {
    e.preventDefault(); // a click on the picture leaves the focus on the page's body
    closeViewer();
  } else if (e.key === "Escape" && ui.tagMenu && !e.defaultPrevented) {
    e.preventDefault(); // this Escape was the menu's; the phone's drawer under it stays open
    closeTagMenu();
  } else if (e.key === "Escape" && ui.repoMenu && !e.defaultPrevented) {
    e.preventDefault(); // the drawer under it stays open, as under the tag menu
    closeRepoMenu();
  } else if (e.key === "Escape" && ui.profile && !e.defaultPrevented) {
    e.preventDefault(); // as for the menus
    closeProfile();
  }
});
window.addEventListener("hashchange", () => {
  if (location.hash.includes("token=") || location.hash.includes("key=")) start();
  else setView(location.hash.slice(1));
});
document.addEventListener("visibilitychange", () => {
  if (!document.hidden && ui.token && ui.conn !== "denied") poll();
});
window.addEventListener("focus", () => {
  if (ui.token && ui.conn !== "denied") markOpenRead();
});
window.murmurFront = appSaysFront; // what the desktop app's window calls
window.addEventListener("blur", markOpenRead); // nobody is looking now, so what comes next is new

NARROW.addEventListener("change", () => {
  if (!NARROW.matches) ui.drawer = false;
  render();
});
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && !e.defaultPrevented && ui.drawer && !ui.setup && !ui.picker && !ui.tagMenu && !ui.profile) setDrawer(false);
});

setInterval(tick, 10000);
start();
