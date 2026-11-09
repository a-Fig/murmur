// The census (#259): counts what each public repo that uses Murmur shows anyone, and sends the
// counts to murmur.afig.dev, which keeps them for the stats page. .github/workflows/census.yml runs
// it each day; it runs anywhere with Node 20 and git:
//
//   CENSUS_KEY=... node site/census/census.mjs            # every repo the site lists
//   node site/census/census.mjs --dry github.com/a/b      # print one repo's counts; send nothing
//
// For each repo it fetches two public things, as any reader could:
// - its bus, refs/murmur/*: each device's log of events (sessions started, messages) and its
//   presence (which names the git user);
// - its default branch's commits, without their files: who committed, and when each first did.
//
// It sends counts and hashes, not content: per UTC day, how many sessions, messages and records
// there were and which devices wrote, each device by a hash with a hash of its owner and of the
// GitHub account it is matched to (#323); and each contributor by a hash, so one person in two
// repos counts once.
// Once the bus is encrypted (#257) a record's kind is hidden, and only records are counted.

import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const SITE = process.env.CENSUS_SITE || 'https://murmur.afig.dev';
const REPO = /^github\.com\/([A-Za-z0-9][A-Za-z0-9-]{0,38})\/([A-Za-z0-9._-]{1,100})$/;

function git(dir, args, opts = {}) {
  return execFileSync('git', ['-C', dir, ...args], {
    encoding: 'utf8', maxBuffer: 1 << 30, stdio: ['ignore', 'pipe', 'pipe'],
    env: { ...process.env, GIT_TERMINAL_PROMPT: '0' }, ...opts,
  });
}

const dayOf = t => {
  const ms = Date.parse(t);
  return Number.isFinite(ms) ? new Date(ms).toISOString().slice(0, 10) : null;
};

// Each commit on ref, newest first: its hash, committer date and message.
function log(dir, ref) {
  const out = git(dir, ['log', '--format=%H%x00%cI%x00%B%x01', ref]);
  return out.split('\x01').map(s => s.replace(/^\n/, '')).filter(s => s.includes('\x00')).map(s => {
    const [hash, date, msg] = s.split('\x00');
    return { hash, date, msg };
  });
}

// The JSON record after a bus commit's subject line, or null.
function record(msg) {
  const i = msg.indexOf('\n\n');
  if (i < 0) return null;
  try {
    const v = JSON.parse(msg.slice(i + 2).trim());
    return v && typeof v === 'object' ? v : null;
  } catch {
    return null;
  }
}

// A GitHub no-reply address stands for its login; any other address for itself.
function identity(email, name) {
  const e = email.trim().toLowerCase();
  const m = /^(?:\d+\+)?([^@]+)@users\.noreply\.github\.com$/.exec(e);
  if (m) return 'login:' + m[1];
  return e ? 'email:' + e : 'name:' + name.trim().toLowerCase();
}

const hash = s => createHash('sha256').update(s).digest('hex').slice(0, 16);

