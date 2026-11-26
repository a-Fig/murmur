// The sidebar and its collapsed form, the update notice, and the repo picker.

import { icon } from "./icons.js";
import { h, when, ago, plural, root } from "./dom.js";
import {
  NAME, COMMAND, NARROW, ui, devices, me, myName, people, findPerson, personOnline, unreadCount, repoLabel,
} from "./state.js";
import { AuthError, api } from "./api.js";
import { deny, poll, tokenEra } from "./sync.js";
import { render, go } from "./render.js";
import {
  liveSessions, quietSessions, hideButton, quietFold, deviceDot, presence, settledFold, deviceLabel, host,
  allConversations, sideSessionBody,
} from "./sessions.js";
import { tagsOf, shownSessions, tagsGroup, tagButton } from "./tags.js";
import { avatar, deviceColor, modeNote } from "./messages.js";
import { startChat, askForName } from "./picker.js";
import { settingsRow } from "./settings.js";
import { updatesRow, daemonNote } from "./updates.js";

export {
  setDrawer, pickSession, connBadge, sidebar, humanTag, personBadges, restartStuck, updateInfo, versionSection,
  closeRepoMenu, repoMenu, loadRepoCount,
};

// A row in the sidebar's nav, as T3 Code's: an icon, a label, and a count or badge at the end.
function navRow(key, iconName, label, current, onclick, end) {
  return h(
    "button",
    { class: "menu-button", "aria-current": current ? "page" : null, "data-key": key, onclick },
    icon(iconName),
    h("span", { class: "label" }, label),
    end,
  );
}

// The sidebar is the sessions themselves, under their devices, as T3 Code's are threads under their
// projects: every session, every conversation and the newest messages at the top, you and Settings
// at the foot. It folds to a strip of faces, and remembers that. On a phone it is a drawer.
const MINI_KEY = NAME + ":sidebar-mini";
let mini = false;
try {
  mini = localStorage.getItem(MINI_KEY) === "1";
} catch {}
function setMini(v) {
  mini = v;
  try {
    localStorage.setItem(MINI_KEY, v ? "1" : "0");
  } catch {}
  render();
}

function setDrawer(open) {
  ui.drawer = open;
  render();
  if (open) root.querySelector(".sidebar .menu-button")?.focus({ preventScroll: true });
  else root.querySelector('[data-key="open-sidebar"]')?.focus({ preventScroll: true });
}

// Picking a session shows its conversations.
function pickSession(key) {
  ui.filter = key;
  ui.conversation = null;
  go("messages");
}

function miniSidebar() {
  const pick = (view) => () => {
    ui.filter = null;
    if (view === "messages") ui.conversation = null;
    go(view);
  };
  // Conversations carries the dot for what is unread.
  const n = unreadCount();
  const top = (view, iconName, title, current, unread) =>
    h(
      "button",
      { class: "mini-btn" + (current ? " current" : ""), title, "aria-label": unread ? title + ", " + unread + " unread" : title, "data-key": "mini-" + view, onclick: pick(view) },
      icon(iconName),
      unread > 0 && h("span", { class: "mini-unread" }),
    );
  return h(
    "aside",
    { class: "sidebar mini", "data-app-sidebar": true, "aria-label": NAME },
    h("button", { class: "mini-btn mini-open", title: "Show the sidebar", "aria-label": "Show the sidebar", "data-key": "mini-open", onclick: () => setMini(false) }, icon("panel")),
    connBadge(true),
    h(
      "div",
      { class: "mini-scroll" },
      top("sessions", "users", "Sessions", ui.view === "sessions"),
      top("messages", "message", "Conversations", ui.view === "messages" && !ui.filter, n),
      top("recent", "clock", "Recent", ui.view === "recent"),
      ui.tagFilter &&
        h(
          "button",
          {
            class: "mini-btn current",
            title: "Tagged " + ui.tagFilter + ": show every session",
            "aria-label": "Tagged " + ui.tagFilter + ": show every session",
            "data-key": "mini-tag",
            onclick: () => {
              ui.tagFilter = null;
              render();
            },
          },
          icon("tag"),
        ),
      devices().map((d) => {
        const live = shownSessions(liveSessions(d));
        if (ui.tagFilter && !live.length) return null;
        return [
          h("div", { class: "mini-dev", title: deviceLabel(d) + " · " + plural(live.length, "session") }, deviceDot(d)),
          live.map((s) => {
            const key = s.addr || s.name;
            return h(
              "button",
              {
                class: "mini-face" + (ui.view === "messages" && ui.filter === key ? " current" : ""),
                title: s.name + (s.doing ? ": " + s.doing : ""),
                "aria-label": s.name,
                "data-key": "mini-s-" + s.name,
                onclick: () => pickSession(key),
              },
              presence(s.name, d),
            );
          }),
        ];
      }),
    ),
    miniUpdate(),
    h("button", { class: "mini-btn mini-foot" + (ui.view === "settings" ? " current" : ""), title: "Settings", "aria-label": "Settings", "data-key": "mini-settings", onclick: () => go("settings") }, icon("settings")),
  );
}

