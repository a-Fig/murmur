// The Worker in front of the static site (wrangler.jsonc's run_worker_first lists the paths it takes).
// It does what the assets can't:
// - serves the agent pages from docs/agents/, pulled from the repo (#140): join.md at /join and
//   /setup, use.md at /use, and at /agents/<name>.md one agent's file from docs/agents/harnesses/
//   with steps.md, the steps every agent shares, after it, so an agent fetches one page (#934);
// - answers `/` with the setup page instead of the HTML when the request prefers markdown, and
//   /agents/, the page for people, with join.md;
// - slices the videos for Range requests;
// - takes the address of a public repo that uses Murmur, the only thing Murmur sends us, and serves
//   the page that counts use, with the same numbers as markdown and JSON for an agent (src/usage.js);
// - takes the daily census of those repos (src/repos.js);
// - answers a path no asset has with a markdown 404 when the request prefers markdown
//   (src/notfound.js). The assets hand such a path to the Worker whatever run_worker_first says.

// Bundled at deploy (the Text rule in wrangler.jsonc), as the copy of last resort when GitHub can't
// be reached and this data center has no cached copy.
import joinMd from '../../docs/agents/join.md';
import useMd from '../../docs/agents/use.md';
import stepsMd from '../../docs/agents/steps.md';
import claudeCodeMd from '../../docs/agents/harnesses/claude-code.md';
import codexMd from '../../docs/agents/harnesses/codex.md';
import opencodeMd from '../../docs/agents/harnesses/opencode.md';
import piMd from '../../docs/agents/harnesses/pi.md';
import museCodeMd from '../../docs/agents/harnesses/muse-code.md';
import grokMd from '../../docs/agents/harnesses/grok.md';
import antigravityMd from '../../docs/agents/harnesses/antigravity.md';
import { checkIn, reportRepo, stats } from './usage.js';
import { census } from './repos.js';
import { prefersMarkdown } from './accept.js';
import { notFound } from './notfound.js';

const SITE = 'https://murmur.afig.dev';
// Each file the agent pages are made of, by its path under docs/agents/ without .md, with the copy
// bundled at deploy.
export const PAGES = {
  join: joinMd, use: useMd, steps: stepsMd,
  'harnesses/claude-code': claudeCodeMd, 'harnesses/codex': codexMd, 'harnesses/opencode': opencodeMd,
  'harnesses/pi': piMd, 'harnesses/muse-code': museCodeMd, 'harnesses/grok': grokMd,
  'harnesses/antigravity': antigravityMd,
};
// The agents with a page, by the name in /agents/<name>.md.
export const HARNESSES = Object.keys(PAGES).filter(p => p.startsWith('harnesses/')).map(p => p.slice('harnesses/'.length));
// How long a page fetched from GitHub is served before it is fetched again. GitHub's raw host caches
// for 5 minutes too, so a merged change shows up here within about 10.
const FRESH_SECONDS = 300;
// How long the last good copy is kept, to serve while GitHub is unreachable.
const KEEP_SECONDS = 30 * 24 * 3600;
const MARKDOWN = 'text/markdown; charset=utf-8';

// Where the pages are read from: main, or for a preview the branch its DOCS_REF variable names
// (`npx wrangler versions upload --var DOCS_REF:<branch>`), so a change to the pages can be tried
// end to end, by an agent following their links, before it merges.
const rawBase = env => `https://raw.githubusercontent.com/a-Fig/murmur/${env?.DOCS_REF ? 'refs/heads/' + env.DOCS_REF : 'main'}/`;

export default {
  async fetch(request, env, ctx) {
    const { pathname } = new URL(request.url);
    if (pathname === '/v1/repo') return reportRepo(request, env);
    if (pathname === '/v1/checkin') return checkIn(request, env, ctx);
    if (pathname === '/v1/census') return census(request, env);
    if (pathname === '/stats' || pathname.startsWith('/stats/')) return stats(request, env);
    const page = /^\/(setup|join|use)(?:\.md)?\/?$/.exec(pathname)?.[1];
    if (page) return agentPage(request, env, ctx, page === 'use' ? ['use'] : ['join'], false);
    const harness = /^\/agents\/([a-z0-9-]+)\.md$/.exec(pathname)?.[1];
    if (harness && HARNESSES.includes(harness)) return agentPage(request, env, ctx, ['harnesses/' + harness, 'steps'], false);
    const markdown = prefersMarkdown(request.headers.get('Accept'));
    if (pathname === '/agents/' && markdown) return agentPage(request, env, ctx, ['join'], false, true);
    if (pathname === '/' && markdown) return agentPage(request, env, ctx, ['join'], true, true);
    if (pathname === '/') return withHeaders(await env.ASSETS.fetch(request), { Vary: 'Accept', Link: `<${SITE}/setup>; rel="alternate"; type="text/markdown"` });
    if (pathname === '/agents/') return withHeaders(await env.ASSETS.fetch(request), { Vary: 'Accept', Link: `<${SITE}/join>; rel="alternate"; type="text/markdown"` });
    if (pathname.endsWith('.mp4')) return video(request, env);
    const res = await env.ASSETS.fetch(request); // a path no asset has
    return res.status === 404 ? notFound(request, res) : res;
  },
};

