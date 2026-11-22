// The files machines read on murmur.afig.dev: the sitemap, robots.txt, llms.txt, and the pages'
// metadata and JSON-LD. Each is checked against its format and against the files it points at.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';
import { HARNESSES } from '../src/worker.js';
import { build } from '../src/build-agents.mjs';

const SITE = 'https://murmur.afig.dev';
const root = fileURLToPath(new URL('..', import.meta.url)); // site/
const read = path => readFileSync(join(root, path), 'utf8');

// Where a URL on the site comes from: a static file, or an agent page the Worker serves from
// docs/agents/ (src/worker.js).
function source(url) {
  assert.ok(url.startsWith(SITE + '/'), `${url} is not on ${SITE}`);
  const path = url.slice(SITE.length);
  const page = /^\/(setup|join|use)$/.exec(path)?.[1];
  if (page) return join(root, '..', 'docs', 'agents', `${page === 'use' ? 'use' : 'join'}.md`);
  const harness = /^\/agents\/([a-z0-9-]+)\.md$/.exec(path)?.[1];
  if (harness) return join(root, '..', 'docs', 'agents', 'harnesses', `${harness}.md`);
  return join(root, path.endsWith('/') ? path + 'index.html' : path);
}

test('the sitemap follows the sitemaps.org protocol and lists pages that exist', () => {
  const xml = read('sitemap.xml');
  assert.match(xml, /^<\?xml version="1\.0" encoding="UTF-8"\?>\n/);
  assert.match(xml, /<urlset xmlns="http:\/\/www\.sitemaps\.org\/schemas\/sitemap\/0\.9">/);
  assert.ok(Buffer.byteLength(xml) < 50 * 1024 * 1024);
  const urls = [...xml.matchAll(/<url>([\s\S]*?)<\/url>/g)].map(m => m[1]);
  assert.ok(urls.length > 0 && urls.length <= 50000);
  const today = new Date().toISOString().slice(0, 10);
  const seen = new Set();
  for (const u of urls) {
    const loc = /<loc>([^<]+)<\/loc>/.exec(u)?.[1];
    const lastmod = /<lastmod>([^<]+)<\/lastmod>/.exec(u)?.[1];
    assert.ok(loc, 'a url without a loc');
    assert.ok(!seen.has(loc), `${loc} listed twice`);
    seen.add(loc);
    assert.ok(!/[&<>"']/.test(loc), `${loc} needs escaping`);
    assert.ok(existsSync(source(loc)), `${loc} has no page`);
    if (/^https:\/\/murmur\.afig\.dev\/(setup|join|use)$/.test(loc)) {
      assert.equal(lastmod, undefined, `${loc} changes with docs/agents, so it has no lastmod`);
      continue;
    }
    assert.match(lastmod ?? '', /^\d{4}-\d{2}-\d{2}$/, `${loc}'s lastmod`);
    assert.ok(!Number.isNaN(Date.parse(lastmod)) && lastmod <= today, `${loc}'s lastmod ${lastmod}`);
  }
  for (const page of ['/', '/daemon/', '/more/', '/about/', '/privacy/', '/contact/', '/setup', '/join', '/use', '/agents/']) assert.ok(seen.has(SITE + page), `${page} missing`);
});

test('robots.txt allows crawling and names the sitemap', () => {
  const robots = read('robots.txt');
  assert.match(robots, /^User-agent: \*$/m);
  assert.match(robots, /^Sitemap: https:\/\/murmur\.afig\.dev\/sitemap\.xml$/m);
  assert.doesNotMatch(robots, /^Disallow: \/\s*$/m);
});

// The <head> metadata both pages share, and the og:image they point at.
function checkMeta(html, canonical) {
  assert.match(html, /<html lang="en">/);
  assert.ok(html.includes(`<link rel="canonical" href="${canonical}">`), 'canonical');
  assert.ok(html.includes(`<meta property="og:url" content="${canonical}">`), 'og:url');
  assert.match(html, /<meta property="og:type" content="(website|article)">/);
  assert.match(html, /<meta property="og:title" content="[^"]+">/);
  assert.match(html, /<meta property="og:description" content="[^"]+">/);
  const image = /<meta property="og:image" content="([^"]+)">/.exec(html)?.[1];
  assert.ok(image, 'og:image');
  assert.ok(existsSync(source(image)), `${image} doesn't exist`);
  assert.match(html, /<meta name="twitter:card" content="summary_large_image">/);
}

test('the home page has its canonical URL, language and Open Graph tags', () => {
  checkMeta(read('index.html'), `${SITE}/`);
});

