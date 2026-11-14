// The census (site/census/census.mjs) and the site's /v1/census (src/repos.js) together: the census's
// requests go to the Worker's handler, on a D1 stand-in, never to murmur.afig.dev.
// `node --test site/test/census.test.mjs`; it needs git, and nothing installed.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { timingSafeEqual } from 'node:crypto';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { census as handle, repoNumbers, repoReport, size } from '../src/repos.js';
import { recordingD1, sqliteD1 } from './d1.mjs';

// The Workers runtime adds this to Web Crypto; node has it only in node:crypto.
crypto.subtle.timingSafeEqual ??= (a, b) => timingSafeEqual(a, b);

process.env.CENSUS_SITE = 'https://census.invalid';
process.env.CENSUS_KEY = 'test-key';
// The census's git, and the tests', read none of the developer's own config. An empty file, not
// os.devNull: Git for Windows can't open NUL's device path ("unable to access").
const gitHome = mkdtempSync(join(tmpdir(), 'census-git-'));
writeFileSync(join(gitHome, 'config'), '');
process.on('exit', () => rmSync(gitHome, { recursive: true, force: true }));
process.env.GIT_CONFIG_GLOBAL = join(gitHome, 'config');
process.env.GIT_CONFIG_NOSYSTEM = '1';
// Run in Actions, the tests' runs would write to the job's own summary. The tests that read one set
// their own.
delete process.env.GITHUB_STEP_SUMMARY;
const { main, count, boxedSize, authors, contributors, apiLookup, tooBig, summaryHead, summaryRow, census: readRepo } = await import('../census/census.mjs');

const today = new Date().toISOString().slice(0, 10);
const daysAgo = n => new Date(Date.now() - n * 864e5).toISOString().slice(0, 10);
const ID = 'a'.repeat(16), PERSON = 'b'.repeat(16);

// One repo's counts, as census() makes them: a day of one device, unless `days` says otherwise.
function counts(repo, { days = [{ day: daysAgo(1), sessions: 1, messages: 2, records: 3, devices: [ID] }], contributors = [] } = {}) {
  return {
    repo: repo.toLowerCase(), days,
    devices: [{ id: ID, person: PERSON }],
    totals: { sessions: 1, messages: 2, records: 3, first_day: days[0]?.day ?? null, last_day: days.at(-1)?.day ?? null },
    contributors,
  };
}

// Sends the census's requests to the Worker's handler with this D1, and keeps what it said.
function site(t, db, { down = new Set() } = {}) {
  const env = { DB: db, CENSUS_KEY: process.env.CENSUS_KEY };
  t.mock.method(globalThis, 'fetch', async (url, init) => {
    assert.ok(String(url).startsWith('https://census.invalid/'), String(url));
    const body = init?.body ? JSON.parse(init.body) : null;
    if (body && down.has(body.repo)) throw new TypeError('fetch failed');
    return handle(new Request(url, init), env);
  });
  const said = { log: [], error: [] };
  t.mock.method(console, 'log', s => said.log.push(String(s)));
  t.mock.method(console, 'error', s => said.error.push(String(s)));
  return said;
}

const sent = db => db.batches.map(b => b.find(s => /INTO repo_census/.test(s.sql)).args[0]);

test('a repo the site refuses does not stop the census: the repos after it are still sent, and the job fails', async t => {
  const names = ['github.com/a/one', 'github.com/b/two', 'github.com/c/three'];
  const db = recordingD1(names);
  const said = site(t, db);
  // A day before the bus existed, which only a census with a bug would send: the site answers 400.
  const early = [{ day: '2026-09-30', sessions: 0, messages: 1, records: 1, devices: [ID] }];
  const read = name => counts(name, name.endsWith('two') ? { days: early } : {});
  await assert.rejects(main(names, { read }), /1 of 3 repos counted but not sent: github\.com\/b\/two/);
  assert.deepEqual(sent(db), ['github.com/a/one', 'github.com/c/three']);
  assert.equal(said.error.length, 1);
  assert.match(said.error[0], /^github\.com\/b\/two: not sent: \/v1\/census: 400 Not counts: totals days\.$/);
  assert.equal(said.log.length, 2);
});

test('a POST that never reaches the site is one repo not sent, not the end of the census', async t => {
  const names = ['github.com/a/one', 'github.com/b/two'];
  const db = recordingD1(names);
  const said = site(t, db, { down: new Set(['github.com/a/one']) });
  await assert.rejects(main(names, { read: name => counts(name) }), /1 of 2 repos counted but not sent: github\.com\/a\/one/);
  assert.deepEqual(sent(db), ['github.com/b/two']);
  assert.match(said.error[0], /^github\.com\/a\/one: not sent: fetch failed$/);
});

test("a repo that can't be read keeps its counts and the job green, as before", async t => {
  const names = ['github.com/a/one', 'github.com/b/gone', 'github.com/c/three'];
  const db = recordingD1(names);
  const said = site(t, db);
  const read = name => {
    if (name.endsWith('gone')) throw Object.assign(new Error('git failed'), { stderr: 'remote: Repository not found.\n' });
    return counts(name);
  };
  await main(names, { read });
  assert.deepEqual(sent(db), ['github.com/a/one', 'github.com/c/three']);
  assert.deepEqual(said.error, ['github.com/b/gone: not counted: remote: Repository not found.']);
});

test('the job fails when no repo could be read at all', async t => {
  site(t, recordingD1(['github.com/a/one']));
  await assert.rejects(main(['github.com/a/one'], { read: () => { throw new Error('offline'); } }), /no repo could be counted/);
});

test('with no repos named, the census asks the site for its list', async t => {
  const names = ['github.com/A/One', 'github.com/b/two'];
  const db = recordingD1(names);
  site(t, db);
  const asked = [];
  await main([], { read: name => (asked.push(name), counts(name)) });
  assert.deepEqual(asked, names);
  assert.deepEqual(sent(db), ['github.com/a/one', 'github.com/b/two']);
});

