// Asking for a star on GitHub (#974), as T3 Code asks for one: once, after the person has sent a few
// messages, so never on a first visit. T3 Code waits for five sends and shows a small card with
// Star and Dismiss; this does the same, but links to GitHub, since the page has no GitHub login to
// star with. The ask is kept with the daemon's page preferences, so the app and the browser share
// it, and showing it counts: whatever the person does, it doesn't come back. No agent sees it.

import { icon } from "./icons.js";
import { h } from "./dom.js";
import { ui, fromName, myName } from "./state.js";
import { starAsked, setStarAsked } from "./preferences.js";
import { render } from "./render.js";

export { askForStar, starCard };

const AFTER_SENDS = 5;
const WAIT_MS = 1500; // after the send it follows, so it doesn't land on the message going out
const REPO = "https://github.com/a-Fig/murmur";

// Called when one of the person's messages has gone: the one that just went may not be among the
// messages yet, so it counts as one more.
function askForStar() {
  if (starAsked || ui.starAsk) return;
  const sent = ui.messages.filter((m) => fromName(m) === myName()).length + 1;
  if (sent < AFTER_SENDS) return;
  setStarAsked();
  setTimeout(() => {
    ui.starAsk = true;
    render();
  }, WAIT_MS);
}

function starCard() {
  if (!ui.starAsk) return null;
  const close = () => {
    ui.starAsk = false;
    render();
  };
  return h(
    "aside",
    { class: "star-ask", "aria-label": "Star Murmur on GitHub", "data-key": "star-ask" },
    h("span", { class: "star-ask-ico", "aria-hidden": "true" }, icon("github")),
    h(
      "div",
      { class: "star-ask-text" },
      h("p", { class: "star-ask-title", "data-announce": "" }, "Enjoying Murmur?"),
      h("p", null, "A star on GitHub helps other teams find it."),
      h(
        "div",
        { class: "star-ask-actions" },
        h("a", { class: "btn btn-primary btn-xs", href: REPO, target: "_blank", rel: "noopener noreferrer", "data-key": "star-ask-go", onclick: close }, "Star on GitHub"),
        h("button", { class: "btn btn-ghost btn-xs", "data-key": "star-ask-no", onclick: close }, "No thanks"),
      ),
    ),
  );
}
