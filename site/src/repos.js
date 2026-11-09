// The public repos that use Murmur, and what the census reads from them (#259).
//
// A repo is listed once one of its clones reports it (internal/publicrepo, src/usage.js). Each day
// the census (site/census/census.mjs, run by .github/workflows/census.yml) reads what every listed
// repo shows anyone, its bus and its commit history, and sends the counts to /v1/census, which keeps
// them in D1 (migrations/0003_census.sql). The stats page draws them (/stats, src/stats.html).

import { addDays } from './archive.js';

const REPO = /^github\.com\/[a-z0-9][a-z0-9-]{0,38}\/[a-z0-9._-]{1,100}$/;
const DAY = /^\d{4}-\d\d-\d\d$/;
const ID = /^[0-9a-f]{16}$/;
// A repo's last 7 days, for "active".
const WEEK = 7;

// /v1/census, for the census only, with the CENSUS_KEY secret as its Bearer token:
// - GET: the listed repos, {"repos": ["github.com/Owner/Name", ...]}.
// - POST: one repo's counts, as census.mjs makes them. A count is only ever raised, never lowered:
//   a bus log can be rewritten, and what has been seen stays counted.
export async function census(request, env) {
  const key = String(env.CENSUS_KEY || '');
  const bearer = /^Bearer\s+(\S+)\s*$/i.exec(request.headers.get('Authorization') || '')?.[1];
  if (!key || !bearer || !(await same(bearer, key))) return new Response('Not found.\n', { status: 404 });
  const headers = { 'Cache-Control': 'no-store' };
  if (request.method === 'GET') {
    const { results } = await env.DB.prepare('SELECT name FROM repos ORDER BY repo').all();
    return Response.json({ repos: results.map(r => r.name) }, { headers });
  }
  if (request.method !== 'POST') return new Response('Only GET and POST.\n', { status: 405, headers: { Allow: 'GET, POST' } });
  let c;
  try {
    c = await request.json();
  } catch {
    return new Response('Not JSON.\n', { status: 400 });
  }
  const bad = invalid(c);
  if (bad) return new Response(`Not counts: ${bad}.\n`, { status: 400 });
  const known = await env.DB.prepare('SELECT 1 FROM repos WHERE repo = ?1').bind(c.repo).first();
  if (!known) return new Response('Not a listed repo.\n', { status: 422 });

  const db = env.DB, t = c.totals;
  const stmts = [
    db.prepare(
      `INSERT INTO repo_census (repo, sessions, messages, records, first_day, last_day, counted)
       VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)
       ON CONFLICT (repo) DO UPDATE SET
         sessions = MAX(sessions, excluded.sessions), messages = MAX(messages, excluded.messages),
         records = MAX(records, excluded.records),
         first_day = MIN(COALESCE(first_day, excluded.first_day), COALESCE(excluded.first_day, first_day)),
         last_day = MAX(COALESCE(last_day, excluded.last_day), COALESCE(excluded.last_day, last_day)),
         counted = excluded.counted`,
    ).bind(c.repo, t.sessions, t.messages, t.records, t.first_day, t.last_day, new Date().toISOString()),
  ];
  // D1 binds at most 100 values a statement, so rows go in groups: 20 days of 5 values, 33 rows of 3.
  const insert = (rows, width, sql, values) => {
    const per = Math.floor(100 / width);
    for (let i = 0; i < rows.length; i += per) {
      const group = rows.slice(i, i + per);
      const tuples = group.map((_, j) => `(${Array.from({ length: width }, (_, k) => `?${width * j + k + 1}`).join(', ')})`).join(', ');
      stmts.push(db.prepare(sql.replace('$ROWS', tuples)).bind(...group.flatMap(values)));
    }
  };
  insert(c.days, 5,
    `INSERT INTO repo_days (repo, day, sessions, messages, records) VALUES $ROWS
     ON CONFLICT (repo, day) DO UPDATE SET
       sessions = MAX(sessions, excluded.sessions), messages = MAX(messages, excluded.messages),
       records = MAX(records, excluded.records)`,
    d => [c.repo, d.day, d.sessions, d.messages, d.records]);
  // An owner once seen is kept: a daemon deletes its presence when it stops.
  insert(c.devices, 3,
    `INSERT INTO devices (repo, device, person) VALUES $ROWS
     ON CONFLICT (repo, device) DO UPDATE SET person = COALESCE(excluded.person, person)`,
    v => [c.repo, v.id, v.person]);
  insert(c.days.flatMap(d => d.devices.map(v => [d.day, v])), 3,
    `INSERT INTO device_days (repo, day, device) VALUES $ROWS ON CONFLICT DO NOTHING`,
    ([day, v]) => [c.repo, day, v]);
  insert(c.contributors, 3,
    `INSERT INTO contributors (repo, id, first) VALUES $ROWS
     ON CONFLICT (repo, id) DO UPDATE SET first = MIN(first, excluded.first)`,
    k => [c.repo, k.id, k.first]);
  await db.batch(stmts);
  return Response.json({ ok: true, days: c.days.length, devices: c.devices.length, contributors: c.contributors.length }, { headers });
}

