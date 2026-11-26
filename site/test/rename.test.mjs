// Repos by GitHub's repository ID (#1250): the census finds each repo on GitHub, reads it by its
// current name, and the site renames the row, or merges two rows that turn out to be one repo.
// `node --test --import ./site/test/register.mjs site/test/rename.test.mjs`; GitHub and the site
// are stand-ins.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { timingSafeEqual } from 'node:crypto';
import { census as handle, repoNumbers } from '../src/repos.js';
import { sqliteD1 } from './d1.mjs';

crypto.subtle.timingSafeEqual ??= (a, b) => timingSafeEqual(a, b);
process.env.CENSUS_SITE = 'https://census.invalid';
process.env.CENSUS_KEY = 'test-key';
delete process.env.GITHUB_STEP_SUMMARY;
const { main, findRepo } = await import('../census/census.mjs');

const sqlite = await sqliteD1();
const skip = !sqlite && 'no node:sqlite in this node';
const daysAgo = n => new Date(Date.now() - n * 864e5).toISOString().slice(0, 10);
const hex = c => c.repeat(16);

// One repo's counts, as census() makes them, for the repo named, with these devices and contributors.
const counts = (repo, { devices = [hex('a')], contributors = [], records = 3, day = daysAgo(1) } = {}) => ({
  repo: repo.toLowerCase(),
  days: [{ day, sessions: 1, messages: 1, records, devices }],
  devices: devices.map(id => ({ id, person: null })),
  totals: { sessions: 1, messages: 1, records, first_day: day, last_day: day },
  contributors: contributors.map(id => ({ id, first: '2026-10-01' })),
});

// Sends the census's requests to the site's handler on db, and keeps what was logged.
function site(t, db) {
  t.mock.method(globalThis, 'fetch', async (url, init) => handle(new Request(url, init), { DB: db, CENSUS_KEY: process.env.CENSUS_KEY }));
  const said = { log: [], error: [] };
  t.mock.method(console, 'log', s => said.log.push(String(s)));
  t.mock.method(console, 'error', s => said.error.push(String(s)));
  return said;
}

const rows = (db, sql) => db.sql.prepare(sql).all().map(r => ({ ...r }));

test('a repo listed under an old name is read by its current one, and the row takes the new name and ID', { skip }, async t => {
  const db = await sqliteD1();
  db.sql.exec(`INSERT INTO repos (repo, name, first) VALUES ('github.com/a-fig/t4', 'github.com/a-Fig/t4', '2026-10-30')`);
  db.sql.exec(`INSERT INTO repo_names (name, repo) VALUES ('github.com/a-fig/t4', 'github.com/a-fig/t4')`);
  site(t, db);
  const read = [];
  const find = async r => (assert.equal(r.id, null), { id: 1435080335, name: 'a-Fig/murmur' });
  await main([], { read: async (name, opts) => (read.push([name, opts.aliases]), counts(name)), find, downloads: async () => [] });
  assert.deepEqual(read, [['github.com/a-Fig/murmur', ['github.com/a-Fig/t4']]]);
  assert.deepEqual(rows(db, 'SELECT repo, name, github_id FROM repos'), [{ repo: 'github.com/a-fig/t4', name: 'github.com/a-Fig/murmur', github_id: 1435080335 }]);
  assert.equal(rows(db, 'SELECT * FROM repo_census')[0].repo, 'github.com/a-fig/t4');

  // The next run finds it by its ID, and tells the census its old name, for #1244's fingerprint.
  const asked = [];
  await main([], { read: async (name, opts) => (read.push([name, opts.aliases]), counts(name)), find: async r => (asked.push(r), { id: r.id, name: 'a-Fig/murmur' }), downloads: async () => [] });
  assert.deepEqual(asked.map(r => [r.key, r.id, r.aliases]), [['github.com/a-fig/t4', 1435080335, ['github.com/a-fig/t4']]]);
  assert.deepEqual(read[1], ['github.com/a-Fig/murmur', ['github.com/a-fig/t4']]);
});

