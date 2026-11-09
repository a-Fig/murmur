// Counting Murmur's use, and the private page that shows Tyler the counts, with the same numbers as
// markdown and JSON for an agent he gives a key to (#150).
//
// Since 2026-11-08 (#199) the only thing Murmur sends us is a public repo's address, once per repo
// (internal/publicrepo), kept in D1's repos table (migrations/). The daily check-ins of earlier
// versions (#148) are no longer stored; the rows they left in the checkins table are kept as
// history, shown at /stats/archive. What the stats page shows of those repos, the census reads
// from what they show anyone (src/repos.js, #259).

import statsHtml from './stats.html';
import archiveHtml from './stats-archive.html';
import { archiveNumbers, archiveReport } from './archive.js';
import { repoNumbers, repoReport } from './repos.js';
import { prefersMarkdown } from './accept.js';

const RELEASES = 'https://api.github.com/repos/a-Fig/murmur/releases/latest';
const LATEST_KEY = 'https://murmur.afig.dev/_cache/latest-release';
const VERSION = /^\d{1,4}\.\d{1,4}\.\d{1,4}(-[0-9A-Za-z.]{1,20})?$/;
const COOKIE = 'murmur_stats';

// POST /v1/checkin: the daily check-in that daemons from before #199 send. None is stored any more
// (Tyler, 2026-11-08), and the body isn't even read. It still answers with the latest release, which
// those daemons show in `mur version`, so they hear of the release that stops them sending it.
export async function checkIn(request, env, ctx) {
  if (request.method !== 'POST') {
    return new Response('Only POST.\n', { status: 405, headers: { Allow: 'POST' } });
  }
  return Response.json({ latest: await latestRelease(ctx) }, { headers: { 'Cache-Control': 'no-store' } });
}

// github.com/<owner>/<name>, as GitHub allows them to be spelled.
const REPO = /^github\.com\/([A-Za-z0-9][A-Za-z0-9-]{0,38})\/([A-Za-z0-9._-]{1,100})$/;

// POST /v1/repo: a public repo that uses Murmur, sent once per repo by internal/publicrepo. Its
// address is the whole request, and all that is kept, with the day it first came. A repo GitHub
// doesn't show to someone signed out is refused, so nothing private or made up is stored.
export async function reportRepo(request, env) {
  if (request.method !== 'POST') {
    return new Response('Only POST.\n', { status: 405, headers: { Allow: 'POST' } });
  }
  const text = await readUpTo(request, 512);
  if (text === null) return new Response('Too long.\n', { status: 413 });
  let r;
  try {
    r = JSON.parse(text);
  } catch {
    return new Response('Not a repo.\n', { status: 400 });
  }
  const m = typeof r?.repo === 'string' ? REPO.exec(r.repo) : null;
  if (!m || m[2] === '.' || m[2] === '..') return new Response('Not a repo.\n', { status: 400 });
  // Stored only once GitHub confirms it public. When GitHub can't say, nothing is stored and the
  // daemon, told to try again, sends it again at its next look.
  const shown = await isPublic(m[1], m[2], env);
  if (shown === false) return new Response('Not a public repo on GitHub.\n', { status: 422 });
  if (shown === null) return new Response("GitHub couldn't confirm it is public; try again later.\n", { status: 503 });
  const day = new Date().toISOString().slice(0, 10);
  // Stored before the answer, so a report that isn't kept fails and the daemon tries again.
  try {
    await env.DB.prepare(
      `INSERT INTO repos (repo, name, first) VALUES (?1, ?2, ?3)
       ON CONFLICT (repo) DO UPDATE SET name = excluded.name`,
    ).bind(r.repo.toLowerCase(), r.repo, day).run();
  } catch (err) {
    console.log(`storing a repo: ${err}`);
    return new Response('Not stored; try again later.\n', { status: 503 });
  }
  return Response.json({ ok: true }, { headers: { 'Cache-Control': 'no-store' } });
}

