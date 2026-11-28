// Starting a conversation: choosing a name, and the new-message and new-group pickers.

import { icon } from "./icons.js";
import { h, when, plural, root } from "./dom.js";
import {
  NAME, ui, devices, me, myName, accountOffers, usableOffers, primaryOffer, people, personOnline,
} from "./state.js";
import { AuthError, api } from "./api.js";
import { deny, poll } from "./sync.js";
import { render, go, setView, starRow } from "./render.js";
import { humanTag } from "./sidebar.js";
import { sessionState, isHidden, presence, host } from "./sessions.js";
import { inGroup, liveGroups, isGroupTag, validTag, sessionByAddr, tagSessions } from "./tags.js";
import { groupConversation, dmKey, personFace, deviceColor, focusComposer } from "./messages.js";
import { moveToName } from "./settings.js";

export {
  startGroup, startChat, askForName, refocus, setupDialog, newMessageButton, newGroupButton, openPicker,
  pickerDialog, startPane,
};

// Start a group chat with these sessions and open it, ready to write in (#517, #518). A group is a
// tag, so starting one tags them; the group shows at once, its members as waiting until their
// devices have the tag. With no tag given, a group that is just these sessions is opened if there is
// one, and otherwise a new one is named for them.
function startGroup(sessions, tag) {
  if (!ui.state || !sessions.length) return;
  if (!me()) return askForName(() => startGroup(sessions, tag));
  tag = tag || groupOf(sessions) || newGroupTag(sessions.map((s) => s.name));
  ui.picker = null;
  ui.filter = null;
  ui.draftTo = null;
  ui.draftGroup = tag;
  ui.conversation = groupConversation(tag).key;
  go("messages");
  setView("messages");
  focusComposer();
  tagSessions(sessions, tag, true);
}

// The group whose members are exactly these sessions, if there is one.
function groupOf(sessions) {
  const want = new Set(sessions.map((s) => s.addr));
  return liveGroups().find((g) => {
    const members = devices().flatMap((d) => d.sessions.filter((s) => inGroup(s, g)));
    return members.length === want.size && members.every((s) => want.has(s.addr));
  });
}

// A group's tag named for its members, by their whole names while those fit in a tag and by their
// first words after that, numbered if a group already has the name.
function newGroupTag(names) {
  const max = 40 - "group:".length;
  const fit = (b, n) => b.toLowerCase().replace(/[^a-z0-9._:-]/g, "-").slice(0, n).replace(/-+$/, "") || "chat";
  let base = names.join("-");
  if (base.length > max) base = names.map((n) => n.split("-")[0]).join("-");
  const taken = new Set(liveGroups());
  let tag = "group:" + fit(base, max);
  for (let i = 2; taken.has(tag); i++) tag = "group:" + fit(base, max - 1 - String(i).length) + "-" + i;
  return tag;
}

// Open the conversation with someone, whether or not it has any messages yet.
function startChat(name) {
  if (!ui.state) return;
  if (!me()) return askForName(() => startChat(name));
  ui.picker = null;
  ui.filter = null;
  // Yourself, from a group's members: there is no chat with yourself, so Conversations.
  if (name === myName()) {
    ui.conversation = null;
    ui.draftTo = null;
    ui.draftGroup = null;
    return go("messages");
  }
  ui.draftTo = name;
  ui.draftGroup = null;
  ui.conversation = dmKey(myName(), name);
  go("messages");
  setView("messages"); // the hash changes later, and the composer has to be there to focus now
  focusComposer();
}

function takenOfferNotice(offers) {
  const bad = offers.find((o) => o.taken || o.problem);
  if (!bad) return "";
  if (bad.taken) return "Someone on " + bad.taken + " already uses " + bad.name + " here, so we can't reuse it from " + bad.repo + ".";
  return "Can't use " + bad.name + " from " + bad.repo + ": " + bad.problem;
}