// What is wrong with a census POST's body, or "" if nothing is.
function invalid(c) {
  const count = n => Number.isSafeInteger(n) && n >= 0;
  const day = d => typeof d === 'string' && DAY.test(d) && !Number.isNaN(Date.parse(d));
  const id = v => typeof v === 'string' && ID.test(v);
  if (!c || typeof c !== 'object') return 'no object';
  if (typeof c.repo !== 'string' || !REPO.test(c.repo)) return 'repo';
  const t = c.totals;
  if (!t || !['sessions', 'messages', 'records'].every(k => count(t[k]))) return 'totals';
  if (!(t.first_day === null || day(t.first_day)) || !(t.last_day === null || day(t.last_day))) return 'totals days';
  if (!Array.isArray(c.days) || c.days.length > 3660) return 'days';
  for (const d of c.days) {
    if (!d || !day(d.day) || !['sessions', 'messages', 'records'].every(k => count(d[k]))) return 'a day';
    if (!Array.isArray(d.devices) || d.devices.length > 1000 || !d.devices.every(id)) return "a day's devices";
  }
  if (!Array.isArray(c.devices) || c.devices.length > 10000) return 'devices';
  for (const v of c.devices) if (!v || !id(v.id) || !(v.person === null || id(v.person))) return 'a device';
  if (!Array.isArray(c.contributors) || c.contributors.length > 100000) return 'contributors';
  for (const k of c.contributors) if (!k || !id(k.id) || !day(k.first)) return 'a contributor';
  return '';
}

async function same(a, b) {
  const enc = new TextEncoder();
  const [x, y] = [enc.encode(a), enc.encode(b)];
  return x.byteLength === y.byteLength && crypto.subtle.timingSafeEqual(x, y);
}

// Everything the stats page shows, for the last `days` days ending today (UTC).
export async function repoNumbers(db, days) {
  const today = new Date().toISOString().slice(0, 10);
  const from = addDays(today, -(days - 1));
  const weekFrom = addDays(today, -(WEEK - 1));
  const [repos, byDay, people, counted] = await db.batch([
    // Each repo, with its totals and when it started: its first day on the bus, or the day it was
    // reported if the census hasn't read it yet. A device whose owner the census has never seen is
    // a person of its own. Its week is how many of the last 7 days its devices wrote anything, presence included.
    db.prepare(
      `SELECT r.repo, r.name, r.first AS reported,
         (SELECT COUNT(*) FROM devices v WHERE v.repo = r.repo) AS devices,
         (SELECT COUNT(DISTINCT COALESCE(v.person, v.device)) FROM devices v WHERE v.repo = r.repo) AS people,
         COALESCE(c.sessions, 0) AS sessions, COALESCE(c.messages, 0) AS messages, COALESCE(c.records, 0) AS records,
         MIN(r.first, COALESCE(c.first_day, r.first)) AS start, c.last_day AS last, c.counted,
         (SELECT COUNT(*) FROM contributors k WHERE k.repo = r.repo) AS contributors,
         (SELECT COUNT(DISTINCT d.day) FROM device_days d WHERE d.repo = r.repo AND d.day >= ?1) AS week
       FROM repos r LEFT JOIN repo_census c ON c.repo = r.repo
       ORDER BY week DESC, records DESC, r.repo`,
    ).bind(weekFrom),
    // Per repo per day: its active devices and the people behind them, and its counts.
    db.prepare(
      `WITH dd AS (
         SELECT d.repo, d.day, COUNT(*) AS devices, COUNT(DISTINCT COALESCE(v.person, d.device)) AS people
         FROM device_days d LEFT JOIN devices v ON v.repo = d.repo AND v.device = d.device
         WHERE d.day BETWEEN ?1 AND ?2 GROUP BY d.repo, d.day),
       k AS (SELECT repo, day FROM dd UNION SELECT repo, day FROM repo_days WHERE day BETWEEN ?1 AND ?2)
       SELECT k.repo, k.day, COALESCE(dd.devices, 0) AS devices, COALESCE(dd.people, 0) AS people,
         COALESCE(r.sessions, 0) AS sessions, COALESCE(r.messages, 0) AS messages, COALESCE(r.records, 0) AS records
       FROM k LEFT JOIN dd ON dd.repo = k.repo AND dd.day = k.day
         LEFT JOIN repo_days r ON r.repo = k.repo AND r.day = k.day
       ORDER BY k.day, k.repo`,
    ).bind(from, today),
    // Each contributor once, however many repos they are in, by the day of their first commit to any.
    db.prepare(`SELECT first AS day, COUNT(*) AS n FROM (SELECT MIN(first) AS first FROM contributors GROUP BY id) GROUP BY first ORDER BY first`),
    db.prepare(`SELECT MAX(counted) AS at FROM repo_census`),
  ]);

  const list = repos.results;
  const names = new Map(list.map(r => [r.repo, r.name]));
  const rows = byDay.results;
  const firsts = people.results;
  const series = [];
  for (let d = from; d <= today; d = addDays(d, 1)) {
    const on = rows.filter(r => r.day === d);
    const sum = k => on.reduce((s, r) => s + r[k], 0);
    series.push({
      day: d,
      repos: list.filter(r => r.start <= d).length,
      activeRepos: on.filter(r => r.devices > 0 || r.records > 0).length,
      contributors: firsts.filter(f => f.day <= d).reduce((s, f) => s + f.n, 0),
      devices: sum('devices'), people: sum('people'), sessions: sum('sessions'), messages: sum('messages'), records: sum('records'),
    });
  }
  const recent = rows.filter(r => r.day >= weekFrom);
  const sumOf = (rs, k) => rs.reduce((s, r) => s + r[k], 0);
  return {
    today, days, week: WEEK,
    counted: counted.results[0]?.at ?? null,
    repos: list.length,
    activeRepos: list.filter(r => r.week > 0).length,
    newRepos: list.filter(r => r.start >= weekFrom).length,
    contributors: firsts.reduce((s, f) => s + f.n, 0),
    newContributors: firsts.filter(f => f.day >= weekFrom).reduce((s, f) => s + f.n, 0),
    devices: sumOf(list, 'devices'),
    people: sumOf(list, 'people'),
    sessions: sumOf(list, 'sessions'),
    messages: sumOf(list, 'messages'),
    records: sumOf(list, 'records'),
    weekSessions: sumOf(recent, 'sessions'),
    weekMessages: sumOf(recent, 'messages'),
    series,
    // Per repo per day, for the charts that split by repo.
    byRepo: rows.map(r => ({ ...r, name: names.get(r.repo) ?? r.repo })),
    repoList: list.map(({ repo, ...r }) => r),
  };
}

