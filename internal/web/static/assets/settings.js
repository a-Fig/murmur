// The Settings view: the account, the theme, and each session's wake-up and busy settings.

import { icon } from "./icons.js";
import { h, when, agoEl, plural, root } from "./dom.js";
import { ui, me, myName, repoLabel } from "./state.js";
import { THEMES, theme, setTheme } from "./preferences.js";
import { AuthError, api } from "./api.js";
import { saveSettings, deny, poll } from "./sync.js";
import { announce, render } from "./render.js";
import { versionSection } from "./sidebar.js";
import { deviceLabel, host } from "./sessions.js";
import { REPO_FILES_OFF } from "./files.js";
import { dmKey, deviceColor, composers, deviceMode, MODE_HELP, setMode } from "./messages.js";

export { minuteEdits, cancelMinutes, settingsRow, moveToName, settingsPage };

// Settings.

const WAKE_CHOICES = [
  ["always", "Always"],
  ["within", "If idle under"],
  ["never", "Never"],
];

// Each wake choice's hover hint, in Settings and under Per session.
const WAKE_TIPS = {
  default: "Use the device-wide Wake an idle session choice for this session",
  always: "Start a new turn for it no matter how long it has been idle",
  within: "Start a new turn only if it has been idle under the minutes beside it",
  never: "Never start a turn for it; messages wait in its inbox until it next runs",
};

// Minutes being typed are saved once the typing pauses. A choice made meanwhile, such as Never,
// cancels them: they belong to the Within it replaced.
const minuteEdits = new Map(); // a control's key → its timer

function cancelMinutes(key) {
  clearTimeout(minuteEdits.get(key));
  minuteEdits.delete(key);
}

function wakeControl(key, wake, onChange, withDefault) {
  const def = ui.settings.defaultMinutes || 5;
  const choices = withDefault ? [["default", "Default"], ...WAKE_CHOICES] : WAKE_CHOICES;
  const current = wake ? wake.when : "default";
  const seg = h(
    "div",
    { class: "segmented", role: "group" },
    choices.map(([v, label]) =>
      h(
        "button",
        {
          "aria-pressed": current === v ? "true" : "false",
          "data-tip": WAKE_TIPS[v],
          "data-key": key + "-" + v,
          onclick: () => {
            if (v === current) return;
            cancelMinutes(key);
            delete ui.rowErrors[key];
            onChange(v === "default" ? null : { when: v, minutes: v === "within" ? wake?.minutes || 0 : 0 });
          },
        },
        label,
      ),
    ),
  );
  if (current !== "within") return [seg];
  const input = h("input", {
    type: "number",
    min: 1,
    max: 1440,
    step: 1,
    inputmode: "numeric",
    placeholder: String(def),
    "aria-label": "Minutes idle",
    title: "Minutes it may be idle and still be woken, from 1 to 1440; blank means " + def + " minutes",
    "data-key": key + "-minutes",
    value: wake.minutes ? String(wake.minutes) : null,
    oninput: (e) => {
      cancelMinutes(key);
      const raw = e.target.value.trim();
      const timer = setTimeout(() => {
        minuteEdits.delete(key);
        const n = raw === "" ? 0 : Number(raw);
        if (!Number.isInteger(n) || n < 0 || n > 1440) {
          ui.rowErrors[key] = "Minutes must be a whole number from 1 to 1440.";
          render();
          return;
        }
        delete ui.rowErrors[key];
        onChange({ when: "within", minutes: n });
      }, 600);
      minuteEdits.set(key, timer);
    },
  });
  return [seg, h("span", { class: "field" }, input), h("span", { class: "field-suffix" }, "min")];
}

const BUSY_CHOICES = [
  ["subject", "Subject only"],
  ["whole", "Whole message"],
];

// Each busy choice's hover hint, in Settings and under Per session.
const BUSY_TIPS = {
  default: "Use the device-wide While a session is working choice for this session",
  subject: "While it is mid-turn, show it only the arriving message's subject",
  whole: "While it is mid-turn, show it the arriving message's whole text",
};