function askForName(then) {
  const a = ui.state?.account;
  const offers = !a?.name ? accountOffers() : [];
  const offer = !a?.name ? primaryOffer() : null;
  const from = document.activeElement?.dataset?.key || null;
  if (offer) {
    ui.setup = {
      mode: "offer",
      offerIndex: offers.indexOf(offer),
      name: offer.name,
      nick: a?.nick || offer.nick || "",
      error: "",
      busy: false,
      then,
      from,
    };
    render();
    root.querySelector('[data-key="setup-accept"]')?.focus();
    return;
  }
  const offerNick = !a?.name && !a?.nick ? (offers.find((o) => o.nick)?.nick || "") : "";
  ui.setup = {
    mode: "edit",
    offerIndex: -1,
    name: (a?.taken && a.name) || a?.suggested || "",
    nick: a?.nick || offerNick,
    error: "",
    busy: false,
    then,
    from,
  };
  render();
  const field = root.querySelector('[data-key="setup-name"]');
  field?.focus();
  field?.select();
}

async function submitName() {
  const setup = ui.setup;
  if (!setup || setup.busy) return;
  const a = ui.state?.account;
  const offers = !a?.name ? accountOffers() : [];
  const offer = setup.mode === "offer" ? offers[setup.offerIndex] || primaryOffer() : null;
  const name = (offer ? offer.name : setup.name).trim();
  const payload = { name };
  const existingNick = a?.nick || "";
  if (setup.mode === "offer" && offer) {
    if (!existingNick && offer.nick) payload.nick = offer.nick;
  } else if (offers.length > 0 && !a?.taken) {
    const trimmedNick = setup.nick.trim();
    if (trimmedNick !== existingNick) payload.nick = trimmedNick;
  }
  setup.busy = true;
  setup.error = "";
  render();
  try {
    const was = myName();
    const account = await api("/api/account", { method: "PUT", body: JSON.stringify(payload) });
    ui.state.account = { ...ui.state.account, ...account, offers: account.offers || [] };
    moveToName(was, account.name); // asked for a new name, they may have drafts under the old one
    ui.setup = null;
    poll();
    setup.then?.();
    render();
    refocus(setup.from);
  } catch (e) {
    if (e instanceof AuthError) return deny(e.token);
    setup.busy = false;
    setup.error = e.message;
    if (setup.mode === "offer") {
      // Switch to edit mode if the offer raced or failed validation so they can pick another name.
      setup.mode = "edit";
      setup.name = a?.suggested || name;
      setup.nick = existingNick || offer?.nick || offers.find((o) => o.nick)?.nick || "";
      poll();
    }
    render();
    root.querySelector('[data-key="setup-name"]')?.focus();
  }
}

// A dialog that closes gives the focus back to what opened it, unless what it did moved it on.
function refocus(key) {
  if (key && !root.contains(document.activeElement)) root.querySelector(`[data-key="${CSS.escape(key)}"]`)?.focus();
}

function dialog(label, close, ...kids) {
  return h(
    "div",
    {
      class: "scrim",
      onclick: (e) => {
        if (e.target === e.currentTarget) close();
      },
      onkeydown: (e) => {
        if (e.key === "Escape") {
          e.preventDefault();
          close();
        }
      },
    },
    h("div", { class: "dialog", role: "dialog", "aria-modal": "true", "aria-label": label }, kids),
  );
}

