// A conversation's messages: runs from one sender, a person's text and an agent's subject and body,
// sending and failed ones, receipts, and the little markup a message may carry.

import { icon } from "./icons.js";
import { h, when } from "./dom.js";
import { ui, fromName, findSession, me, myName } from "./state.js";
import { deliver } from "./sync.js";
import { announce } from "./render.js";
import { personBadges } from "./sidebar.js";
import { host } from "./sessions.js";
import { filesOnly, messageFiles, fileList } from "./files.js";
import { showTipNow } from "./tips.js";
import { messageDetail } from "./messages.js";
import { avatar, nameButton, knownName, withNames } from "./faces.js";
import { pendingIn, editFailed } from "./composer.js";

export { chatMessages, personText, messageText, dayLabel, clockTime };

// A sender's messages run together under one header, as long as they come close together.
const RUN_MS = 7 * 60 * 1000;

function chatMessages(c) {
  const out = [];
  let prev = null;
  let prevDay = null;
  for (const m of c.messages) {
    const at = when(m.at);
    const day = at && new Date(at).toDateString();
    if (day && day !== prevDay) {
      out.push(h("div", { class: "chat-day" }, h("span", null, dayLabel(at))));
      prev = null;
      prevDay = day;
    }
    const from = fromName(m);
    const head = !prev || fromName(prev) !== from || !at || at - (when(prev.at) || 0) > RUN_MS;
    if (ui.newMark?.key === c.key && ui.newMark.id === m.id) out.push(h("div", { class: "chat-new" }, h("span", null, "New")));
    out.push(chatMessage(m, from, at, head));
    prev = m;
  }
  for (const item of pendingIn(c)) {
    const head = !prev || fromName(prev) !== myName() || item.status === "failed";
    out.push(pendingMessage(item, head));
    prev = { from: myName(), fromAddr: me()?.addr };
  }
  return out;
}

// What a person wrote, whole, from the subject and body the bus carries it as (web.SplitText). A
// subject that is only the body's start, cut short, is left out, as RepeatsSubject in
// internal/view/view.go leaves it out of the agent's turn and inbox.
function personText(m) {
  const subject = m.subject || "";
  const body = m.body || "";
  if (repeatsSubject(subject, body)) return body;
  return body ? subject + "\n" + body : subject;
}

// repeatsSubject is RepeatsSubject in internal/view/view.go: a subject cut short with "…" or "..."
// that is only the start of the body, compared word by word, as a subject's spaces are collapsed.
function repeatsSubject(subject, body) {
  const end = ["…", "..."].find((e) => subject.endsWith(e));
  if (!end || !body) return false;
  const stem = subject.slice(0, -end.length);
  const words = (s) => s.trim().split(/\s+/).join(" ");
  return body.startsWith(stem) || words(body).startsWith(words(stem));
}

// A message's text: a person's as they wrote it, an agent's with its subject over its body. An
// agent's subject that only repeats the start of its body is left out, as a turn leaves it (#1049).
function messageText(m) {
  if (m.fromPerson) return h("div", { class: "chat-msg-body rich" }, rich(personText(m)));
  const subject = m.subject && !repeatsSubject(m.subject, (m.body || "").trim()) ? m.subject : "";
  return [subject && h("div", { class: "chat-msg-subject" }, subject), m.body && h("div", { class: "chat-msg-body rich" }, rich(m.body))];
}

// A message the person wrote that isn't among the messages yet. It looks written at once, as a
// chat app's does (#613): a clock while its push to the remote is on its way, which took 2.4 to
// 4.7 s from a Windows laptop on 2026-11-15, then the tick for sent. The daemon finishes the push
// if the page closes meanwhile.
function pendingMessage(item, head) {
  const failed = item.status === "failed";
  return h(
    "article",
    { class: "chat-msg chat-msg-own" + (head ? " chat-msg-first" : "") + (failed ? " chat-msg-failed" : ""), "data-cid": item.cid },
    item.text && h("div", { class: "chat-bubble rich" }, rich(item.text)),
    item.files?.length > 0 && fileList("out-" + item.cid, item.files, (i) => item.files[i]),
    failed
      ? h(
          "div",
          { class: "msg-failed" },
          h("span", { class: "msg-failed-why", "data-announce": /^not sent/i.test(item.error) ? item.error : "Not sent: " + item.error }, item.error),
          h("button", { class: "btn btn-ghost btn-xs", "data-key": "retry-" + item.cid, onclick: () => deliver(item) }, icon("retry"), "Try again"),
          h("button", { class: "btn btn-ghost btn-xs", "data-key": "edit-" + item.cid, onclick: () => editFailed(item) }, icon("pencil"), "Edit"),
        )
      : h("div", { class: "chat-msg-meta" }, sendingMark(item)),
  );
}

