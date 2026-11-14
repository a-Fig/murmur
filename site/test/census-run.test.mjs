// The stats page's run button (src/census-run.js, #557): it dispatches the census workflow, and
// its rate limit — one manual run per 15 minutes, 10 per UTC day — is counted in D1. Run with the
// site's tests: `node --test --import ./site/test/register.mjs site/test/*.test.mjs`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { timingSafeEqual } from 'node:crypto';
import { runCensus, PER_DAY } from '../src/census-run.js';
import { stats } from '../src/usage.js';
import { sqliteD1 } from './d1.mjs';

// The Workers runtime adds this to Web Crypto; node has it only in node:crypto.
crypto.subtle.timingSafeEqual ??= (a, b) => timingSafeEqual(a, b);

const sqlite = await sqliteD1();
const skip = { skip: !sqlite && 'no node:sqlite in this node' };
const NOW = Date.parse('2026-11-14T12:00:00.000Z');
const HEADERS = { 'Cache-Control': 'no-store' };

const post = (headers = {}) => new Request('https://murmur.afig.dev/stats/run-census', {
  method: 'POST', headers: { 'Content-Type': 'application/json', ...headers }, body: '{}',
});

// Stubs GitHub for one test and puts the real fetch back, whatever happens.
async function withFetch(answer, fn) {
  const real = globalThis.fetch;
  const calls = [];
  globalThis.fetch = async (url, init) => {
    calls.push({ url, init });
    if (answer instanceof Error) throw answer;
    return answer;
  };
  try {
    return await fn(calls);
  } finally {
    globalThis.fetch = real;
  }
}

const rows = async db => (await db.prepare('SELECT COUNT(*) AS n FROM census_runs').first()).n;
const seed = async (db, at) =>
  db.prepare('INSERT INTO census_runs (id, at) VALUES (?1, ?2)').bind(crypto.randomUUID(), at).run();

test('a run dispatches the census workflow on main, once, with the run token', skip, async () => {
  const db = await sqliteD1();
  await withFetch(new Response(null, { status: 204 }), async calls => {
    const res = await runCensus(post(), { DB: db, CENSUS_RUN_TOKEN: 'run-token' }, HEADERS, { now: NOW });
    assert.equal(res.status, 202);
    const body = await res.json();
    assert.equal(body.ok, true);
    assert.equal(body.runsToday, 1);
    assert.equal(body.perDay, PER_DAY);
    assert.match(body.message, /^Census started \(1 of 10 manual runs today\)/);
    assert.equal(calls.length, 1);
    assert.equal(calls[0].url, 'https://api.github.com/repos/a-Fig/murmur/actions/workflows/census.yml/dispatches');
    assert.equal(calls[0].init.method, 'POST');
    assert.equal(calls[0].init.headers.Authorization, 'Bearer run-token');
    assert.deepEqual(JSON.parse(calls[0].init.body), { ref: 'main' });
  });
  assert.equal(await rows(db), 1);
});

test('without CENSUS_RUN_TOKEN nothing is dispatched and no slot is spent', skip, async () => {
  const db = await sqliteD1();
  await withFetch(new Response(null, { status: 204 }), async calls => {
    const res = await runCensus(post(), { DB: db }, HEADERS, { now: NOW });
    assert.equal(res.status, 503);
    assert.match((await res.json()).message, /no CENSUS_RUN_TOKEN/);
    assert.equal(calls.length, 0);
  });
  assert.equal(await rows(db), 0);
});

test('a second run within 15 minutes is refused and dispatches nothing', skip, async () => {
  const db = await sqliteD1();
  await seed(db, '2026-11-14T11:55:00.000Z');
  await withFetch(new Response(null, { status: 204 }), async calls => {
    const res = await runCensus(post(), { DB: db, CENSUS_RUN_TOKEN: 'run-token' }, HEADERS, { now: NOW });
    assert.equal(res.status, 429);
    assert.equal(res.headers.get('Retry-After'), '600');
    const body = await res.json();
    assert.equal(body.runsToday, 1);
    assert.equal(body.retryAfterSeconds, 600);
    assert.match(body.message, /try again in 10 minutes/);
    assert.equal(calls.length, 0);
  });
  assert.equal(await rows(db), 1);
});

test("a slot freed while a refused run is being answered doesn't make its wait NaN", skip, async () => {
  const db = await sqliteD1();
  await seed(db, '2026-11-14T11:55:00.000Z');
  // The blocking run's dispatch fails and frees its slot after this run's INSERT was refused but
  // before its state is read: the day's rows vanish under it, as the review of #557 pointed out.
  const gone = {
    ...db,
    prepare(sql) {
      if (sql.includes('COUNT(*) AS n FROM census_runs')) db.sql.exec('DELETE FROM census_runs');
      return db.prepare(sql);
    },
  };
  await withFetch(new Response(null, { status: 204 }), async calls => {
    const res = await runCensus(post(), { DB: gone, CENSUS_RUN_TOKEN: 'run-token' }, HEADERS, { now: NOW });
    assert.equal(res.status, 429);
    assert.equal(res.headers.get('Retry-After'), '1');
    assert.match((await res.json()).message, /try again in 1 minute/);
    assert.equal(calls.length, 0);
  });
});