function setupDialog() {
  const s = ui.setup;
  const a = ui.state?.account;
  const taken = me()?.taken || "";
  const offers = !a?.name ? accountOffers() : [];
  const usable = !a?.name ? usableOffers() : [];
  if (s.mode === "offer") {
    const current = offers[s.offerIndex];
    if (!current || current.taken || current.problem) {
      if (usable.length) {
        s.offerIndex = offers.indexOf(usable[0]);
        s.name = usable[0].name;
        s.nick = a?.nick || usable[0].nick || "";
      } else {
        s.mode = "edit";
        s.name = a?.suggested || "";
        s.nick = a?.nick || offers.find((o) => o.nick)?.nick || "";
      }
    }
  }
  const close = () => {
    ui.setup = null;
    render();
    refocus(s.from);
  };
  if (s.mode === "offer") {
    const offer = offers[s.offerIndex] || usable[0];
    const shownNick = a?.nick || offer.nick || "";
    const title = "Use your name from " + offer.repo + "?";
    const blocked = offers.find((o, i) => i < s.offerIndex && (o.taken || o.problem));
    return dialog(
      title,
      close,
      h("h2", { class: "dialog-title" }, "Use your name from ", h("span", { class: "setup-repo" }, offer.repo), "?"),
      h(
        "p",
        { class: "dialog-line" },
        "Kept private on your machine. Accept to share your name with this repo's team.",
      ),
      h(
        "form",
        {
          class: "setup-form",
          onsubmit: (e) => {
            e.preventDefault();
            submitName();
          },
        },
        h(
          "div",
          { class: "setup-offer" },
          h("span", { class: "setup-face" }, faceOf(offer.name)),
          h(
            "div",
            { class: "setup-offer-body" },
            h(
              "div",
              { class: "setup-offer-grid" },
              h("span", { class: "setup-offer-k" }, "Name"),
              h("span", { class: "setup-offer-v" }, offer.name),
              h("span", { class: "setup-offer-k" }, "Device"),
              shownNick
                ? h("span", { class: "setup-offer-v setup-offer-row2" }, shownNick)
                : h("span", { class: "setup-offer-v setup-offer-row2" }, offer.name, " ", h("span", { class: "setup-offer-muted" }, "(default)")),
            ),
            !shownNick && offer.nickProblem && h("p", { class: "setup-note" }, offer.nickProblem),
          ),
          h(
            "button",
            {
              type: "button",
              class: "btn btn-outline btn-xs setup-edit",
              title: "Edit name or device nickname",
              "data-key": "setup-edit",
              disabled: s.busy,
              onclick: () => {
                s.mode = "edit";
                s.name = offer.name;
                s.nick = a?.nick || offer.nick || "";
                s.error = "";
                render();
                const field = root.querySelector('[data-key="setup-name"]');
                field?.focus();
                field?.select();
              },
            },
            icon("pencil"),
            "Edit",
          ),
        ),
        blocked && h("p", { class: "setup-note" }, takenOfferNotice([blocked])),
        usable.length > 1 && h(
          "div",
          { class: "setup-others" },
          h("span", { class: "setup-others-label" }, "From other repos"),
          usable.map((o) => {
            const idx = offers.indexOf(o);
            const active = idx === s.offerIndex;
            return h(
              "button",
              {
                type: "button",
                class: "btn btn-xs " + (active ? "btn-outline" : "btn-ghost"),
                "data-key": "setup-pick-" + idx,
                onclick: () => {
                  s.offerIndex = idx;
                  s.name = o.name;
                  s.nick = a?.nick || o.nick || "";
                  render();
                },
              },
              o.name + (o.nick ? " (" + o.nick + ")" : "") + " · " + o.repo,
            );
          }),
        ),
        s.error && h("p", { class: "dialog-error", id: "setup-error", role: "alert" }, s.error),
        h(
          "div",
          { class: "dialog-actions" },
          h("button", { type: "button", class: "btn btn-ghost", "data-key": "setup-cancel", onclick: close }, "Cancel"),
          h("button", { type: "submit", class: "btn btn-primary", "data-key": "setup-accept", disabled: s.busy }, s.busy ? "Saving…" : "Accept"),
        ),
      ),
    );
  }
  const hasCrossRepo = offers.length > 0 && !taken;
  const takenNotice = hasCrossRepo ? takenOfferNotice(offers) : "";
  const nickProblem = hasCrossRepo ? (offers.find((o) => o.nickProblem)?.nickProblem || "") : "";
  return dialog(
    taken ? "Choose a new name" : "Choose your name",
    close,
    h("h2", { class: "dialog-title" }, taken ? "Choose a new name" : "Choose your name"),
    h(
      "p",
      { class: "dialog-line" },
      taken
        ? "The person on " + taken + " chose " + me().name + " first, so messages to that name go to them. Pick another; what was sent to you before still reaches you."
        : takenNotice
          ? takenNotice + " Pick another name below — continuing shares it with this repo's team."
          : hasCrossRepo
            ? "Kept private on your machine. Continue to share your name with this repo's team."
            : "Agents see it, and send to it.",
    ),
    h(
      "form",
      {
        class: "setup-form",
        onsubmit: (e) => {
          e.preventDefault();
          submitName();
        },
      },
      hasCrossRepo && h("label", { class: "setup-label", for: "setup-name" }, "Your name"),
      h(
        "div",
        { class: "setup-row" + (hasCrossRepo ? " setup-row-labeled" : "") },
        h("span", { class: "setup-face" }, faceOf(s.name)),
        h(
          "span",
          { class: "field setup-field" + (s.error ? " field-bad" : "") },
          h("input", {
            id: "setup-name",
            type: "text",
            value: s.name,
            maxlength: 32,
            placeholder: "ada-lovelace",
            autocomplete: "nickname",
            autocapitalize: "none",
            spellcheck: "false",
            "aria-label": "Your name",
            "aria-invalid": s.error ? "true" : null,
            "aria-describedby": s.error ? "setup-error" : null,
            "data-key": "setup-name",
            oninput: (e) => {
              s.name = e.target.value.toLowerCase().replace(/[\s_]+/g, "-");
              s.error = "";
              if (e.target.value !== s.name) e.target.value = s.name;
              root.querySelector(".setup-face")?.replaceChildren(faceOf(s.name));
              root.querySelector(".dialog-error")?.remove();
              root.querySelector(".setup-field")?.classList.remove("field-bad");
              e.target.removeAttribute("aria-invalid");
              e.target.removeAttribute("aria-describedby");
            },
          }),
        ),
      ),
      hasCrossRepo && h(
        "div",
        { class: "setup-nick-block" },
        h("label", { class: "setup-label", for: "setup-nick" }, "Device nickname"),
        h(
          "span",
          { class: "field setup-field" },
          h("input", {
            id: "setup-nick",
            type: "text",
            value: s.nick,
            maxlength: 32,
            placeholder: s.name ? "Default (" + s.name + ")" : "Same as your name",
            autocomplete: "off",
            autocapitalize: "none",
            spellcheck: "false",
            "aria-label": "Device nickname",
            "data-key": "setup-nick",
            oninput: (e) => {
              s.nick = e.target.value.toLowerCase().replace(/[\s_]+/g, "-");
              s.error = "";
              if (e.target.value !== s.nick) e.target.value = s.nick;
              root.querySelector(".dialog-error")?.remove();
            },
          }),
        ),
        h("p", { class: "setup-note" }, nickProblem || "Shown beside this device's ID. Leave blank to use your name."),
      ),
      s.error && h("p", { class: "dialog-error", id: "setup-error", role: "alert" }, s.error),
      h(
        "div",
        { class: "dialog-actions" },
        h("button", { type: "button", class: "btn btn-ghost", "data-key": "setup-cancel", onclick: close }, "Cancel"),
        h("button", { type: "submit", class: "btn btn-primary", "data-key": "setup-continue", disabled: s.busy }, s.busy ? "Saving…" : "Continue"),
      ),
    ),
  );
}