function busyControl(key, busy, onChange, withDefault) {
  const choices = withDefault ? [["default", "Default"], ...BUSY_CHOICES] : BUSY_CHOICES;
  const current = busy || "default";
  return h(
    "div",
    { class: "segmented", role: "group" },
    choices.map(([v, label]) =>
      h(
        "button",
        {
          "aria-pressed": current === v ? "true" : "false",
          "data-tip": BUSY_TIPS[v],
          "data-key": key + "-" + v,
          onclick: () => {
            if (v !== current) onChange(v === "default" ? null : v);
          },
        },
        label,
      ),
    ),
  );
}

function settingsRow(key, title, description, control, extraClass = "") {
  return h(
    "div",
    { class: "row" },
    h(
      "div",
      { class: "row-grid " + extraClass },
      h("div", null, h("div", { class: "row-title", id: "row-" + key }, title), description && h("p", { class: "row-description" }, description)),
      // Named by the row's title, so each of a column of Always, Within and Never says whose it is.
      h("div", { class: "row-control", role: control ? "group" : null, "aria-labelledby": control ? "row-" + key : null }, control),
    ),
    ui.rowErrors[key] && h("p", { class: "row-error", "data-announce": "" }, ui.rowErrors[key]),
  );
}

function accountSection() {
  const a = me();
  const value = ui.accountDraft ?? (a?.name || ui.state?.account?.suggested || "");
  return h(
    "section",
    { class: "section" },
    h("div", { class: "section-head" }, h("h2", { class: "section-title" }, "You"), h("p", { class: "section-note" }, "How teammates see you and this device.")),
    h(
      "div",
      { class: "group" },
      settingsRow("account", "Your name", a?.taken ? "The person on " + a.taken + " chose this name first. Choose another; until you do, you can't send." : "Teammates and their agents see it, and send to it.", [
        h(
          "span",
          { class: "field account-field" },
          h("input", {
            type: "text",
            value,
            maxlength: 32,
            autocomplete: "nickname",
            spellcheck: "false",
            "aria-label": "Your name",
            title: "The name teammates and their agents see for you and address messages to",
            "aria-invalid": ui.rowErrors.account ? "true" : null,
            "data-key": "account-name",
            oninput: (e) => {
              ui.accountDraft = e.target.value;
            },
            onkeydown: (e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                saveAccount(e.target.value.trim());
              }
            },
          }),
        ),
        h("button", { class: "btn btn-outline btn-xs", title: "Save this name so teammates and their agents can see and address you by it", "data-key": "account-save", onclick: (e) => saveAccount(e.currentTarget.parentElement.querySelector("input").value.trim()) }, a ? "Save" : "Choose"),
      ]),
      nickRow(),
      colorRow(),
    ),
  );
}

// The palette's names, in the order of the dev-c classes in style.css.
const COLORS = ["Rose", "Clay", "Ochre", "Olive", "Sage", "Sea green", "Teal", "Steel blue", "Violet", "Plum", "Berry", "Cocoa"];

// This device's color (#573): Auto, or one of the palette's that no other device has picked. A pick
// that lost to another device's is given up, and the row says whose it is now until Auto is pressed
// or another color picked. Buttons, not radios: each press is written to the remote, so the arrow
// keys mustn't pick.
function colorRow() {
  const acct = ui.state?.account;
  const pick = acct?.color || 0;
  const taken = new Map(); // a place in the palette, to who picked it
  for (const d of ui.state?.devices || []) if (!d.self && d.picked) taken.set(d.color, d.nick || d.owner || d.id);
  const swatch = (c, props, ...kids) => {
    const who = c ? taken.get(c - 1) : null;
    return h(
      "button",
      {
        ...props,
        "aria-pressed": c === pick ? "true" : "false",
        "aria-disabled": who ? "true" : null,
        "data-key": "color-" + c,
        onclick: () => !who && saveColor(c),
      },
      ...kids,
    );
  };
  return settingsRow(
    "color",
    "Device colour",
    acct?.colorLost && !pick ? acct.colorLost + " has yours now, so you're on Auto." : "Fills your sessions' faces on everyone's page.",
    h(
      "div",
      { class: "swatches" },
      swatch(0, { class: "swatch-auto", "data-tip": "Let Murmur assign this device a colour no other device has picked" }, "Auto"),
      COLORS.map((name, i) => {
        const who = taken.get(i);
        return swatch(i + 1, { class: "swatch dev-c" + i, "aria-label": who ? name + ", " + who + "'s" : name, title: who ? who + "'s" : name });
      }),
    ),
    "row-stack",
  );
}

