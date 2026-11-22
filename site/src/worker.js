// The Worker in front of the static site (wrangler.jsonc's run_worker_first lists the paths it takes).
// It does what the assets can't:
// - serves the agent pages, docs/agents/*.md, pulled from the repo (#140), and each agent's page,
//   docs/agents/harnesses/<name>.md, at /agents/<name>.md, and at /agents/<name>/ to a request that
//   prefers markdown, where a browser gets the page for people (#934);
// - answers `/` with the setup page instead of the HTML when the request prefers markdown;
// - slices the videos for Range requests;
// - takes the address of a public repo that uses Murmur, the only thing Murmur sends us, and serves
//   the page that counts use, with the same numbers as markdown and JSON for an agent (src/usage.js);
// - takes the daily census of those repos (src/repos.js);
// - answers a path no asset has with a markdown 404 when the request prefers markdown
//   (src/notfound.js). The assets hand such a path to the Worker whatever run_worker_first says.

// Bundled at deploy (the Text rule in wrangler.jsonc), as the copy of last resort when GitHub can't
// be reached and this data center has no cached copy.
import setupMd from '../../docs/agents/setup.md';
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
import t3CodeMd from '../../docs/agents/harnesses/t3-code.md';
import { checkIn, reportRepo, stats } from './usage.js';
import { census } from './repos.js';
import { prefersMarkdown } from './accept.js';
import { notFound } from './notfound.js';

const SITE = 'https://murmur.afig.dev';
// Each agent page by its path under docs/agents/, without .md, with the copy bundled at deploy.
export const PAGES = {
  setup: setupMd, join: joinMd, use: useMd, steps: stepsMd,
  'harnesses/claude-code': claudeCodeMd, 'harnesses/codex': codexMd, 'harnesses/opencode': opencodeMd,
  'harnesses/pi': piMd, 'harnesses/muse-code': museCodeMd, 'harnesses/grok': grokMd,
  'harnesses/antigravity': antigravityMd, 'harnesses/t3-code': t3CodeMd,
};
// The agents with a page, by the name in /agents/<name>/.
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
    const page = /^\/(setup|join|use|steps)(?:\.md)?\/?$/.exec(pathname)?.[1];
    if (page) return agentPage(request, env, ctx, page, false);
    const harness = /^\/agents\/([a-z0-9-]+)(\.md|\/?)$/.exec(pathname);
    if (harness && HARNESSES.includes(harness[1])) {
      const [, name, md] = harness;
      if (md === '.md') return agentPage(request, env, ctx, 'harnesses/' + name, false);
      if (prefersMarkdown(request.headers.get('Accept'))) {
        const res = await agentPage(request, env, ctx, 'harnesses/' + name, false);
        res.headers.set('Vary', 'Accept');
        return res;
      }
      return withHeaders(await env.ASSETS.fetch(request), { Vary: 'Accept', Link: `<${SITE}/agents/${name}.md>; rel="alternate"; type="text/markdown"` });
    }
    if (pathname === '/' && prefersMarkdown(request.headers.get('Accept'))) return agentPage(request, env, ctx, 'setup', true);
    if (pathname === '/') {
      return withHeaders(await env.ASSETS.fetch(request), { Vary: 'Accept', Link: `<${SITE}/setup>; rel="alternate"; type="text/markdown"` });
    }
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

// The page from the cache if it was fetched from GitHub in the last FRESH_SECONDS; else from GitHub;
// else, if GitHub is unreachable or answers with an error, the last good copy this data center kept;
// else the copy bundled at deploy, with a line above it saying so. X-Murmur-Source says which.
async function agentPage(request, env, ctx, page, atRoot) {
  if (request.method !== 'GET' && request.method !== 'HEAD') {
    return new Response('Only GET and HEAD.\n', { status: 405, headers: { Allow: 'GET, HEAD' } });
  }
  const raw = `${rawBase(env)}docs/agents/${page}.md`;
  const ref = env?.DOCS_REF ? `${env.DOCS_REF}/` : '';
  const freshKey = `${SITE}/_cache/fresh/${ref}${page}.md`, keptKey = `${SITE}/_cache/kept/${ref}${page}.md`;
  const cache = caches.default;

  let text, source;
  const fresh = await cache.match(freshKey);
  if (fresh) [text, source] = [await fresh.text(), 'cache'];
  if (text === undefined) {
    try {
      const res = await fetch(raw, { signal: AbortSignal.timeout(5000), cache: 'no-store' });
      if (!res.ok) throw new Error(`GitHub answered ${res.status}`);
      [text, source] = [await res.text(), 'github'];
      const store = (key, seconds) => cache.put(key, new Response(text, { headers: { 'Content-Type': MARKDOWN, 'Cache-Control': `max-age=${seconds}` } }));
      ctx.waitUntil(Promise.all([store(freshKey, FRESH_SECONDS), store(keptKey, KEEP_SECONDS)]));
    } catch (err) {
      console.log(`fetching ${raw}: ${err}`);
      const kept = await cache.match(keptKey);
      [text, source] = kept ? [await kept.text(), 'kept-copy'] : [PAGES[page], 'deployed-copy'];
      text = `> GitHub couldn't be reached just now, so this is ${kept ? 'the last copy fetched from it' : "the copy from this site's last deploy"}. ` +
        `It may be out of date: the current page is ${raw}.

` + text;
    }
  }

  if (atRoot) {
    text = `> This is Murmur's website, murmur.afig.dev, as markdown for a coding agent: below is its setup page, also at ${SITE}/setup. ` +
      `If your user asked you to set Murmur up, follow it, and ask your user before you install or run anything. ` +
      `The other agent pages: ${SITE}/join (joining a repo that already uses Murmur) and ${SITE}/use (the tools). An index: ${SITE}/llms.txt.\n\n` + text;
  }
  // A preview's pages link to the preview and to its branch, so an agent following them stays on it.
  if (env?.DOCS_REF) {
    text = text.replaceAll(SITE + '/', new URL(request.url).origin + '/')
      .replaceAll('https://raw.githubusercontent.com/a-Fig/murmur/main/', rawBase(env));
  }
  const headers = {
    'Content-Type': MARKDOWN,
    'Cache-Control': `public, max-age=${source === 'github' || source === 'cache' ? FRESH_SECONDS : 60}`,
    'X-Murmur-Source': source,
    'Link': `<${raw}>; rel="canonical"`,
  };
  if (atRoot) headers['Vary'] = 'Accept';
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
