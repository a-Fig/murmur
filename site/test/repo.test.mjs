// POST /v1/repo (src/usage.js, reportRepo): what it asks GitHub, what it stores, and what it
// refuses. `node --test --import ./site/test/register.mjs site/test/repo.test.mjs`; GitHub is a
// stand-in, so it needs no network.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { reportRepo } from '../src/usage.js';
import { sqliteD1 } from './d1.mjs';

// fetch as reportRepo sees it: GitHub's API says every repo is public, and each call is counted.
// `repos` maps an owner/name to what the API answers for it, an ID and current name, as GitHub
// answers a renamed repo's old name by following its redirect.
function gitHub(repos = {}) {
  const calls = [];
  const real = globalThis.fetch;
  globalThis.fetch = async url => {
    calls.push(String(url));
    const at = /repos\/(.*)$/.exec(String(url))?.[1];
    return Response.json({ private: false, ...(repos[at] ?? {}) });
  };
  return { calls, restore: () => { globalThis.fetch = real; } };
}

const post = (db, body, type = 'application/json') => reportRepo(new Request('https://murmur.invalid/v1/repo', {
  method: 'POST', headers: type ? { 'Content-Type': type } : {}, body,
}), { DB: db });

const sqlite = await sqliteD1();
const skip = !sqlite && 'no node:sqlite in this node';

test('a repo already stored is answered from D1, without asking GitHub again', { skip }, async t => {
  const gh = gitHub();
  t.after(gh.restore);
  const db = await sqliteD1();
  const body = JSON.stringify({ repo: 'github.com/Ada/Engine' });
  for (let i = 0; i < 3; i++) {
    const res = await post(db, body);
    assert.equal(res.status, 200);
    assert.deepEqual(await res.json(), { ok: true });
  }
  // The first POST asked GitHub and stored the repo; the next two found it stored.
  assert.deepEqual(gh.calls, ['https://api.github.com/repos/Ada/Engine']);
  const rows = db.sql.prepare('SELECT repo, name FROM repos').all().map(r => ({ ...r }));
  assert.deepEqual(rows, [{ repo: 'github.com/ada/engine', name: 'github.com/Ada/Engine' }]);
  // Another spelling of the same repo is the same row, so it doesn't ask either.
  assert.equal((await post(db, JSON.stringify({ repo: 'github.com/ada/engine' }))).status, 200);
  assert.equal(gh.calls.length, 1);
});

test("a repo D1 can't look up is checked with GitHub, as a new one is", async t => {
  const gh = gitHub();
  t.after(gh.restore);
  const stored = [], looked = [];
  // D1 fails the first lookup, the one that would answer without asking GitHub, and no other.
  const statement = sql => ({
    sql,
    bind: (...args) => ({
      sql, args,
      async first() {
        looked.push(sql);
        if (looked.length === 1) throw new Error('D1 is down');
        return null;
      },
      async run() { stored.push({ sql, args }); return { success: true }; },
    }),
  });
  const db = { prepare: statement, async batch(stmts) { stored.push(...stmts); return stmts.map(() => ({ results: [] })); } };
  const res = await post(db, JSON.stringify({ repo: 'github.com/ada/engine' }));
  assert.equal(res.status, 200);
  // It looked in D1 first, and that failing didn't stop it.
  assert.equal(looked[0], 'SELECT 1 FROM repos WHERE lower(name) = ?1');
  assert.equal(gh.calls.length, 1);
  assert.match(stored[0].sql, /^\s*INSERT INTO repos/);
});

test('a POST that is not application/json is refused before anything is read or asked', { skip }, async t => {
  const gh = gitHub();
  t.after(gh.restore);
  const db = await sqliteD1();
  const body = JSON.stringify({ repo: 'github.com/ada/engine' });
  // What a web page can send from a visitor's browser without a preflight, and no type at all.
  for (const type of ['text/plain', 'text/plain;charset=UTF-8', 'application/x-www-form-urlencoded', 'multipart/form-data', null]) {
    const res = await post(db, body, type);
    assert.equal(res.status, 415, `${type}`);
    assert.equal(res.headers.get('Accept'), 'application/json');
  }
  assert.equal(gh.calls.length, 0);
  assert.equal(db.sql.prepare('SELECT COUNT(*) AS n FROM repos').get().n, 0);
  // A charset, or another case, is still JSON.
  assert.equal((await post(db, body, 'Application/JSON; charset=utf-8')).status, 200);
});