// Whether GitHub shows owner/name to someone signed out: true, false, or null when GitHub can't say.
// The API answers first. A Worker shares Cloudflare's egress addresses, and others have usually
// spent the API's rate limit for them (#253), so when the API can't say, git's own endpoint on github.com does.
async function isPublic(owner, name, env) {
  return (await publicOnApi(owner, name, env)) ?? (await publicByGit(owner, name));
}

// The API's answer, or null when it can't give one (a rate limit, an outage). GITHUB_TOKEN, if that
// secret is set, only raises the rate limit: a private repo it can see answers private: true, and
// is refused.
async function publicOnApi(owner, name, env) {
  const headers = { 'User-Agent': 'murmur-site', Accept: 'application/vnd.github+json' };
  if (env.GITHUB_TOKEN) headers.Authorization = `Bearer ${env.GITHUB_TOKEN}`;
  try {
    const res = await fetch(`https://api.github.com/repos/${owner}/${name}`, { headers, signal: AbortSignal.timeout(5000) });
    if (res.status === 404) return false;
    if (!res.ok) throw new Error(`GitHub answered ${res.status}`);
    return (await res.json()).private === false;
  } catch (err) {
    console.log(`is ${owner}/${name} public, by the API: ${err}`);
    return null;
  }
}

// What git itself is told, signed out, by github.com/owner/name/info/refs: 200 for a public repo, 401
// for a private or missing one. The repo's own page would do, but the same address is also a topic,
// a product page and more (github.com/topics/javascript answers 200), so it can't say a repo is
// there. A renamed repo redirects to its new name and is followed; the address stored is still the
// one reported, since it leads to a public repo. Anything else is no answer.
async function publicByGit(owner, name) {
  try {
    // Protocol 2, so a big repo answers with its capabilities and not every ref.
    const res = await fetch(`https://github.com/${owner}/${name}/info/refs?service=git-upload-pack`, {
      headers: { 'User-Agent': 'git/murmur-site', 'Git-Protocol': 'version=2' }, signal: AbortSignal.timeout(5000),
    });
    res.body?.cancel();
    if (res.status === 401 || res.status === 404) return false;
    if (res.status !== 200) throw new Error(`GitHub answered ${res.status}`);
    if (res.headers.get('Content-Type') !== 'application/x-git-upload-pack-advertisement') throw new Error('not git');
    return true;
  } catch (err) {
    console.log(`is ${owner}/${name} public, by git: ${err}`);
    return null;
  }
}

// The body as text, or null if it is longer than max bytes, without reading past max.
async function readUpTo(request, max) {
  if (Number(request.headers.get('Content-Length')) > max) return null;
  if (!request.body) return '';
  const reader = request.body.getReader(), chunks = [];
  let size = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    size += value.byteLength;
    if (size > max) {
      await reader.cancel();
      return null;
    }
    chunks.push(value);
  }
  const all = new Uint8Array(size);
  let at = 0;
  for (const c of chunks) {
    all.set(c, at);
    at += c.byteLength;
  }
  return new TextDecoder().decode(all);
}

// The latest release's version, without the v, from GitHub at most once an hour per data center;
// "" if GitHub can't say.
async function latestRelease(ctx) {
  const cache = caches.default;
  const hit = await cache.match(LATEST_KEY);
  if (hit) return hit.text();
  try {
    const res = await fetch(RELEASES, {
      headers: { 'User-Agent': 'murmur-site', Accept: 'application/vnd.github+json' },
      signal: AbortSignal.timeout(5000),
    });
    if (!res.ok) throw new Error(`GitHub answered ${res.status}`);
    const tag = String((await res.json()).tag_name || '').replace(/^v/, '');
    if (!VERSION.test(tag)) throw new Error(`no version in ${JSON.stringify(tag)}`);
    ctx.waitUntil(cache.put(LATEST_KEY, new Response(tag, { headers: { 'Cache-Control': 'max-age=3600' } })));
    return tag;
  } catch (err) {
    console.log(`latest release: ${err}`);
    return '';
  }
}