async function saveColor(color) {
  delete ui.rowErrors.color;
  if (color === (ui.state?.account?.color || 0) && !ui.state?.account?.colorLost) return render();
  try {
    const account = await api("/api/color", { method: "PUT", body: JSON.stringify({ color }) });
    if (ui.state) ui.state.account = { ...ui.state.account, ...account, color: account.color || 0, colorLost: account.colorLost || "" };
    poll();
  } catch (e) {
    if (e instanceof AuthError) return deny(e.token);
    ui.rowErrors.color = e.message;
  }
  render();
}

// This device's nickname (#533), as `mur nickname` sets it: a label teammates see beside the
// device's ID, and no address. This page calls its own device "This device", so the row says the
// nickname is what the others see.
function nickRow() {
  const acct = ui.state?.account;
  const nick = acct?.nick || "";
  const id = ui.state?.device || "";
  const value = ui.nickDraft ?? nick;
  const without = acct?.name ? "Without one, they see your name, " + acct.name + "." : "";
  return settingsRow("nick", "Device nickname", ["Shown to teammates beside this device's ID" + (id ? ", " + id : "") + ". Only a label: nothing is sent to it.", without].filter(Boolean).join(" "), [
    h(
      "span",
      { class: "field account-field" },
      h("input", {
        type: "text",
        value,
        maxlength: 32,
        placeholder: "None",
        autocomplete: "off",
        autocapitalize: "none",
        spellcheck: "false",
        "aria-label": "Device nickname",
        title: "Optional label teammates see beside this device's ID; nothing is addressed to it",
        "aria-invalid": ui.rowErrors.nick ? "true" : null,
        "data-key": "nick-name",
        oninput: (e) => {
          ui.nickDraft = e.target.value;
        },
        onkeydown: (e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            saveNick(e.target.value.trim());
          }
        },
      }),
    ),
    h("button", { class: "btn btn-outline btn-xs", title: "Save this nickname beside this device's ID for teammates", "data-key": "nick-save", onclick: (e) => saveNick(e.currentTarget.parentElement.querySelector("input").value.trim()) }, "Save"),
    nick && h("button", { class: "btn btn-ghost btn-xs", title: "Clear the nickname; teammates see your name beside this device instead", "data-key": "nick-clear", onclick: () => saveNick("") }, "Clear"),
  ]);
}

async function saveNick(nick) {
  delete ui.rowErrors.nick;
  if (nick === (ui.state?.account?.nick || "")) {
    ui.nickDraft = null;
    return render(); // nothing to change
  }
  try {
    const account = await api("/api/nick", { method: "PUT", body: JSON.stringify({ nick }) });
    if (ui.state) ui.state.account = { ...ui.state.account, ...account, nick: account.nick || "" };
    ui.nickDraft = null;
    announce(nick ? "This device is now called " + nick + "." : "Nickname cleared.");
    poll();
  } catch (e) {
    if (e instanceof AuthError) return deny(e.token);
    ui.rowErrors.nick = e.message; // it starts with a nickname, ID or name, all lowercase
  }
  render();
}

// A direct conversation's key has the person's name in it, so a new name moves what is unsent to the
// conversation's new key. Left under the old one, it would be out of reach, and would hold up a reload.
function moveToName(was, name) {
  if (!was || was === name) return;
  const rekey = (key) => {
    if (!key.startsWith("dm:")) return key;
    const pair = JSON.parse(key.slice(3));
    return pair.includes(was) ? dmKey(name, pair.find((n) => n !== was) || name) : key;
  };
  for (const [key, el] of [...composers]) {
    const moved = rekey(key);
    if (moved === key) continue;
    composers.delete(key);
    composers.set(moved, el);
    el.dataset.key = "composer-" + moved;
    el.dataset.composer = moved;
  }
  for (const item of ui.outbox) {
    const moved = rekey(item.key);
    if (moved === item.key) continue;
    const pair = JSON.parse(moved.slice(3));
    item.key = moved;
    item.conversation = { ...item.conversation, key: moved, pair, title: pair.join(" & ") };
  }
  ui.restored = null;
}

