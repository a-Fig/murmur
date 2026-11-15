// What the stats say of the census itself (src/repos.js, health()): when it last ran, whether it is
// late, and which repos that run didn't count. `node --test site/test/report.test.mjs`; it needs
// nothing installed.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { timingSafeEqual } from 'node:crypto';
import { census as handle, repoNumbers, repoReport, health } from '../src/repos.js';
import { archiveReport } from '../src/archive.js';
import { statsD1, sqliteD1 } from './d1.mjs';

// The Workers runtime adds this to Web Crypto; node has it only in node:crypto.
crypto.subtle.timingSafeEqual ??= (a, b) => timingSafeEqual(a, b);

const ago = hours => new Date(Date.now() - hours * 3600e3).toISOString();
const daysAgo = n => new Date(Date.now() - n * 864e5).toISOString().slice(0, 10);
const time = iso => `${iso.slice(0, 16).replace('T', ' ')} UTC`;
// A repo as repoNumbers' first query returns it.
const row = (name, counted) => ({
  repo: name.toLowerCase(), name, reported: daysAgo(5), devices: 1, people: 1, sessions: 1, messages: 1, records: 1,
  start: daysAgo(5), last: daysAgo(1), counted, contributors: 1, week: 1,
});
const report = async repos => {
  const n = await repoNumbers(statsD1(repos), 14);
  return { n, md: repoReport(n, 'https://murmur.invalid') };
};
const section = md => md.slice(md.indexOf('## The census'));

test('a census that counted every repo a few hours ago says so, and no more', async () => {
  const last = ago(2);
  const { n, md } = await report([row('github.com/a/one', last), row('github.com/b/two', ago(2.1)), row('github.com/c/three', ago(2.4))]);
  assert.equal(n.counted, last);
  assert.deepEqual(n.census, { last, hours: 2, late: false, repos: 3, fresh: [], notCounted: [] });
  assert.equal(section(md), `## The census\n\nIt last ran at ${time(last)}, 2 hours ago, and counted all 3 repos.\n`);
  // Never "0 hours ago".
  assert.match(section((await report([row('github.com/a/one', ago(0.5))])).md), /, less than an hour ago, and counted the 1 repo\.\n$/);
});