// The mark under a message on its way: a clock until it is on the remote, then one tick, as
// receiptMark shows it once the message is listed. Its tip promises the message goes out only
// once the daemon has it (item.posted): a new group's message waits here first for its members'
// tags (deliver). A button, as receiptMark's is, so the keyboard and a tap reach the tip.
function sendingMark(item) {
  const sent = item.status === "sent";
  const tip = sent ? "Sent" : item.posted ? "Sending: it goes out even if you close this page" : "Sending";
  return h(
    "button",
    {
      class: "receipt-mark",
      "data-state": sent ? "sent" : "sending",
      "data-key": "sending-" + item.cid,
      "data-tip": tip,
      "data-tip-labelled": "1",
      "aria-label": tip,
      onclick: (e) => showTipNow(e.currentTarget, e.pointerType === "mouse" ? "hover" : "tap"),
    },
    icon(sent ? "check" : "clock"),
  );
}

function dayLabel(t) {
  const today = new Date();
  if (new Date(t).toDateString() === today.toDateString()) return "Today";
  today.setDate(today.getDate() - 1);
  if (new Date(t).toDateString() === today.toDateString()) return "Yesterday";
  return new Date(t).toLocaleDateString(undefined, { year: "numeric", month: "long", day: "numeric" });
}

function clockTime(at) {
  return at ? new Date(at).toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" }) : "";
}

// Says Copied only once the clipboard has it: writing can fail, as when the page isn't focused.
function copyText(text) {
  const w = navigator.clipboard?.writeText(text);
  if (!w) return announce("Couldn't copy.");
  w.then(
    () => announce("Copied."),
    () => announce("Couldn't copy."),
  );
}

function copyButton(text) {
  return h(
    "button",
    {
      class: "btn btn-ghost btn-icon-xs",
      title: "Copy",
      "aria-label": "Copy message",
      onclick: (e) => {
        copyText(text);
        const b = e.currentTarget;
        b.replaceChildren(icon("check"));
        b.classList.add("copied");
        setTimeout(() => (b.replaceChildren(icon("copy")), b.classList.remove("copied")), 1200);
      },
    },
    icon("copy"),
  );
}

function chatMessage(m, from, at, head) {
  const sender = findSession(from);
  const mine = from === myName();
  const copy = m.fromPerson ? personText(m) : (m.subject ? m.subject + "\n\n" : "") + (m.body || "");
  const time = h("span", { class: "chat-msg-time", title: messageDetail(m) }, clockTime(at));
  if (mine) {
    return h(
      "article",
      { class: "chat-msg chat-msg-own" + (head ? " chat-msg-first" : ""), "data-id": m.id },
      !filesOnly(m) && h("div", { class: "chat-bubble rich" }, rich(personText(m))),
      messageFiles(m),
      h("div", { class: "chat-msg-meta" }, time, copyButton(copy), receiptMark(m)),
    );
  }
  // Everyone else's message has its sender's face beside it, and a run of messages from one sender
  // shows the face once.
  const via = [sender?.session.agent, sender && !sender.device.self ? host(sender.device.id) : null].filter(Boolean).join(" · ");
  return h(
    "article",
    { class: "chat-msg chat-msg-face" + (head ? " chat-msg-first" : ""), "data-id": m.id, title: head ? null : messageDetail(m) },
    head ? avatar(from) : h("span", { class: "avatar-gap" }),
    h(
      "div",
      { class: "chat-msg-main" },
      head && h("div", { class: "chat-msg-head" }, h("button", { type: "button", class: "chat-msg-from truncate", "data-profile": from, "aria-haspopup": "dialog", "data-key": "from-" + m.id }, from), m.fromPerson && personBadges(from, m.fromAddr), via && h("span", { class: "chat-msg-via truncate" }, via), time),
      !filesOnly(m) && messageText(m),
      messageFiles(m),
    ),
    h("div", { class: "msg-tools" }, copyButton(copy)),
  );
}

// How far one of the person's messages got (#26), as a chat app shows it: one tick for sent, two
// for delivered, and two in the accent colour for read (docs/design/receipts.md). The states in
// between, queued and handed over, read as sent, and so does a recipient whose Murmur sends no
// receipts. A message to several shows the lowest of them, so it reads as read only once everyone
// has read it. The tick's tooltip says what its state means, then who has it and since when, and
// nothing else shows them (Tyler, 2026-11-13): on hover, on keyboard focus, and on touch, where
// nothing hovers, on a tap. A message from before receipts, or past the newest 200, shows no mark
// rather than a guess.
const RECEIPT_RANK = { sent: 0, delivered: 1, read: 2 };

function receiptFold(state) {
  return state === "read" || state === "delivered" ? state : "sent";
}

function receiptLabel(state) {
  return state === "read" ? "Read" : state === "delivered" ? "Delivered" : "Sent";
}