// The assets' response with these headers added.
function withHeaders(res, extra) {
  const headers = new Headers(res.headers);
  for (const [k, v] of Object.entries(extra)) headers.append(k, v);
  return new Response(res.body, { status: res.status, headers });
}

// One file of an agent page: from the cache if it was fetched from GitHub in the last FRESH_SECONDS;
// else from GitHub; else, if GitHub is unreachable or answers with an error, the last good copy this
// data center kept; else the copy bundled at deploy. source says which.
async function agentFile(env, ctx, page) {
  const raw = `${rawBase(env)}docs/agents/${page}.md`;
  const ref = env?.DOCS_REF ? `${env.DOCS_REF}/` : '';
  const freshKey = `${SITE}/_cache/fresh/${ref}${page}.md`, keptKey = `${SITE}/_cache/kept/${ref}${page}.md`;
  const cache = caches.default;
  const fresh = await cache.match(freshKey);
  if (fresh) return { raw, text: await fresh.text(), source: 'cache' };
  try {
    const res = await fetch(raw, { signal: AbortSignal.timeout(5000), cache: 'no-store' });
    if (!res.ok) throw new Error(`GitHub answered ${res.status}`);
    const text = await res.text();
    const store = (key, seconds) => cache.put(key, new Response(text, { headers: { 'Content-Type': MARKDOWN, 'Cache-Control': `max-age=${seconds}` } }));
    ctx.waitUntil(Promise.all([store(freshKey, FRESH_SECONDS), store(keptKey, KEEP_SECONDS)]));
    return { raw, text, source: 'github' };
  } catch (err) {
    console.log(`fetching ${raw}: ${err}`);
    const kept = await cache.match(keptKey);
    return { raw, text: kept ? await kept.text() : PAGES[page], source: kept ? 'kept-copy' : 'deployed-copy' };
  }
}

// An agent page: the files named in parts, one after another (an agent's own file, then the steps
// every agent shares). X-Murmur-Source says where the least fresh of them came from, and a line at
// the top says so when any of them isn't GitHub's current copy.
async function agentPage(request, env, ctx, parts, atRoot, vary = atRoot) {
  if (request.method !== 'GET' && request.method !== 'HEAD') {
    return new Response('Only GET and HEAD.\n', { status: 405, headers: { Allow: 'GET, HEAD' } });
  }
  const files = await Promise.all(parts.map(p => agentFile(env, ctx, p)));
  let text = files.map(f => f.text.trimEnd()).join('\n\n') + '\n';
  const order = ['cache', 'github', 'kept-copy', 'deployed-copy'];
  const source = files.map(f => f.source).reduce((a, b) => order.indexOf(b) > order.indexOf(a) ? b : a);
  if (source === 'kept-copy' || source === 'deployed-copy') {
    text = `> GitHub couldn't be reached just now, so this is ${source === 'kept-copy' ? 'the last copy fetched from it' : "the copy from this site's last deploy"}. ` +
      `It may be out of date: the current text is ${files.map(f => f.raw).join(' and ')}.\n\n` + text;
  }

  if (atRoot) {
    text = `> This is Murmur's website, murmur.afig.dev, as markdown for a coding agent: below is its setup page, also at ${SITE}/setup. ` +
      `Follow it, and ask your user before you install or run anything. How to use the tools once joined: ${SITE}/use. An index: ${SITE}/llms.txt.\n\n` + text;
  }
  // A preview's links to the other agent pages go to the preview and its branch, so an agent
  // following them stays on it. Other addresses, such as where mur reports a public repo, stay.
  if (env?.DOCS_REF) {
    text = text.replace(/https:\/\/murmur\.afig\.dev\/(?=(?:setup|join|use)(?![\w-])|agents\/)/g, new URL(request.url).origin + '/')
      .replaceAll('https://raw.githubusercontent.com/a-Fig/murmur/main/', rawBase(env));
  }
  const headers = {
    'Content-Type': MARKDOWN,
    'Cache-Control': `public, max-age=${source === 'github' || source === 'cache' ? FRESH_SECONDS : 60}`,
    'X-Murmur-Source': source,
    'Link': `<${files[0].raw}>; rel="canonical"`,
  };
  if (vary) headers['Vary'] = 'Accept';
  return new Response(request.method === 'HEAD' ? null : text, { headers });
}

// Static assets answer a Range request with the whole file and a 200, and Safari won't play a video
// served that way. So the videos come through here. They are under 1 MB, small enough to slice in
// memory.
async function video(request, env) {
  const res = await env.ASSETS.fetch(request);
  if (!res.ok) return res;
  const headers = new Headers(res.headers);
  headers.set('Accept-Ranges', 'bytes');
  const m = /^bytes=(\d*)-(\d*)$/.exec(request.headers.get('Range') || '');
  if (!m || (!m[1] && !m[2])) return new Response(res.body, { status: res.status, headers });

  const body = await res.arrayBuffer(), size = body.byteLength;
  const start = m[1] ? +m[1] : Math.max(0, size - +m[2]);
  const end = m[1] && m[2] ? Math.min(+m[2], size - 1) : size - 1;
  if (start > end) return new Response(null, { status: 416, headers: { 'Content-Range': `bytes */${size}` } });
  headers.set('Content-Range', `bytes ${start}-${end}/${size}`);
  headers.set('Content-Length', String(end - start + 1));
  return new Response(request.method === 'HEAD' ? null : body.slice(start, end + 1), { status: 206, headers });
}