// The face a name would have: a person's on this device, whatever is typed so far.
function faceOf(name) {
  return personFace(name, "avatar dev-fill " + deviceColor((ui.state?.devices || []).find((d) => d.self)));
}

// Everyone a person can start a conversation with: people, then sessions, those at work first.
function reachable() {
  const rows = [];
  for (const p of people()) {
    if (p.name !== myName()) rows.push({ name: p.name, d: p.device, person: p, line: personOnline(p) ? "On " + NAME + " now" : "Person", rank: personOnline(p) ? 0 : 1, at: when(p.lastActive) || 0 });
  }
  for (const d of devices()) {
    for (const s of d.sessions) {
      if (isHidden(s)) continue;
      const st = sessionState(d, s);
      rows.push({ name: s.name, d, s, line: s.doing || s.agent || "", rank: st === "on" ? 0 : st === "idle" ? 1 : st === "dnd" ? 2 : 3, at: when(s.lastActive) || when(s.started) || 0 });
    }
  }
  return rows.sort((a, b) => a.rank - b.rank || b.at - a.at);
}

function matches(r, q) {
  if (!q) return true;
  return [r.name, r.line, r.s?.branch, r.s?.agent, r.d.nick, r.d.owner, r.d.id].some((x) => (x || "").toLowerCase().includes(q));
}

// In the new-message list a row is an option of its combobox, picked with the arrow keys while the
// focus stays in the field; elsewhere it is a plain button. Making a group chat, picked is whether
// the row is ticked, which is what a list that picks several marks as selected.
function personRow(r, cur, onclick, option, picked = null) {
  const several = picked !== null;
  return h(
    "button",
    option
      ? { class: "pick-row" + (cur ? " current" : ""), role: "option", id: "pick-" + r.name, tabindex: -1, "aria-selected": (several ? picked : cur) ? "true" : "false", "data-pick": r.name, onclick }
      : { class: "pick-row", "data-pick": r.name, "data-key": "start-" + r.name, onclick },
    several && h("span", { class: "g-pick" + (picked ? " on" : ""), "aria-hidden": "true" }, picked && icon("check")),
    presence(r.name, r.d),
    h(
      "span",
      { class: "pick-text" },
      h("span", { class: "name-line" }, h("span", { class: "pick-name truncate" }, r.name), r.person && humanTag()),
      h("span", { class: "pick-line truncate" }, r.line),
    ),
    h("span", { class: "pick-where truncate" }, r.person ? "" : r.d.self ? "This device" : r.d.nick || r.d.owner || host(r.d.id)),
  );
}