// Whether the page can reach its daemon. Nothing shows while it can.
function connBadge(small) {
  if (ui.conn !== "down" || ui.restarting) return null;
  const tip = "Can't reach the daemon. The page keeps trying; " + COMMAND + " ui starts it again.";
  return small
    ? h("span", { class: "mini-conn", role: "img", title: tip, "aria-label": "Disconnected", "data-announce": tip }, h("span", { class: "dot warn" }))
    : h("span", { class: "conn-badge", title: tip, "data-announce": tip }, h("span", { class: "dot warn" }), "Disconnected");
}

function sidebar() {
  if (mini && !NARROW.matches) return miniSidebar();
  const devs = devices();
  const allCount = allConversations().length;
  const live = devs.reduce((n, d) => n + liveSessions(d).length, 0);
  const unread = unreadCount();
  return h(
    "aside",
    { class: "sidebar", "data-app-sidebar": true, "aria-label": NAME, id: "sidebar" },
    h(
      "div",
      { class: "sidebar-header" },
      h("img", { class: "brand-mark", src: "/assets/lockup-white.svg", alt: "" }),
      repoButton(),
      connBadge(false),
      NARROW.matches
        ? h("button", { class: "icon-btn", title: "Close", "aria-label": "Close the sidebar", "data-key": "close-sidebar", onclick: () => setDrawer(false) }, icon("panel"))
        : h("button", { class: "icon-btn", title: "Hide the sidebar", "aria-label": "Hide the sidebar", "data-key": "fold", onclick: () => setMini(true) }, icon("panel")),
    ),
    h(
      "div",
      { class: "sidebar-content" },
      h(
        "nav",
        { class: "sidebar-group g-top", "aria-label": "Views" },
        navRow("top-sessions", "users", "Sessions", ui.view === "sessions", () => ((ui.filter = null), go("sessions")), h("span", { class: "menu-count" }, live)),
        // What is unread stands in for the count while there is any.
        navRow(
          "all",
          "message",
          "Conversations",
          ui.view === "messages" && !ui.filter,
          () => ((ui.filter = null), (ui.conversation = null), go("messages")),
          unread > 0 ? h("span", { class: "unread-badge", "aria-label": unread + " unread" }, unread) : h("span", { class: "menu-count" }, allCount),
        ),
        navRow("top-recent", "clock", "Recent", ui.view === "recent", () => ((ui.filter = null), go("recent"))),
      ),
      ui.notice && h("button", { class: "side-notice", title: "Dismiss", "data-key": "notice", "data-announce": "", onclick: () => ((ui.notice = ""), render()) }, ui.notice),
      modeNote(),
      peopleGroup(),
      tagsGroup(),
      devs.map((d) => deviceGroup(d)),
    ),
    sidebarFoot(),
  );
}