// The stats, for whoever holds a key: Tyler in a browser, and agents he gives one to (#150). Two
// views, each a page, the same numbers as markdown, and as JSON, which the page draws:
//
// - /stats, /stats/report.md, /stats/data?days=N: the public repos that use Murmur, and what the
//   census reads from them (#259, src/repos.js).
// - /stats/archive, /stats/archive/report.md, /stats/archive/data?days=N: the check-ins of earlier
//   versions, which are no longer stored (src/archive.js).
//
// A page's own address answers with the markdown report for anything but a browser, or a browser
// whose Accept asks for markdown.
//
// The keys are the STATS_KEY secret: one or more, separated by spaces or commas, so each reader has
// its own and one can be revoked by putting the secret again without it. A key is taken from a
// `?key=` in the address, an `Authorization: Bearer` header, or the cookie a browser is given: a
// browser that opens /stats?key=... is sent to /stats with the key in a cookie, so it leaves the
// address bar at once. Anything else is answered in place, since a fetch tool may follow a redirect
// without keeping the cookie. Without a key, as without the secret, none of it exists.
const VIEWS = {
  '/stats': { html: statsHtml, numbers: repoNumbers, report: repoReport, days: 90 },
  '/stats/archive': { html: archiveHtml, numbers: archiveNumbers, report: archiveReport, days: 14 },
};

export async function stats(request, env) {
  const url = new URL(request.url);
  const notFound = () => new Response('Not found.\n', { status: 404, headers: { 'X-Robots-Tag': 'noindex' } });
  const keys = String(env.STATS_KEY || '').split(/[\s,]+/).filter(Boolean);
  if (keys.length === 0) return notFound();
  const given = url.searchParams.get('key');
  const bearer = /^Bearer\s+(\S+)\s*$/i.exec(request.headers.get('Authorization') || '')?.[1];
  const cookie = (request.headers.get('Cookie') || '').split(';').map(s => s.trim()).find(s => s.startsWith(COOKIE + '='));
  const inCookie = cookie ? safeDecode(cookie.slice(COOKIE.length + 1)) : null;
  const key = given ?? bearer ?? inCookie;
  if (key === null || !(await anyOf(key, keys))) return notFound();

  const path = url.pathname.replace(/\/$/, '');
  const base = path.replace(/\/(report\.md|data)$/, '');
  const view = VIEWS[base];
  if (!view) return notFound();
  const page = path === base;
  if (page && given !== null && request.headers.get('Sec-Fetch-Mode') === 'navigate') {
    return new Response(null, {
      status: 303,
      headers: {
        Location: base,
        'Set-Cookie': `${COOKIE}=${encodeURIComponent(key)}; Path=/stats; Max-Age=31536000; HttpOnly; Secure; SameSite=Lax`,
        'Cache-Control': 'no-store',
      },
    });
  }
  const headers = { 'Cache-Control': 'no-store', 'X-Robots-Tag': 'noindex' };
  const days = Math.min(730, Math.max(14, parseInt(url.searchParams.get('days'), 10) || view.days));
  const fromCookie = given === null && bearer === undefined;
  if (page && fromCookie && !prefersMarkdown(request.headers.get('Accept'))) {
    return new Response(view.html, {
      headers: { ...headers, 'Content-Type': 'text/html; charset=utf-8', Link: `<${base}/report.md>; rel="alternate"; type="text/markdown"` },
    });
  }
  if (page || path === base + '/report.md') {
    const text = view.report(await view.numbers(env.DB, days), url.origin);
    return new Response(text, { headers: { ...headers, 'Content-Type': 'text/markdown; charset=utf-8' } });
  }
  return Response.json(await view.numbers(env.DB, days), { headers });
}

function safeDecode(s) {
  try {
    return decodeURIComponent(s);
  } catch {
    return null;
  }
}

// Whether key is one of keys, comparing with every one so the time taken doesn't say which.
async function anyOf(key, keys) {
  let found = false;
  for (const k of keys) found = (await same(key, k)) || found;
  return found;
}

async function same(a, b) {
  const enc = new TextEncoder();
  const [x, y] = [enc.encode(a), enc.encode(b)];
  return x.byteLength === y.byteLength && crypto.subtle.timingSafeEqual(x, y);
}

