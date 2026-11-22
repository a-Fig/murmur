// The Worker's answers to a path no page has, and that the videos still go through it.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import worker, { HARNESSES, PAGES } from '../src/worker.js';
import { NOT_FOUND_MD } from '../src/notfound.js';

// The static assets as the Worker sees them: the files named here, and an empty 404 for the rest.
function assets(files) {
  return {
    async fetch(request) {
      const { pathname } = new URL(request.url);
      if (pathname in files) return new Response(files[pathname], { status: 200, headers: { 'Content-Type': 'video/mp4' } });
      return new Response(null, { status: 404 });
    },
  };
}

const env = { ASSETS: assets({ '/how-wide.mp4': '0123456789' }) };
const get = (path, headers = {}, method = 'GET') => worker.fetch(new Request(`https://murmur.afig.dev${path}`, { method, headers }), env, {});

test('an agent that asks for markdown gets a markdown 404', async () => {
  for (const accept of ['text/markdown', 'text/markdown, text/html;q=0.9', 'text/markdown, */*']) {
    const res = await get('/__no-such-page', { Accept: accept });
    assert.equal(res.status, 404, accept);
    assert.equal(res.headers.get('Content-Type'), 'text/markdown; charset=utf-8');
    assert.match(res.headers.get('Vary'), /Accept/);
    assert.equal(res.headers.get('X-Robots-Tag'), 'noindex');
    const body = await res.text();
    assert.equal(body, NOT_FOUND_MD);
  }
});

