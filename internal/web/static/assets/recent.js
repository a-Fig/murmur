// The Recent view: what sessions did, and what GitHub says of their pull requests and issues.

import { icon } from "./icons.js";
import { h, when, plural } from "./dom.js";
import { ui, devices, nameOf, fromName, toName } from "./state.js";
import { render, go } from "./render.js";
import { humanTag } from "./sidebar.js";
import { host, emptyState } from "./sessions.js";
import { inGroup, liveGroups } from "./tags.js";
import {
  messageDetail, conversationOf, avatar, nameButton, messageText, dayLabel, clockTime,
} from "./messages.js";
import { filesOnly } from "./files.js";

export { recentPage };

// Recent: what agents have been doing, newest first, as much as a render can build quickly (#448).
// A message opens its conversation. Between the messages are the things sessions did: joined or
// left, changed their status or tags, committed, and what GitHub says became of pull requests and
// issues. It is a feed, not a log, so what repeats is one line: a session's status changes close
// together show the last, and its commits to one branch are counted.
const RECENT_MAX = 300;
const RECENT_RUN = 30 * 60 * 1000; // how far apart two of a kind may be and still be one line
// How long before GitHub's time for an issue's close a session's close of it may be and still be the
// one GitHub lists: the hook tells of it after gh returns, so later, but clocks differ a little.
const ISSUE_NEAR = 10 * 60 * 1000;