// The numbers as markdown, for an agent: what the page shows, in tables, with what they mean.
export function repoReport(n, origin) {
  const table = (head, rows) => [head, head.map(() => '---'), ...rows].map(r => `| ${r.join(' | ')} |`).join('\n');
  const short = name => name.replace(/^github\.com\//, '');
  const weeks = [];
  for (let i = n.series.length - 1; i >= 0; i -= n.week) {
    const w = n.series.slice(Math.max(0, i - n.week + 1), i + 1);
    const sum = k => w.reduce((s, d) => s + d[k], 0);
    weeks.push([n.series[i].day, n.series[i].repos, n.series[i].contributors, sum('sessions'), sum('messages'), sum('records')]);
  }
  const repos = n.repoList.length
    ? table(['Repo', 'Contributors', 'Devices', 'People', 'Sessions', 'Messages', 'Bus records', 'Days active of the last 7', 'First on the bus', 'Last on the bus'],
      n.repoList.map(r => [`[${short(r.name)}](https://${r.name})`, r.contributors, r.devices, r.people, r.sessions, r.messages, r.records, r.week, r.start, r.last ?? '–']))
    : 'None yet.';
  return `# Public repos using Murmur, ${n.today}

**${n.repos} public repos** use Murmur; ${n.activeRepos} had bus activity in the last ${n.week} days, and ${n.newRepos} started in them.

${table(['Measure', 'Count'], [
    ['Public repos using Murmur', n.repos],
    [`Active repos (anything on the bus in the last ${n.week} days)`, n.activeRepos],
    ['Contributors to those repos (each person once)', n.contributors],
    [`New contributors in the last ${n.week} days`, n.newContributors],
    ['Devices that have run Murmur in them', n.devices],
    ['People behind those devices', n.people],
    ['Agent sessions', n.sessions],
    [`Agent sessions in the last ${n.week} days`, n.weekSessions],
    ['Messages between agents', n.messages],
    [`Messages in the last ${n.week} days`, n.weekMessages],
    ['Bus records', n.records],
  ])}

## By week

Each week is the ${n.week} days ending on the date shown, back over the last ${n.days} days; the newest first. Repos and contributors are totals as of that day.

${table(['Week ending', 'Repos', 'Contributors', 'Sessions', 'Messages', 'Bus records'], weeks)}

## Repos

Most active in the last ${n.week} days first.

${repos}

## What the numbers mean

- A **repo** is listed once one of its clones reports it: Murmur sends a public repo's address, once per repo, and nothing else.
- Everything else is read by the census from what each repo shows anyone, once a day${n.counted ? ` (last at ${n.counted})` : ''}: its bus, \`refs/murmur/*\`, and its default branch's commits.
- A **contributor** has committed to a repo's default branch, at any time; one person in two repos counts once. A **device** is one clone running Murmur; a **person**, one git user name among a repo's devices, as their presence shows it, so one person in two repos counts twice. A daemon deletes its presence when it stops, so a device whose owner the census never saw counts as a person of its own.
- A **session** is an agent session, counted once on the day it started, however often its daemon restarts; a **message**, one agent's message to another. A **bus record** is anything a device wrote to its log. Once the bus is encrypted (#257), a record's kind is hidden and only records are counted.
- A count is only ever raised. Days are UTC.

The check-ins of earlier versions, which are no longer stored, are at ${origin}/stats/archive/report.md. The same numbers as JSON: ${origin}/stats/data?days=N (N from 14 to 730, default 90), with the same key. This page takes \`?days=N\` too.
`;
}
