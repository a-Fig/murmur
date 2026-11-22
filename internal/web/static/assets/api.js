// Talking to the daemon: the token, a one-time key traded for one, and the fetch that sends it.

import { TOKEN_KEY, VIEWS, ui } from "./state.js";
import { poll, inApp } from "./sync.js";

export { AuthError, takeToken, takeKey, appSaysFront, api };

class AuthError extends Error {
  constructor(token) {
    super("unauthorized");
    this.token = token;
  }
}

// The token arrives once, in the URL fragment, which never reaches a server or a Referer. It is
// kept for this tab only, and taken out of the address bar at once. The fragment may also name a
// view: #messages&token=...
function takeToken() {
  const parts = location.hash.slice(1).split("&");
  const tok = parts.find((p) => p.startsWith("token="));
  if (tok) {
    let value = tok.slice(6);
    try {
      value = decodeURIComponent(value);
    } catch {}
    sessionStorage.setItem(TOKEN_KEY, value);
    const view = parts.find((p) => VIEWS.includes(p)) || "messages";
    history.replaceState(null, "", location.pathname + location.search + "#" + view);
  }
  return sessionStorage.getItem(TOKEN_KEY);
}

// A page opened from another repo's page (#348) arrives with a one-time key, not the token, and
// trades it with its own daemon for the token. The key is spent either way, so it leaves the
// address bar first. If the trade fails, the tab keeps whatever token it already had here.
async function takeKey() {
  const parts = location.hash.slice(1).split("&");
  const k = parts.find((p) => p.startsWith("key="));
  if (!k) return;
  const view = parts.find((p) => VIEWS.includes(p)) || "messages";
  history.replaceState(null, "", location.pathname + location.search + "#" + view);
  try {
    const res = await fetch("/api/key", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ key: k.slice(4) }), cache: "no-store" });
    const got = res.ok ? await res.json() : null;
    if (got?.token) sessionStorage.setItem(TOKEN_KEY, got.token);
  } catch {}
}

// Whether nobody is looking at the page. A browser says so when its tab is in the background or
// its window minimized. The desktop app's window under another window stays visible to the page
// (#638), so the window itself says whether it is in front (appSaysFront), and while it isn't, the
// page counts as hidden: an app left open behind the editor doesn't keep the person active.
function pageHidden() {
  return document.hidden || ui.appFront === false;
}

// The desktop app's window saying whether it is in front, each time that changes. Back in front, the
// page polls at once, as a tab shown again does, so the person is active again without a wait.
function appSaysFront(front) {
  const back = front && ui.appFront === false;
  ui.appFront = !!front;
  if (back && ui.token && ui.conn !== "denied") poll();
}

// init.raw asks for the response itself, for what isn't JSON (a file). A body that isn't text is a
// form, which the browser gives its own content type.
async function api(path, init = {}) {
  const token = ui.token;
  const headers = { Authorization: "Bearer " + token };
  if (typeof init.body === "string") headers["Content-Type"] = "application/json";
  // A tab nobody can see still polls, but mustn't keep the daemon running for good. The desktop
  // app's window is someone waiting for messages, minimized or not: it keeps the daemon running,
  // though hidden it doesn't count as someone looking.
  if (pageHidden()) headers["X-Page-Hidden"] = "1";
  if (inApp()) headers["X-Page-App"] = "1";
  const res = await fetch(path, { ...init, headers, cache: "no-store" });
  if (res.status === 401) throw new AuthError(token);
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw Object.assign(new Error(body.error || res.status + " " + res.statusText), { status: res.status });
  }
  if (init.raw) return res;
  return res.status === 204 ? null : res.json();
}
