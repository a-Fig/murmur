// POST /v1/repo (src/usage.js, reportRepo): what it asks GitHub, what it stores, and what it
// refuses. `node --test --import ./site/test/register.mjs site/test/repo.test.mjs`; GitHub is a
// stand-in, so it needs no network.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { reportRepo } from '../src/usage.js';
import { sqliteD1 } from './d1.mjs';

// fetch as reportRepo sees it: GitHub's API says every repo is public, and each call is counted.
function gitHub() {
  const calls = [];
  const real = globalThis.fetch;
  globalThis.fetch = async url => {
    calls.push(String(url));
    return Response.json({ private: false });
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
  const statement = sql => ({
    bind: (...args) => ({
      async first() { looked.push(sql); throw new Error('D1 is down'); },
      async run() { stored.push({ sql, args }); return { success: true }; },
    }),
  });
  const res = await post({ prepare: statement }, JSON.stringify({ repo: 'github.com/ada/engine' }));
  assert.equal(res.status, 200);
  // It looked in D1 first, and that failing didn't stop it.
  assert.deepEqual(looked, ['SELECT 1 FROM repos WHERE repo = ?1']);
  assert.equal(gh.calls.length, 1);
  assert.equal(stored.length, 1);
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