async function saveAccount(name) {
  delete ui.rowErrors.account;
  try {
    const account = await api("/api/account", { method: "PUT", body: JSON.stringify({ name }) });
    const was = myName();
    if (ui.state) ui.state.account = { ...ui.state.account, ...account };
    moveToName(was, account.name);
    ui.accountDraft = null;
    poll();
  } catch (e) {
    if (e instanceof AuthError) return deny(e.token);
    ui.rowErrors.account = e.message;
  }
  render();
}

// Each session's own settings, both of them on one row, folded under the defaults so a device with
// many sessions doesn't fill the page. It is folded until opened (Tyler, 2026-11-16); the fold
// says how many sessions there are and how many set their own.
function perSessionFold(rows, set) {
  ui.settingsOpen = ui.settingsOpen || new Set();
  if (!rows.length) return [];
  const open = ui.settingsOpen.has("sessions");
  const head = h(
    "button",
    {
      class: "row row-fold",
      "aria-expanded": open ? "true" : "false",
      title: open ? "Hide the wake and while-working choices set per session" : "Show the wake and while-working choices set per session on this device",
      "data-key": "fold-sessions",
      onclick: () => {
        open ? ui.settingsOpen.delete("sessions") : ui.settingsOpen.add("sessions");
        render();
      },
    },
    icon("chevronRight", "row-fold-chev"),
    h("span", { class: "row-title" }, "Per session"),
    h("span", { class: "row-fold-meta" }, plural(rows.length, "session") + (set ? " · " + set + " set" : "")),
  );
  if (open) for (const r of rows) r.classList.add("row-sub");
  return [head, ...(open ? rows : [])];
}

// One session's row under Per session: its wake-up and what it is shown while working, each
// Default until it is set.
function sessionSettingsRow(s, wake, busy, saveWake, saveBusy) {
  const line = (label, ...control) => h("div", { class: "ps-line" }, h("span", { class: "ps-label" }, label), h("div", { class: "row-control ps-control", role: "group", "aria-label": label + ", " + s.name }, control));
  return h(
    "div",
    { class: "row" },
    h(
      "div",
      { class: "ps-row" },
      h("div", { class: "row-title ps-name" }, h("span", { class: "truncate", "data-profile": s.name }, s.name), s.agent && h("span", { class: "badge badge-secondary" }, s.agent)),
      line("Wake", wakeControl("wake-" + s.name, wake, (w) => saveWake(s.name, w), true)),
      line("While working", busyControl("busy-" + s.name, busy, (b) => saveBusy(s.name, b), true)),
    ),
    ui.rowErrors["wake-" + s.name] && h("p", { class: "row-error", "data-announce": "" }, ui.rowErrors["wake-" + s.name]),
  );
}

// Each theme's hover hint describes its palette.
const THEME_TIPS = {
  dark: "Black page like T3 Code's dark theme",
  dim: "Soft dark-grey page, lighter than Dark",
  light: "White page with dark text",
  sky: "Pale blue panes with navy text, Rubik and butter message bubbles",
  night: "Near-black navy panes with white text, Rubik and blue message bubbles",
  day: "Butter page with a cream sidebar, navy text, Rubik and blue message bubbles",
};

function themeSection() {
  return h(
    "section",
    { class: "section" },
    h("div", { class: "section-head" }, h("h2", { class: "section-title" }, "Appearance"), h("p", { class: "section-note" }, "Shared by the app and browser on this device.")),
    h(
      "div",
      { class: "group" },
      settingsRow(
        "theme",
        "Theme",
        null,
        h(
          "div",
          { class: "segmented", role: "radiogroup", "aria-label": "Theme" },
          THEMES.map(([id, label]) =>
            // A radio group: one Tab stop, the arrow keys move the choice (WCAG 4.1.2).
            h(
              "button",
              {
                role: "radio",
                "aria-checked": theme === id ? "true" : "false",
                tabindex: theme === id ? 0 : -1,
                "data-tip": THEME_TIPS[id],
                "data-key": "theme-" + id,
                onclick: () => setTheme(id),
                onkeydown: (e) => {
                  const step = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }[e.key];
                  if (!step) return;
                  e.preventDefault();
                  const i = THEMES.findIndex(([t]) => t === theme);
                  const next = THEMES[(i + step + THEMES.length) % THEMES.length][0];
                  setTheme(next);
                  root.querySelector(`[data-key="theme-${next}"]`)?.focus();
                },
              },
              label,
            ),
          ),
        ),
      ),
    ),
  );
}