function newMessageButton() {
  return h("button", { class: "icon-btn", title: "New message", "aria-label": "New message", "data-key": "new-message", onclick: () => openPicker() }, icon("compose"));
}

function newGroupButton() {
  return h("button", { class: "icon-btn", title: "New group chat", "aria-label": "New group chat", "data-key": "new-group", onclick: () => openPicker(true) }, icon("users"));
}

// The new-message list, or with group set, the same list making a group chat (#518).
function openPicker(group) {
  if (!me()) return askForName(() => openPicker(group));
  ui.picker = { q: "", index: 0, from: document.activeElement?.dataset?.key || null, group: group ? newGroupDraft() : null };
  render();
  root.querySelector('[data-key="picker-q"]')?.focus();
}

// A group chat being made: the sessions picked, by address in the order picked, and the name typed
// for it, if any (without its group: prefix); until one is typed the field offers one.
function newGroupDraft() {
  return { picked: [], name: "", named: false, err: "" };
}

function pickerDialog() {
  const p = ui.picker;
  const g = p.group;
  const close = () => {
    ui.picker = null;
    render();
    if (p.from) root.querySelector(`[data-key="${CSS.escape(p.from)}"]`)?.focus();
  };
  const q = p.q.trim().toLowerCase();
  // A group's members are sessions: a group is a tag, and a person has none.
  const rows = reachable().filter((r) => (!g || r.s) && matches(r, q));
  p.index = Math.min(p.index, Math.max(rows.length - 1, 0));
  // A tick leaves the focus in the field, on the row ticked, so the keys go on from there.
  const toggle = (r) => {
    const i = g.picked.indexOf(r.s.addr);
    i < 0 ? g.picked.push(r.s.addr) : g.picked.splice(i, 1);
    g.err = "";
    p.index = Math.max(0, rows.indexOf(r));
    render();
    root.querySelector('[data-key="picker-q"]')?.focus();
  };
  const pick = (r) => (g ? toggle(r) : startChat(r.name));
  const mode = (group) => {
    p.group = group ? newGroupDraft() : null;
    p.q = "";
    p.index = 0;
    render();
    root.querySelector('[data-key="picker-q"]')?.focus();
  };
  return dialog(
    g ? "New group chat" : "New message",
    close,
    h(
      "label",
      { class: "pick-search" },
      icon("search"),
      h("input", {
        type: "text",
        value: p.q,
        placeholder: g ? "Add sessions to the group" : "Message a session or a person",
        autocomplete: "off",
        spellcheck: "false",
        role: "combobox",
        "aria-expanded": "true",
        "aria-controls": "pick-list",
        "aria-autocomplete": "list",
        "aria-activedescendant": rows[p.index] ? "pick-" + rows[p.index].name : null,
        "aria-label": g ? "Sessions to add" : "Who to message",
        "data-key": "picker-q",
        oninput: (e) => {
          p.q = e.target.value;
          p.index = 0;
          render();
        },
        onkeydown: (e) => {
          if (e.key === "ArrowDown" || e.key === "ArrowUp") {
            e.preventDefault();
            p.index = Math.max(0, Math.min(rows.length - 1, p.index + (e.key === "ArrowDown" ? 1 : -1)));
            render();
            root.querySelector(".pick-row.current")?.scrollIntoView({ block: "nearest" });
          } else if (e.key === "Enter" && g && (e.metaKey || e.ctrlKey)) {
            e.preventDefault();
            makeGroup();
          } else if (e.key === "Enter" && rows[p.index]) {
            e.preventDefault();
            pick(rows[p.index]);
          }
        },
      }),
    ),
    h(
      "div",
      { class: "pick-list", id: "pick-list", tabindex: -1, role: rows.length ? "listbox" : null, "aria-multiselectable": rows.length && g ? "true" : null, "aria-label": rows.length ? (g ? "Sessions" : "Sessions and people") : null },
      rows.length
        ? rows.map((r, i) => personRow(r, i === p.index, () => pick(r), true, g ? g.picked.includes(r.s.addr) : null))
        : h("p", { class: "pick-none", "data-announce": "" }, g && !q ? "No sessions are running." : "Nobody matches."),
    ),
    g ? groupFoot(g, () => mode(false), makeGroup) : h("div", { class: "pick-foot" }, h("button", { class: "btn btn-ghost btn-sm", "data-key": "pick-group", onclick: () => mode(true) }, icon("users"), "New group chat")),
  );

  function makeGroup() {
    const sessions = g.picked.map(sessionByAddr).filter(Boolean);
    if (!sessions.length) {
      g.err = "Pick at least one session.";
      return render();
    }
    if (!g.name.trim()) return startGroup(sessions); // the group that is just them, or a new one
    const tag = "group:" + g.name.trim();
    if (!validTag(tag) || !isGroupTag(tag)) {
      g.err = "A name is lowercase letters, digits and . _ : -, up to " + (40 - 6) + ".";
      return render();
    }
    startGroup(sessions, tag);
  }
}

