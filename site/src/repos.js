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
// The first day a bus could have anything on it: Murmur began on 2026-10-26.
const EPOCH = '2026-10-01';
// D1's limits (developers.cloudflare.com/d1/platform/limits, updated 2026-10-30): 100 bound values a
// statement; a whole db.batch() must finish within 30 seconds; and a Worker request may make 1,000
// queries (50 on the Free plan), a batch counting as one. A census POST at the most invalid() takes
// is about 6,770 statements of up to 33 rows: in batches of 200, 34 batches, plus one query to find
// the repo, which is within even the Free plan's 50 queries. CPU is another limit: a Free plan
// Worker has 10 ms of it a request, and parsing a POST that size and building its statements
// would take more. That was so before batches, and the Paid plan's limit is far above it.
const BATCH = 200;
// How long one census run can take: its job's timeout in .github/workflows/census.yml, so change
// both together. A repo counted within this of the newest count was counted in the same run.
// A census run by hand on one repo (census.mjs's usage) is a run of its own: until the next
// scheduled run, every other repo looks missed by it.
const RUN = 30 * 60e3;
// The census runs once a day, at 05:17 UTC. A scheduled run can start hours late on GitHub, so
// the newest count is late only once it is older than this.
const LATE = 30 * 3600e3;

// /v1/census, for the census only, with the CENSUS_KEY secret as its Bearer token:
// - GET: the listed repos, {"repos": ["github.com/Owner/Name", ...]}.
// - POST: one repo's counts, as census.mjs makes them. A count is only ever raised, never lowered:
//   a bus log can be rewritten, and what has been seen stays counted.
//
// bytes, in each day and in the totals, is how much the devices wrote to their logs (#550,
// census.mjs): exact, like records, and raised like them. A census from before it sends none.
//
// est_sessions and est_messages, in each day and in the totals, are the census's estimates of what
// an encrypted bus hides (#550, census.mjs), kept in their own columns beside the exact counts and
// raised in the same way. A census from before them sends none, and they stay as they were.
// Raising only has a cost here that it doesn't have for an exact count: if the census later
// estimates differently and lower, what is stored stays, until someone lowers it in D1 by hand.
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
  const stmts = [];
  // D1 binds at most 100 values a statement, so rows go in groups: 12 days of 8 values, 33 rows of 3.
  const insert = (rows, width, sql, values) => {
    const per = Math.floor(100 / width);
    for (let i = 0; i < rows.length; i += per) {
      const group = rows.slice(i, i + per);
      const tuples = group.map((_, j) => `(${Array.from({ length: width }, (_, k) => `?${width * j + k + 1}`).join(', ')})`).join(', ');
      stmts.push(db.prepare(sql.replace('$ROWS', tuples)).bind(...group.flatMap(values)));
    }
  };
  insert(c.days, 8,
    `INSERT INTO repo_days (repo, day, sessions, messages, records, est_sessions, est_messages, bytes) VALUES $ROWS
     ON CONFLICT (repo, day) DO UPDATE SET
       sessions = MAX(sessions, excluded.sessions), messages = MAX(messages, excluded.messages),
       records = MAX(records, excluded.records), bytes = MAX(bytes, excluded.bytes),
       est_sessions = MAX(est_sessions, excluded.est_sessions), est_messages = MAX(est_messages, excluded.est_messages)`,
    d => [c.repo, d.day, d.sessions, d.messages, d.records, d.est_sessions ?? 0, d.est_messages ?? 0, d.bytes ?? 0]);
  // An owner once seen is kept: a daemon deletes its presence when it stops. So is an account. A
  // census from before #323 sends none.
  insert(c.devices, 4,
    `INSERT INTO devices (repo, device, person, account) VALUES $ROWS
     ON CONFLICT (repo, device) DO UPDATE SET
       person = COALESCE(excluded.person, person), account = COALESCE(excluded.account, account)`,
    v => [c.repo, v.id, v.person, v.account ?? null]);
  insert(c.days.flatMap(d => d.devices.map(v => [d.day, v])), 3,
    `INSERT INTO device_days (repo, day, device) VALUES $ROWS ON CONFLICT DO NOTHING`,
    ([day, v]) => [c.repo, day, v]);
  insert(c.contributors, 3,
    `INSERT INTO contributors (repo, id, first) VALUES $ROWS
     ON CONFLICT (repo, id) DO UPDATE SET first = MIN(first, excluded.first)`,
    k => [c.repo, k.id, k.first]);
  stmts.push(db.prepare(
    `INSERT INTO repo_census (repo, sessions, messages, records, first_day, last_day, counted, est_sessions, est_messages, bytes)
     VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10)
     ON CONFLICT (repo) DO UPDATE SET
       sessions = MAX(sessions, excluded.sessions), messages = MAX(messages, excluded.messages),
       records = MAX(records, excluded.records), bytes = MAX(bytes, excluded.bytes),
       est_sessions = MAX(est_sessions, excluded.est_sessions), est_messages = MAX(est_messages, excluded.est_messages),
       first_day = MIN(COALESCE(first_day, excluded.first_day), COALESCE(excluded.first_day, first_day)),
       last_day = MAX(COALESCE(last_day, excluded.last_day), COALESCE(excluded.last_day, last_day)),
       counted = excluded.counted`,
  ).bind(c.repo, t.sessions, t.messages, t.records, t.first_day, t.last_day, new Date().toISOString(),
    t.est_sessions ?? 0, t.est_messages ?? 0, t.bytes ?? 0));
  // A batch must finish within D1's 30 seconds, and a big repo makes thousands of statements, so
  // they go BATCH at a time. Each batch is its own transaction. Every write only raises a count or
  // adds a row, so a POST cut short between batches leaves less written, never anything wrong, and
  // the next day's census writes the rest. The totals go last, so `counted` moves only once
  // everything else is in.
  for (let i = 0; i < stmts.length; i += BATCH) await db.batch(stmts.slice(i, i + BATCH));
  return Response.json({ ok: true, days: c.days.length, devices: c.devices.length, contributors: c.contributors.length }, { headers });
}