// Settings (#775, Tyler, 2026-11-16) is one screen for the repo that is open, grouped by what each
// setting is about: you, messages to your sessions, files, mutes. Every one of those is this
// repo's, since a device is one clone. The theme and About apply to every repo.
function settingsSection(key, title, note, ...rows) {
  return h(
    "section",
    { class: "section", "data-key": "set-" + key },
    h("div", { class: "section-head" }, h("h2", { class: "section-title" }, title), note && h("p", { class: "section-note" }, note)),
    h("div", { class: "group" }, ...rows),
  );
}

// Every setting is a segmented control, on/off ones too: one kind of control across Settings
// (Tyler, 2026-11-17, #862). hints says what Off and On each do, on hover.
function onOffControl(key, on, save, hints, disabled = false) {
  return h(
    "div",
    { class: "segmented", role: "group" },
    [[false, "Off"], [true, "On"]].map(([v, label], i) =>
      h("button", { "aria-pressed": on === v ? "true" : "false", "data-tip": hints?.[i], "data-key": key + (v ? "-on" : "-off"), disabled, onclick: () => on !== v && save(v) }, label),
    ),
  );
}

const REPO_MODES = {
  team: ["Team", "Every teammate can read messages sent in Team mode."],
  private: ["Private", "Only the devices involved can read each new message."],
  off: ["Off", "New records are readable by anyone with access to the repo."],
};

async function saveRepoPolicy(change) {
  if (ui.repoPolicyBusy) return;
  const token = ui.token;
  ui.repoPolicyBusy = true;
  ui.rowErrors["repo-policy"] = "";
  render();
  try {
    await api("/api/repo-policy", { method: "PATCH", body: JSON.stringify(change) });
    if (token === ui.token) await poll();
  } catch (e) {
    if (e instanceof AuthError) return deny(e.token);
    if (token === ui.token) ui.rowErrors["repo-policy"] = e.message;
  } finally {
    ui.repoPolicyBusy = false;
    render();
  }
}

function changeRepoMode(mode) {
  const warning = mode === "off" ? " If this repo is public, anyone on the internet can read new records." : "";
  if (window.confirm("Switch encryption to " + REPO_MODES[mode][0] + "? " + REPO_MODES[mode][1] + warning + " Existing history keeps the protection it had when it was written.")) {
    saveRepoPolicy({ encryptionMode: mode });
  }
}

function repoPolicySection() {
  const state = ui.state;
  if (!state?.meshOwner) return null;
  const mode = state.encryptionMode || "team";
  const [label, help] = REPO_MODES[mode] || [mode, ""];
  const allowFiles = !state.repoFilesOff;
  const owner = state.devices.find((d) => d.id === state.meshOwner);
  const ownerName = owner?.person?.name || (owner ? deviceLabel(owner) : state.meshOwner);
  const modes = h(
    "div",
    { class: "segmented", role: "group", "aria-label": "Encryption mode" },
    Object.entries(REPO_MODES).map(([value, [text, hint]]) =>
      h("button", { "aria-pressed": mode === value ? "true" : "false", "data-key": "repo-mode-" + value, "data-tip": hint, disabled: ui.repoPolicyBusy, onclick: () => mode !== value && changeRepoMode(value) }, text),
    ),
  );
  return settingsSection(
    "repo-policy",
    "Repo settings",
    state.ownsMesh ? "Changes apply to everyone in this repo." : "Managed by the mesh owner, " + ownerName + ".",
    settingsRow("repo-mode", "Encryption mode", help, state.ownsMesh ? modes : h("span", null, label)),
    settingsRow(
      "repo-files",
      "Allow files",
      allowFiles ? "People and agents can attach files when their device settings allow it." : REPO_FILES_OFF + " Older devices may still publish attachments.",
      state.ownsMesh ? onOffControl("repo-files", allowFiles, (value) => saveRepoPolicy({ allowFiles: value }), null, ui.repoPolicyBusy) : h("span", null, allowFiles ? "On" : "Off"),
    ),
    ui.rowErrors["repo-policy"] && h("p", { class: "row-error", role: "alert" }, ui.rowErrors["repo-policy"]),
  );
}