// The tooltip's first line: the state, and in a few words what it leaves out, for one recipient or
// for several (Tyler, 2026-11-13: a few words, no sentences). Delivered is knowing a message waits,
// read is having it (#325); sent covers every step before.
const RECEIPT_MEANS = {
  sent: ["Sent", "Sent"],
  delivered: ["Delivered: not read yet", "Delivered: not all read yet"],
  read: ["Read", "Read"],
};

function receiptMeaning(state, several) {
  return RECEIPT_MEANS[state][several ? 1 : 0];
}

// One recipient as the tooltip lists them: their name, and for several their own state, with the
// time they got there if they got past sent.
function receiptEntry(r) {
  const state = receiptFold(r.state);
  const at = state !== "sent" && when(r.at);
  const today = at && new Date(at).toDateString() === new Date().toDateString();
  return { name: r.name, state, time: !at ? "" : today ? clockTime(at) : new Date(at).toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" }) };
}

function receiptLine(e, several) {
  return [e.name, several ? receiptLabel(e.state) : "", e.time].filter(Boolean).join(" · ");
}

function receiptMark(m) {
  const rs = ui.state?.receipts?.[m.id];
  if (!rs) return null;
  const entries = rs.map(receiptEntry).sort((a, b) => RECEIPT_RANK[b.state] - RECEIPT_RANK[a.state] || a.name.localeCompare(b.name));
  const state = entries.reduce((low, e) => (RECEIPT_RANK[e.state] < RECEIPT_RANK[low] ? e.state : low), "read");
  const several = entries.length > 1;
  // A message that found nobody in presence to count (a group with no member online) is sent.
  const lines = entries.length ? [receiptMeaning(state, several), ...entries.map((e) => receiptLine(e, several))] : ["Sent: nobody online"];
  // A button, so the keyboard reaches it and focus shows its tooltip. A tap shows it too: the page's
  // tips follow the mouse, and a touch has none. A tap elsewhere hides it, as any tip is. Its label is
  // the tip's text, so the tip doesn't describe it again (data-tip-labelled).
  return h(
    "button",
    {
      class: "receipt-mark",
      "data-state": entries.length ? state : "sent",
      "data-key": "receipt-" + m.id,
      "data-tip": lines.join("\n"),
      "data-tip-labelled": "1",
      "aria-label": lines.join(". "),
      onclick: (e) => showTipNow(e.currentTarget, e.pointerType === "mouse" ? "hover" : "tap"),
    },
    icon(entries.length && state !== "sent" ? "checks" : "check"),
  );
}

// Message bodies are Markdown-ish text from agents. A small subset becomes structure: paragraphs,
// lists, fenced code, inline code, bold and https links. All of it reaches the page as text nodes.
function rich(text) {
  const out = [];
  const lines = text.replace(/\r\n/g, "\n").split("\n");
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (/^\s*```/.test(line)) {
      const code = [];
      i++;
      while (i < lines.length && !/^\s*```/.test(lines[i])) code.push(lines[i++]);
      i++;
      out.push(h("pre", null, h("code", null, code.join("\n"))));
      continue;
    }
    const bullet = /^\s*[-*•] +/;
    const num = /^\s*\d+[.)] +/;
    if (bullet.test(line) || num.test(line)) {
      const re = bullet.test(line) ? bullet : num;
      const items = [];
      while (i < lines.length && lines[i].trim()) {
        if (re.test(lines[i])) items.push(lines[i].replace(re, ""));
        else if (items.length) items[items.length - 1] += " " + lines[i].trim();
        else break;
        i++;
      }
      out.push(h(re === bullet ? "ul" : "ol", null, items.map((t) => h("li", null, inline(t)))));
      continue;
    }
    if (!line.trim()) {
      i++;
      continue;
    }
    const para = [];
    while (i < lines.length && lines[i].trim() && !/^\s*```/.test(lines[i]) && !bullet.test(lines[i]) && !num.test(lines[i])) para.push(lines[i++]);
    out.push(h("p", null, para.map((l, k) => [k ? h("br") : null, inline(l)])));
  }
  return out;
}

function inline(t) {
  const out = [];
  const re = /(`[^`]+`)|(\*\*[^*]+\*\*)|(https:\/\/[^\s)<>]+[^\s)<>.,;:!?'"])/g;
  let last = 0;
  for (let m; (m = re.exec(t)); ) {
    if (m.index > last) withNames(t.slice(last, m.index), out);
    if (m[1] && knownName(m[1].slice(1, -1))) out.push(nameButton(m[1].slice(1, -1), "rich-name", h("code", null, m[1].slice(1, -1))));
    else if (m[1]) out.push(h("code", null, m[1].slice(1, -1)));
    else if (m[2]) {
      const bold = [];
      withNames(m[2].slice(2, -2), bold);
      out.push(h("strong", null, bold));
    }
    else out.push(h("a", { href: m[3], target: "_blank", rel: "noopener noreferrer" }, m[3]));
    last = re.lastIndex;
  }
  if (last < t.length) withNames(t.slice(last), out);
  return out;
}