// ghRepo is the owner/name, lowercased, of a pull request's or an issue's address on GitHub, or "".
function ghRepo(url) {
  const m = /^https:\/\/(?:www\.)?github\.com\/([\w.-]+\/[\w.-]+)\/(?:issues|pull)\/\d+(?:[/?#]|$)/i.exec(url || "");
  return m ? m[1].toLowerCase() : "";
}

// The session a pull request is from: the one on its branch, or that committed to it.
function sessionOnBranch(branch) {
  if (!branch) return "";
  let committed = "";
  for (const d of ui.state?.devices || []) {
    for (const s of d.sessions || []) {
      if (s.branch === branch && branch !== "main" && branch !== "master") return s.name;
      if (!committed && (s.committed || []).includes(branch)) committed = s.name;
    }
  }
  return committed;
}

// Everything Recent can show, as { t, who, group, ... }, in no order.
function recentEntries() {
  const out = [];
  // A group's message is each of its members' as well as its sender's, so a member's filter shows
  // it (#485). Members are the sessions in the group now, by inGroup, as the filter has them.
  const members = new Map();
  const sessions = devices().flatMap((d) => d.sessions);
  for (const g of liveGroups()) members.set(g, sessions.filter((s) => inGroup(s, g)).map((s) => s.name));
  for (const m of ui.messages) {
    const to = m.toKind === "session" ? [toName(m)] : m.toKind === "group" ? members.get(m.to) || [] : [];
    out.push({ t: m.sortAt, group: "messages", m, who: [fromName(m), ...to] });
  }
  // An issue a session of this device filed or closed, as its hook saw (#510), names the session on
  // GitHub's event for it: the same number in the repository of the event's address, which needn't
  // be the remote's (gh lists a clone's upstream first), and for a close, one about when GitHub's
  // was, since an issue reopened and closed again was closed last by someone else. One GitHub hasn't
  // listed (yet) shows on its own, if it is in the repository GitHub's listing is of: one, as one
  // run of gh lists one, so an event without an address is in it too.
  const listedRepo = ui.github.map((g) => ghRepo(g.url)).find(Boolean) || "";
  const repoOf = (g) => ghRepo(g.url) || listedRepo;
  const listed = new Map();
  for (const g of ui.github) if (g.kind === "issue" && repoOf(g)) listed.set(repoOf(g) + " " + g.what + " " + g.number, g);
  const issues = new Map();
  // A session's status changes, and its commits to a branch, that follow each other closely are one entry.
  const runs = new Map();
  const acts = [...ui.acts].sort((a, b) => a.t - b.t);
  for (const a of acts) {
    const who = nameOf(a.addr, a.who);
    if (a.kind === "issue") {
      const repo = (a.repo || "").toLowerCase();
      const key = repo + " " + a.what + " " + a.issue;
      const g = listed.get(key);
      if (g && (a.what !== "closed" || a.t >= when(g.at) - ISSUE_NEAR)) {
        issues.set(key, who);
        continue;
      }
      if (!repo || repo !== listedRepo) continue;
    }
    const key = a.kind === "status" ? "status " + who : a.kind === "commits" ? "commits " + who + " " + (a.branch || "") : "";
    const run = key && runs.get(key);
    if (run && a.t - run.t <= RECENT_RUN) {
      run.t = a.t;
      run.n++;
      run.commits += a.commits || 0;
      run.a = a;
      continue;
    }
    const e = { t: a.t, group: "activity", a, who: [who], n: 1, commits: a.commits || 0 };
    if (key) runs.set(key, e);
    out.push(e);
  }
  for (const g of ui.github) {
    const t = when(g.at);
    if (!t) continue;
    const e = { t: Math.min(t, Date.now()), group: "activity", g, who: [sessionOnBranch(g.branch)] };
    if (g.kind === "issue") {
      // Who closed it, where a session here did; else who filed it. Both count for the filter.
      const repo = repoOf(g);
      const filer = (repo && issues.get(repo + " opened " + g.number)) || "";
      const closer = (repo && g.what === "closed" && issues.get(repo + " closed " + g.number)) || "";
      e.who = closer ? [closer, filer] : [filer];
      e.closer = !!closer;
    }
    out.push(e);
  }
  return out;
}

function recentPage() {
  const all = recentEntries();
  const names = [...new Set(all.flatMap((e) => e.who).filter(Boolean))].sort();
  if (ui.recentWho && !names.includes(ui.recentWho)) names.push(ui.recentWho);
  const shown = all.filter((e) => (ui.recentKind === "all" || e.group === ui.recentKind) && (!ui.recentWho || e.who.includes(ui.recentWho)));
  const list = shown.sort((a, b) => b.t - a.t).slice(0, RECENT_MAX);
  const out = [];
  let day = null;
  for (const e of list) {
    const d = e.t && new Date(e.t).toDateString();
    if (d && d !== day) {
      out.push(h("div", { class: "chat-day" }, h("span", null, dayLabel(e.t))));
      day = d;
    }
    out.push(e.m ? openable(recentItem(e.m, when(e.m.at)), e.m, when(e.m.at)) : e.a ? activityItem(e) : githubItem(e));
  }
  const count = (kind) => all.filter((e) => kind === "all" || e.group === kind).length;
  const filtered = ui.recentKind !== "all" || !!ui.recentWho;
  return h(
    "div",
    { class: "g-page g-recent" },
    // A heading at the top of the column, as on main, so the list has something to start from.
    h(
      "header",
      { class: "g-page-head" },
      h("h1", null, "Recent activity"),
      h("p", null, shown.length > RECENT_MAX ? "Newest " + RECENT_MAX + " of " + shown.length : plural(shown.length, "item") + (filtered ? " shown" : "") + ", newest first"),
    ),
    h(
      "div",
      { class: "g-recent-filters" },
      h(
        "div",
        { class: "segmented", role: "tablist", "aria-label": "What to show" },
        [
          ["all", "All"],
          ["messages", "Messages"],
          ["activity", "Activity"],
        ].map(([id, label]) =>
          h(
            "button",
            {
              role: "tab",
              "aria-selected": ui.recentKind === id ? "true" : "false",
              "data-key": "recent-" + id,
              onclick: () => {
                ui.recentKind = id;
                render();
              },
            },
            label,
            h("span", { class: "g-tab-n" }, count(id)),
          ),
        ),
      ),
      h(
        "label",
        { class: "g-recent-who" },
        h("span", { class: "sr-only" }, "Whose"),
        h(
          "select",
          {
            class: "select",
            "data-key": "recent-who",
            onchange: (ev) => {
              ui.recentWho = ev.target.value;
              render();
            },
          },
          h("option", { value: "", selected: !ui.recentWho }, "Everyone"),
          names.map((n) => h("option", { value: n, selected: ui.recentWho === n }, n)),
        ),
      ),
    ),
    list.length
      ? h("div", { class: "g-feed" }, out)
      : filtered
        ? emptyState("clock", "Nothing matches", "Show everything, or pick another session.")
        : emptyState("clock", "Nothing yet", "Messages between sessions, and what the sessions do, show here as this daemon learns of them."),
  );
}

// One line of activity: an icon where a message has its sender's face, who, what, and when. tone
// colors the icon by what happened (style.css's .g-act-*), and "" leaves it grey.
function activityLine(iconName, tone, t, key, ...what) {
  return h(
    "div",
    { class: tone ? "g-act g-act-" + tone : "g-act", "data-id": key },
    h("span", { class: "g-act-ico" }, icon(iconName)),
    h("p", { class: "g-act-text" }, what),
    h("time", { class: "chat-msg-time", title: t ? new Date(t).toLocaleString() : "" }, clockTime(t)),
  );
}

function actWho(name) {
  return name ? nameButton(name, "g-act-who") : h("span", { class: "g-act-who" }, name);
}

function actBranch(branch) {
  return h("span", { class: "g-act-branch" }, icon("branch"), branch);
}

function activityItem(e) {
  const a = e.a;
  const who = actWho(e.who[0]);
  switch (a.kind) {
    case "started":
      return activityLine("logIn", "", e.t, a.id, who, " joined", a.agent && [" as ", a.agent], a.branch && [" on ", actBranch(a.branch)]);
    case "stopped":
      return activityLine("logOut", "", e.t, a.id, who, " left");
    case "status":
      return activityLine("activity", "status", e.t, a.id, who, " is now ", h("span", { class: "g-act-quote" }, a.doing), e.n > 1 && h("span", { class: "g-act-more" }, " after " + plural(e.n - 1, "earlier update")));
    case "tags": {
      const added = (a.added || []).map((t) => h("span", { class: "g-act-tag" }, t));
      const removed = (a.removed || []).map((t) => h("span", { class: "g-act-tag g-act-tag-off" }, t));
      return activityLine("tag", "", e.t, a.id, who, added.length ? [" is tagged ", added] : null, removed.length ? [added.length ? " and no longer " : " is no longer tagged ", removed] : null);
    }
    case "commits":
      return activityLine(
        "commit",
        "commit",
        e.t,
        a.id,
        who,
        e.commits ? " made " + plural(e.commits, "commit") : " committed",
        a.branch ? [e.commits ? " on " : " to ", actBranch(a.branch)] : null,
        a.subject && h("span", { class: "g-act-more" }, e.commits > 1 ? ", the last: " : ": ", a.subject),
      );
    case "issue":
      return a.what === "closed" ? activityLine("issueClosed", "done", e.t, a.id, who, " closed issue #" + a.issue) : activityLine("issue", "open", e.t, a.id, who, " filed issue #" + a.issue);
  }
  return activityLine("activity", "", e.t, a.id, who, " ", a.kind);
}

function githubItem(e) {
  const g = e.g;
  const pr = g.kind === "pr";
  const what = pr ? "pull request" : "issue";
  const num = g.url ? h("a", { href: g.url, target: "_blank", rel: "noopener noreferrer" }, "#" + g.number) : "#" + g.number;
  const who = e.who[0] ? actWho(e.who[0]) : g.author ? h("span", { class: "g-act-login" }, g.author) : "Someone";
  const title = h("span", { class: "g-act-more" }, ": ", g.title);
  if (g.what === "opened") return activityLine(pr ? "pr" : "issue", "open", e.t, g.id, who, pr ? " opened pull request " : " filed issue ", num, title);
  if (e.closer) return activityLine("issueClosed", "done", e.t, g.id, who, " closed issue ", num, title);
  // GitHub's list says who opened it, not who merged or closed it.
  // GitHub's colors: a merged pull request and a closed issue are done, a closed pull request is not.
  const merged = g.what === "merged";
  const verb = merged ? " was merged" : " was closed";
  return activityLine(merged ? "merge" : pr ? "prClosed" : "issueClosed", merged || !pr ? "done" : "closed", e.t, g.id, e.who[0] ? [who, "'s " + what + " "] : what[0].toUpperCase() + what.slice(1) + " ", num, verb, title);
}

function recentItem(m, at) {
  const from = fromName(m);
  const c = conversationOf(m);
  const to = m.toKind === "all" ? [icon("broadcast"), "Everyone"] : m.toKind === "group" ? [icon("tag"), m.to] : m.toKind === "device" ? [icon("laptop"), host(m.to)] : [avatar(toName(m)), h("span", { "data-profile": toName(m) }, toName(m))];
  const open = () => {
    ui.filter = null;
    ui.conversation = c.key;
    ui.conversationAnchor = m.id;
    go("messages");
  };
  return h(
    "article",
    { class: "chat-msg chat-msg-face chat-msg-first g-item", "data-id": m.id, tabindex: 0, role: "button", "aria-label": "Open the conversation", onclick: open, onkeydown: (e) => (e.key === "Enter" && e.target === e.currentTarget ? open() : null) },
    avatar(from),
    h(
      "div",
      { class: "chat-msg-main" },
      h(
        "div",
        { class: "chat-msg-head" },
        h("span", { class: "chat-msg-from truncate", "data-profile": from }, from),
        m.fromPerson && humanTag(),
        h("span", { class: "g-to" }, icon("arrowRight", "g-to-arrow"), h("span", { class: "g-to-who truncate" }, to)),
        h("span", { class: "chat-msg-time", title: messageDetail(m) }, clockTime(at)),
      ),
      !filesOnly(m) && messageText(m),
      m.files?.length > 0 && h("div", { class: "msg-files-line" }, icon("paperclip"), h("span", { class: "truncate" }, m.files.map((f) => f.name).join(", "))),
    ),
  );
}

// A Recent item is a button onto its conversation. Its name is what it shows, in the order it shows
// it (WCAG 2.5.3), Space works as well as Enter, and a link or a name in its text is left to the
// conversation it opens from the keyboard, since a button can't hold another control. A click on a
// name still opens its card.
function openable(el, m, at) {
  const to = m.toKind === "all" ? "Everyone" : m.toKind === "group" ? m.to : m.toKind === "device" ? host(m.to) : toName(m);
  el.setAttribute("aria-label", fromName(m) + " to " + to + ", " + clockTime(at) + ": " + (m.subject || ""));
  const space = (e) => {
    if (e.key !== " " || e.target !== e.currentTarget) return;
    e.preventDefault();
    e.currentTarget.click();
  };
  el.addEventListener("keydown", space);
  el.listeners.push(["keydown", space]);
  for (const a of el.querySelectorAll("a, button")) a.tabIndex = -1;
  return el;
}