function settingsPage() {
  const sections = [repoPolicySection(), ui.state ? accountSection() : null];
  if (ui.settings) {
    const set = ui.settings;
    // A session is shown by its address until its name lands, and the daemon keeps these settings
    // by name: one saved under the address would never apply, so the session isn't offered (#43).
    const local = ((ui.state?.devices || []).find((d) => d.self)?.sessions || []).filter((s) => !s.name.includes("/")).sort((a, b) => a.name.localeCompare(b.name));
    const wakeOver = set.sessionWake || {};
    const busyOver = set.sessionBusy || {};
    const saveWake = (name, w) => {
      // From the settings as they are now: a pause in typing minutes can end after another choice
      // was made.
      const next = { ...(ui.settings.sessionWake || {}) };
      if (w) next[name] = w;
      else delete next[name];
      saveSettings({ ...ui.settings, sessionWake: next });
    };
    const saveBusy = (name, b) => {
      const next = { ...(ui.settings.sessionBusy || {}) };
      if (b) next[name] = b;
      else delete next[name];
      saveSettings({ ...ui.settings, sessionBusy: next });
    };
    const own = local.filter((s) => wakeOver[s.name] || busyOver[s.name]).length;
    sections.push(
      settingsSection(
        "messages",
        "Messages to your sessions",
        null,
        settingsRow(
          "mode",
          "Availability",
          MODE_HELP[deviceMode()],
          h(
            "div",
            { class: "segmented", role: "group" },
            [["online", "Online"], ["dnd", "Do not disturb"], ["invisible", "Invisible"]].map(([v, label]) =>
              h("button", { "aria-pressed": deviceMode() === v ? "true" : "false", "data-tip": MODE_HELP[v], "data-key": "mode-" + v, onclick: () => deviceMode() !== v && setMode(v) }, label),
            ),
          ),
        ),
        settingsRow(
          "wake",
          "Wake an idle session",
          "When another session's message may start an idle session's turn. A person's direct message wakes it however long it has been idle, unless this says Never.",
          wakeControl("wake", set.wake, (w) => saveSettings({ ...ui.settings, wake: w || { when: "within" } }), false),
        ),
        settingsRow("busy", "While a session is working", "What it is shown when a message arrives mid-turn.", busyControl("busy", set.busy || "subject", (b) => saveSettings({ ...ui.settings, busy: b }), false)),
        perSessionFold(
          local.map((s) => sessionSettingsRow(s, wakeOver[s.name] || null, busyOver[s.name] || null, saveWake, saveBusy)),
          own,
        ),
      ),
    );
    // Files are off both ways until the person turns them on, per repo (#765, Tyler, 2026-11-16).
    // Each way, a main setting for the person and their agents, and once it is on, one that keeps it
    // from the agents (Tyler, 2026-11-16).
    const fileRows = (key, title, on, onText, offText, prop, agentsOff, agentsOnText, agentsOffText, agentsProp, hints, agentsHints) => {
      const rows = [settingsRow(key, title, on ? onText : offText, onOffControl(key, on, (v) => saveSettings({ ...ui.settings, [prop]: v }, false, prop), hints))];
      if (on) {
        const sub = settingsRow(
          "agents-" + key,
          "Agents too",
          agentsOff ? agentsOffText : agentsOnText,
          onOffControl("agents-" + key, !agentsOff, (v) => saveSettings({ ...ui.settings, [agentsProp]: !v }, false, agentsProp), agentsHints),
        );
        sub.classList.add("row-sub");
        rows.push(sub);
      }
      return rows;
    };
    if (ui.state?.repoFilesOff) {
      sections.push(settingsSection("files", "Files", REPO_FILES_OFF));
    } else sections.push(
      settingsSection(
        "files",
        "Files",
        null,
        fileRows(
          "send-files",
          "Send files",
          !!set.sendFiles,
          "You can attach files to what you write here: screenshots, logs, diffs.",
          "No files go out from this device. Attaching one asks you to turn this on.",
          "sendFiles",
          !!set.agentsSendOff,
          "Your agents here can send files too, only from this repo or the temp folder.",
          "Only you send files. An agent's message with files isn't sent, and it is told you can turn this on.",
          "agentsSendOff",
          ["Send no files from this device; attaching one asks you to turn this on first", "Let you, and your agents if Agents too is on, attach files to messages from this device"],
          ["Agents on this device may not send files; a file message from one is not sent and it is told you can turn this on", "Agents on this device may send files, only from this repo or the temp folder"],
        ),
        fileRows(
          "receive-files",
          "Receive files",
          !!set.receiveFiles,
          "Files other devices send are downloaded into this repo's .git folder and shown here.",
          "Messages arrive without their files, which aren't downloaded. Senders are told.",
          "receiveFiles",
          !!set.agentsReceiveOff,
          "Your agents here get the files sent to them too.",
          "Only you get files. Your agents get messages without them, and senders are told.",
          "agentsReceiveOff",
          ["Do not download files sent here; messages arrive without them and senders are told", "Download files sent here into this repo's .git folder and show them on this page"],
          ["Only you receive files here; agents get the message without its files and senders are told", "Agents on this device receive files sent to them as well as their messages"],
        ),
      ),
    );
    if (ui.settingsError) sections.push(h("p", { class: "row-error section-head", "data-announce": "" }, ui.settingsError));
  }

  const mutes = ui.state?.mutes || [];
  sections.push(
    settingsSection(
      "mutes",
      "Mutes",
      "A muted session's messages don't reach the session that muted it.",
      mutes.length === 0
        ? h("div", { class: "row-empty" }, "None")
        : mutes.map((m) => {
            const key = "mute-" + m.by + "-" + m.muted;
            const at = when(m.at);
            return settingsRow(
              key,
              h(
                "span",
                { class: "mute-line" },
                icon("bellOff"),
                h("span", { class: "truncate" }, m.by),
                h("span", { class: "verb" }, "muted"),
                h("span", { class: "truncate" }, m.muted),
              ),
              at && agoEl("span", "", at),
              m.canUnmute &&
                h(
                  "button",
                  {
                    class: "btn btn-outline btn-xs",
                    title: "Unmute: " + m.by + " starts receiving " + m.muted + "'s messages again",
                    "data-key": key,
                    disabled: ui.muting.has(key),
                    onclick: () => unmute(m, key),
                  },
                  "Unmute",
                ),
              "keep-inline",
            );
          }),
    ),
  );

  sections.push(h("div", { class: "settings-everywhere" }, h("p", { class: "settings-everywhere-label" }, "For every repo"), themeSection(), versionSection()));
  return h("div", { class: "page" }, settingsScope(), sections);
}

// Which repo, and which device, the settings above the line are for: in mur app the repo picker
// switches between repos, each with settings of its own.
function settingsScope() {
  if (!ui.state) return null;
  const self = (ui.state.devices || []).find((d) => d.self);
  return h(
    "p",
    { class: "settings-scope" },
    "For ",
    h("strong", null, repoLabel()),
    self ? [" on ", h("span", { class: "dev-swatch " + deviceColor(self), "aria-hidden": "true" }), h("strong", null, self.nick || host(self.id))] : null,
  );
}

async function unmute(m, key) {
  ui.muting.add(key);
  delete ui.rowErrors[key];
  render();
  try {
    await api("/api/unmute", { method: "POST", body: JSON.stringify({ by: m.by, muted: m.muted }) });
    ui.state.mutes = ui.state.mutes.filter((x) => !(x.by === m.by && x.muted === m.muted));
  } catch (e) {
    if (e instanceof AuthError) return deny(e.token);
    ui.rowErrors[key] = e.message;
  }
  ui.muting.delete(key);
  render();
}