// What is wrong with a census POST's body, or "" if nothing is.
//
// No day may be after tomorrow (UTC): a day in the future would keep a repo active and its people
// new for ever. Nor may a bus day be before EPOCH, when there was no bus. A contributor's first
// commit can be years before that. The census dates what it reads the same way (census.mjs).
// Its tooBig() has the size limits below, to skip a repo too big for them: change both together.
function invalid(c) {
  const latest = addDays(new Date().toISOString().slice(0, 10), 1);
  const count = n => Number.isSafeInteger(n) && n >= 0;
  const day = d => typeof d === 'string' && DAY.test(d) && !Number.isNaN(Date.parse(d)) && d <= latest;
  const busDay = d => day(d) && d >= EPOCH;
  const id = v => typeof v === 'string' && ID.test(v);
  // The bytes and the estimates (#550), which a census from before them leaves out.
  const estimates = o => ['bytes', 'est_sessions', 'est_messages'].every(k => o[k] === undefined || count(o[k]));
  if (!c || typeof c !== 'object') return 'no object';
  if (typeof c.repo !== 'string' || !REPO.test(c.repo)) return 'repo';
  const t = c.totals;
  if (!t || !['sessions', 'messages', 'records'].every(k => count(t[k])) || !estimates(t)) return 'totals';
  if (!(t.first_day === null || busDay(t.first_day)) || !(t.last_day === null || busDay(t.last_day))) return 'totals days';
  if (!Array.isArray(c.days) || c.days.length > 3660) return 'days';
  for (const d of c.days) {
    if (!d || !busDay(d.day) || !['sessions', 'messages', 'records'].every(k => count(d[k])) || !estimates(d)) return 'a day';
    if (!Array.isArray(d.devices) || d.devices.length > 1000 || !d.devices.every(id)) return "a day's devices";
  }
  // A device on a day is a row, so without this a POST could carry 3,660,000 of them.
  if (c.days.reduce((n, d) => n + d.devices.length, 0) > 100000) return "the days' devices";
  if (!Array.isArray(c.devices) || c.devices.length > 10000) return 'devices';
  for (const v of c.devices) {
    if (!v || !id(v.id) || !(v.person === null || id(v.person)) || !(v.account == null || id(v.account))) return 'a device';
  }
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
//
// records and bytes are exact whether or not a bus is encrypted: how many records its devices wrote
// to their logs, and how many bytes their events came to.
//
// sessions and messages are the exact counts, wherever they appear: the totals, each day of series,
// byRepo and repoList. estSessions and estMessages beside them are the estimates of what encrypted
// buses hide (#550, census.mjs), and the figure to show is the two together, marked as an estimate
// when the second isn't 0 (approx()).
export async function repoNumbers(db, days) {
  const today = new Date().toISOString().slice(0, 10);
  const from = addDays(today, -(days - 1));
  const weekFrom = addDays(today, -(WEEK - 1));
  const [repos, byDay, people, counted, devs] = await db.batch([
    // Each repo, with its totals and when it started: its first day on the bus, or the day it was
    // reported if the census hasn't read it yet. A device whose owner the census has never seen is
    // a person of its own. Its week is how many of the last 7 days its devices wrote anything, presence included.
    db.prepare(
      `SELECT r.repo, r.name, r.first AS reported,
         (SELECT COUNT(*) FROM devices v WHERE v.repo = r.repo) AS devices,
         (SELECT COUNT(DISTINCT COALESCE(v.person, v.device)) FROM devices v WHERE v.repo = r.repo) AS people,
         COALESCE(c.sessions, 0) AS sessions, COALESCE(c.messages, 0) AS messages, COALESCE(c.records, 0) AS records,
         COALESCE(c.est_sessions, 0) AS estSessions, COALESCE(c.est_messages, 0) AS estMessages,
         COALESCE(c.bytes, 0) AS bytes,
         MIN(r.first, COALESCE(c.first_day, r.first)) AS start, c.last_day AS last, c.counted,
         (SELECT COUNT(*) FROM contributors k WHERE k.repo = r.repo) AS contributors,
         (SELECT COUNT(DISTINCT d.day) FROM device_days d WHERE d.repo = r.repo AND d.day BETWEEN ?1 AND ?2) AS week
       FROM repos r LEFT JOIN repo_census c ON c.repo = r.repo
       ORDER BY week DESC, records DESC, r.repo`,
    ).bind(weekFrom, today),
    // Per repo per day: its active devices and the people behind them, and its counts.
    db.prepare(
      `WITH dd AS (
         SELECT d.repo, d.day, COUNT(*) AS devices, COUNT(DISTINCT COALESCE(v.person, d.device)) AS people
         FROM device_days d LEFT JOIN devices v ON v.repo = d.repo AND v.device = d.device
         WHERE d.day BETWEEN ?1 AND ?2 GROUP BY d.repo, d.day),
       k AS (SELECT repo, day FROM dd UNION SELECT repo, day FROM repo_days WHERE day BETWEEN ?1 AND ?2)
       SELECT k.repo, k.day, COALESCE(dd.devices, 0) AS devices, COALESCE(dd.people, 0) AS people,
         COALESCE(r.sessions, 0) AS sessions, COALESCE(r.messages, 0) AS messages, COALESCE(r.records, 0) AS records,
         COALESCE(r.est_sessions, 0) AS estSessions, COALESCE(r.est_messages, 0) AS estMessages,
         COALESCE(r.bytes, 0) AS bytes
       FROM k LEFT JOIN dd ON dd.repo = k.repo AND dd.day = k.day
         LEFT JOIN repo_days r ON r.repo = k.repo AND r.day = k.day
       ORDER BY k.day, k.repo`,
    ).bind(from, today),
    // Each contributor once, however many repos they are in, by the day of their first commit to any.
    // Nothing after today counts, here or above: the site refuses later days now, but rows from
    // before it did can't then make a repo active, or a person new, for ever.
    db.prepare(
      `SELECT first AS day, COUNT(*) AS n FROM (SELECT MIN(first) AS first FROM contributors GROUP BY id)
       WHERE first <= ?1 GROUP BY first ORDER BY first`,
    ).bind(today),
    db.prepare(`SELECT MAX(counted) AS at FROM repo_census`),
    // Every device, for users: who is behind it, and its first and last day on the bus, up to today
    // as above.
    db.prepare(
      `SELECT v.repo, v.device, v.person, v.account, COALESCE(MIN(d.day), r.first) AS first, MAX(d.day) AS last
       FROM devices v JOIN repos r ON r.repo = v.repo
         LEFT JOIN device_days d ON d.repo = v.repo AND d.device = v.device AND d.day <= ?1
       GROUP BY v.repo, v.device`,
    ).bind(today),
  ]);
  const u = users(devs.results);
  const userFirsts = [...u.users.values()].map(x => x.first).sort();

  const list = repos.results;
  const names = new Map(list.map(r => [r.repo, r.name]));
  const rows = byDay.results;
  const firsts = people.results;
  const series = [];
  for (let d = from; d <= today; d = addDays(d, 1)) {
    const on = rows.filter(r => r.day === d);
    const sum = k => on.reduce((s, r) => s + (r[k] ?? 0), 0);
    series.push({
      day: d,
      repos: list.filter(r => r.start <= d).length,
      activeRepos: on.filter(r => r.devices > 0 || r.records > 0).length,
      contributors: firsts.filter(f => f.day <= d).reduce((s, f) => s + f.n, 0),
      users: userFirsts.filter(f => f <= d).length,
      devices: sum('devices'), people: sum('people'), sessions: sum('sessions'), messages: sum('messages'), records: sum('records'),
      estSessions: sum('estSessions'), estMessages: sum('estMessages'), bytes: sum('bytes'),
    });
  }
  const recent = rows.filter(r => r.day >= weekFrom);
  const sumOf = (rs, k) => rs.reduce((s, r) => s + (r[k] ?? 0), 0);
  const last = counted.results[0]?.at ?? null;
  return {
    today, days, week: WEEK,
    counted: last,
    census: health(list, Date.now()),
    repos: list.length,
    activeRepos: list.filter(r => r.week > 0).length,
    newRepos: list.filter(r => r.start >= weekFrom).length,
    contributors: firsts.reduce((s, f) => s + f.n, 0),
    newContributors: firsts.filter(f => f.day >= weekFrom).reduce((s, f) => s + f.n, 0),
    users: u.users.size,
    newUsers: userFirsts.filter(f => f >= weekFrom).length,
    activeUsers: [...u.users.values()].filter(x => x.last && x.last >= weekFrom).length,
    // How each user was told apart, for the page's note on what users means: matched to an account,
    // by their git name only, or by their device only.
    usersBy: u.by,
    devices: sumOf(list, 'devices'),
    people: sumOf(list, 'people'),
    sessions: sumOf(list, 'sessions'),
    messages: sumOf(list, 'messages'),
    records: sumOf(list, 'records'),
    bytes: sumOf(list, 'bytes'),
    weekRecords: sumOf(recent, 'records'),
    weekBytes: sumOf(recent, 'bytes'),
    estSessions: sumOf(list, 'estSessions'),
    estMessages: sumOf(list, 'estMessages'),
    weekSessions: sumOf(recent, 'sessions'),
    weekMessages: sumOf(recent, 'messages'),
    weekEstSessions: sumOf(recent, 'estSessions'),
    weekEstMessages: sumOf(recent, 'estMessages'),
    series,
    // Per repo per day, for the charts that split by repo.
    byRepo: rows.map(r => ({ ...r, name: names.get(r.repo) ?? r.repo })),
    repoList: list.map(({ repo, ...r }) => r),
  };
}

// Whether the census is keeping up, from when it last read each listed repo (repo_census.counted):
// - last: its last run, which is the newest count and the counts within RUN of it;
// - hours: how long ago that was, and late: whether that is longer than LATE;
// - fresh: the repos never counted that were listed on or after that run's day. Anyone can list a
//   repo (#298), so most days some are, and they are new, not missed;
// - notCounted: the repos that run didn't count, the longest uncounted first, each with when it
//   last was (null for never) and the day it was listed. A repo isn't counted when it can't be
//   read or the census skips it (census.mjs); the run's page in Actions says which, and why.
export function health(list, now) {
  const last = list.map(r => r.counted).filter(Boolean).sort().at(-1) ?? null;
  const at = last ? Date.parse(last) : NaN;
  const ago = Number.isFinite(at) ? now - at : null;
  // The day the run began, not the day of its newest count: a run that crosses midnight UTC took
  // its list of repos the day before, so a repo listed after midnight was never in it.
  const runDay = Number.isFinite(at) ? new Date(at - RUN).toISOString().slice(0, 10) : last?.slice(0, 10);
  const fresh = [], notCounted = [];
  for (const r of list) {
    if (r.counted && Date.parse(r.counted) >= at - RUN) continue;
    const row = { name: r.name, counted: r.counted ?? null, listed: r.reported };
    (!r.counted && (!last || r.reported >= runDay) ? fresh : notCounted).push(row);
  }
  notCounted.sort((a, b) => (a.counted ?? '').localeCompare(b.counted ?? '') || a.name.localeCompare(b.name));
  return {
    last,
    hours: ago === null ? null : Math.floor(ago / 3600e3),
    late: ago !== null && ago > LATE,
    repos: list.length,
    fresh,
    notCounted,
  };
}

// The people using Murmur, approximately (#323), from every device's row: { repo, device, person,
// account, first, last }. Murmur sends nothing that names a person, so a user is the best of:
// - the GitHub account the census matched the device's owner to (census.mjs, accounts());
// - else the account an owner of the same git name was matched to in another repo, if exactly one;
// - else the owner's git name; else the device itself.
// Each counts once across repos, from its devices' first day to their last. by says how many users
// were told apart each way.
export function users(devices) {
  const accountsOf = new Map(); // person -> Set of accounts
  for (const v of devices) {
    if (!v.person || !v.account) continue;
    if (!accountsOf.has(v.person)) accountsOf.set(v.person, new Set());
    accountsOf.get(v.person).add(v.account);
  }
  const out = new Map(); // key -> { first, last }
  for (const v of devices) {
    const via = accountsOf.get(v.person);
    const account = v.account ?? (via?.size === 1 ? [...via][0] : null);
    const key = account ? 'account:' + account : v.person ? 'person:' + v.person : `device:${v.repo}/${v.device}`;
    const u = out.get(key);
    if (!u) out.set(key, { first: v.first, last: v.last });
    else {
      if (v.first && (!u.first || v.first < u.first)) u.first = v.first;
      if (v.last && (!u.last || v.last > u.last)) u.last = v.last;
    }
  }
  const by = { account: 0, name: 0, device: 0 };
  for (const k of out.keys()) by[k.startsWith('account:') ? 'account' : k.startsWith('person:') ? 'name' : 'device']++;
  return { users: out, by };
}

// A count with the estimate of what encryption hides added (#550): "≈170" when any of it is
// estimated, and the exact count as it is when none is.
export const approx = (exact, est) => (est ? `≈${exact + est}` : String(exact));

// A number of bytes as people read one: 950 B, 12.3 kB, 4.56 MB. 1 kB is 1,000 bytes.
export function size(n) {
  const b = n ?? 0;
  if (b < 1000) return `${b} B`;
  const [v, unit] = b < 1e6 ? [b / 1e3, 'kB'] : b < 1e9 ? [b / 1e6, 'MB'] : [b / 1e9, 'GB'];
  return `${v >= 100 ? v.toFixed(0) : v >= 10 ? v.toFixed(1) : v.toFixed(2)} ${unit}`;
}

// The numbers as markdown, for an agent: what the page shows, in tables, with what they mean.
export function repoReport(n, origin) {
  const table = (head, rows) => [head, head.map(() => '---'), ...rows].map(r => `| ${r.join(' | ')} |`).join('\n');
  // A repo's name as a link's label, with what Markdown would read in it (__a__ as bold) escaped.
  const short = name => name.replace(/^github\.com\//, '').replace(/[\\`*_[\]<>|]/g, '\\$&');
  const weeks = [];
  for (let i = n.series.length - 1; i >= 0; i -= n.week) {
    const w = n.series.slice(Math.max(0, i - n.week + 1), i + 1);
    const sum = k => w.reduce((s, d) => s + (d[k] ?? 0), 0);
    weeks.push([n.series[i].day, n.series[i].repos, n.series[i].users, n.series[i].contributors,
      sum('records'), size(sum('bytes')), approx(sum('sessions'), sum('estSessions')), approx(sum('messages'), sum('estMessages'))]);
  }
  const repos = n.repoList.length
    ? table(['Repo', 'Contributors', 'Devices', 'People', 'Bus records', 'Data', 'Sessions', 'Messages', 'Days active of the last 7', 'First on the bus', 'Last on the bus'],
      n.repoList.map(r => [`[${short(r.name)}](https://${r.name})`, r.contributors, r.devices, r.people, r.records, size(r.bytes),
        approx(r.sessions, r.estSessions), approx(r.messages, r.estMessages), r.week, r.start, r.last ?? '–']))
    : 'None yet.';
  // A count with its noun, so one repo or user doesn't read as "1 public repos use".
  const count = (k, one, many = one + 's') => `${k} ${k === 1 ? one : many}`;
  return `# Public repos using Murmur, ${n.today}

**${count(n.repos, 'public repo')}** ${n.repos === 1 ? 'uses' : 'use'} Murmur; ${n.activeRepos} had bus activity in the last ${n.week} days, and ${n.newRepos} started in them. About **${count(n.users, 'user')}** ${n.users === 1 ? 'has' : 'have'} run it there; ${n.activeUsers} in the last ${n.week} days.

${table(['Measure', 'Count'], [
    ['Public repos using Murmur', n.repos],
    ['Users, approximately (see below)', n.users],
    [`Users active in the last ${n.week} days`, n.activeUsers],
    [`New users in the last ${n.week} days`, n.newUsers],
    [`Active repos (anything on the bus in the last ${n.week} days)`, n.activeRepos],
    ['Contributors to those repos (each person once)', n.contributors],
    [`New contributors in the last ${n.week} days`, n.newContributors],
    ['Devices that have run Murmur in them', n.devices],
    ['People behind those devices', n.people],
    ['Bus records', n.records],
    [`Bus records in the last ${n.week} days`, n.weekRecords ?? 0],
    ['Data in those records', size(n.bytes)],
    [`Data in the last ${n.week} days`, size(n.weekBytes)],
    ['Agent sessions', approx(n.sessions, n.estSessions)],
    ['Of those sessions, estimated (see below)', n.estSessions ?? 0],
    [`Agent sessions in the last ${n.week} days`, approx(n.weekSessions, n.weekEstSessions)],
    ['Messages between agents', approx(n.messages, n.estMessages)],
    ['Of those messages, estimated (see below)', n.estMessages ?? 0],
    [`Messages in the last ${n.week} days`, approx(n.weekMessages, n.weekEstMessages)],
  ])}

Repos, devices, contributors, bus records and data are measured exactly, whether or not a bus is encrypted. Users, sessions and messages are the figures that matter most and can't all be read, so where they are estimated they are marked, and "What the numbers mean" says how.

## By week

Each week is the ${n.week} days ending on the date shown, back over the last ${n.days} days; the newest first. Repos, users and contributors are totals as of that day.

${table(['Week ending', 'Repos', 'Users', 'Contributors', 'Bus records', 'Data', 'Sessions', 'Messages'], weeks)}

## Repos

Most active in the last ${n.week} days first.

${repos}

## What the numbers mean

- A **repo** is listed once one of its clones reports it: Murmur sends a public repo's address, once per repo, and nothing else.
- Everything else is read by the census from what each repo shows anyone, once a day${n.counted ? ` (last at ${n.counted})` : ''}: its bus, \`refs/murmur/*\`, and its default branch's commits.
- A **user** is approximate: Murmur sends nothing that names a person. The census matches the git user name each device's presence shows to a commit author of that name in the repo, and takes that author's GitHub account; each account counts once across repos. Where no commit matches, the git name counts instead, and where no name was ever seen, the device. Of ${count(n.users, 'user')}, ${n.usersBy.account} ${n.usersBy.account === 1 ? 'was' : 'were'} matched to an account, ${n.usersBy.name} counted by git name, and ${n.usersBy.device} by device.
- A **contributor** has committed to a repo's default branch, at any time; one person in two repos counts once. A **device** is one clone running Murmur; a **person**, one git user name among a repo's devices, as their presence shows it, so one person in two repos counts twice. A daemon deletes its presence when it stops, so a device whose owner the census never saw counts as a person of its own.
- A **session** is an agent session, counted once on the day it started, however often its daemon restarts; a **message**, one agent's message to another. A **bus record** is anything a device wrote to its log, and **data** is the size of those records' events, as JSON, before encryption. Both are always exact, and data is the one measure of how much a bus carries that encryption doesn't hide.
- A figure marked **≈** includes an estimate. Since Murmur 0.3 a bus is encrypted, which hides what each record is, and nothing is decrypted to count it. A message is then an encrypted record large enough to be one. Checked against the true kinds on four buses (868 messages, 1,598 other records), that missed 2% of the messages, 0 to 5% by repo, the short ones with no body, and took no other record for a message; it would take one for a session's start on a branch of about 40 characters or more, or a tags event with many tags, and those buses had neither. A session is then one that the repo's name registry, which isn't encrypted, names: with the exact count it gave 569 of those buses' 570 sessions. It also names a session that another session started, which writes no start. \`site/census/census.mjs\` has the measurements.
- A count is only ever raised. Days are UTC.

The check-ins of earlier versions, which are no longer stored, are at ${origin}/stats/archive/report.md. The same numbers as JSON: ${origin}/stats/data?days=N (N from 14 to 730, default 90), with the same key. This page takes \`?days=N\` too.

## The census

${censusReport(n.census, table, short)}
`;
}

// The census's health, from health(), for the end of the report.
function censusReport(h, table, short) {
  const time = iso => `${iso.slice(0, 16).replace('T', ' ')} UTC`;
  const n = (k, one, many = one + 's') => `${k} ${k === 1 ? one : many}`;
  if (!h.repos) return 'It has no repos to count yet.';
  if (!h.last) return `It hasn't counted any of the ${n(h.repos, 'repo')} yet.`;
  const total = h.repos - h.fresh.length, counted = total - h.notCounted.length;
  const ago = h.hours ? `${n(h.hours, 'hour')} ago` : 'less than an hour ago';
  const of = counted === total ? (total === 1 ? 'the 1 repo' : `all ${total} repos`) : `${counted} of ${n(total, 'repo')}`;
  const since = h.fresh.length ? ` ${n(h.fresh.length, 'repo')} listed since ${h.fresh.length === 1 ? "hasn't" : "haven't"} been counted yet.` : '';
  const lines = [];
  if (h.late) lines.push(`**It hasn't run for ${h.hours} hours**, and it runs once a day.`);
  lines.push(`It last ran at ${time(h.last)}, ${ago}, and counted ${of}.${since}`);
  if (h.notCounted.length) {
    lines.push(`Not counted in that run, so their numbers are as of when each last was:

${table(['Repo', 'Last counted'], h.notCounted.map(r => [`[${short(r.name)}](https://${r.name})`, r.counted ? time(r.counted) : `never; listed ${r.listed}`]))}

A repo isn't counted when it can't be read, or is skipped for its time, refs or size. The census's run in Actions says which, and why: https://github.com/a-Fig/murmur/actions/workflows/census.yml.`);
  }
  return lines.join('\n\n');
}
