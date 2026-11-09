// The archive (#259): the daily check-ins that Murmur 0.2.3 to 0.2.x sent (#148), from 2026-11-03
// until the release that stopped storing them (#199) was deployed on 2026-11-09. Their rows stay in
// the checkins table and get no new ones, so these numbers are history: every "today" below is the
// last day with a check-in, and an active user is one who checked in in the 7 days up to it. src/usage.js serves them at
// /stats/archive, with src/stats-archive.html.

// "Active" is a check-in in the last ACTIVE_DAYS days; the goal is Tyler's.
const ACTIVE_DAYS = 7;
const GOAL = 100;

export const addDays = (day, n) => new Date(Date.parse(day + 'T00:00:00Z') + n * 864e5).toISOString().slice(0, 10);

// Everything the archive shows, for the `days` days ending on the last day with a check-in.
export async function archiveNumbers(db, days) {
  const ended = await db.prepare('SELECT MAX(day) AS day FROM checkins').first();
  const today = ended?.day ?? new Date().toISOString().slice(0, 10);
  const from = addDays(today, -(days - 1));
  const weekFrom = addDays(today, -(ACTIVE_DAYS - 1));
  const prevFrom = addDays(today, -(2 * ACTIVE_DAYS - 1)), prevTo = addDays(today, -ACTIVE_DAYS);
  const [series, firsts, current, teams, prev] = await db.batch([
    // Per day: users that day, and users in the ACTIVE_DAYS ending that day.
    db.prepare(
      `WITH RECURSIVE days(d) AS (SELECT ?1 UNION ALL SELECT date(d, '+1 day') FROM days WHERE d < ?2)
       SELECT d AS day,
         (SELECT COUNT(DISTINCT user) FROM checkins WHERE day = d) AS dau,
         (SELECT COUNT(DISTINCT user) FROM checkins WHERE day BETWEEN date(d, '-${ACTIVE_DAYS - 1} days') AND d) AS wau,
         (SELECT COUNT(DISTINCT team) FROM checkins WHERE day BETWEEN date(d, '-${ACTIVE_DAYS - 1} days') AND d) AS teams
       FROM days`,
    ).bind(from, today),
    // How many users first checked in on each day.
    db.prepare(`SELECT first AS day, COUNT(*) AS n FROM (SELECT MIN(day) AS first FROM checkins GROUP BY user) GROUP BY first`),
    // Each active user's latest check-in.
    db.prepare(`SELECT user, version, os, arch, MAX(day) AS last FROM checkins WHERE day >= ?1 GROUP BY user`).bind(weekFrom),
    db.prepare(
      `SELECT team, COUNT(DISTINCT CASE WHEN day >= ?1 THEN user END) AS active, COUNT(DISTINCT user) AS users,
         MIN(day) AS first, MAX(day) AS last
       FROM checkins GROUP BY team ORDER BY active DESC, users DESC, last DESC`,
    ).bind(weekFrom),
    db.prepare(`SELECT COUNT(DISTINCT user) AS n FROM checkins WHERE day BETWEEN ?1 AND ?2`).bind(prevFrom, prevTo),
  ]);

  const firstRows = firsts.results;
  const newSince = since => firstRows.filter(r => r.day >= since).reduce((s, r) => s + r.n, 0);
  // New users per ACTIVE_DAYS-long week, the last ending today.
  const weeks = [];
  for (let end = today; end >= from; end = addDays(end, -ACTIVE_DAYS)) {
    const start = addDays(end, -(ACTIVE_DAYS - 1));
    weeks.unshift({ start, end, n: firstRows.filter(r => r.day >= start && r.day <= end).reduce((s, r) => s + r.n, 0) });
  }
  const tally = (rows, f) => {
    const m = new Map();
    for (const r of rows) m.set(f(r), (m.get(f(r)) || 0) + 1);
    return [...m].map(([k, n]) => ({ k, n })).sort((a, b) => b.n - a.n || String(a.k).localeCompare(String(b.k)));
  };
  const active = current.results;
  const activeTeams = teams.results.filter(t => t.active > 0);
  const sizeOf = n => (n >= 6 ? '6+' : n >= 3 ? '3–5' : String(n));
  const sizes = ['1', '2', '3–5', '6+'].map(k => ({ k, n: activeTeams.filter(t => sizeOf(t.active) === k).length }));
  const last = series.results[series.results.length - 1] || { dau: 0, wau: 0, teams: 0 };

  return {
    today, goal: GOAL, activeDays: ACTIVE_DAYS, days,
    active: active.length,
    activeBefore: prev.results[0]?.n ?? 0,
    activeToday: last.dau,
    teams: activeTeams.length,
    newUsers: newSince(weekFrom),
    allUsers: firstRows.reduce((s, r) => s + r.n, 0),
    allTeams: teams.results.length,
    series: series.results,
    newByWeek: weeks,
    versions: tally(active, r => r.version),
    systems: tally(active, r => `${r.os}/${r.arch}`),
    teamSizes: sizes,
    teamList: teams.results.slice(0, 50).map(t => ({ ...t, team: t.team.slice(0, 8) })),
    checkInsEnded: today,
  };
}