test('the daemon page has its canonical URL, language and Open Graph tags', () => {
  checkMeta(read('daemon/index.html'), `${SITE}/daemon/`);
});

// The words a reader sees: the body, without scripts, styles, drawings or tags.
function visibleText(html) {
  const body = /<body[^>]*>([\s\S]*)<\/body>/.exec(html)?.[1] ?? '';
  return body
    .replace(/<(script|style|svg|noscript)\b[\s\S]*?<\/\1>/g, ' ')
    .replace(/<[^>]+>/g, ' ')
    .replace(/&[a-z]+;|&#\d+;/g, ' ')
    .replace(/\s+/g, ' ')
    .trim();
}

// About, Privacy and Contact: what an agent or a person checks before trusting the site.
for (const [page, title] of [['about', /About/], ['privacy', /Privacy/], ['contact', /Contact/], ['more', /Other ways/]]) {
  test(`the ${page} page exists, has its metadata, and says something`, () => {
    const file = `${page}/index.html`;
    assert.ok(existsSync(join(root, file)), `${file} missing`);
    const html = read(file);
    checkMeta(html, `${SITE}/${page}/`);
    assert.match(/<title>([^<]+)<\/title>/.exec(html)?.[1] ?? '', title);
    assert.match(html, /<meta name="description" content="[^"]{50,}">/);
    const text = visibleText(html);
    assert.ok(text.length >= 500, `${page} has ${text.length} characters of text`);
    // A heading's name is its text alone; a subtitle goes beside it, not inside.
    for (const [h2] of html.matchAll(/<h2>[\s\S]*?<\/h2>/g)) assert.match(h2, /^<h2>[^<]+<\/h2>$/, `${page}: ${h2}`);
    // Its stylesheet is published with it.
    for (const [, href] of html.matchAll(/<link rel="stylesheet" href="(\/[^"]+)">/g)) assert.ok(existsSync(source(SITE + href)), `${href} missing`);
  });
}

test('the privacy page dates itself and is the full account of what Murmur sends', () => {
  const html = read('privacy/index.html');
  assert.match(visibleText(html), /As of \d{4}-\d{2}-\d{2}/);
  for (const what of ['murmur.afig.dev/v1/repo', 'refs/murmur/reported', 'internal/publicrepo']) assert.ok(html.includes(what), what);
  // The steps an agent follows say it in a line and send the rest here.
  assert.ok(readFileSync(join(root, '..', 'docs', 'agents', 'steps.md'), 'utf8').includes(`${SITE}/privacy/`));
  // Every host the page names as one a browser contacts is one the page really loads.
  for (const host of ['fonts.googleapis.com', 'fonts.gstatic.com']) assert.ok(html.includes(`href="https://${host}`), host);
});

test('every page ends with the same links, and each one leads somewhere', () => {
  const pages = ['index.html', 'daemon/index.html', 'more/index.html', 'about/index.html', 'privacy/index.html', 'contact/index.html', 'agents/index.html'];
  for (const file of pages) {
    const html = read(file);
    const foot = /<footer class="site-foot">([\s\S]*?)<\/footer>/.exec(html)?.[1];
    assert.ok(foot, `${file} has no footer`);
    // After <main>, so it is the page's contentinfo landmark.
    assert.ok(html.indexOf('<footer class="site-foot">') > html.lastIndexOf('</main>'), `${file}'s footer is inside <main>`);
    const hrefs = [...foot.matchAll(/<a href="([^"]+)"/g)].map(m => m[1]);
    assert.deepEqual(hrefs, ['/about/', '/privacy/', '/contact/', 'https://github.com/a-Fig/murmur'], file);
    for (const href of hrefs.filter(h => h.startsWith('/'))) assert.ok(existsSync(source(SITE + href)), `${href} has no page`);
  }
});

test('the home page\'s JSON-LD parses, says what Murmur is, and uses words the page uses', () => {
  const html = read('index.html');
  const blocks = [...html.matchAll(/<script type="application\/ld\+json">([\s\S]*?)<\/script>/g)].map(m => JSON.parse(m[1]));
  assert.equal(blocks.length, 1);
  const [ld] = blocks;
  assert.equal(ld['@context'], 'https://schema.org');
  const byType = Object.fromEntries(ld['@graph'].map(n => [n['@type'], n]));
  const app = byType.SoftwareApplication;
  assert.ok(app);
  // An open-source project kept by one person: no organization is invented for it.
  assert.equal(byType.Organization, undefined);
  assert.equal(app.author['@type'], 'Person');
  for (const key of ['name', 'description', 'url', 'applicationCategory', 'operatingSystem', 'offers', 'keywords']) assert.ok(app[key], key);
  assert.equal(app.name, 'Murmur');
  assert.equal(app.url, `${SITE}/`);
  assert.ok(existsSync(source(app.image)));
  assert.equal(app.description, /<meta name="description" content="([^"]+)">/.exec(html)[1]);
  // Search engines and agents take these as the product's terms, so each must be on the page.
  const text = html.replace(/<script[\s\S]*?<\/script>|<style[\s\S]*?<\/style>|<[^>]+>/g, ' ').toLowerCase();
  for (const word of app.keywords.split(',').map(w => w.trim())) assert.ok(text.includes(word.toLowerCase()), `keyword ${word} isn't on the page`);
});

