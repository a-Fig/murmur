// The theme and the two other preferences the daemon keeps for the person (only mine, folded tags):
// loading, saving and migrating them from the browser's own storage.

import { h } from "./dom.js";
import { NAME, ui } from "./state.js";
import { AuthError, api } from "./api.js";
import { deny, tokenEra } from "./sync.js";
import { render } from "./render.js";

export { THEMES, theme, loadPreferences, setTheme, onlyMine, setOnlyMine, tagsFolded, setTagsFolded };

// The theme is chosen in Settings. style.css reads it from <html data-theme>.
const THEMES = [
  ["dark", "Dark"],
  ["dim", "Dim"],
  ["light", "Light"],
  ["murmur", "Murmur"],
  ["sky", "Sky"],
  ["night", "Night"],
  ["day", "Day"],
];
const initialTheme = document.documentElement.dataset.theme;
const legacyKeys = { theme: ":theme", onlyMine: ":only-mine", tagsFolded: ":tags-folded" };
function legacyPreferences() {
  const prefs = {};
  try {
    const t = localStorage.getItem(NAME + legacyKeys.theme);
    if (THEMES.some(([id]) => id === t)) prefs.theme = t;
    for (const field of ["onlyMine", "tagsFolded"]) {
      const value = localStorage.getItem(NAME + legacyKeys[field]);
      if (value === "1" || value === "0") prefs[field] = value === "1";
    }
  } catch {}
  return prefs;
}
let theme = (document.documentElement.hasAttribute("data-theme-saved") ? initialTheme : legacyPreferences().theme) || initialTheme;
document.documentElement.dataset.theme = theme;

async function loadPreferences(era) {
  const legacy = legacyPreferences();
  const fields = Object.keys(legacy);
  const migration = fields.length ? { method: "POST", body: JSON.stringify(legacy) } : {};
  try {
    const saved = await api("/api/preferences", { ...migration, signal: AbortSignal.timeout(2000) });
    if (era !== tokenEra) return;
    theme = saved.theme || initialTheme;
    document.documentElement.dataset.theme = theme;
    onlyMine = saved.onlyMine ?? false;
    tagsFolded = saved.tagsFolded ?? false;
    try {
      for (const field of fields) localStorage.removeItem(NAME + legacyKeys[field]);
    } catch {}
  } catch (e) {
    if (era !== tokenEra) return;
    if (e instanceof AuthError) return deny(e.token);
    // The import failed, as when another daemon holds the theme file's lock. What this clone has
    // saved is read all the same, so an old filter in this browser doesn't hide it (#999); the old
    // keys stay, and the next load imports them. A daemon that didn't answer the import (no status)
    // isn't asked again: the page would wait another two seconds for the same silence.
    const saved = fields.length && e.status ? await api("/api/preferences", { signal: AbortSignal.timeout(2000) }).catch(() => ({})) : {};
    if (era !== tokenEra) return;
    if (saved.theme) document.documentElement.dataset.theme = theme = saved.theme;
    onlyMine = saved.onlyMine ?? legacy.onlyMine ?? false;
    tagsFolded = saved.tagsFolded ?? legacy.tagsFolded ?? false;
  }
}

let preferenceWrites = Promise.resolve();
function savePreference(field, value) {
  const era = tokenEra;
  preferenceWrites = preferenceWrites.then(async () => {
    if (era !== tokenEra) return;
    try {
      await api("/api/preferences", { method: "PATCH", body: JSON.stringify({ [field]: value }), keepalive: true });
      if (era === tokenEra && ui.preferenceError) {
        ui.preferenceError = "";
        render();
      }
    } catch (e) {
      if (era !== tokenEra) return;
      if (e instanceof AuthError) return deny(e.token);
      ui.preferenceError = "Couldn't save this preference.";
      render();
    }
  });
}

// An easter egg (#499), after the old Discord joke about light mode: going from a dark theme to
// Light flashes the screen white. It happens the first time in this browser and one time in twenty
// after that, so it stays a surprise. Someone who asked for reduced motion never gets it.
const FLASH_KEY = NAME + ":flashbanged";
const FLASH_ODDS = 0.05;
function flashbang() {
  if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;
  let seen = false;
  try {
    seen = localStorage.getItem(FLASH_KEY) !== null;
    localStorage.setItem(FLASH_KEY, "1");
  } catch {
    seen = true; // nowhere to remember the first time, so every time takes its chances
  }
  if (seen && Math.random() >= FLASH_ODDS) return;
  document.querySelector(".flashbang")?.remove();
  const flash = h("div", { class: "flashbang", "aria-hidden": "true" });
  flash.addEventListener("animationend", () => flash.remove());
  document.body.append(flash);
}

function setTheme(t) {
  if (t === "light" && (theme === "dark" || theme === "dim" || theme === "night")) flashbang();
  theme = t;
  document.documentElement.dataset.theme = t;
  savePreference("theme", t);
  render();
}

// Whether the conversation list shows only the conversations you are in, remembered like the
// sidebar.
let onlyMine = false;
function setOnlyMine(v) {
  onlyMine = v;
  savePreference("onlyMine", v);
  render();
}

// Whether the sidebar's Tags are folded (#569), remembered like the sidebar.
let tagsFolded = false;
function setTagsFolded(v) {
  tagsFolded = v;
  savePreference("tagsFolded", v);
  render();
}