// A device, like a project in T3 Code's sidebar: a header that folds it, then its sessions. A tag
// filter (#270) leaves out the sessions without the tag, and a device left with none.
function deviceGroup(d) {
  ui.closedDevices = ui.closedDevices || new Set();
  const closed = ui.closedDevices.has(d.id);
  const live = shownSessions(liveSessions(d));
  if (ui.tagFilter && !live.length && !shownSessions(quietSessions(d)).length) return null;
  return h(
    "div",
    { class: "sidebar-group dev-group" + (d.reachable ? "" : " stale") },
    h(
      "button",
      {
        class: "device-link",
        "aria-expanded": closed ? "false" : "true",
        "data-key": "dev-" + d.id,
        title: d.reachable ? d.id : d.id + ", last seen " + ago(when(d.lastSeen) || 0),
        onclick: () => {
          closed ? ui.closedDevices.delete(d.id) : ui.closedDevices.add(d.id);
          render();
        },
      },
      icon("chevronRight", "dev-chev"),
      h(
        "span",
        { class: "label" },
        h("span", { class: "dev-swatch " + deviceColor(d), "aria-hidden": "true" }),
        h("span", { class: "dev-name truncate" }, deviceLabel(d)),
        h("span", { class: "dev-host truncate" }, host(d.id)),
      ),
      d.dnd && h("span", { class: "dev-you dev-dnd", title: "Do not disturb: messages to its agents wait" }, icon("bellOff")),
      d.self ? h("span", { class: "dev-you" }, "you") : !d.reachable && h("span", { class: "dev-you" }, "offline"),
      h("span", { class: "menu-count" }, live.length),
    ),
    !closed && [
      live.length > 0 &&
        h(
          "ul",
          { class: "sidebar-menu side-sessions" },
          live.map((s) => {
            const key = s.addr || s.name;
            return h(
              "li",
              { class: "side-li" },
              h(
                "button",
                {
                  class: "side-session",
                  "aria-current": ui.view === "messages" && ui.filter === key ? "page" : null,
                  "data-key": "ss-" + s.name,
                  title: [s.name, s.doing, s.branch && "on " + s.branch, tagsOf(s).names.join(", ")].filter(Boolean).join(" · "),
                  onclick: () => pickSession(key),
                },
                ...sideSessionBody(d, s),
              ),
              h("span", { class: "side-actions" }, s.addr && tagButton(s), messageButton(s.name), s.addr && hideButton(s)),
            );
          }),
        ),
      quietFold(d),
      !ui.tagFilter && settledFold(d.id),
    ],
  );
}

// The foot says who you are here and carries the update notice. With no account yet, it is where
// you get one; with a name someone else claimed first (#268), where you choose another.
function sidebarFoot() {
  const a = me();
  const update = updateItem();
  const chip = a
    ? a.taken
      ? h(
          "button",
          { class: "account-chip", title: "Someone else chose your name first. Choose a new one to send again.", "data-key": "account-chip", onclick: () => askForName(null) },
          avatar(a.name, false),
          h("span", { class: "account-name truncate" }, a.name),
          h("span", { class: "name-taken" }, "New name"),
        )
      : h("button", { class: "account-chip", title: "Your account", "data-key": "account-chip", onclick: () => go("settings") }, avatar(a.name, false), h("span", { class: "account-name truncate" }, a.name), ownerBadge(ui.state?.device))
    : h("button", { class: "account-chip account-none", title: "Agents see your name, and send to it.", "data-key": "account-chip", onclick: () => askForName(null) }, h("span", { class: "avatar account-blank", "aria-hidden": "true" }, icon("user")), h("span", { class: "account-name truncate" }, "Choose your name"));
  return h(
    "div",
    { class: "sidebar-foot" },
    update,
    h(
      "div",
      { class: "foot-account" },
      chip,
      h("button", { class: "icon-btn", title: "Settings", "aria-label": "Settings", "aria-current": ui.view === "settings" ? "page" : null, "data-key": "nav-settings", onclick: () => go("settings") }, icon("settings")),
    ),
  );
}

// The human tag (#267): a person's account has it, worked out from the address, and nothing else
// does. It looks like the session tags it sits among, and can't be taken off.
function humanTag() {
  return h("span", { class: "badge tag badge-outline tag-human", title: "A person, not an agent. " + NAME + " gives this tag to every person's account, and only to one." }, "human");
}

function ownerBadge(device) {
  return device && device === ui.state?.meshOwner
    ? h("span", { class: "badge tag badge-outline", title: "Mesh owner" }, "Owner")
    : null;
}

function personBadges(name, addr) {
  const device = addr?.split("/")[0] || (name === myName() ? ui.state?.device : findPerson(name)?.device.id);
  return [humanTag(), ownerBadge(device)];
}