test("a census whose newest count is over 30 hours old is late, and says how long it hasn't run", async () => {
  const { n, md } = await report([row('github.com/a/one', ago(52.5)), row('github.com/b/two', ago(52.6))]);
  assert.equal(n.census.hours, 52);
  assert.equal(n.census.late, true);
  // Said first, before when it last ran.
  assert.match(section(md), /^## The census\n\n\*\*It hasn't run for 52 hours\*\*, and it runs once a day\.\n\nIt last ran at .*, 52 hours ago, and counted all 2 repos\.\n$/);
  // 30 hours is not yet late: a scheduled run can start hours after its time.
  assert.equal((await report([row('github.com/a/one', ago(29.9))])).n.census.late, false);
});

test('a repo the last run did not count is listed with when it last was, and one never counted too', async () => {
  const before = ago(27);
  const { n, md } = await report([
    row('github.com/a/one', ago(3)),
    // 29 minutes before the newest count is the same run: a run takes at most its job's 30 minutes.
    row('github.com/b/two', ago(3 + 29 / 60)),
    row('github.com/C/Skipped', before),
    // Listed days before the run, and still never counted: that run missed it.
    row('github.com/e/old', null),
  ]);
  assert.deepEqual(n.census.notCounted, [
    { name: 'github.com/e/old', counted: null, listed: daysAgo(5) },
    { name: 'github.com/C/Skipped', counted: before, listed: daysAgo(5) },
  ]);
  const s = section(md);
  assert.match(s, /and counted 2 of 4 repos\.\n/);
  assert.doesNotMatch(s, /hasn't run|listed since/);
  assert.ok(s.includes(`| [e/old](https://github.com/e/old) | never; listed ${daysAgo(5)} |`), s);
  assert.ok(s.includes(`| [C/Skipped](https://github.com/C/Skipped) | ${time(before)} |`), s);
  assert.ok(s.includes('https://github.com/a-Fig/murmur/actions/workflows/census.yml'), s);
});

test('a repo listed on or after the last run\'s day is new, not missed by it', async () => {
  const last = ago(1);
  const { n, md } = await report([
    row('github.com/a/one', last),
    { ...row('github.com/d/new', null), reported: last.slice(0, 10) },
    { ...row('github.com/f/newer', null), reported: daysAgo(-1) },
  ]);
  assert.deepEqual(n.census.fresh.map(r => r.name), ['github.com/d/new', 'github.com/f/newer']);
  assert.deepEqual(n.census.notCounted, []);
  assert.equal(section(md), `## The census\n\nIt last ran at ${time(last)}, 1 hour ago, and counted the 1 repo. 2 repos listed since haven't been counted yet.\n`);
});

test('a late census that skipped a repo says both', async () => {
  const { md } = await report([row('github.com/a/one', ago(40)), row('github.com/b/two', ago(70))]);
  const s = section(md);
  assert.match(s, /\*\*It hasn't run for 40 hours\*\*, and it runs once a day\.\n\nIt last ran at .*, 40 hours ago, and counted 1 of 2 repos\.\n\nNot counted in that run/);
  assert.ok(s.includes('| [b/two](https://github.com/b/two) |'), s);
});

test('before the census has run, the report says it has counted nothing', async () => {
  const { n, md } = await report([row('github.com/a/one', null)]);
  assert.deepEqual(n.census, { last: null, hours: null, late: false, repos: 1, fresh: [{ name: 'github.com/a/one', counted: null, listed: daysAgo(5) }], notCounted: [] });
  assert.equal(section(md), "## The census\n\nIt hasn't counted any of the 1 repo yet.\n");
  assert.equal(section((await report([])).md), '## The census\n\nIt has no repos to count yet.\n');
});

const sqlite = await sqliteD1();

test("the health comes from repo_census as the census's POSTs leave it", { skip: !sqlite && 'no node:sqlite in this node' }, async () => {
  const db = sqlite, key = 'test-key';
  const post = repo => handle(new Request('https://census.invalid/v1/census', {
    method: 'POST', headers: { Authorization: `Bearer ${key}` },
    body: JSON.stringify({ repo, days: [], devices: [], contributors: [], totals: { sessions: 0, messages: 0, records: 0, first_day: null, last_day: null } }),
  }), { DB: db, CENSUS_KEY: key });
  for (const repo of ['github.com/a/one', 'github.com/a/two']) {
    db.sql.prepare('INSERT INTO repos (repo, name, first) VALUES (?1, ?1, ?2)').run(repo, daysAgo(3));
    assert.equal((await post(repo)).status, 200);
  }
  assert.deepEqual((await repoNumbers(db, 14)).census.notCounted, []);
  // As if a day has passed and the census counted only one of them.
  const then = ago(24);
  db.sql.prepare("UPDATE repo_census SET counted = ?1 WHERE repo = 'github.com/a/two'").run(then);
  const n = await repoNumbers(db, 14);
  assert.deepEqual(n.census.notCounted, [{ name: 'github.com/a/two', counted: then, listed: daysAgo(3) }]);
  assert.equal(n.census.late, false);
});

test("a repo listed after midnight is new to a run that began before it and ended after", () => {
  // The run took its list at 23:50 and counted its last repo at 00:10; the repo was listed at 23:55.
  const h = health([
    { name: 'github.com/a/one', counted: '2026-11-11T00:10:00.000Z', reported: '2026-11-01' },
    { name: 'github.com/b/two', counted: '2026-11-10T23:51:00.000Z', reported: '2026-11-01' },
    { name: 'github.com/d/new', counted: null, reported: '2026-11-10' },
  ], Date.parse('2026-11-11T02:00:00Z'));
  assert.deepEqual(h.fresh.map(r => r.name), ['github.com/d/new']);
  assert.deepEqual(h.notCounted, []);
});

test("a repo's name is a link's label in the report, with what Markdown would read in it escaped", async () => {
  const { md } = await report([row('github.com/a/one', ago(2)), row('github.com/a/__demo__', ago(30)), row('github.com/x-1/v2.0_b-c', ago(30))]);
  // In the census's table of repos it missed.
  assert.ok(section(md).includes('| [a/\\_\\_demo\\_\\_](https://github.com/a/__demo__) |'), section(md));
  assert.ok(section(md).includes('| [x-1/v2.0\\_b-c](https://github.com/x-1/v2.0_b-c) |'), section(md));
  // And in the table of every repo: letters, digits, dots and hyphens as they are, the URL untouched.
  const main = md.slice(0, md.indexOf('## The census'));
  assert.ok(main.includes('| [a/\\_\\_demo\\_\\_](https://github.com/a/__demo__) |'), main);
  assert.ok(main.includes('| [x-1/v2.0\\_b-c](https://github.com/x-1/v2.0_b-c) |'), main);
  assert.ok(main.includes('| [a/one](https://github.com/a/one) |'), main);
});

// #152: one of something reads as one, in both reports' headlines.
test('a report of one repo and one user says "repo" and "user", not "repos" and "users"', async () => {
  const { n } = await report([row('github.com/a/one', ago(2))]);
  const one = repoReport({ ...n, repos: 1, users: 1, usersBy: { account: 1, name: 0, device: 0 } }, 'https://murmur.invalid');
  assert.match(one, /^\*\*1 public repo\*\* uses Murmur; /m);
  assert.match(one, /About \*\*1 user\*\* has run it there;/);
  assert.match(one, /Of 1 user, 1 was matched to an account,/);
  const two = repoReport({ ...n, repos: 2, users: 2, usersBy: { account: 2, name: 0, device: 0 } }, 'https://murmur.invalid');
  assert.match(two, /^\*\*2 public repos\*\* use Murmur; /m);
  assert.match(two, /About \*\*2 users\*\* have run it there;/);
  assert.match(two, /Of 2 users, 2 were matched to an account,/);

  const archive = active => archiveReport({
    today: '2026-11-11', checkInsEnded: '2026-11-08', goal: 100, active, activeBefore: 0, activeDays: 7, activeToday: 0,
    teams: 0, newUsers: 0, allUsers: active, allTeams: 0, days: 14, series: [], newByWeek: [], teamList: [], teamSizes: [],
    versions: [], systems: [],
  }, 'https://murmur.invalid');
  assert.match(archive(1), /^\*\*1 active user\*\*, 1% of the goal/m);
  assert.match(archive(3), /^\*\*3 active users\*\*, 3% of the goal/m);
});