test("an old address reported while GitHub gives no ID waits for one, unless its repo has no ID either", { skip }, async t => {
  const db = await sqliteD1();
  // a-Fig/t4, stored with its ID, was renamed to a-Fig/murmur; old-only has no ID yet.
  db.sql.exec(`INSERT INTO repos (repo, name, first, github_id) VALUES ('github.com/a-fig/t4', 'github.com/a-Fig/murmur', '2026-11-01', 1435080335), ('github.com/b/old-only', 'github.com/b/old-only', '2026-11-01', NULL)`);
  db.sql.exec(`INSERT INTO repo_names (name, repo) VALUES ('github.com/a-fig/t4', 'github.com/a-fig/t4'), ('github.com/a-fig/murmur', 'github.com/a-fig/t4'), ('github.com/b/old-only', 'github.com/b/old-only'), ('github.com/b/was', 'github.com/b/old-only')`);
  // The API can't answer; git says public, with no ID.
  const real = globalThis.fetch;
  globalThis.fetch = async url => (String(url).includes('api.github.com')
    ? new Response('', { status: 403 })
    : new Response('', { status: 200, headers: { 'Content-Type': 'application/x-git-upload-pack-advertisement' } }));
  t.after(() => { globalThis.fetch = real; });
  // A new repo may have taken a-Fig/t4, and nothing would ask about it again: try later.
  assert.equal((await post(db, JSON.stringify({ repo: 'github.com/a-Fig/t4' }))).status, 503);
  // b/was belongs to a repo the census asks about by that name each day: ok.
  assert.equal((await post(db, JSON.stringify({ repo: 'github.com/b/was' }))).status, 200);
  assert.equal(db.sql.prepare('SELECT COUNT(*) AS n FROM repos').get().n, 2);
});

// The real rename here: a-Fig/t4 became a-Fig/murmur, and GitHub's API answers the old name with
// the repo under its new one.
const MURMUR = { id: 1435080335, full_name: 'a-Fig/murmur' };

test('a repo is stored with its GitHub ID and current name, and an old address finds the same row', { skip }, async t => {
  const gh = gitHub({ 'a-Fig/t4': MURMUR, 'a-Fig/murmur': MURMUR });
  t.after(gh.restore);
  const db = await sqliteD1();
  assert.equal((await post(db, JSON.stringify({ repo: 'github.com/a-Fig/t4' }))).status, 200);
  assert.equal((await post(db, JSON.stringify({ repo: 'github.com/a-Fig/murmur' }))).status, 200);
  const rows = db.sql.prepare('SELECT repo, name, github_id FROM repos').all().map(r => ({ ...r }));
  assert.deepEqual(rows, [{ repo: 'github.com/a-fig/t4', name: 'github.com/a-Fig/murmur', github_id: 1435080335 }]);
  const names = db.sql.prepare('SELECT name, repo FROM repo_names ORDER BY name').all().map(r => ({ ...r }));
  assert.deepEqual(names, [
    { name: 'github.com/a-fig/murmur', repo: 'github.com/a-fig/t4' },
    { name: 'github.com/a-fig/t4', repo: 'github.com/a-fig/t4' },
  ]);
  // The current name is answered from D1 now; the old one only by asking GitHub.
  const asked = gh.calls.length;
  assert.equal((await post(db, JSON.stringify({ repo: 'github.com/a-fig/murmur' }))).status, 200);
  assert.equal(gh.calls.length, asked);
});

test('a new repo at a renamed repo\'s old address is a repo of its own, under a key of its own', { skip }, async t => {
  const gh = gitHub({ 'a-Fig/murmur': MURMUR });
  t.after(gh.restore);
  const db = await sqliteD1();
  // Stored as a-Fig/t4 before the rename, and renamed since.
  db.sql.exec(`INSERT INTO repos (repo, name, first, github_id) VALUES ('github.com/a-fig/t4', 'github.com/a-Fig/murmur', '2026-11-01', 1435080335)`);
  db.sql.exec(`INSERT INTO repo_names (name, repo) VALUES ('github.com/a-fig/t4', 'github.com/a-fig/t4'), ('github.com/a-fig/murmur', 'github.com/a-fig/t4')`);
  gh.restore();
  const gh2 = gitHub({ 'a-Fig/t4': { id: 7, full_name: 'a-Fig/t4' } });
  t.after(gh2.restore);
  assert.equal((await post(db, JSON.stringify({ repo: 'github.com/a-Fig/t4' }))).status, 200);
  const rows = db.sql.prepare('SELECT repo, name, github_id FROM repos ORDER BY repo').all().map(r => ({ ...r }));
  assert.deepEqual(rows, [
    { repo: 'gh:7', name: 'github.com/a-Fig/t4', github_id: 7 },
    { repo: 'github.com/a-fig/t4', name: 'github.com/a-Fig/murmur', github_id: 1435080335 },
  ]);
});