test('the markdown 404 says what happened and where to go', () => {
  assert.ok(NOT_FOUND_MD.length >= 20);
  assert.match(NOT_FOUND_MD, /^# Not found\n/);
  for (const link of ['https://murmur.afig.dev/llms.txt', 'https://murmur.afig.dev/sitemap.xml', 'https://murmur.afig.dev/setup', 'https://murmur.afig.dev/join', 'https://murmur.afig.dev/use']) {
    assert.ok(NOT_FOUND_MD.includes(link), link);
  }
});

test('a HEAD for markdown gets the 404 without a body', async () => {
  const res = await get('/__no-such-page', { Accept: 'text/markdown' }, 'HEAD');
  assert.equal(res.status, 404);
  assert.equal(res.headers.get('Content-Type'), 'text/markdown; charset=utf-8');
  assert.equal(await res.text(), '');
});

test('a browser, or a client that names nothing, keeps the assets\' empty 404', async () => {
  for (const accept of [undefined, '*/*', 'text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8', 'text/markdown;q=0.1, text/*']) {
    const res = await get('/__no-such-page', accept ? { Accept: accept } : {});
    assert.equal(res.status, 404, String(accept));
    assert.equal(res.headers.get('Content-Type'), null, String(accept));
    assert.equal(await res.text(), '', String(accept));
    assert.match(res.headers.get('Vary'), /Accept/);
  }
});

test('the videos are still sliced for a Range request', async () => {
  const res = await get('/how-wide.mp4', { Range: 'bytes=2-5' });
  assert.equal(res.status, 206);
  assert.equal(res.headers.get('Content-Range'), 'bytes 2-5/10');
  assert.equal(await res.text(), '2345');
});

test('a missing video is a 404 as before, markdown or not', async () => {
  assert.equal((await get('/missing.mp4')).status, 404);
  assert.equal((await get('/missing.mp4', { Accept: 'text/markdown' })).status, 404);
});

// The agent pages, read from GitHub (here, a stand-in that answers with the URL it was asked for)
// with no cached copy.
function withGitHub(answer, fn) {
  return async () => {
    const saved = { fetch: globalThis.fetch, caches: globalThis.caches };
    const asked = [];
    globalThis.fetch = async url => { asked.push(String(url)); return answer(String(url)); };
    globalThis.caches = { default: { match: async () => undefined, put: async () => {} } };
    try { await fn(asked); } finally { Object.assign(globalThis, saved); }
  };
}
const ok = url => new Response(`page at ${url}, linking https://murmur.afig.dev/use, https://murmur.afig.dev/agents/pi.md, https://murmur.afig.dev/v1/repo and https://raw.githubusercontent.com/a-Fig/murmur/main/install.sh\n`);
const ctx = { waitUntil() {} };
const getIn = (e, path, headers = {}) => worker.fetch(new Request(`https://murmur.afig.dev${path}`, { headers }), e, ctx);
const RAW = 'https://raw.githubusercontent.com/a-Fig/murmur/main/docs/agents/';

test('each agent\'s page is its own file with the shared steps after it, in one fetch', withGitHub(ok, async asked => {
  for (const name of HARNESSES) {
    const res = await getIn(env, `/agents/${name}.md`);
    assert.equal(res.status, 200, name);
    assert.equal(res.headers.get('Content-Type'), 'text/markdown; charset=utf-8');
    const text = await res.text();
    assert.match(text, new RegExp(`^page at ${RAW}harnesses/${name}\.md`));
    assert.ok(text.indexOf(`${RAW}steps.md`) > text.indexOf(`harnesses/${name}.md`), `${name}: the steps come after`);
    assert.equal(res.headers.get('Link'), `<${RAW}harnesses/${name}.md>; rel="canonical"`);
  }
  assert.equal(asked.length, 2 * HARNESSES.length);
}));

test('/setup and /join are the one entry page, join.md', withGitHub(ok, async () => {
  for (const path of ['/setup', '/setup.md', '/join', '/join.md']) {
    assert.match(await (await getIn(env, path)).text(), new RegExp(`^page at ${RAW}join\\.md,`), path);
  }
  assert.match(await (await getIn(env, '/use')).text(), new RegExp(`^page at ${RAW}use\.md`));
}));

test('/agents/ is join.md for a request that prefers markdown, and the page for people otherwise', withGitHub(ok, async () => {
  const md = await getIn(env, '/agents/', { Accept: 'text/markdown' });
  assert.match(await md.text(), new RegExp(`^page at ${RAW}join\.md`));
  assert.match(md.headers.get('Vary'), /Accept/);
  const people = { ASSETS: { fetch: async () => new Response('<!doctype html>', { headers: { 'Content-Type': 'text/html' } }) } };
  const html = await getIn(people, '/agents/', { Accept: 'text/html' });
  assert.equal(await html.text(), '<!doctype html>');
  assert.match(html.headers.get('Link'), /<https:\/\/murmur\.afig\.dev\/join>; rel="alternate"; type="text\/markdown"/);
  assert.match(html.headers.get('Vary'), /Accept/);
}));

test('an agent name with no page, the steps alone, and the logos are left to the assets', withGitHub(ok, async asked => {
  for (const path of ['/agents/cursor.md', '/agents/steps.md', '/steps', '/agents/claude.svg']) assert.equal((await getIn(env, path)).status, 404, path);
  assert.equal(asked.length, 0);
}));

test('with GitHub down, an agent\'s page is the copies bundled at deploy', withGitHub(() => new Response('', { status: 503 }), async () => {
  const res = await getIn(env, '/agents/pi.md');
  const text = await res.text();
  assert.match(text, /^> GitHub couldn't be reached/);
  assert.equal(text.match(/GitHub couldn't be reached/g).length, 1, 'one note, at the top');
  assert.ok(text.includes(PAGES['harnesses/pi'].trimEnd()));
  assert.ok(text.trimEnd().endsWith(PAGES.steps.trimEnd()));
  assert.equal(res.headers.get('X-Murmur-Source'), 'deployed-copy');
}));

test('a preview reads the pages from its branch, and points their links at itself', withGitHub(ok, async asked => {
  const preview = { ...env, DOCS_REF: 't3/some-branch' };
  const res = await worker.fetch(new Request('https://harness-docs-murmur-site.example.workers.dev/agents/codex.md'), preview, ctx);
  const text = await res.text();
  assert.equal(asked[0], 'https://raw.githubusercontent.com/a-Fig/murmur/refs/heads/t3/some-branch/docs/agents/harnesses/codex.md');
  assert.ok(text.includes('https://harness-docs-murmur-site.example.workers.dev/use'));
  assert.ok(text.includes('https://raw.githubusercontent.com/a-Fig/murmur/refs/heads/t3/some-branch/install.sh'));
  assert.ok(text.includes('https://harness-docs-murmur-site.example.workers.dev/agents/pi.md'));
  // Where mur reports a public repo is named as it is, not as the preview.
  assert.ok(text.includes('https://murmur.afig.dev/v1/repo'));
  assert.equal(text.match(/https:\/\/murmur\.afig\.dev\//g).length, 2);
}));