// Under the list while it makes a group: the group's name, and the way back to one person.
function groupFoot(g, back, make) {
  const sessions = g.picked.map(sessionByAddr).filter(Boolean);
  const same = sessions.length ? groupOf(sessions) : null;
  const offered = same ? same.slice(6) : sessions.length ? newGroupTag(sessions.map((s) => s.name)).slice(6) : "";
  const tag = "group:" + (g.name.trim() || offered);
  const note = g.err || (tag === same ? tag + " has just these sessions; it opens." : g.named && liveGroups().includes(tag) ? tag + " exists; these sessions join it." : "");
  return h(
    "form",
    {
      class: "pick-group",
      onsubmit: (e) => {
        e.preventDefault();
        make();
      },
    },
    h(
      "label",
      { class: "field pick-group-name" + (g.err ? " field-bad" : "") },
      h("span", { class: "pick-group-prefix", "aria-hidden": "true" }, "group:"),
      h("input", {
        type: "text",
        value: g.named ? g.name : offered,
        placeholder: offered || "name",
        maxlength: 34,
        autocomplete: "off",
        autocapitalize: "none",
        spellcheck: "false",
        "aria-label": "Group name, after group:",
        "aria-invalid": g.err ? "true" : null,
        "data-key": "group-name",
        oninput: (e) => {
          const el = e.target;
          g.name = el.value.toLowerCase().replace(/\s+/g, "-");
          g.named = true;
          g.err = "";
          if (el.value !== g.name) {
            const at = el.selectionStart;
            el.value = g.name;
            el.setSelectionRange(at, at);
          }
          render();
        },
      }),
    ),
    note && h("p", { class: g.err ? "dialog-error" : "pick-group-note", "data-announce": "" }, note),
    h(
      "div",
      { class: "pick-group-actions" },
      h("button", { type: "button", class: "btn btn-ghost btn-sm", "data-key": "pick-back", onclick: back }, "Message one instead"),
      h("button", { type: "submit", class: "btn btn-primary btn-sm", "data-key": "pick-make", disabled: !sessions.length }, sessions.length ? "Start group chat with " + plural(sessions.length, "session") : "Start group chat"),
    ),
  );
}

// With only your conversations shown and none of them yet: who you could talk to.
function startPane() {
  const all = reachable();
  const rows = all.slice(0, 8);
  return h(
    "section",
    { class: "chat-pane start-pane", "aria-label": "Start a conversation" },
    starRow(),
    h(
      "div",
      { class: "start-inner" },
      h("h2", { class: "start-title" }, rows.length ? "Who do you want to talk to?" : "Nobody is here yet."),
      rows.length > 0 && h("div", { class: "pick-list start-list" }, rows.map((r) => personRow(r, false, () => startChat(r.name)))),
      (all.length > rows.length || rows.length > 1) &&
        h(
          "div",
          { class: "start-more" },
          all.length > rows.length && h("button", { class: "btn btn-ghost", "data-key": "start-more", onclick: () => openPicker() }, "See everyone"),
          rows.length > 1 && h("button", { class: "btn btn-ghost", "data-key": "start-group", onclick: () => openPicker(true) }, icon("users"), "New group chat"),
        ),
    ),
  );
}