function peopleGroup() {
  const others = people().filter((p) => p.name !== myName());
  if (!others.length) return null;
  return h(
    "div",
    { class: "sidebar-group dev-group" },
    h("div", { class: "group-label" }, h("span", { class: "label" }, "People"), h("span", { class: "menu-count" }, others.length)),
    h(
      "ul",
      { class: "sidebar-menu side-sessions" },
      others.map((p) =>
        h(
          "li",
          { class: "side-li" },
          h(
            "button",
            { class: "side-session", "data-key": "person-" + p.name, title: "Message " + p.name, onclick: () => startChat(p.name) },
            presence(p.name, p.device),
            h(
              "span",
              { class: "side-session-text" },
              h("span", { class: "name-line" }, h("span", { class: "side-session-name truncate", "data-profile": p.name }, p.name), personBadges(p.name, p.addr)),
              h("span", { class: "side-session-doing truncate" }, personOnline(p) ? "On " + NAME + " now" : deviceLabel(p.device)),
            ),
          ),
        ),
      ),
    ),
  );
}

function messageButton(name) {
  return h(
    "button",
    {
      class: "side-msg",
      "data-tip": "Message",
      "aria-label": "Message " + name,
      "data-key": "msg-" + name,
      onclick: (e) => {
        e.stopPropagation();
        startChat(name);
      },
    },
    icon("message"),
  );
}

// A newer release (#240). Like T3 Code's sidebar and Discord's top bar, it is small, it stays until
// it's dealt with, and it acts in one click: a restart onto an installed update, or the way to
// install one by hand.
const RESTART_WAIT_MS = 20000; // how long the page waits for the new daemon before giving up

function restartStuck() {
  return ui.restarting > 0 && ui.conn === "down" && Date.now() - ui.restarting > RESTART_WAIT_MS;
}

function updateInfo() {
  const v = ui.state?.version;
  if (!v) return null;
  const to = v.latest ? " " + v.latest : "";
  if (ui.restarting) {
    return restartStuck()
      ? { kind: "busy", text: "Run " + COMMAND + " ui again", title: "The restarted daemon serves the page at a new address." }
      : { kind: "busy", text: "Restarting", title: "Restarting onto" + (to || " the update") + ". The page reconnects on its own." };
  }
  if (v.ready) return { kind: "ready", text: "Restart to update", title: NAME + to + " is installed. Restart its daemon onto it now.", act: restartNow };
  if (!v.latest) return null;
  if (v.installing || ui.installing) return { kind: "busy", text: "Installing " + v.latest, title: NAME + " is installing it, as asked." };
  if (v.auto) return { kind: "auto", text: "Updating to " + v.latest, title: NAME + " installs it on its own." };
  return { kind: "manual", text: v.latest + " is out", title: (v.off ? "Updates are off here." : "The last update failed here.") + " Settings can install it.", act: () => go("settings") };
}

function updateIcon(info) {
  return info.kind === "busy" && !restartStuck() ? icon("rotateCw", "spin") : icon("download");
}

function updateItem() {
  const info = updateInfo();
  if (!info) return null;
  const cls = "menu-button update update-" + info.kind;
  const kids = [updateIcon(info), h("span", { class: "label" }, info.text)];
  return info.act
    ? h("button", { class: cls, title: info.title, "data-key": "update", onclick: info.act }, kids)
    : h("div", { class: cls, title: info.title, "data-announce": info.text + ". " + info.title }, kids);
}

function miniUpdate() {
  const info = updateInfo();
  if (!info) return null;
  const label = info.text + ". " + info.title;
  return h(
    "button",
    { class: "mini-btn mini-update update-" + info.kind, title: label, "aria-label": label, "data-key": "mini-update", disabled: !info.act, onclick: info.act },
    updateIcon(info),
  );
}

async function restartNow() {
  if (ui.restarting) return;
  ui.restarting = Date.now();
  delete ui.rowErrors.version;
  render();
  try {
    await api("/api/restart", { method: "POST" });
  } catch (e) {
    ui.restarting = 0;
    if (e instanceof AuthError) return deny(e.token);
    ui.rowErrors.version = "Couldn't restart: " + e.message;
    render();
    return;
  }
  poll();
}