// What one repo shows: its counts by day, its totals, and its devices.
//
// A device is sent as a hash of its ID, with a hash of its owner (the git user name its presence
// shows) when the bus shows one. A daemon deletes its presence when it stops, so the owner is often
// missing; the site keeps an owner once it has seen one, and counts a device whose owner it has
// never seen as a person of its own. A device counts as active on a day it wrote anything: an event
// or its presence.
//
// A session counts once, on the first day it started: a daemon that restarts announces its running
// sessions again under the same IDs.
export function count(dir) {
  const refs = git(dir, ['for-each-ref', '--format=%(refname)', 'refs/murmur/']).split('\n').filter(Boolean);
  const days = new Map(); // day -> { devices: Set, sessions, messages, records }
  const day = d => {
    if (!days.has(d)) days.set(d, { devices: new Set(), sessions: 0, messages: 0, records: 0 });
    return days.get(d);
  };
  const owners = new Map(); // device -> git user name, from its newest presence that shows one
  const started = new Map(); // session -> the first day it started
  const seen = new Set();
  const devices = new Set();
  let messages = 0, records = 0;
  for (const ref of refs) {
    const [, , kind, device] = ref.split('/');
    if ((kind !== 'mesh' && kind !== 'presence') || !device) continue;
    for (const c of log(dir, ref)) {
      if (seen.has(c.hash)) continue;
      seen.add(c.hash);
      const r = record(c.msg);
      if (!r) continue;
      const d = dayOf(r.at) || dayOf(c.date);
      if (!d) continue;
      devices.add(device);
      const t = day(d);
      t.devices.add(device);
      if (kind === 'presence') {
        if (typeof r.owner === 'string' && r.owner && !owners.has(device)) owners.set(device, r.owner);
        continue;
      }
      t.records++, records++;
      if (r.type === 'message') t.messages++, messages++;
      if (r.type === 'session.started') {
        const id = typeof r.from === 'string' && r.from ? r.from : c.hash;
        if (!started.has(id) || d < started.get(id)) started.set(id, d);
      }
    }
  }
  for (const d of started.values()) day(d).sessions++;
  const sorted = [...days.keys()].sort();
  return {
    days: sorted.map(d => {
      const t = days.get(d);
      return { day: d, sessions: t.sessions, messages: t.messages, records: t.records, devices: [...t.devices].map(v => hash('device:' + v)).sort() };
    }),
    // The owner's name stays here: census() hashes it, and matches it to an account, before sending.
    devices: [...devices].sort().map(v => ({ id: hash('device:' + v), owner: owners.get(v) ?? null })),
    totals: { sessions: started.size, messages, records, first_day: sorted[0] ?? null, last_day: sorted[sorted.length - 1] ?? null },
  };
}

// The default branch's human commits: each one's hash, author name (lowercased), identity and day.
export function authors(dir) {
  const out = git(dir, ['log', '--format=%H%x00%aE%x00%aN%x00%aI', 'FETCH_HEAD']);
  const all = [];
  for (const line of out.split('\n')) {
    const [sha, email, name, at] = line.split('\x00');
    if (!at || /\[bot\]/i.test(name) || /\[bot\]@/i.test(email)) continue;
    all.push({ sha, name: name.trim().toLowerCase(), identity: identity(email, name), day: dayOf(at) });
  }
  return all;
}

// Everyone who has committed to the default branch, with the day of their first commit there.
export function contributors(commits) {
  const first = new Map();
  for (const c of commits) {
    const id = hash(c.identity);
    if (c.day && (!first.has(id) || c.day < first.get(id))) first.set(id, c.day);
  }
  return [...first].map(([id, f]) => ({ id, first: f }));
}

// The GitHub account behind each device owner, for the stats page's users (#323). Murmur sends
// nothing that names an account, and a bus commit names only its device, so an owner (the git user
// name its presence shows) is matched to the repo's commit authors of the same name.
//
// The site keeps whatever account a device is sent last, so the choice must not drift as history
// grows: identities are taken oldest first, by each one's oldest commit, which new commits never
// change. A GitHub login wins, the oldest; else the oldest email that lookup(sha) turns into a
// login, so one person committing from two addresses is one account; else the oldest email.
// lookup answers the commit author's login, null when GitHub links the commit to no account, or
// undefined when it couldn't ask. An owner with no commit under its name is left out, and so is one
// with an email that couldn't be asked about before a login was found: it might have been the
// login, and the site keeps the account it was sent before. At most MAX_LOOKUPS emails are asked
// about per owner.
const MAX_LOOKUPS = 5;
export async function accounts(owners, commits, lookup) {
  const byName = new Map(); // name -> identity -> { sha, at }: its oldest commit, and that commit's place
  commits.forEach((c, at) => { // newest first, so the last one kept is the oldest
    if (!byName.has(c.name)) byName.set(c.name, new Map());
    byName.get(c.name).set(c.identity, { sha: c.sha, at });
  });
  const out = new Map(); // owner -> identity
  for (const owner of new Set(owners)) {
    const m = byName.get(owner.trim().toLowerCase());
    if (!m) continue;
    const oldestFirst = [...m].sort(([, x], [, y]) => y.at - x.at);
    const login = oldestFirst.find(([who]) => who.startsWith('login:'));
    if (login) {
      out.set(owner, login[0]);
      continue;
    }
    let found = null, unsure = false;
    for (const [, { sha }] of oldestFirst.slice(0, MAX_LOOKUPS)) {
      const l = await lookup(sha);
      if (l === undefined) { unsure = true; break; }
      if (l) { found = 'login:' + l.toLowerCase(); break; }
    }
    if (found) out.set(owner, found);
    else if (!unsure) out.set(owner, oldestFirst[0][0]);
  }
  return out;
}