test('two rows that turn out to be one repo merge: larger counts, joined sets, the earlier first day', { skip }, async t => {
  const db = await sqliteD1();
  db.sql.exec(`INSERT INTO repos (repo, name, first) VALUES ('github.com/a-fig/t4', 'github.com/a-Fig/t4', '2026-10-30'), ('github.com/a-fig/murmur', 'github.com/a-Fig/murmur', '2026-11-05')`);
  db.sql.exec(`INSERT INTO repo_names (name, repo) VALUES ('github.com/a-fig/t4', 'github.com/a-fig/t4'), ('github.com/a-fig/murmur', 'github.com/a-fig/murmur')`);
  const post = c => handle(new Request('https://census.invalid/v1/census', {
    method: 'POST', headers: { Authorization: `Bearer ${process.env.CENSUS_KEY}` }, body: JSON.stringify(c),
  }), { DB: db, CENSUS_KEY: process.env.CENSUS_KEY });
  const gh = { id: 1435080335, name: 'a-Fig/murmur' };
  // Each row counted the same bus before either had an ID: the old one saw more of it.
  assert.equal((await post({ ...counts('github.com/a-fig/t4', { devices: [hex('a'), hex('b')], contributors: [hex('c')], records: 9 }), key: 'github.com/a-fig/t4' })).status, 200);
  assert.equal((await post({ ...counts('github.com/a-fig/murmur', { devices: [hex('a'), hex('d')], contributors: [hex('c'), hex('e')], records: 5 }), key: 'github.com/a-fig/murmur', github: gh })).status, 200);
  // Now the old row learns it is the same repo, and is merged into the one that has the ID.
  assert.equal((await post({ ...counts('github.com/a-fig/murmur', { records: 4 }), key: 'github.com/a-fig/t4', github: gh })).status, 200);
  assert.deepEqual(rows(db, 'SELECT repo, name, first, github_id FROM repos'), [{ repo: 'github.com/a-fig/murmur', name: 'github.com/a-Fig/murmur', first: '2026-10-30', github_id: 1435080335 }]);
  assert.deepEqual(rows(db, 'SELECT records FROM repo_days'), [{ records: 9 }]);
  assert.deepEqual(rows(db, 'SELECT records FROM repo_census'), [{ records: 9 }]);
  assert.deepEqual(rows(db, 'SELECT device FROM devices ORDER BY device').map(r => r.device), [hex('a'), hex('b'), hex('d')]);
  assert.deepEqual(rows(db, 'SELECT id FROM contributors ORDER BY id').map(r => r.id), [hex('c'), hex('e')]);
  assert.deepEqual(rows(db, 'SELECT name, repo FROM repo_names ORDER BY name'), [
    { name: 'github.com/a-fig/murmur', repo: 'github.com/a-fig/murmur' },
    { name: 'github.com/a-fig/t4', repo: 'github.com/a-fig/murmur' },
  ]);
  for (const table of ['repo_days', 'devices', 'device_days', 'contributors', 'repo_census']) {
    assert.equal(rows(db, `SELECT COUNT(*) AS n FROM ${table} WHERE repo = 'github.com/a-fig/t4'`)[0].n, 0, table);
  }
  const n = await repoNumbers(db, 14);
  assert.equal(n.repos, 1);
  assert.equal(n.repoList[0].name, 'github.com/a-Fig/murmur');
});

test("a row GitHub already gave another ID isn't renamed by an answer about a different repo", { skip }, async () => {
  const db = await sqliteD1();
  db.sql.exec(`INSERT INTO repos (repo, name, first, github_id) VALUES ('github.com/a-fig/t4', 'github.com/a-Fig/murmur', '2026-10-30', 1435080335)`);
  db.sql.exec(`INSERT INTO repo_names (name, repo) VALUES ('github.com/a-fig/t4', 'github.com/a-fig/t4')`);
  const res = await handle(new Request('https://census.invalid/v1/census', {
    method: 'POST', headers: { Authorization: `Bearer ${process.env.CENSUS_KEY}` },
    body: JSON.stringify({ ...counts('github.com/a-fig/t4'), key: 'github.com/a-fig/t4', github: { id: 7, name: 'a-Fig/t4' } }),
  }), { DB: db, CENSUS_KEY: process.env.CENSUS_KEY });
  assert.equal(res.status, 200);
  assert.deepEqual(rows(db, 'SELECT name, github_id FROM repos'), [{ name: 'github.com/a-Fig/murmur', github_id: 1435080335 }]);
});

test('a repo GitHub says is gone keeps its counts, and is not read', { skip }, async t => {
  const db = await sqliteD1();
  db.sql.exec(`INSERT INTO repos (repo, name, first, github_id) VALUES ('github.com/a/gone', 'github.com/a/gone', '2026-10-30', 5)`);
  const said = site(t, db);
  let read = 0;
  await assert.rejects(main([], { read: async () => (read++, counts('github.com/a/gone')), find: async () => false, downloads: async () => [] }), /no repo could be counted/);
  assert.equal(read, 0);
  assert.match(said.error[0], /^github\.com\/a\/gone: not counted: GitHub has no such public repo/);
});

test('when GitHub cannot say, the repo is read by the name it was listed under, as before', { skip }, async t => {
  const db = await sqliteD1();
  db.sql.exec(`INSERT INTO repos (repo, name, first) VALUES ('github.com/a/one', 'github.com/a/one', '2026-10-30')`);
  site(t, db);
  const read = [];
  await main([], { read: async name => (read.push(name), counts(name)), find: async () => null, downloads: async () => [] });
  assert.deepEqual(read, ['github.com/a/one']);
  assert.deepEqual(rows(db, 'SELECT github_id FROM repos'), [{ github_id: null }]);
});

test('the census asks GitHub by ID when it has one, else by name, and follows a rename', async t => {
  const asked = [];
  t.mock.method(globalThis, 'fetch', async url => {
    asked.push(String(url));
    if (String(url).endsWith('/repos/a/gone')) return new Response('{}', { status: 404 });
    if (String(url).endsWith('/repos/a/busy')) return new Response('{}', { status: 403 });
    return Response.json({ id: 1435080335, full_name: 'a-Fig/murmur', private: false });
  });
  assert.deepEqual(await findRepo({ id: 1435080335, name: 'github.com/a-Fig/t4' }), { id: 1435080335, name: 'a-Fig/murmur' });
  assert.deepEqual(await findRepo({ name: 'github.com/a-Fig/t4' }), { id: 1435080335, name: 'a-Fig/murmur' });
  assert.equal(await findRepo({ name: 'github.com/a/gone' }), false);
  assert.equal(await findRepo({ name: 'github.com/a/busy' }), null);
  assert.deepEqual(asked.slice(0, 2), ['https://api.github.com/repositories/1435080335', 'https://api.github.com/repos/a-Fig/t4']);
});