// Update now (#1045): the daemon installs the release the row shows, once, with updates off here or
// failing. It answers at once; the state's polls bring the install's progress and its outcome.
async function installNow() {
  const v = ui.state?.version;
  if (!v?.latest || ui.installing) return;
  ui.installing = true;
  delete ui.rowErrors.version;
  render();
  try {
    await api("/api/updates/install", { method: "POST", body: JSON.stringify({ version: v.latest }) });
    if (ui.state?.version === v) ui.state.version = { ...v, installing: true, installError: "", canInstall: false };
  } catch (e) {
    if (e instanceof AuthError) {
      ui.installing = false;
      return deny(e.token);
    }
    ui.rowErrors.version = "Couldn't install " + v.latest + ": " + e.message;
  }
  ui.installing = false;
  render();
  poll();
}

// Settings' About section: this version and, to its right, whether a newer one is out (#1114). Under
// the version go the daemon's protocol and when it last checked; what the Updates row below already
// says (its choice, and what overrides it) isn't said again here. Only what is happening to the
// newer version is: it installing, installed and waiting for a restart, or failed.
function versionSection() {
  const v = ui.state?.version;
  if (!v?.running) return null;
  let detail = null;
  let button = null;
  if (ui.restarting) {
    detail = restartStuck() ? "Run " + COMMAND + " ui again to reopen the page." : "Restarting onto " + (v.latest || "the update") + ".";
  } else if (v.ready) {
    detail = "Installed. It starts when the daemon restarts.";
    button = h("button", { class: "btn btn-outline btn-xs", title: "Restart this device's daemon now onto the newer version already installed", "data-key": "version-restart", onclick: restartNow }, "Restart now");
  } else if (v.latest && (v.installing || ui.installing)) {
    detail = "Installing it.";
  } else if (v.latest && v.auto) {
    detail = "Installing it on its own.";
  } else if (v.latest) {
    // Off is the Updates row's own choice; a failure is news.
    detail = [v.off ? "" : "The last update failed here.", v.installError && "Couldn't install it: " + v.installError + "."].filter(Boolean).join(" ") || null;
    if (v.canInstall) {
      button = h("button", { class: "btn btn-outline btn-xs", title: "Install " + v.latest + " now. Updates stay as they are", "data-key": "version-install", onclick: installNow }, "Update now");
    }
  }
  const status = h(
    "span",
    { class: "version-status", "data-key": "version-status" },
    h("span", { class: "version-dot " + (v.latest ? "warn" : "ok"), "aria-hidden": "true" }),
    v.latest ? v.latest + " available" : "Up to date",
  );
  const note = [daemonNote(), detail && h("span", { class: "version-detail" }, detail)];
  const row = settingsRow("version", COMMAND + " " + v.running, note.some(Boolean) ? note : null, [status, button]);
  return h("section", { class: "section" }, h("div", { class: "section-head" }, h("h2", { class: "section-title" }, "About")), h("div", { class: "group" }, row, updatesRow()));
}

// The repo picker (#348): this machine's clones that joined, each served by its own daemon. Picking
// one moves this tab to that repo's page, whose daemon is started if it isn't running. Its button
// shows the repo as #1018 drew it (#1112): a dot, filled while the repo's daemon is running and a
// ring while the page can't reach it, the repo, and how many repos the picker holds.
function repoButton() {
  const name = ui.state ? repoLabel() : ""; // nothing until the state says, rather than a name that changes
  const up = ui.conn !== "down";
  const n = ui.repos?.length;
  return h(
    "button",
    {
      class: "repo-btn",
      "aria-haspopup": "menu",
      "aria-expanded": ui.repoMenu ? "true" : "false",
      title: "Switch repo",
      "data-key": "repo",
      onclick: (e) => {
        if (ui.repoMenu) return closeRepoMenu();
        const r = e.currentTarget.getBoundingClientRect();
        ui.repoMenu = { x: r.left, top: r.bottom + 4, repos: null, opening: null, err: "" };
        render();
        focusRepoMenu();
        loadRepos();
      },
    },
    h("span", { class: "dot" + (up ? " ok" : ""), "aria-hidden": "true" }),
    h("span", { class: "repo-name truncate" }, name),
    n ? h("span", { class: "repo-count" }, plural(n, "repo")) : null,
    icon("chevronDown"),
  );
}