test('llms.txt follows llmstxt.org and says when to use Murmur', () => {
  const txt = read('llms.txt');
  const lines = txt.split('\n');
  assert.match(lines[0], /^# Murmur$/);
  assert.match(lines.find((l, i) => i > 0 && l.trim()) ?? '', /^> /, 'a blockquote summary after the title');
  assert.ok(!/^#{3,} /m.test(txt), 'only H1 and H2 headings');
  assert.equal((txt.match(/^# /gm) || []).length, 1, 'one H1');
  // Before the first H2, any markdown but headings; after it, each section is a list of links.
  const sections = txt.split(/^## /m).slice(1);
  assert.ok(sections.length > 0);
  for (const section of sections) {
    const [, ...body] = section.split('\n');
    for (const line of body.filter(l => l.trim())) {
      assert.match(line, /^- \[[^\]]+\]\(https:\/\/[^)\s]+\)(: .+)?$/, `not a link list item: ${line}`);
    }
  }
  const intro = txt.split(/^## /m)[0];
  assert.match(intro, /When to use Murmur/);
  assert.match(intro, /How to call it/);
  // The tools it names are the ones the use page documents.
  const use = readFileSync(join(root, '..', 'docs', 'agents', 'use.md'), 'utf8');
  for (const tool of ['who', 'lookup', 'status', 'tags', 'send', 'inbox']) {
    assert.ok(intro.includes('`' + tool + '`'), `llms.txt doesn't name ${tool}`);
    assert.ok(use.includes('`' + tool + '`'), `use.md doesn't document ${tool}`);
  }
});

// Each agent has a section on the page for people, built from the file its markdown page is made of,
// and the home page's Supported tiles, README and llms.txt lead to it (#934).
test('the page on each agent is built from docs/agents/harnesses, and is up to date', () => {
  const html = read('agents/index.html');
  assert.equal(html.replace(/\r\n/g, '\n'), build(), 'site/agents/index.html is out of date: run node site/src/build-agents.mjs');
  checkMeta(html, `${SITE}/agents/`);
  for (const [h2] of html.matchAll(/<h2>[\s\S]*?<\/h2>/g)) assert.match(h2, /^<h2>[^<]+<\/h2>$/, h2);
});

for (const name of HARNESSES) {
  test(`${name} has a section for people and a page for agents, and both are linked`, () => {
    assert.ok(read('agents/index.html').includes(`<section class="fact" id="${name}">`), `no section for ${name}`);
    assert.ok(read('agents/index.html').includes(`${SITE}/agents/${name}.md`), `the section doesn't give ${name}'s page for agents`);
    assert.ok(read('index.html').includes(`<a href="/agents/#${name}">`), `the home page doesn't link to ${name}'s section`);
    assert.ok(read('llms.txt').includes(`(${SITE}/agents/${name}.md)`), `llms.txt doesn't list ${name}'s page`);
    assert.ok(readFileSync(join(root, '..', 'README.md'), 'utf8').includes(`${SITE}/agents/#${name}`), `the README doesn't link to ${name}'s section`);
  });
}

test('the tests and package.json are not published with the site', () => {
  const ignored = read('.assetsignore').split('\n');
  assert.ok(ignored.includes('test') && ignored.includes('package.json'));
});

test('the home page\'s links within the page each lead to a section on it, and the hero leads to Setup', () => {
  const html = read('index.html');
  const ids = new Set([...html.matchAll(/\sid="([^"]+)"/g)].map(m => m[1]));
  const anchors = [...html.matchAll(/<a [^>]*href="#([^"]+)"/g)].map(m => m[1]);
  assert.ok(anchors.length > 0);
  for (const id of anchors) assert.ok(ids.has(id), `#${id} has no element`);
  const hero = /<main class="st[^"]*">([\s\S]*?)<\/main>/.exec(html)?.[1] ?? '';
  assert.match(hero, /<a class="button" href="#setup">/, 'the hero\'s button goes to Setup');
});