// The numbers as markdown, for an agent: what the page shows, in tables, with what they mean.
export function archiveReport(n, origin) {
  const pct = Math.round((100 * n.active) / n.goal);
  const diff = n.active - n.activeBefore;
  const change = diff === 0 ? 'the same as' : `${diff > 0 ? 'up' : 'down'} ${Math.abs(diff)} from`;
  const table = (head, rows) =>
    [head, head.map(() => '---'), ...rows].map(r => `| ${r.join(' | ')} |`).join('\n');
  const byDay = new Map(n.series.map(s => [s.day, s]));
  const weeks = [...n.newByWeek].reverse().map(w => {
    const s = byDay.get(w.end) || { wau: 0, teams: 0 };
    return [w.end, s.wau, s.teams, w.n];
  });
  const recent = n.series.slice(-14).reverse().map(s => [s.day, s.dau, s.wau]);
  const tally = (rows, what) => (rows.length ? table([what, 'Active users'], rows.map(r => [r.k, r.n])) : 'None yet.');
  const teams = n.teamList.length
    ? table(['Team', 'Active users', 'Users ever', 'First check-in', 'Last check-in'], n.teamList.map(t => [t.team, t.active, t.users, t.first, t.last]))
    : 'None yet.';
  return `# Archive: Murmur's active users, as of ${n.today}

> History. These numbers come from the daily check-ins of Murmur 0.2.3 onward, which the site stopped storing after ${n.checkInsEnded}, so every "today" and "last 7 days" here ends on that day. Murmur now sends only a public repo's address, once per repo; what those repos show is at ${origin}/stats/report.md.

**${n.active} active users**, ${pct}% of the goal of ${n.goal} (${Math.max(0, n.goal - n.active)} to go), ${change} the ${n.activeDays} days before (${n.activeBefore}).

${table(['Measure', 'Count'], [
    ['Active users (a check-in in the last 7 days)', n.active],
    ['Users today', n.activeToday],
    ['Active teams', n.teams],
    [`New users (first check-in in the last ${n.activeDays} days)`, n.newUsers],
    ['Returning active users', Math.max(0, n.active - n.newUsers)],
    ['Users ever', n.allUsers],
    ['Teams ever', n.allTeams],
  ])}

## By week

Each week is the ${n.activeDays} days ending on the date shown, back over the last ${n.days} days; the newest first.

${table(['Week ending', 'Active users', 'Active teams', 'New users'], weeks)}

## The last 14 days

${table(['Day', 'Users that day', `Active users (${n.activeDays} days ending that day)`], recent)}

## Active users' versions

${tally(n.versions, 'Version')}

## Active users' systems

${tally(n.systems, 'OS/arch')}

## Active teams by size

${table(['Active users', 'Teams'], n.teamSizes.map(s => [s.k, s.n]))}

## Teams

Most active first, at most 50. A team is shown by the first 8 characters of its hash.

${teams}

## What the numbers mean

- A **user** is one user account on one machine, known by a random ID, so one person on two machines counts twice.
- A **team** is one repo, known by a hash of its remote's address. No names, repos, messages or paths are sent.
- A daemon checks in at most once a UTC day, and only while one of its sessions is open or for 10 minutes after, so a check-in means someone used Murmur that day. Days are UTC.
- Check-ins began with Murmur 0.2.3 (2026-11-03), and the last was on ${n.checkInsEnded}.

The same numbers as JSON: ${origin}/stats/archive/data?days=N (N from 14 to 730, default 14), with the same key. This page takes \`?days=N\` too.
`;
}