// What the site does with one POST.
const post = (db, c) => handle(new Request('https://census.invalid/v1/census', {
  method: 'POST', headers: { Authorization: `Bearer ${process.env.CENSUS_KEY}` }, body: JSON.stringify(c),
}), { DB: db, CENSUS_KEY: process.env.CENSUS_KEY });
const hex = (n, w = 16) => n.toString(16).padStart(w, '0');
const many = n => Array.from({ length: n }, (_, i) => ({ id: hex(i), first: daysAgo(i % 30) }));

test("a big repo's writes go in batches D1 can finish, with its totals last", async () => {
  const db = recordingD1(['github.com/big/repo']);
  const res = await post(db, counts('github.com/big/repo', { contributors: many(40000) }));
  assert.equal(res.status, 200, await res.clone().text());
  // 40,000 contributors in rows of 33 are 1,213 statements, with a day, a device, its day and the totals.
  assert.deepEqual(db.batches.map(b => b.length), [200, 200, 200, 200, 200, 200, 17]);
  const stmts = db.batches.flat();
  const rows = stmts.filter(s => /INTO contributors/.test(s.sql)).reduce((n, s) => n + s.args.length / 3, 0);
  assert.equal(rows, 40000);
  assert.ok(stmts.every(s => s.args.length <= 100), 'D1 binds at most 100 values a statement');
  assert.match(stmts.at(-1).sql, /INTO repo_census/);
  assert.equal(stmts.filter(s => /INTO repo_census/.test(s.sql)).length, 1);
});

test('a POST with more device-days than the site keeps is refused', async () => {
  const db = recordingD1(['github.com/big/repo']);
  const devices = Array.from({ length: 1000 }, (_, i) => hex(i));
  // The same day, again and again: there aren't yet 101 days since Murmur began.
  const days = Array.from({ length: 101 }, () => ({ day: daysAgo(1), sessions: 0, messages: 0, records: 0, devices }));
  const res = await post(db, counts('github.com/big/repo', { days }));
  assert.equal(res.status, 400);
  assert.equal(await res.text(), "Not counts: the days' devices.\n");
  assert.equal(db.batches.length, 0);
});

const sqlite = await sqliteD1();

test('the batched writes land in SQLite as D1 would keep them', { skip: !sqlite && 'no node:sqlite in this node' }, async () => {
  const db = sqlite;
  db.sql.exec(`INSERT INTO repos (repo, name, first) VALUES ('github.com/big/repo', 'github.com/Big/Repo', '${daysAgo(3)}')`);
  const c = counts('github.com/big/repo', { contributors: many(40000) });
  for (let i = 0; i < 2; i++) assert.equal((await post(db, c)).status, 200);
  assert.equal(db.batches, 14);
  const one = sql => db.sql.prepare(sql).get();
  assert.equal(one('SELECT COUNT(*) AS n FROM contributors').n, 40000);
  assert.equal(one('SELECT COUNT(*) AS n FROM device_days').n, 1);
  assert.deepEqual({ ...one('SELECT sessions, messages, records, first_day, last_day FROM repo_census') },
    { sessions: 1, messages: 2, records: 3, first_day: daysAgo(1), last_day: daysAgo(1) });
});