// The button's count comes from the same list as the picker, asked for once a page; opening the
// picker asks again, and its answer updates the count too. Without it the button goes without.
async function loadRepoCount() {
  const era = tokenEra;
  try {
    const r = await api("/api/repos");
    if (era !== tokenEra) return;
    ui.repos = r.repos || [];
    render();
  } catch {
    // An error here is the poll's to show.
  }
}

// The menu holds the keyboard while it is open (the page behind it is inert): the focus goes to its
// current repo, the arrow keys move it, and closing it gives it back to the button.
function closeRepoMenu() {
  ui.repoMenu = null;
  render();
  root.querySelector('[data-key="repo"]')?.focus();
}

function focusRepoMenu() {
  if (!root.querySelector(".repo-menu")?.contains(document.activeElement)) {
    (root.querySelector(".repo-row.current") || root.querySelector(".repo-row"))?.focus();
  }
}

async function loadRepos() {
  const m = ui.repoMenu;
  try {
    const r = await api("/api/repos");
    if (ui.repoMenu !== m) return;
    m.repos = r.repos || [];
    ui.repos = m.repos;
  } catch (e) {
    if (e instanceof AuthError) return deny(e.token);
    if (ui.repoMenu !== m) return;
    m.repos = [];
    m.err = e.message;
  }
  render();
  focusRepoMenu();
}

async function openRepo(r) {
  const m = ui.repoMenu;
  if (r.current) return closeRepoMenu();
  if (m.opening) return;
  m.opening = r.id;
  m.err = "";
  render();
  try {
    const { url } = await api("/api/repos/open", { method: "POST", body: JSON.stringify({ id: r.id }) });
    // Starting a daemon can take a while, and the picker may have been closed meanwhile.
    if (ui.repoMenu !== m) return;
    location.assign(url);
  } catch (e) {
    if (e instanceof AuthError) return deny(e.token);
    if (ui.repoMenu !== m) return;
    m.opening = null;
    m.err = e.message;
    if (/isn't joined/.test(e.message)) ui.repos = m.repos = (m.repos || []).filter((x) => x.id !== r.id);
    render();
  }
}

function repoMenu() {
  const m = ui.repoMenu;
  const repos = m.repos || [{ id: "", name: repoLabel(), current: true, running: true }];
  // A path tells two clones of one repo apart; one alone goes by its name.
  const named = new Map();
  for (const r of repos) named.set(repoLabel(r), (named.get(repoLabel(r)) || 0) + 1);
  const menu = h(
    "div",
    {
      class: "tag-menu repo-menu",
      role: "menu",
      "aria-label": "Repos",
      onkeydown: (e) => {
        const rows = [...e.currentTarget.querySelectorAll(".repo-row:not(:disabled)")];
        const at = rows.indexOf(document.activeElement);
        const to = { ArrowDown: at + 1, ArrowUp: at - 1, Home: 0, End: rows.length - 1 }[e.key];
        if (to == null || !rows.length) return;
        e.preventDefault();
        rows[(to + rows.length) % rows.length].focus();
      },
    },
    h(
      "div",
      { class: "tag-menu-list" },
      repos.map((r) =>
        h(
          "button",
          {
            class: "tag-menu-row repo-row" + (r.current ? " current" : ""),
            role: "menuitemradio",
            "aria-checked": r.current ? "true" : "false",
            title: r.path || null,
            "data-key": "repo-" + r.id,
            disabled: !!m.opening && m.opening !== r.id,
            onclick: () => openRepo(r),
          },
          h("span", { class: "dot" + (r.running ? " ok" : ""), role: "img", "aria-label": r.running ? "Running" : "Not running" }),
          h("span", { class: "repo-row-text" }, h("span", { class: "tm-name truncate" }, repoLabel(r)), named.get(repoLabel(r)) > 1 && r.path && h("span", { class: "repo-path truncate" }, r.path)),
          m.opening === r.id ? icon("rotateCw", "spin") : r.current && icon("check"),
        ),
      ),
    ),
    m.err && h("p", { class: "tm-err", "data-announce": "" }, m.err),
  );
  // Placed through the CSSOM: the page's CSP refuses style attributes.
  menu.style.left = m.x + "px";
  menu.style.top = m.top + "px";
  return h(
    "div",
    {
      class: "menu-scrim",
      onclick: (e) => {
        if (e.target === e.currentTarget) closeRepoMenu();
      },
    },
    menu,
  );
}
