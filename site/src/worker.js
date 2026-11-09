// The Worker in front of the static site (wrangler.jsonc's run_worker_first lists the paths it takes).
// It does four things the assets can't:
// - serves the agent pages, docs/agents/*.md, pulled from the repo (#140);
// - answers `/` with the setup page instead of the HTML when the request prefers markdown;
// - slices the videos for Range requests;
// - takes the address of a public repo that uses Murmur, the only thing Murmur sends us, and serves
//   the page that counts use, with the same numbers as markdown and JSON for an agent (src/usage.js);
// - takes the daily census of those repos (src/repos.js).

// Bundled at deploy (the Text rule in wrangler.jsonc), as the copy of last resort when GitHub can't
// be reached and this data center has no cached copy.
import setupMd from '../../docs/agents/setup.md';
import joinMd from '../../docs/agents/join.md';
import useMd from '../../docs/agents/use.md';
import { checkIn, reportRepo, stats } from './usage.js';
import { census } from './repos.js';
import { prefersMarkdown } from './accept.js';

const RAW = 'https://raw.githubusercontent.com/a-Fig/murmur/main/docs/agents/';
const SITE = 'https://murmur.afig.dev';
const PAGES = { setup: setupMd, join: joinMd, use: useMd };
// How long a page fetched from GitHub is served before it is fetched again. GitHub's raw host caches
// for 5 minutes too, so a merged change shows up here within about 10.
const FRESH_SECONDS = 300;
// How long the last good copy is kept, to serve while GitHub is unreachable.
const KEEP_SECONDS = 30 * 24 * 3600;
const MARKDOWN = 'text/markdown; charset=utf-8';

export default {
  async fetch(request, env, ctx) {
    const { pathname } = new URL(request.url);
    if (pathname === '/v1/repo') return reportRepo(request, env);
    if (pathname === '/v1/checkin') return checkIn(request, env, ctx);
    if (pathname === '/v1/census') return census(request, env);
    if (pathname === '/stats' || pathname.startsWith('/stats/')) return stats(request, env);
    const page = /^\/(setup|join|use)(?:\.md)?\/?$/.exec(pathname)?.[1];
    if (page) return agentPage(request, ctx, page, false);
    if (pathname === '/' && prefersMarkdown(request.headers.get('Accept'))) return agentPage(request, ctx, 'setup', true);
    if (pathname === '/') {
      const res = await env.ASSETS.fetch(request);
      const headers = new Headers(res.headers);
      headers.append('Vary', 'Accept');
      headers.append('Link', `<${SITE}/setup>; rel="alternate"; type="text/markdown"`);
      return new Response(res.body, { status: res.status, headers });
    }
    return video(request, env); // the *.mp4 paths
  },
};

// The page from the cache if it was fetched from GitHub in the last FRESH_SECONDS; else from GitHub;
// else, if GitHub is unreachable or answers with an error, the last good copy this data center kept;
// else the copy bundled at deploy, with a line above it saying so. X-Murmur-Source says which.
async function agentPage(request, ctx, page, atRoot) {
  if (request.method !== 'GET' && request.method !== 'HEAD') {
    return new Response('Only GET and HEAD.\n', { status: 405, headers: { Allow: 'GET, HEAD' } });
  }
  const raw = `${RAW}${page}.md`;
  const freshKey = `${SITE}/_cache/fresh/${page}.md`, keptKey = `${SITE}/_cache/kept/${page}.md`;
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
