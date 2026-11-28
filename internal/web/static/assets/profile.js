// The full view of a session or a person (#716), which the profile card opens: who it is and how it
// is, what it has been doing, and who it works with. It is all what the page already holds
// (presence, messages and Recent's feed), so it asks the daemon for nothing more.

import { icon } from "./icons.js";
import { h, when, ago, agoEl, plural } from "./dom.js";
import { ui, fromName, toName, findSession, findPerson, isPerson, myName, personOnline } from "./state.js";
import { go } from "./render.js";
import { personBadges } from "./sidebar.js";
import { PERSON_TIPS, presence, settledSessions, host, deviceLabel, hideButton } from "./sessions.js";
import { tagsLine, profileLine, tagsOf, isGroupTag } from "./tags.js";
import { avatar, deviceOfName, headMeta } from "./messages.js";
import { startChat } from "./picker.js";
import { feedOf } from "./recent.js";

export { profilePage, openFullView };

const FEED_MAX = 40; // the newest lines of its activity shown; Recent has the rest
const MATES_MAX = 6;

// Its address names it (#profile/quiet-otter), so Back goes from one full view to the one before.
function openFullView(name) {
  ui.profile = null;
  go("profile/" + encodeURIComponent(name));
}

function profilePage() {
  const name = ui.profileOf;
  const f = findSession(name);
  const s = f?.session || null;
  const person = findPerson(name);
  const d = f?.device || person?.device || deviceOfName(name);
  const own = name === myName();
  const human = !!person || own || isPerson(name);
  const settled = !s && !human ? settledSessions().find((r) => r.name === name) : null;
  const feed = feedOf(name, FEED_MAX);
  const mates = worksWith(name);
  const groups = s ? tagsOf(s).names.filter(isGroupTag) : groupsWrittenIn(name);
  const face = d && (s || human) ? presence(name, d) : avatar(name);
  face.classList.add("g-full-face");
  const status = s
    ? profileLine(d, s)
    : h("p", { class: "g-head-status" }, human ? [h("span", null, PERSON_TIPS[own || (person && personOnline(person)) ? "on" : "off"]), d && h("span", null, d.self ? "This device" : host(d.id))] : settled ? "Stopped " + ago(settled.last) : "Not running");
  return h(
    "div",
    { class: "g-page g-full" },
    h(
      "header",
      { class: "g-full-head" },
      face,
      h("div", { class: "g-full-id" }, h("h1", { class: "name-line" }, h("span", null, name), human && personBadges(name)), status),
      h(
        "div",
        { class: "g-full-actions" },
        !own && h("button", { class: "btn btn-outline btn-sm", "data-key": "full-msg", onclick: () => startChat(name) }, icon("message"), "Message"),
        h("button", { class: "btn btn-ghost btn-sm", "data-key": "full-convs", onclick: () => showConversations(name, s, person) }, "Conversations"),
        s?.addr && hideButton(s),
      ),
    ),
    s && h("div", { class: "g-full-about" }, s.doing && h("p", { class: "g-full-doing" }, s.doing), headMeta(s), tagsLine(s)),
    h(
      "div",
      { class: "g-full-grid" },
      s && section("How it's been", facts(d, s)),
      section(
        "Works with",
        mates.length || groups.length
          ? h(
              "div",
              { class: "g-full-mates" },
              mates.map(([n, count]) =>
                h(
                  "button",
                  { type: "button", class: "g-full-mate", "data-profile": n, "aria-haspopup": "dialog", "data-key": "mate-" + n },
                  avatar(n, false),
                  h("span", { class: "truncate" }, n),
                  h("span", { class: "g-full-n" }, plural(count, "message")),
                ),
              ),
              groups.length > 0 && h("div", { class: "g-full-groups" }, groups.map((g) => h("span", { class: "stag", title: g }, g))),
            )
          : h("p", { class: "g-full-none" }, "No messages with anyone yet."),
      ),
    ),
    section(
      "Activity",
      feed.items.length ? h("div", { class: "g-feed g-full-feed" }, feed.items) : h("p", { class: "g-full-none" }, "Nothing yet."),
      feed.total > FEED_MAX &&
        h(
          "button",
          {
            class: "btn btn-ghost btn-sm g-full-more",
            "data-key": "full-recent",
            onclick: () => {
              ui.recentKind = "all";
              ui.recentWho = name;
              go("recent");
            },
          },
          "All " + feed.total + " in Recent",
          icon("arrowRight"),
        ),
    ),
  );
}

function section(title, ...kids) {
  return h("section", { class: "section" }, h("div", { class: "section-head" }, h("h2", { class: "section-title" }, title)), h("div", { class: "group g-full-box" }, kids));
}

// When it started, when it last did something, where it runs, and its hook, if it isn't running.
function facts(d, s) {
  const started = when(s.started);
  const active = when(s.lastActive);
  const row = (label, ...value) => [h("dt", null, label), h("dd", null, value)];
  return h(
    "dl",
    { class: "g-full-facts" },
    started && row("Started", agoEl("span", { title: new Date(started).toLocaleString() }, started)),
    active && row("Last active", agoEl("span", { title: new Date(active).toLocaleString() }, active)),
    row("Device", d.self ? "This device" : deviceLabel(d), h("span", { class: "g-full-host" }, " " + host(d.id))),
    s.worktree && row("Folder", h("span", { class: "g-full-path", title: s.worktree }, s.worktree)),
    s.hookOff && row("Hook", h("span", { class: "badge badge-warning" }, "Not running"), " It isn't told of messages while it works."),
  );
}

// Who it has had direct messages with, most first.
function worksWith(name) {
  const n = new Map();
  for (const m of ui.messages) {
    if (m.toKind !== "session") continue;
    const from = fromName(m);
    const to = toName(m);
    const other = from === name ? to : to === name ? from : null;
    if (other && other !== name) n.set(other, (n.get(other) || 0) + 1);
  }
  return [...n].sort((a, b) => b[1] - a[1]).slice(0, MATES_MAX);
}

// A person holds no group tags: their groups are those they have written in.
function groupsWrittenIn(name) {
  const out = new Set();
  for (const m of ui.messages) if (m.toKind === "group" && fromName(m) === name) out.add(m.to);
  return [...out].sort();
}

function showConversations(name, s, person) {
  ui.filter = s?.addr || person?.addr || name;
  ui.conversation = null;
  go("messages");
}