// lookup for accounts, from GitHub's API: the commit author's login, null when the API answers that
// no account is linked, or undefined when it can't be asked (a rate limit, an outage, a timeout).
// Once rate limited it stops asking. GITHUB_TOKEN, if set, raises the limit.
function apiLookup(owner, name) {
  let off = false;
  return async sha => {
    if (off) return undefined;
    const headers = { 'User-Agent': 'murmur-census', Accept: 'application/vnd.github+json' };
    if (process.env.GITHUB_TOKEN) headers.Authorization = `Bearer ${process.env.GITHUB_TOKEN}`;
    try {
      const res = await fetch(`https://api.github.com/repos/${owner}/${name}/commits/${sha}`, { headers, signal: AbortSignal.timeout(10000) });
      if (res.status === 403 || res.status === 429) off = true;
      if (!res.ok) return undefined;
      const login = (await res.json())?.author?.login;
      return typeof login === 'string' && login ? login : null;
    } catch {
      return undefined;
    }
  };
}

// Fetches one repo's bus and its default branch's commits into a scratch repo and counts them.
export async function census(name, lookup = apiLookup) {
  const m = REPO.exec(name);
  if (!m) throw new Error(`not a repo: ${name}`);
  const dir = mkdtempSync(join(tmpdir(), 'census-'));
  try {
    git(dir, ['init', '-q', '--bare']);
    const url = `https://github.com/${m[1]}/${m[2]}.git`;
    git(dir, ['fetch', '-q', '--no-tags', url, '+refs/murmur/*:refs/murmur/*']);
    const bus = count(dir);
    git(dir, ['fetch', '-q', '--no-tags', '--filter=tree:0', url, 'HEAD']);
    const commits = authors(dir);
    const matched = await accounts(bus.devices.map(v => v.owner).filter(Boolean), commits, lookup(m[1], m[2]));
    const devices = bus.devices.map(({ id, owner }) => ({
      id,
      person: owner ? hash('owner:' + owner.trim().toLowerCase()) : null,
      account: matched.has(owner) ? hash(matched.get(owner)) : null,
    }));
    return { repo: name.toLowerCase(), ...bus, devices, contributors: contributors(commits) };
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

async function call(path, init = {}) {
  const res = await fetch(SITE + path, {
    ...init,
    headers: { Authorization: `Bearer ${process.env.CENSUS_KEY}`, 'Content-Type': 'application/json', ...init.headers },
  });
  const text = await res.text();
  if (!res.ok) throw new Error(`${path}: ${res.status} ${text.trim()}`);
  return JSON.parse(text);
}

async function main(args) {
  const dry = args.includes('--dry');
  let names = args.filter(a => !a.startsWith('--'));
  if (!dry && !process.env.CENSUS_KEY) throw new Error('CENSUS_KEY is not set');
  if (!names.length) names = (await call('/v1/census')).repos;
  let failed = 0;
  for (const name of names) {
    let c;
    try {
      c = await census(name);
    } catch (err) {
      // A repo that has gone private or been deleted can't be read, and keeps the counts it had.
      console.error(`${name}: not counted: ${String(err.stderr || err.message).trim()}`);
      failed++;
      continue;
    }
    const line = `${name}: ${c.devices.length} devices, ${new Set(c.devices.map(v => v.person ?? v.id)).size} people, ` +
      `${c.devices.filter(v => v.account).length} matched to an account, ${c.totals.sessions} sessions, ` +
      `${c.totals.messages} messages, ${c.totals.records} records, ${c.contributors.length} contributors, ${c.days.length} days`;
    if (dry) {
      console.log(line);
      console.log(JSON.stringify(c.days));
      continue;
    }
    await call('/v1/census', { method: 'POST', body: JSON.stringify(c) });
    console.log(line);
  }
  if (failed === names.length && names.length) throw new Error('no repo could be counted');
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  main(process.argv.slice(2)).catch(err => {
    console.error(String(err.message || err));
    process.exit(1);
  });
}