test('a run 15 minutes after the last one is allowed', skip, async () => {
  const db = await sqliteD1();
  await seed(db, '2026-11-14T11:45:00.000Z');
  await withFetch(new Response(null, { status: 204 }), async calls => {
    const res = await runCensus(post(), { DB: db, CENSUS_RUN_TOKEN: 'run-token' }, HEADERS, { now: NOW });
    assert.equal(res.status, 202);
    assert.equal((await res.json()).runsToday, 2);
    assert.equal(calls.length, 1);
  });
});

test('the tenth run of a UTC day is the last: the eleventh is refused until midnight', skip, async () => {
  const db = await sqliteD1();
  for (let h = 1; h <= PER_DAY; h++) await seed(db, `2026-11-14T${String(h).padStart(2, '0')}:00:00.000Z`);
  await withFetch(new Response(null, { status: 204 }), async calls => {
    const res = await runCensus(post(), { DB: db, CENSUS_RUN_TOKEN: 'run-token' }, HEADERS, { now: NOW });
    assert.equal(res.status, 429);
    assert.equal(res.headers.get('Retry-After'), '43200');
    const body = await res.json();
    assert.equal(body.runsToday, PER_DAY);
    assert.match(body.message, /10 manual runs already today/);
    assert.equal(calls.length, 0);
  });
  assert.equal(await rows(db), PER_DAY);
});

test("yesterday's runs spend none of today's quota", skip, async () => {
  const db = await sqliteD1();
  for (let i = 0; i < PER_DAY; i++) await seed(db, '2026-11-13T23:00:00.000Z');
  await withFetch(new Response(null, { status: 204 }), async calls => {
    const res = await runCensus(post(), { DB: db, CENSUS_RUN_TOKEN: 'run-token' }, HEADERS, { now: NOW });
    assert.equal(res.status, 202);
    assert.equal(calls.length, 1);
  });
});

test('a dispatch GitHub refuses frees the slot, so a retry can start a run', skip, async () => {
  const db = await sqliteD1();
  const env = { DB: db, CENSUS_RUN_TOKEN: 'run-token' };
  await withFetch(new Response('no', { status: 403 }), async calls => {
    const res = await runCensus(post(), env, HEADERS, { now: NOW });
    assert.equal(res.status, 502);
    assert.match((await res.json()).message, /didn't start the census \(403\)/);
    assert.equal(calls.length, 1);
  });
  assert.equal(await rows(db), 0);
  await withFetch(new Response(null, { status: 204 }), async () => {
    assert.equal((await runCensus(post(), env, HEADERS, { now: NOW })).status, 202);
  });
  assert.equal(await rows(db), 1);
});

test('a dispatch that never answers frees the slot too', skip, async () => {
  const db = await sqliteD1();
  await withFetch(new Error('down'), async () => {
    const res = await runCensus(post(), { DB: db, CENSUS_RUN_TOKEN: 'run-token' }, HEADERS, { now: NOW });
    assert.equal(res.status, 502);
  });
  assert.equal(await rows(db), 0);
});

test('only a JSON POST is taken', skip, async () => {
  const db = await sqliteD1();
  const env = { DB: db, CENSUS_RUN_TOKEN: 'run-token' };
  const get = new Request('https://murmur.afig.dev/stats/run-census');
  assert.equal((await runCensus(get, env, HEADERS, { now: NOW })).status, 405);
  const plain = new Request('https://murmur.afig.dev/stats/run-census', {
    method: 'POST', headers: { 'Content-Type': 'text/plain' }, body: 'x',
  });
  assert.equal((await runCensus(plain, env, HEADERS, { now: NOW })).status, 415);
  assert.equal(await rows(db), 0);
});

test('the endpoint sits behind the stats key, and only /stats has it', skip, async () => {
  const db = await sqliteD1();
  const env = { DB: db, STATS_KEY: 'stats-key', CENSUS_RUN_TOKEN: 'run-token' };
  await withFetch(new Response(null, { status: 204 }), async calls => {
    // No key: the endpoint doesn't exist, and nothing is dispatched.
    assert.equal((await stats(post(), env)).status, 404);
    // The archive has no run button to back.
    const archive = new Request('https://murmur.afig.dev/stats/archive/run-census', {
      method: 'POST', headers: { 'Content-Type': 'application/json', Cookie: 'murmur_stats=stats-key' }, body: '{}',
    });
    assert.equal((await stats(archive, env)).status, 404);
    assert.equal(calls.length, 0);
    // The key the page's cookie carries: a run starts.
    const res = await stats(post({ Cookie: 'murmur_stats=stats-key' }), env);
    assert.equal(res.status, 202);
    assert.equal(calls.length, 1);
  });
});