// A bare repo in a temporary directory, and a way to commit to it as a device or a person would,
// with the dates given.
function repo(t) {
  const dir = mkdtempSync(join(tmpdir(), 'census-test-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const git = (args, { input = '', date, who = 'Dev <dev@example.com>' } = {}) => {
    const [, name, email] = /^(.*) <(.*)>$/.exec(who);
    const env = { ...process.env, GIT_AUTHOR_NAME: name, GIT_AUTHOR_EMAIL: email, GIT_COMMITTER_NAME: name, GIT_COMMITTER_EMAIL: email };
    if (date) env.GIT_AUTHOR_DATE = env.GIT_COMMITTER_DATE = date;
    return execFileSync('git', ['-C', dir, ...args], { input, encoding: 'utf8', env, stdio: ['pipe', 'pipe', 'pipe'] });
  };
  git(['init', '-q', '--bare']);
  const tree = git(['hash-object', '-t', 'tree', '-w', '--stdin']).trim();
  const tips = new Map();
  const commit = (ref, message, opts) => {
    const parent = tips.get(ref);
    const sha = git(['commit-tree', tree, ...(parent ? ['-p', parent] : [])], { ...opts, input: message }).trim();
    git(['update-ref', ref, sha]);
    tips.set(ref, sha);
    return sha;
  };
  return { dir, git, commit };
}
const event = r => `event\n\n${JSON.stringify(r)}\n`;
// A repo's totals without its bytes, for the tests that are about something else.
const noBytes = ({ bytes, ...t }) => t;
const NOW = Date.parse('2026-11-09T12:00:00Z');

test("a bus time after tomorrow, or before Murmur, gives way to its commit's date, and neither is counted", t => {
  const { dir, commit } = repo(t);
  const mesh = 'refs/murmur/mesh/dev1';
  commit(mesh, event({ type: 'message', at: '2026-11-05T10:00:00Z' }), { date: '2026-11-05T10:00:00Z' });
  commit(mesh, event({ type: 'message', at: '9999-12-31T00:00:00Z' }), { date: '2026-11-06T00:00:00Z' });
  commit(mesh, event({ type: 'message', at: '2020-01-01T00:00:00Z' }), { date: '2026-11-07T00:00:00Z' });
  // A clock a few hours fast is believed.
  commit(mesh, event({ type: 'message', at: '2026-11-10T20:00:00Z' }), { date: '2026-11-10T20:00:00Z' });
  commit(mesh, event({ type: 'message', at: '9999-12-31T00:00:00Z' }), { date: '2099-01-01T00:00:00Z' });
  // A year JavaScript can read but the site's YYYY-MM-DD can't hold.
  commit(mesh, event({ type: 'message', at: '+275760-09-13T00:00:00Z' }), { date: '2026-11-08T00:00:00Z' });
  // A device whose every time is in the future was never seen.
  commit('refs/murmur/presence/dev2', event({ owner: 'Someone', at: '9999-12-31T00:00:00Z' }), { date: '2099-01-01T00:00:00Z' });

  const c = count(dir, { now: NOW });
  assert.deepEqual(c.days.map(d => [d.day, d.messages]), [['2026-11-05', 1], ['2026-11-06', 1], ['2026-11-07', 1], ['2026-11-08', 1], ['2026-11-10', 1]]);
  assert.deepEqual(noBytes(c.totals), { sessions: 0, messages: 5, records: 5, est_sessions: 0, est_messages: 0, first_day: '2026-11-05', last_day: '2026-11-10' });
  assert.equal(c.devices.length, 1);
});

test("a commit dated after tomorrow counts its author as of today; an old one is believed", t => {
  const { dir, commit } = repo(t);
  commit('refs/heads/main', 'one\n', { date: '2025-01-01T00:00:00Z', who: 'Ann <ann@example.com>' });
  commit('refs/heads/main', 'two\n', { date: '2099-01-01T00:00:00Z', who: 'Bob <bob@example.com>' });
  const tip = commit('refs/heads/main', 'three\n', { date: '2026-11-01T00:00:00Z', who: 'Ann <ann@example.com>' });
  writeFileSync(join(dir, 'FETCH_HEAD'), `${tip}\t\tbranch 'main' of test\n`);
  assert.deepEqual(contributors(authors(dir, { now: NOW })).map(k => k.first).sort(), ['2025-01-01', '2026-11-09']);
});

test('the site refuses a day after tomorrow, and a bus day before Murmur, but not an old first commit', async () => {
  const name = 'github.com/a/one';
  const status = async c => {
    const res = await post(recordingD1([name]), c);
    return [res.status, (await res.text()).trim()];
  };
  const one = (day, contributors = []) => {
    const c = counts(name, { days: [{ day, sessions: 0, messages: 1, records: 1, devices: [ID] }], contributors });
    c.totals.first_day = c.totals.last_day = daysAgo(1);
    return c;
  };
  assert.deepEqual(await status(one(daysAgo(-1))), [200, JSON.stringify({ ok: true, days: 1, devices: 1, contributors: 0 })]);
  assert.deepEqual(await status(one(daysAgo(-2))), [400, 'Not counts: a day.']);
  assert.deepEqual(await status(one('9999-12-31')), [400, 'Not counts: a day.']);
  assert.deepEqual(await status(one('2026-09-30')), [400, 'Not counts: a day.']);
  assert.deepEqual(await status(counts(name, { days: [{ day: '9999-12-31', sessions: 0, messages: 0, records: 0, devices: [] }] })), [400, 'Not counts: totals days.']);
  assert.deepEqual(await status(one(daysAgo(1), [{ id: ID, first: '9999-12-31' }])), [400, 'Not counts: a contributor.']);
  assert.equal((await status(one(daysAgo(1), [{ id: ID, first: '2001-02-03' }])))[0], 200);
});

test('rows dated after today make no repo active and no one new', { skip: !sqlite && 'no node:sqlite in this node' }, async () => {
  const db = await sqliteD1();
  const run = (sql, ...args) => db.sql.prepare(sql).run(...args);
  run(`INSERT INTO repos (repo, name, first) VALUES ('github.com/a/one', 'github.com/a/one', ?1)`, daysAgo(30));
  run(`INSERT INTO devices (repo, device, person) VALUES ('github.com/a/one', ?1, NULL)`, ID);
  run(`INSERT INTO device_days (repo, day, device) VALUES ('github.com/a/one', '9999-12-31', ?1)`, ID);
  run(`INSERT INTO contributors (repo, id, first) VALUES ('github.com/a/one', ?1, '9999-12-31')`, ID);
  let n = await repoNumbers(db, 14);
  assert.deepEqual([n.activeRepos, n.contributors, n.newContributors], [0, 0, 0]);
  // The device is a user, from the day its repo was reported, but neither active nor new.
  assert.deepEqual([n.users, n.activeUsers, n.newUsers], [1, 0, 0]);

  run(`INSERT INTO device_days (repo, day, device) VALUES ('github.com/a/one', ?1, ?2)`, today, ID);
  run(`INSERT INTO contributors (repo, id, first) VALUES ('github.com/a/one', ?1, ?2)`, PERSON, today);
  n = await repoNumbers(db, 14);
  assert.deepEqual([n.activeRepos, n.contributors, n.newContributors], [1, 1, 1]);
  assert.deepEqual([n.users, n.activeUsers, n.newUsers], [1, 1, 1]);
  assert.equal(n.repoList[0].week, 1);
});

// A repo for census() to fetch, served as GitHub serves one: partial fetches allowed, HEAD on main,
// which has one commit by Ann.
function remote(t) {
  const r = repo(t);
  r.git(['config', 'uploadpack.allowFilter', 'true']);
  r.git(['symbolic-ref', 'HEAD', 'refs/heads/main']);
  r.commit('refs/heads/main', 'start\n', { who: 'Ann <ann@example.com>' });
  return { ...r, url: 'file://' + r.dir, at: new Date(Date.now() - 864e5).toISOString() };
}
// census()'s account lookup, in place of GitHub's API: no commit is linked to an account.
const unlinked = () => async () => null;

test("census() fetches a repo's bus and default branch, and only the refs it reads", async t => {
  const r = remote(t);
  r.commit('refs/murmur/mesh/dev1', event({ type: 'session.started', from: 's1', at: r.at }));
  r.commit('refs/murmur/mesh/dev1', event({ type: 'message', at: r.at }));
  r.commit('refs/murmur/presence/dev1', event({ owner: 'Ann', at: r.at }));
  // Under refs/murmur/ but not read: were it fetched, the bus would have 3 refs, more than 2.
  r.commit('refs/murmur/junk', event({ type: 'message', at: r.at }));
  const c = await readRepo('github.com/A/One', { url: r.url, maxRefs: 2, lookup: unlinked });
  assert.equal(c.repo, 'github.com/a/one');
  const day = r.at.slice(0, 10);
  assert.deepEqual(noBytes(c.totals), { sessions: 1, messages: 1, records: 2, est_sessions: 0, est_messages: 0, first_day: day, last_day: day });
  assert.equal(c.devices.length, 1);
  assert.notEqual(c.devices[0].person, null);
  // Ann's presence matches Ann's commit on main, so her device has an account.
  assert.notEqual(c.devices[0].account, null);
  assert.equal(c.contributors.length, 1);
});

test('a repo with more refs on its bus than the census reads is skipped', async t => {
  const r = remote(t);
  for (const d of ['dev1', 'dev2', 'dev3']) r.commit(`refs/murmur/mesh/${d}`, event({ type: 'message', at: r.at }));
  await assert.rejects(readRepo('github.com/a/one', { url: r.url, maxRefs: 2, lookup: unlinked }), { skipped: true, message: '3 refs on its bus, more than 2' });
});

test('a repo that takes longer than its budget is skipped', async t => {
  const r = remote(t);
  r.commit('refs/murmur/mesh/dev1', event({ type: 'message', at: r.at }));
  await assert.rejects(readRepo('github.com/a/one', { url: r.url, budget: 1, lookup: unlinked }), { skipped: true, message: 'took too long to count' });
});

const tooSlow = name => {
  if (name.endsWith('two')) throw Object.assign(new Error('took too long to count'), { skipped: true });
  return counts(name);
};

test('a skipped repo is logged and warned of, and leaves the run green', async t => {
  const names = ['github.com/a/one', 'github.com/b/two', 'github.com/c/three'];
  const db = recordingD1(names);
  const said = site(t, db);
  const was = process.env.GITHUB_ACTIONS;
  process.env.GITHUB_ACTIONS = 'true';
  t.after(() => (was === undefined ? delete process.env.GITHUB_ACTIONS : (process.env.GITHUB_ACTIONS = was)));
  await main(names, { read: tooSlow });
  assert.deepEqual(sent(db), ['github.com/a/one', 'github.com/c/three']);
  assert.deepEqual(said.error, ['github.com/b/two: skipped: took too long to count']);
  assert.ok(said.log.includes('::warning title=census::github.com/b/two skipped: took too long to count'), said.log.join('\n'));
});

test('a run in which every repo is skipped or unreadable fails', async t => {
  site(t, recordingD1(['github.com/b/two', 'github.com/d/gone']));
  const read = name => {
    if (name.endsWith('gone')) throw new Error('git failed');
    return tooSlow(name);
  };
  await assert.rejects(main(['github.com/b/two', 'github.com/d/gone'], { read }), { message: 'no repo could be counted' });
});

// GitHub's API, never answering: each request waits until it is aborted.
function hangingApi(t) {
  const asked = [];
  t.mock.method(globalThis, 'fetch', (url, init) => {
    asked.push(String(url));
    return new Promise((_, reject) => init.signal.addEventListener('abort', () => reject(init.signal.reason)));
  });
  return asked;
}

test("an account lookup gives up at the repo's deadline, not 10 seconds later", async t => {
  const asked = hangingApi(t);
  const started = Date.now();
  const lookup = apiLookup('a', 'one', started + 200);
  assert.equal(await lookup('0'.repeat(40)), undefined);
  const took = Date.now() - started;
  assert.ok(took < 5000, `took ${took} ms`);
  assert.deepEqual(asked, [`https://api.github.com/repos/a/one/commits/${'0'.repeat(40)}`]);
});

test('no account lookup starts after the deadline', async t => {
  const asked = hangingApi(t);
  assert.equal(await apiLookup('a', 'one', Date.now() - 1)('0'.repeat(40)), undefined);
  assert.deepEqual(asked, []);
});

// One repo's counts, each just over one of the site's size limits.
const day = (devices = [ID]) => ({ day: daysAgo(1), sessions: 0, messages: 0, records: 0, devices });
const thousand = Array.from({ length: 1000 }, (_, i) => hex(i));
const oversized = {
  'days on its bus': counts('github.com/a/one', { days: Array.from({ length: 3661 }, () => day()) }),
  'devices on one day': counts('github.com/a/one', { days: [day([...thousand, hex(1000)])] }),
  "devices on the bus's days": counts('github.com/a/one', { days: Array.from({ length: 101 }, () => day(thousand)) }),
  devices: { ...counts('github.com/a/one'), devices: Array.from({ length: 10001 }, (_, i) => ({ id: hex(i), person: null })) },
  contributors: counts('github.com/a/one', { contributors: many(100001) }),
};

test("the census's size limits are the site's: what the site refuses for its size, the census skips", async () => {
  for (const [what, c] of Object.entries(oversized)) {
    assert.match(tooBig(c), new RegExp(`^\\d+ ${what}, more than the site takes \\(\\d+\\)$`), what);
    assert.equal((await post(recordingD1(['github.com/a/one']), c)).status, 400, what);
  }
  // At the limits, neither says no.
  const atLimits = [
    counts('github.com/a/one', { days: [day(thousand)] }),
    counts('github.com/a/one', { days: Array.from({ length: 100 }, () => day(thousand)), contributors: many(100000) }),
    { ...counts('github.com/a/one'), devices: Array.from({ length: 10000 }, (_, i) => ({ id: hex(i), person: null })) },
  ];
  for (const c of atLimits) {
    assert.equal(tooBig(c), '');
    assert.equal((await post(recordingD1(['github.com/a/one']), c)).status, 200);
  }
});

test('a repo too big for the site is skipped, and nothing is sent for it', async t => {
  const names = ['github.com/a/one', 'github.com/b/two'];
  const db = recordingD1(names);
  const said = site(t, db);
  const read = name => (name.endsWith('two') ? { ...oversized['devices on one day'], repo: name } : counts(name));
  await main(names, { read });
  assert.deepEqual(sent(db), ['github.com/a/one']);
  assert.deepEqual(said.error, ['github.com/b/two: skipped: 1001 devices on one day, more than the site takes (1000)']);
});

// A file for $GITHUB_STEP_SUMMARY, as Actions gives a step, for this test only.
function stepSummary(t) {
  const dir = mkdtempSync(join(tmpdir(), 'census-summary-'));
  const file = join(dir, 'summary.md');
  writeFileSync(file, '');
  process.env.GITHUB_STEP_SUMMARY = file;
  t.after(() => {
    delete process.env.GITHUB_STEP_SUMMARY;
    rmSync(dir, { recursive: true, force: true });
  });
  return () => readFileSync(file, 'utf8');
}

test("the run's summary has a row for each repo, its counts or why it has none, and the totals sent", async t => {
  const names = ['github.com/a/one', 'github.com/b/two', 'github.com/c/gone', 'github.com/d/four'];
  site(t, recordingD1(names));
  const read = name => {
    if (name.endsWith('gone')) throw Object.assign(new Error('git failed'), { stderr: 'remote: Repository not found.\n' });
    if (name.endsWith('four')) return { ...counts(name), contributors: many(3) };
    return tooSlow(name);
  };
  const written = stepSummary(t);
  await main(names, { read });
  const lines = written().split('\n');
  assert.match(lines[0], /^## Census, \d{4}-\d\d-\d\d \d\d:\d\d UTC$/);
  assert.deepEqual(lines.slice(1), [
    '',
    '4 repos to count.',
    '',
    '| Repo | Result | Devices | People | Matched to an account | Sessions | Estimated sessions | Messages | Estimated messages | Records | Bytes | Contributors | Days |',
    '| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |',
    '| [a/one](https://github.com/a/one) | sent | 1 | 1 | 0 | 1 | 0 | 2 | 0 | 3 | 0 | 0 | 1 |',
    '| [b/two](https://github.com/b/two) | skipped: took too long to count |  |  |  |  |  |  |  |  |  |  |  |',
    '| [c/gone](https://github.com/c/gone) | not read: remote: Repository not found. |  |  |  |  |  |  |  |  |  |  |  |',
    '| [d/four](https://github.com/d/four) | sent | 1 | 1 | 0 | 1 | 0 | 2 | 0 | 3 | 0 | 3 | 1 |',
    '| **Total** | 2 sent | 2 | 2 | 0 | 2 | 0 | 4 | 0 | 6 | 0 | 3 | 2 |',
    '',
    '4 repos: 2 sent, 1 skipped, 1 not read.',
    '',
  ]);
});

test("each repo's row is in the summary as soon as it is done, so a run cut off keeps what it did", async t => {
  const names = ['github.com/a/one', 'github.com/b/two', 'github.com/c/three'];
  site(t, recordingD1(names));
  const written = stepSummary(t);
  // What the summary held as each repo was started. The third never finishes, as when the job is
  // cancelled at its timeout while a repo is read.
  const seen = [];
  let stuck;
  const reached = new Promise(resolve => (stuck = resolve));
  const read = name => {
    seen.push(written());
    if (name.endsWith('three')) return stuck(), new Promise(() => {});
    return tooSlow(name);
  };
  main(names, { read });
  await reached;
  assert.match(seen[0], /\n3 repos to count\.\n\n\| Repo \|.*\n\| --- \|.*\n$/);
  assert.match(seen[1], /\n\| \[a\/one\]\(https:\/\/github\.com\/a\/one\) \| sent \| 1 \|.*\n$/);
  assert.match(seen[2], /\n\| \[b\/two\]\(https:\/\/github\.com\/b\/two\) \| skipped: took too long to count \|.*\n$/);
  assert.doesNotMatch(seen[2], /Total/);
});

test("a run that fails for a repo the site refused still writes its summary, with the site's answer", async t => {
  const names = ['github.com/a/one', 'github.com/b/two'];
  site(t, recordingD1(names));
  const early = [{ day: '2026-09-30', sessions: 0, messages: 1, records: 1, devices: [ID] }];
  const written = stepSummary(t);
  await assert.rejects(main(names, { read: name => counts(name, name.endsWith('two') ? { days: early } : {}) }), /1 of 2 repos counted but not sent/);
  const out = written();
  assert.ok(out.endsWith('\n2 repos: 1 sent, 1 not sent.\n'), out);
  assert.ok(out.includes('| [b/two](https://github.com/b/two) | not sent: /v1/census: 400 Not counts: totals days. | 1 | 1 | 0 | 1 | 0 | 2 | 0 | 3 | 0 | 0 | 1 |'), out);
  assert.ok(out.includes('| **Total** | 1 sent | 1 | 1 | 0 | 1 | 0 | 2 | 0 | 3 | 0 | 0 | 1 |'), out);
});

test('a dry run says in its summary that it counted and sent nothing', async t => {
  const db = recordingD1(['github.com/a/one']);
  site(t, db);
  const written = stepSummary(t);
  await main(['--dry', 'github.com/a/one'], { read: name => counts(name) });
  const out = written();
  assert.ok(out.includes('| [a/one](https://github.com/a/one) | counted: not sent (--dry) | 1 | 1 | 0 | 1 | 0 | 2 | 0 | 3 | 0 | 0 | 1 |\n'), out);
  assert.ok(out.endsWith('| **Total** | 1 counted | 1 | 1 | 0 | 1 | 0 | 2 | 0 | 3 | 0 | 0 | 1 |\n\n1 repo: 1 counted.\n'), out);
  assert.equal(db.batches.length, 0);
});

test("a reason in the summary stays on its line and in its cell, whatever git or the site said", () => {
  assert.equal(summaryRow({ name: 'github.com/a/one', kind: 'not read', why: 'fatal: one\n| two | <img src=x> [link](https://x) `code` **bold**' }),
    '| [a/one](https://github.com/a/one) | not read: fatal: one \\| two \\| \\<img src=x\\> \\[link\\](https://x) \\`code\\` \\*\\*bold\\*\\* |  |  |  |  |  |  |  |  |  |  |  |\n');
  assert.equal(summaryHead(0, new Date(NOW)), '## Census, 2026-11-09 12:00 UTC\n\nNo repos listed.\n');
  // A name GitHub allows can still hold what Markdown reads: __demo__ would show as bold demo.
  assert.ok(summaryRow({ name: 'github.com/a/__demo__', kind: 'skipped', why: 'x' }).startsWith('| [a/\\_\\_demo\\_\\_](https://github.com/a/__demo__) |'));
});

// Estimates of what an encrypted bus hides (#550).

// An encrypted event as a device writes it, holding an event `size` bytes long: what is encrypted
// is the event's JSON, and AES-GCM adds 16 bytes to it (internal/bus/box.go). The bytes here are
// zeros: the census reads the length and nothing else. The record around the box says nothing,
// not even when: Go writes its empty time as year 1, so the census dates it by its commit.
const boxed = size => event({ v: 1, id: '', type: 'box', from: '', at: '0001-01-01T00:00:00Z', box: { eph: 'e', keys: { team: 'k' }, nonce: 'n', data: Buffer.alloc(size + 16).toString('base64') } });
const AT = '2026-11-05T10:00:00Z';
// The record a bus commit's message holds.
const record = msg => JSON.parse(msg.slice(msg.indexOf('\n\n') + 2));

test("an encrypted record's size is the event's inside it, whatever its base64 padding", () => {
  for (const size of [1, 2, 3, 120, 121, 122, 1090]) assert.equal(boxedSize(record(boxed(size))), size, String(size));
  // Not a box, or not one Murmur wrote: no size, so no estimate.
  for (const r of [{ type: 'message' }, { type: 'box' }, { type: 'box', box: null }, { type: 'box', box: { data: 7 } },
    { type: 'box', box: { data: 'not base64!' } }, { type: 'box', box: { data: 'AAAA' } }, { type: 'message', box: { data: Buffer.alloc(500).toString('base64') } }]) {
    assert.equal(boxedSize(r), null, JSON.stringify(r));
  }
});

test('an encrypted record 180 bytes or more beyond its device ID is an estimated message, and every record is still counted', t => {
  const { dir, commit } = repo(t);
  // dev1 is 4 characters. A stop is about 101 bytes beyond the ID, the longest start seen 171.
  const mesh = 'refs/murmur/mesh/dev1';
  for (const beyond of [101, 145, 171, 179]) commit(mesh, boxed(4 + beyond), { date: AT });
  for (const beyond of [180, 181, 900, 2574]) commit(mesh, boxed(4 + beyond), { date: AT });
  // The same 184 bytes from a device with a 24-character ID are 160 beyond it: a session's start.
  commit('refs/murmur/mesh/a-much-longer-device-id1', boxed(184), { date: AT });
  // An unencrypted message is counted exactly, and never estimated as well.
  commit(mesh, event({ type: 'message', at: AT, body: 'x'.repeat(500) }), { date: AT });
  // A box that isn't one Murmur writes is a record, and nothing more.
  commit(mesh, event({ type: 'box', at: AT, box: { data: '!'.repeat(400) } }), { date: AT });

  const c = count(dir, { now: NOW });
  assert.deepEqual(noBytes(c.totals), { sessions: 0, messages: 1, records: 11, est_sessions: 0, est_messages: 4, first_day: '2026-11-05', last_day: '2026-11-05' });
  assert.deepEqual(c.days.map(d => [d.day, d.messages, d.est_messages, d.records]), [['2026-11-05', 1, 4, 11]]);
});

test("an estimated message is counted on the day of its commit, the only date an encrypted record shows", t => {
  const { dir, commit } = repo(t);
  commit('refs/murmur/mesh/dev1', boxed(500), { date: '2026-11-05T23:59:00Z' });
  commit('refs/murmur/mesh/dev1', boxed(500), { date: '2026-11-06T00:01:00Z' });
  // One whose commit's date can't be believed either isn't counted at all.
  commit('refs/murmur/mesh/dev1', boxed(500), { date: '2099-01-01T00:00:00Z' });
  // A presence is never a message, encrypted or not, however large.
  commit('refs/murmur/presence/dev1', boxed(5000), { date: '2026-11-06T00:01:00Z' });
  const c = count(dir, { now: NOW });
  assert.deepEqual(c.days.map(d => [d.day, d.est_messages, d.records]), [['2026-11-05', 1, 1], ['2026-11-06', 1, 1]]);
});

const named = r => `name\n\n${JSON.stringify({ v: 1, ...r })}\n`;

test('a session the name registry shows and no unencrypted start does is an estimated session, once', t => {
  const { dir, commit } = repo(t);
  const mesh = 'refs/murmur/mesh/dev1', names = 'refs/murmur/names';
  // s1 started in the clear, so it is counted exactly, on the day it started.
  commit(mesh, event({ type: 'session.started', from: 'dev1/s1', at: '2026-11-04T10:00:00Z' }), { date: '2026-11-04T10:00:00Z' });
  commit(names, named({ name: 'quiet-otter', device: 'dev1', session: 's1', at: '2026-11-05T10:00:00Z' }), { date: '2026-11-05T10:00:00Z' });
  // s2 and s3 started on an encrypted log: only the registry shows them.
  commit(mesh, boxed(4 + 145), { date: '2026-11-05T11:00:00Z' });
  commit(names, named({ name: 'brisk-finch', device: 'dev1', session: 's2', at: '2026-11-05T11:00:00Z' }), { date: '2026-11-05T11:00:00Z' });
  commit(names, named({ name: 'calm-heron', device: 'dev2', session: 's3', at: '2026-11-06T09:00:00Z' }), { date: '2026-11-06T09:00:00Z' });
  // s2 named again counts once, on its first day.
  commit(names, named({ name: 'bold-wren', device: 'dev1', session: 's2', at: '2026-11-07T09:00:00Z' }), { date: '2026-11-07T09:00:00Z' });
  // A name set aside names no session, and a person is not an agent session.
  commit(names, named({ name: 'mild-crane', device: 'dev1', at: '2026-11-06T09:00:00Z' }), { date: '2026-11-06T09:00:00Z' });
  commit(names, named({ name: 'ada-lovelace', device: 'dev1', session: 'human', at: '2026-11-06T09:00:00Z' }), { date: '2026-11-06T09:00:00Z' });
  // A snapshot isn't read: it would date every session it lists to its own day.
  commit(names, named({ used: { 'warm-lark': 'dev1/s9' }, at: '2026-11-06T09:00:00Z' }), { date: '2026-11-06T09:00:00Z' });
  // Nor is anything that isn't an address Murmur writes, or a time that can't be believed.
  commit(names, named({ name: 'odd-one', device: 'Dev 1', session: 's4', at: '2026-11-06T09:00:00Z' }), { date: '2026-11-06T09:00:00Z' });
  commit(names, named({ name: 'late-one', device: 'dev1', session: 's5', at: '9999-01-01T00:00:00Z' }), { date: '2099-01-01T00:00:00Z' });

  const c = count(dir, { now: NOW });
  assert.deepEqual(noBytes(c.totals), { sessions: 1, messages: 0, records: 2, est_sessions: 2, est_messages: 0, first_day: '2026-11-04', last_day: '2026-11-06' });
  assert.deepEqual(c.days.map(d => [d.day, d.sessions, d.est_sessions]), [['2026-11-04', 1, 0], ['2026-11-05', 0, 1], ['2026-11-06', 0, 1]]);
  // The registry makes no device: dev2 wrote nothing to a log or a presence.
  assert.equal(c.devices.length, 1);
  assert.deepEqual(c.days.map(d => d.devices.length), [1, 1, 0]);
});

test("census() fetches the name registry too, and counts a repo that has none", async t => {
  const r = remote(t);
  r.commit('refs/murmur/mesh/dev1', boxed(600), { date: r.at });
  r.commit('refs/murmur/names', named({ name: 'quiet-otter', device: 'dev1', session: 's1', at: r.at }));
  const c = await readRepo('github.com/a/one', { url: r.url, lookup: unlinked });
  const day = r.at.slice(0, 10);
  assert.deepEqual(noBytes(c.totals), { sessions: 0, messages: 0, records: 1, est_sessions: 1, est_messages: 1, first_day: day, last_day: day });

  const bare = remote(t);
  bare.commit('refs/murmur/mesh/dev1', boxed(600), { date: bare.at });
  const b = await readRepo('github.com/a/two', { url: bare.url, lookup: unlinked });
  assert.deepEqual(noBytes(b.totals), { sessions: 0, messages: 0, records: 1, est_sessions: 0, est_messages: 1, first_day: day, last_day: day });
});

test("the run's log and summary give the estimates beside the exact counts", async t => {
  const db = recordingD1(['github.com/a/one']);
  const said = site(t, db);
  const written = stepSummary(t);
  const c = counts('github.com/a/one');
  c.totals.est_sessions = 20, c.totals.est_messages = 237, c.totals.bytes = 4100;
  c.days[0].est_sessions = 20, c.days[0].est_messages = 237, c.days[0].bytes = 4100;
  await main(['github.com/a/one'], { read: () => c });
  assert.match(said.log[0], /, 1 sessions and about 20 more estimated, 2 messages and about 237 more estimated, 3 records of 4100 bytes, /);
  assert.ok(written().includes('| [a/one](https://github.com/a/one) | sent | 1 | 1 | 0 | 1 | 20 | 2 | 237 | 3 | 4100 | 0 | 1 |'), written());
  const totals = db.batches.flat().find(s => /INTO repo_census/.test(s.sql));
  assert.deepEqual(totals.args.slice(-3), [20, 237, 4100]);
});

test('the site refuses an estimate that is not a count, and takes a POST with none', async () => {
  const name = 'github.com/a/one';
  const status = async c => {
    const res = await post(recordingD1([name]), c);
    return [res.status, (await res.text()).trim()];
  };
  const withDay = est => counts(name, { days: [{ day: daysAgo(1), sessions: 1, messages: 2, records: 3, devices: [ID], ...est }] });
  const withTotals = est => { const c = counts(name); Object.assign(c.totals, est); return c; };
  assert.equal((await status(counts(name)))[0], 200);
  assert.equal((await status(withDay({ est_sessions: 0, est_messages: 9 })))[0], 200);
  for (const bad of [-1, 1.5, '3', null]) {
    assert.deepEqual(await status(withDay({ bytes: bad })), [400, 'Not counts: a day.'], String(bad));
    assert.deepEqual(await status(withTotals({ bytes: bad })), [400, 'Not counts: totals.'], String(bad));
    assert.deepEqual(await status(withDay({ est_messages: bad })), [400, 'Not counts: a day.'], String(bad));
    assert.deepEqual(await status(withTotals({ est_sessions: bad })), [400, 'Not counts: totals.'], String(bad));
  }
});

test('estimates are kept beside the exact counts, only ever raised, and left alone by a census that sends none', { skip: !sqlite && 'no node:sqlite in this node' }, async () => {
  const db = await sqliteD1();
  const name = 'github.com/a/one';
  db.sql.exec(`INSERT INTO repos (repo, name, first) VALUES ('${name}', '${name}', '${daysAgo(3)}')`);
  // A row from before the estimates: its columns are 0.
  db.sql.exec(`INSERT INTO repo_days (repo, day, sessions, messages, records) VALUES ('${name}', '${daysAgo(2)}', 4, 5, 6)`);
  const send = async (est, totals) => {
    const c = counts(name, { days: [{ day: daysAgo(1), sessions: 1, messages: 2, records: 30, devices: [ID], ...est }] });
    Object.assign(c.totals, { records: 30, ...totals });
    assert.equal((await post(db, c)).status, 200);
  };
  const day = () => ({ ...db.sql.prepare('SELECT sessions, messages, records, est_sessions, est_messages FROM repo_days WHERE day = ?').get(daysAgo(1)) });
  const totals = () => ({ ...db.sql.prepare('SELECT sessions, messages, records, est_sessions, est_messages FROM repo_census').get() });
  await send({ est_sessions: 3, est_messages: 20 }, { est_sessions: 3, est_messages: 20 });
  assert.deepEqual(day(), { sessions: 1, messages: 2, records: 30, est_sessions: 3, est_messages: 20 });
  assert.deepEqual(totals(), { sessions: 1, messages: 2, records: 30, est_sessions: 3, est_messages: 20 });
  // A census from before the estimates, or a rewritten log: nothing is lowered.
  await send({}, {});
  await send({ est_sessions: 1, est_messages: 25 }, { est_sessions: 1, est_messages: 25 });
  assert.deepEqual(day(), { sessions: 1, messages: 2, records: 30, est_sessions: 3, est_messages: 25 });
  assert.deepEqual(totals(), { sessions: 1, messages: 2, records: 30, est_sessions: 3, est_messages: 25 });

  const n = await repoNumbers(db, 14);
  // The exact counts are what they were; the estimates are beside them.
  assert.deepEqual([n.sessions, n.estSessions, n.messages, n.estMessages, n.records], [1, 3, 2, 25, 30]);
  assert.deepEqual([n.weekSessions, n.weekEstSessions, n.weekMessages, n.weekEstMessages], [5, 3, 7, 25]);
  const { sessions, estSessions, messages, estMessages } = n.repoList[0];
  assert.deepEqual([sessions, estSessions, messages, estMessages], [1, 3, 2, 25]);
  const last = n.series.at(-2);
  assert.deepEqual([last.day, last.sessions, last.estSessions, last.messages, last.estMessages], [daysAgo(1), 1, 3, 2, 25]);
  const before = n.byRepo.find(r => r.day === daysAgo(2));
  assert.deepEqual([before.sessions, before.estSessions, before.messages, before.estMessages], [4, 0, 5, 0]);

  // The report marks what includes an estimate, says how much of it is one, and how it is made.
  const md = repoReport(n, 'https://murmur.invalid');
  assert.ok(md.includes('| Agent sessions | ≈4 |\n| Of those sessions, estimated (see below) | 3 |'), md);
  assert.ok(md.includes('| Messages between agents | ≈27 |\n| Of those messages, estimated (see below) | 25 |'), md);
  assert.ok(md.includes(`| Messages in the last ${n.week} days | ≈32 |`), md);
  assert.match(md, /\| \[a\/one\]\(https:\/\/github\.com\/a\/one\) \| 0 \| 1 \| 1 \| 30 \| 0 B \| ≈4 \| ≈27 \|/);
  assert.match(md, /- A figure marked \*\*≈\*\* includes an estimate\./);
});

test('a report with nothing estimated marks nothing', { skip: !sqlite && 'no node:sqlite in this node' }, async () => {
  const db = await sqliteD1();
  const name = 'github.com/a/one';
  db.sql.exec(`INSERT INTO repos (repo, name, first) VALUES ('${name}', '${name}', '${daysAgo(3)}')`);
  assert.equal((await post(db, counts(name))).status, 200);
  const md = repoReport(await repoNumbers(db, 14), 'https://murmur.invalid');
  assert.ok(md.includes('| Agent sessions | 1 |\n| Of those sessions, estimated (see below) | 0 |'), md);
  assert.doesNotMatch(md.slice(0, md.indexOf('## What the numbers mean')), /≈/);
});

// What is measured exactly, encrypted or not (#550): the bytes the devices wrote.

test("a bus's bytes are its events' sizes as JSON, the same encrypted or not, by day", t => {
  const { dir, commit } = repo(t);
  const mesh = 'refs/murmur/mesh/dev1';
  const clear = { v: 1, type: 'message', at: '2026-11-05T10:00:00Z', subject: 'héllo', body: 'x'.repeat(300) };
  const stop = { v: 1, type: 'session.stopped', at: '2026-11-06T10:00:00Z' };
  commit(mesh, event(clear), { date: '2026-11-05T10:00:00Z' });
  // The same event encrypted weighs what it did: the size of the event inside the box, not of the
  // box, its keys and its base64.
  commit(mesh, boxed(Buffer.byteLength(JSON.stringify(clear))), { date: '2026-11-05T11:00:00Z' });
  commit(mesh, event(stop), { date: '2026-11-06T10:00:00Z' });
  // A box that isn't one Murmur writes weighs what its record does.
  const junk = { type: 'box', at: '2026-11-06T11:00:00Z', box: { data: '!!' } };
  commit(mesh, event(junk), { date: '2026-11-06T11:00:00Z' });
  // A presence is no record, so it adds no bytes.
  commit('refs/murmur/presence/dev1', event({ owner: 'Ann', at: '2026-11-06T10:00:00Z', host: 'x'.repeat(500) }), { date: '2026-11-06T10:00:00Z' });

  const one = Buffer.byteLength(JSON.stringify(clear));
  const c = count(dir, { now: NOW });
  assert.deepEqual(c.days.map(d => [d.day, d.records, d.bytes]), [
    ['2026-11-05', 2, 2 * one],
    ['2026-11-06', 2, Buffer.byteLength(JSON.stringify(stop)) + Buffer.byteLength(JSON.stringify(junk))],
  ]);
  assert.equal(c.totals.bytes, c.days[0].bytes + c.days[1].bytes);
  assert.equal(one, JSON.stringify(clear).length + 1, 'bytes, not characters: é is two');
});

test('bytes are kept, only ever raised, and shown as sizes beside the records', { skip: !sqlite && 'no node:sqlite in this node' }, async () => {
  const db = await sqliteD1();
  const name = 'github.com/a/one';
  db.sql.exec(`INSERT INTO repos (repo, name, first) VALUES ('${name}', '${name}', '${daysAgo(3)}')`);
  const send = async bytes => {
    const c = counts(name, { days: [{ day: daysAgo(1), sessions: 1, messages: 2, records: 3, devices: [ID], ...bytes }] });
    Object.assign(c.totals, bytes);
    assert.equal((await post(db, c)).status, 200);
  };
  await send({ bytes: 123456 });
  // A census from before bytes sends none, and a rewritten log fewer: what is kept stays.
  await send({});
  await send({ bytes: 500 });
  assert.equal(db.sql.prepare('SELECT bytes FROM repo_days').get().bytes, 123456);
  assert.equal(db.sql.prepare('SELECT bytes FROM repo_census').get().bytes, 123456);

  const n = await repoNumbers(db, 14);
  assert.deepEqual([n.records, n.weekRecords, n.bytes, n.weekBytes], [3, 3, 123456, 123456]);
  assert.equal(n.repoList[0].bytes, 123456);
  assert.equal(n.series.at(-2).bytes, 123456);
  assert.equal(n.byRepo[0].bytes, 123456);
  const md = repoReport(n, 'https://murmur.invalid');
  assert.ok(md.includes('| Bus records | 3 |\n| Bus records in the last 7 days | 3 |\n| Data in those records | 123 kB |\n| Data in the last 7 days | 123 kB |'), md);
  assert.match(md, /\| \[a\/one\]\(https:\/\/github\.com\/a\/one\) \| 0 \| 1 \| 1 \| 3 \| 123 kB \| 1 \| 2 \|/);
});

test('a size reads as people write one', () => {
  assert.deepEqual([0, 999, 1000, 12345, 123456, 999999, 4560000, 2.5e9, undefined].map(size),
    ['0 B', '999 B', '1.00 kB', '12.3 kB', '123 kB', '1000 kB', '4.56 MB', '2.50 GB', '0 B']);
});
