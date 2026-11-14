// The census (#259): counts what each public repo that uses Murmur shows anyone, and sends the
// counts to murmur.afig.dev, which keeps them for the stats page. .github/workflows/census.yml runs
// it each day; it runs anywhere with Node 20 and git:
//
//   CENSUS_KEY=... node site/census/census.mjs            # every repo the site lists
//   node site/census/census.mjs --dry github.com/a/b      # print one repo's counts; send nothing
//
// Run by hand with a key and a repo named, it sends that repo's counts. The stats then take it for
// the census's last run (src/repos.js, RUN), and every other repo looks missed by it until the
// next scheduled run.
//
// For each repo it fetches two public things, as any reader could, and commits only:
// - its bus, refs/murmur/mesh/*, refs/murmur/presence/* and refs/murmur/names: each device's log of
//   events (sessions started, messages), its presence (which names the git user), and the registry
//   of session names;
// - its default branch's commits, without their files: who committed, and when each first did.
//
// It sends counts and hashes, not content: per UTC day, how many sessions, messages and records
// there were, how many bytes those records' events came to, and which devices wrote, each device
// by a hash with a hash of its owner and of the GitHub account it is matched to (#323); and each
// contributor by a hash, so one person in two repos counts once.
//
// Since Murmur 0.3 every event is encrypted (#156), and its kind is sealed inside with the rest.
// The record count and the bytes stay exact. The sessions and messages an encrypted log hides are
// estimated (#550), and sent apart from the exact counts, as est_sessions and est_messages.
// "Estimating what encryption hides", below, says how, and how far off the estimates are.

import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { appendFileSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const SITE = process.env.CENSUS_SITE || 'https://murmur.afig.dev';
const REPO = /^github\.com\/([A-Za-z0-9][A-Za-z0-9-]{0,38})\/([A-Za-z0-9._-]{1,100})$/;

// What one repo may cost: BUDGET of time, for its git and its account lookups, and MAX_REFS refs
// on its bus, each a git log. Anyone can list a repo, and one with huge objects or 100,000 refs
// would otherwise hold the census past its job's 30 minutes, and every repo after it would go
// uncounted. A repo over either is skipped.
//
// MAX_REFS is a cost, not the site's limit: 2,000 refs can be up to 2,000 devices, and the site
// takes at most 1,000 on one day. tooBig() checks a repo's counts against the site's limits once
// they are made, so a repo too big for the site is skipped here, with the reason, not refused there.
const BUDGET = 3 * 60e3;
const MAX_REFS = 2000;

const skip = message => Object.assign(new Error(message), { skipped: true });

// Why the site would refuse these counts for their size (src/repos.js, invalid()), or "".
export function tooBig(c) {
  const over = (n, max, what) => (n > max ? `${n} ${what}, more than the site takes (${max})` : '');
  return over(c.days.length, 3660, 'days on its bus') ||
    over(c.days.reduce((n, d) => Math.max(n, d.devices.length), 0), 1000, 'devices on one day') ||
    over(c.days.reduce((n, d) => n + d.devices.length, 0), 100000, "devices on the bus's days") ||
    over(c.devices.length, 10000, 'devices') ||
    over(c.contributors.length, 100000, 'contributors');
}

// Runs git in dir, killed at `deadline` (a Date.now() time).
function git(dir, args, deadline = Infinity) {
  const timeout = deadline - Date.now();
  if (timeout <= 0) throw skip('took too long to count');
  try {
    return execFileSync('git', ['-C', dir, ...args], {
      encoding: 'utf8', maxBuffer: 1 << 30, stdio: ['ignore', 'pipe', 'pipe'],
      env: { ...process.env, GIT_TERMINAL_PROMPT: '0' },
      ...(Number.isFinite(timeout) && { timeout, killSignal: 'SIGKILL' }),
    });
  } catch (err) {
    if (err.code === 'ETIMEDOUT') throw skip('took too long to count');
    throw err;
  }
}

const dayOf = t => {
  const ms = Date.parse(t);
  const d = Number.isFinite(ms) ? new Date(ms).toISOString().slice(0, 10) : null;
  return d && /^\d{4}-\d\d-\d\d$/.test(d) ? d : null;
};

// A time on the bus or the branch is the writer's clock's, and can say anything from year 0 to 9999.
// A day after tomorrow (UTC) isn't believed: a clock can run a little fast, but a day in the future
// would keep a repo active and its people new for ever. Nor is a bus day before EPOCH, when there
// was no bus yet. The site refuses both (src/repos.js has the same EPOCH).
const EPOCH = '2026-10-01';
const latestDay = now => new Date(now + 864e5).toISOString().slice(0, 10);

// The largest commit message a reader of the bus reads: Murmur's own limit (internal/bus/repo.go,
// MaxRecord), which nothing it writes comes near. Git stores a message compressed, so a repo can
// push megabytes of padding in a few kilobytes; a daemon passes such a commit over as junk, and so
// does the census, or one commit could add gigabytes to a repo's bytes for good.
const MAX_RECORD = 1 << 20;

// Each commit on a bus ref, newest first: its hash, committer date and message. One whose message
// is over MAX_RECORD is left out.
function log(dir, ref, deadline) {
  const out = git(dir, ['log', '--format=%H%x00%cI%x00%B%x01', ref], deadline);
  return out.split('\x01').map(s => s.replace(/^\n/, '')).filter(s => s.includes('\x00') && Buffer.byteLength(s) <= MAX_RECORD).map(s => {
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

// The length in bytes of the JSON after a bus commit's subject line.
const jsonBytes = msg => Buffer.byteLength(msg.slice(msg.indexOf('\n\n') + 2).trim());

// A GitHub no-reply address stands for its login; any other address for itself.
function identity(email, name) {
  const e = email.trim().toLowerCase();
  const m = /^(?:\d+\+)?([^@]+)@users\.noreply\.github\.com$/.exec(e);
  if (m) return 'login:' + m[1];
  return e ? 'email:' + e : 'name:' + name.trim().toLowerCase();
}

const hash = s => createHash('sha256').update(s).digest('hex').slice(0, 16);

// Estimating what encryption hides (#550)
//
// Nothing here is decrypted: the census holds no key. It reads only public repos, as anyone can,
// and uses two things VISION lists as visible to any reader of one: how large each record is, and
// the name registry. A size says roughly what kind of event a record is, never what it says.
//
// Messages, from size. An encrypted event's box holds the event's JSON, encrypted with AES-GCM,
// which adds 16 bytes (internal/bus/box.go), so the box's length gives the event's length exactly.
// An event's length follows from its fields (internal/bus/event.go):
// - a session's stop is 66 bytes plus its address and its time, so about 100 more than the length
//   of its device's ID, which its ref shows;
// - a start adds 23 and its agent and branch;
// - a message adds 13 and its recipient and subject, and 10 and its body when it has one.
// So an encrypted event at least MESSAGE_FROM bytes longer than its device's ID is counted as a
// message.
//
// Measured on 2026-11-13 against the unencrypted history of two public repos, a-Fig/murmur and
// SomeoneElseSt/aie-hack (three devices; 155 messages, 589 starts and 505 stops), in bytes beyond
// the device's ID: stops were 98 to 101, starts 130 to 171, messages 158 to 2,574. At 180:
// - 8 of the 155 messages (5%) were shorter and would be missed: each had no body and a subject
//   under 40 characters;
// - none of the 1,094 session events was as long. A start is, once its agent and branch together
//   pass about 55 characters; the longest seen were 47. Any cut from 172 to 180 took no start for
//   a message; 180 leaves room for a longer branch, at the cost of 3 more messages missed than 172.
// So the estimate runs about 5% low on traffic like that history's. It can also run high, by what
// that history couldn't measure:
// - a start from a session on a long branch reads as a message, as above, and again each time
//   its daemon restarts and announces it;
// - it has no tags or group.add events (page and group actions, internal/bus/event.go). One is
//   about the size of a start, and reads as a message with some 40 characters of tags or more.
// And lower: every message in it was to one session or device. One to a group or to everyone has
// a shorter recipient, so more of the short ones are missed.
//
// Checked on 2026-11-13 against the truth, on four buses Tyler's machine could read whole: three
// whose team key it holds, opened there to read each event's type and lengths and nothing else,
// and one never encrypted. They held 868 messages and 1,598 other records; 154 of the messages
// and 1,018 of the others were among the measurements above. The cut missed 19 messages (2.2%; 0
// to 5.2% by repo), each with no body, and took no other record for a message. The longest start
// was 173, 7 bytes short of the cut, and the 4 tags events were 139 to 173. Of 12 messages to
// everyone it missed 1; none was to a group. Each of the 667 events opened was exactly as long as
// its box said.
//
// Sessions, from the name registry, not from size. A start re-announced after its daemon
// restarts, a tags event and a group.add are all the size of a start: start-sized records were
// 1.35 times the sessions in murmur's unencrypted history, and 1.3 to 3 times the registry's
// sessions on three encrypted buses. The registry (refs/murmur/names, internal/bus/names.go) is
// not encrypted, and each session that takes a name is recorded there, by its ID. It held 445
// of the 446 sessions those two histories started, and one they didn't show. So a session the
// registry names that no unencrypted start shows is counted as estimated, on the day of its
// registry record. It is told from the exact ones by its ID, so none counts twice. In the check
// above, the exact count and this estimate together gave 569 of the four buses' 570 sessions, and
// 61 of the 62 that only an encrypted start showed. Two things make it an estimate:
// - since 2026-11-13 a session another session started takes a name but writes no start (#487),
//   so the registry counts it, where the exact count no longer would;
// - a snapshot (a registry rebuilt after it was lost) isn't read, since it would date every older
//   session to the day it was written. Sessions from before one are missed. The site keeps the
//   days it already had, and only ever raises a count, so a repo's total then stays at what it
//   was until the sessions since the snapshot pass it.
const MESSAGE_FROM = 180;
const GCM_TAG = 16;
const BASE64 = /^[A-Za-z0-9+/]*={0,2}$/;
const NAME = /^[a-z0-9-]{1,40}$/;

// The length in bytes of the event inside an encrypted record, or null if r isn't one.
export function boxedSize(r) {
  const data = r.type === 'box' && r.box && typeof r.box === 'object' ? r.box.data : null;
  if (typeof data !== 'string' || data.length % 4 || !BASE64.test(data)) return null;
  const size = (data.length / 4) * 3 - (data.endsWith('==') ? 2 : data.endsWith('=') ? 1 : 0) - GCM_TAG;
  return size > 0 ? size : null;
}

// The session a registry record gives a name to, as device/session, or null: a reservation names
// no session, and a person (device/human, #268) isn't an agent session.
function named(r) {
  if (typeof r.device !== 'string' || typeof r.session !== 'string') return null;
  if (!NAME.test(r.device) || !NAME.test(r.session) || r.session === 'human') return null;
  return `${r.device}/${r.session}`;
}

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
//
// A record is dated by its `at`, or by its commit's date when `at` isn't believable, and isn't
// counted when neither is.
//
// bytes is how much the devices wrote to their logs (#550): the length of each record's event as
// JSON, which for an encrypted event is the event inside the box, so that a log weighs the same
// encrypted or not. Like records it is exact either way, and unlike them it grows with what the
// agents say to each other, so it is the one measure of how much a bus carries that encryption
// doesn't hide.
//
// sessions and messages are exact: what unencrypted records show. est_sessions and est_messages
// are the estimates of what encrypted ones hide, to be added to them (see "Estimating what
// encryption hides", above).
export function count(dir, { now = Date.now(), deadline = Infinity, maxRefs = MAX_REFS } = {}) {
  const latest = latestDay(now);
  const busDay = t => {
    const d = dayOf(t);
    return d && d >= EPOCH && d <= latest ? d : null;
  };
  const refs = git(dir, ['for-each-ref', '--format=%(refname)', 'refs/murmur/'], deadline).split('\n').filter(Boolean);
  if (refs.length > maxRefs) throw skip(`${refs.length} refs on its bus, more than ${maxRefs}`);
  const days = new Map(); // day -> { devices: Set, sessions, messages, records, bytes, est_sessions, est_messages }
  const day = d => {
    if (!days.has(d)) days.set(d, { devices: new Set(), sessions: 0, messages: 0, records: 0, bytes: 0, est_sessions: 0, est_messages: 0 });
    return days.get(d);
  };
  const owners = new Map(); // device -> git user name, from its newest presence that shows one
  const started = new Map(); // session -> the first day it started
  const registered = new Map(); // session -> the first day the registry named it
  const seen = new Set();
  const devices = new Set();
  let messages = 0, records = 0, bytes = 0, estMessages = 0;
  for (const ref of refs) {
    const [, , kind, device] = ref.split('/');
    if (ref === 'refs/murmur/names') {
      for (const c of log(dir, ref, deadline)) {
        const r = record(c.msg);
        const id = r && named(r);
        const d = id && (busDay(r.at) || busDay(c.date));
        if (d && (!registered.has(id) || d < registered.get(id))) registered.set(id, d);
      }
      continue;
    }
    if ((kind !== 'mesh' && kind !== 'presence') || !device) continue;
    for (const c of log(dir, ref, deadline)) {
      if (seen.has(c.hash)) continue;
      seen.add(c.hash);
      const r = record(c.msg);
      if (!r) continue;
      const d = busDay(r.at) || busDay(c.date);
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
      const size = boxedSize(r);
      if (size !== null && size - device.length >= MESSAGE_FROM) t.est_messages++, estMessages++;
      const wrote = size ?? jsonBytes(c.msg);
      t.bytes += wrote, bytes += wrote;
    }
  }
  for (const d of started.values()) day(d).sessions++;
  let estSessions = 0;
  for (const [id, d] of registered) if (!started.has(id)) day(d).est_sessions++, estSessions++;
  const sorted = [...days.keys()].sort();
  return {
    days: sorted.map(d => {
      const t = days.get(d);
      return {
        day: d, sessions: t.sessions, messages: t.messages, records: t.records, bytes: t.bytes,
        est_sessions: t.est_sessions, est_messages: t.est_messages,
        devices: [...t.devices].map(v => hash('device:' + v)).sort(),
      };
    }),
    // The owner's name stays here: census() hashes it, and matches it to an account, before sending.
    devices: [...devices].sort().map(v => ({ id: hash('device:' + v), owner: owners.get(v) ?? null })),
    totals: {
      sessions: started.size, messages, records, bytes, est_sessions: estSessions, est_messages: estMessages,
      first_day: sorted[0] ?? null, last_day: sorted[sorted.length - 1] ?? null,
    },
  };
}

// The default branch's human commits: each one's hash, author name (lowercased), identity and day.
// A commit dated after tomorrow is on the branch all the same, so it is dated today: its author
// counts as a contributor as of today, and the site keeps the earliest day it is sent. Old dates
// are believed: people commit for years before a repo takes up Murmur.
export function authors(dir, { now = Date.now(), deadline = Infinity } = {}) {
  const today = new Date(now).toISOString().slice(0, 10), latest = latestDay(now);
  const out = git(dir, ['log', '--format=%H%x00%aE%x00%aN%x00%aI', 'FETCH_HEAD'], deadline);
  const all = [];
  for (const line of out.split('\n')) {
    const [sha, email, name, at] = line.split('\x00');
    if (!at || /\[bot\]/i.test(name) || /\[bot\]@/i.test(email)) continue;
    let day = dayOf(at);
    if (day > latest) day = today;
    all.push({ sha, name: name.trim().toLowerCase(), identity: identity(email, name), day });
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
// name its presence shows) is matched to the repo's commit authors of the same name. Of the
// identities that name has committed as, a GitHub login wins, then the one with the most commits.
// An email is turned into its login when lookup(sha) can say, so the same person committing from
// two addresses is one account. lookup answers the commit author's login, null when GitHub links
// the commit to no account, or undefined when it couldn't ask. An owner with no commit under its
// name is left out, and so is one whose lookup couldn't be asked: the site keeps the account it
// was matched to before, and sending the email in its place would replace a login with it.
export async function accounts(owners, commits, lookup) {
  const byName = new Map(); // name -> identity -> { n, sha }
  for (const c of commits) {
    if (!byName.has(c.name)) byName.set(c.name, new Map());
    const m = byName.get(c.name);
    if (!m.has(c.identity)) m.set(c.identity, { n: 0, sha: c.sha });
    m.get(c.identity).n++;
  }
  const isLogin = s => (s.startsWith('login:') ? 1 : 0);
  const out = new Map(); // owner -> identity
  for (const owner of new Set(owners)) {
    const m = byName.get(owner.trim().toLowerCase());
    if (!m) continue;
    const [[who, { sha }]] = [...m].sort(([a, x], [b, y]) => isLogin(b) - isLogin(a) || y.n - x.n || a.localeCompare(b));
    const login = isLogin(who) ? null : await lookup(sha);
    if (login === undefined) continue;
    out.set(owner, login ? 'login:' + login.toLowerCase() : who);
  }
  return out;
}

// lookup for accounts, from GitHub's API: the commit author's login, null when the API answers that
// no account is linked, or undefined when it can't be asked (a rate limit, an outage, a timeout).
// Once rate limited it stops asking. GITHUB_TOKEN, if set, raises the limit.
//
// The repo's deadline holds here too: a request is cut off at it, as well as at 10 seconds, and
// none starts after it. A repo with a thousand owners and a slow API would otherwise take hours.
// The repo is still counted, without the accounts not asked for, and the site keeps the ones it
// was sent before.
export function apiLookup(owner, name, deadline = Infinity) {
  let off = false;
  return async sha => {
    const left = deadline - Date.now();
    if (off || left <= 0) return undefined;
    const headers = { 'User-Agent': 'murmur-census', Accept: 'application/vnd.github+json' };
    if (process.env.GITHUB_TOKEN) headers.Authorization = `Bearer ${process.env.GITHUB_TOKEN}`;
    try {
      const res = await fetch(`https://api.github.com/repos/${owner}/${name}/commits/${sha}`, { headers, signal: AbortSignal.timeout(Math.min(10000, left)) });
      if (res.status === 403 || res.status === 429) off = true;
      if (!res.ok) return undefined;
      const login = (await res.json())?.author?.login;
      return typeof login === 'string' && login ? login : null;
    } catch {
      return undefined;
    }
  };
}

// Fetches one repo's bus and its default branch's commits into a scratch repo and counts them,
// within BUDGET. `url`, where it is fetched from, and `lookup` are for the tests.
//
// Only the refs count() reads are fetched, and of them only commits: a bus commit's tree is empty
// (VISION), and a ref elsewhere under refs/murmur/, or a commit made to look like a bus's, could
// point at gigabytes.
//
// The name registry is fetched on its own, because a fetch that names a ref the remote doesn't
// have fails whole, and a bus from before the registry has none. Without it the repo is counted as
// before, with no estimate of its sessions; the site keeps the estimate it was last sent.
export async function census(name, { url, budget = BUDGET, maxRefs = MAX_REFS, lookup = apiLookup } = {}) {
  const m = REPO.exec(name);
  if (!m) throw new Error(`not a repo: ${name}`);
  const deadline = Date.now() + budget;
  url ??= `https://github.com/${m[1]}/${m[2]}.git`;
  const dir = mkdtempSync(join(tmpdir(), 'census-'));
  try {
    git(dir, ['init', '-q', '--bare'], deadline);
    git(dir, ['fetch', '-q', '--no-tags', '--filter=tree:0', url,
      '+refs/murmur/mesh/*:refs/murmur/mesh/*', '+refs/murmur/presence/*:refs/murmur/presence/*'], deadline);
    try {
      git(dir, ['fetch', '-q', '--no-tags', '--filter=tree:0', url, '+refs/murmur/names:refs/murmur/names'], deadline);
    } catch (err) {
      if (err.skipped) throw err;
    }
    const bus = count(dir, { deadline, maxRefs });
    git(dir, ['fetch', '-q', '--no-tags', '--filter=tree:0', url, 'HEAD'], deadline);
    const commits = authors(dir, { deadline });
    const matched = await accounts(bus.devices.map(v => v.owner).filter(Boolean), commits, lookup(m[1], m[2], deadline));
    const devices = bus.devices.map(({ id, owner }) => ({
      id,
      person: owner ? hash('owner:' + owner.trim().toLowerCase()) : null,
      account: matched.has(owner) ? hash(matched.get(owner)) : null,
    }));
    return { repo: name.toLowerCase(), ...bus, devices, contributors: contributors(commits) };
  } finally {
    // Retried: a git killed at the deadline may leave a child writing here for a moment.
    rmSync(dir, { recursive: true, force: true, maxRetries: 5 });
  }
}

// The run as Markdown, for its page in Actions ($GITHUB_STEP_SUMMARY), written as it goes: a
// head with how many repos are listed, then each repo's row as it is done, with its counts or why
// it has none, then the totals of those sent and how many were sent, skipped, not read and not
// sent. A run cancelled at its job's timeout keeps the rows it wrote, without the end.
//
// A reason can be git's or the site's words, so it is kept to one line, and anything Markdown or
// a table would read in it is escaped.
//
// Sessions and Messages are the exact counts; the estimates of what encryption hides have their
// own columns, and a repo's sessions or messages are the two together.
const COLUMNS = ['Devices', 'People', 'Matched to an account', 'Sessions', 'Estimated sessions', 'Messages', 'Estimated messages', 'Records', 'Bytes', 'Contributors', 'Days'];
const KINDS = ['sent', 'counted', 'skipped', 'not read', 'not sent'];
const cells = cs => `| ${cs.join(' | ')} |\n`;
const numbers = c => [c.devices.length, new Set(c.devices.map(v => v.person ?? v.id)).size,
  c.devices.filter(v => v.account).length, c.totals.sessions, c.totals.est_sessions ?? 0,
  c.totals.messages, c.totals.est_messages ?? 0, c.totals.records, c.totals.bytes ?? 0, c.contributors.length, c.days.length];
const text = s => String(s).replace(/\s+/g, ' ').trim().slice(0, 300).replace(/[\\`*_[\]<>|]/g, '\\$&');

export function summaryHead(listed, now) {
  const head = `## Census, ${now.toISOString().slice(0, 16).replace('T', ' ')} UTC\n\n`;
  if (!listed) return head + 'No repos listed.\n';
  return head + `${listed} ${listed === 1 ? 'repo' : 'repos'} to count.\n\n` +
    cells(['Repo', 'Result', ...COLUMNS]) + cells(['---', '---', ...COLUMNS.map(() => '---:')]);
}

export function summaryRow(r) {
  const m = REPO.exec(r.name);
  const repo = m ? `[${text(`${m[1]}/${m[2]}`)}](https://${r.name})` : text(r.name);
  return cells([repo, text(r.why ? `${r.kind}: ${r.why}` : r.kind), ...(r.counts ? numbers(r.counts) : COLUMNS.map(() => ''))]);
}

// The summary's totals, kept as numbers as each repo is done (tallied), so that a run holds one
// repo's counts at a time: a big repo's contributors and days are dropped once its row is written.
export function summaryTally() {
  return { repos: 0, kinds: {}, sent: 0, sentKind: '', total: COLUMNS.map(() => 0) };
}

export function tallied(t, r) {
  t.repos++;
  t.kinds[r.kind] = (t.kinds[r.kind] ?? 0) + 1;
  if (r.kind === 'sent' || r.kind === 'counted') {
    t.sent++;
    t.sentKind ||= r.kind;
    numbers(r.counts).forEach((n, i) => { t.total[i] += n; });
  }
}

export function summaryEnd(t) {
  if (!t.repos) return '';
  const tally = KINDS.map(k => [k, t.kinds[k] ?? 0]).filter(([, n]) => n);
  return cells(['**Total**', `${t.sent} ${t.sentKind || 'sent'}`, ...t.total]) +
    `\n${t.repos} ${t.repos === 1 ? 'repo' : 'repos'}: ${tally.map(([k, n]) => `${n} ${k}`).join(', ')}.\n`;
}

// Appends to $GITHUB_STEP_SUMMARY, when it is set. A write that fails is logged once, and the rest
// are dropped: the summary is for reading, and the run goes on without it.
function stepSummary() {
  let file = process.env.GITHUB_STEP_SUMMARY;
  return s => {
    if (!file || !s) return;
    try {
      appendFileSync(file, s);
    } catch (err) {
      console.error(`writing the run's summary: ${err.message}`);
      file = null;
    }
  };
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

// Counts each repo and sends its counts, one repo at a time. `read` is census, but for the tests.
//
// What goes wrong with one repo is that repo's alone: the census goes on to the next, so a repo the
// site refuses can't leave every repo after it uncounted.
// - A repo that can't be read, because it has gone private or been deleted, keeps the counts it had.
// - A repo skipped for what it would cost (BUDGET, MAX_REFS) or for its size (tooBig) is logged,
//   and warned of in Actions, but leaves the run green. Anyone can list a repo (#298), and one
//   skipped today is skipped every day, until someone removes it from D1. A run red every day for
//   that would teach whoever reads it to ignore red.
// - Counts the site refuses, or that never reach it, fail the run once every repo has had its turn.
//   The census sends only what the site's limits allow, so a refusal means the two disagree, or
//   the site is down: something to fix.
// - So does a run in which no repo could be counted at all.
export async function main(args, { read = census } = {}) {
  const dry = args.includes('--dry');
  let names = args.filter(a => !a.startsWith('--'));
  if (!dry && !process.env.CENSUS_KEY) throw new Error('CENSUS_KEY is not set');
  if (!names.length) names = (await call('/v1/census')).repos;
  let unread = 0;
  const skipped = [], unsent = [];
  const write = stepSummary();
  write(summaryHead(names.length, new Date()));
  const tally = summaryTally(); // r is { name, kind, why, counts }, written and tallied, then let go
  const done = r => (tallied(tally, r), write(summaryRow(r)));
  for (const name of names) {
    let c;
    try {
      c = await read(name);
      const big = tooBig(c);
      if (big) throw skip(big);
    } catch (err) {
      if (err.skipped) {
        console.error(`${name}: skipped: ${err.message}`);
        // An annotation on the run's page. The name passed REPO and the reason is the census's own,
        // so neither can carry a workflow command of its own.
        if (process.env.GITHUB_ACTIONS === 'true') console.log(`::warning title=census::${name} skipped: ${err.message}`);
        skipped.push(name);
        done({ name, kind: 'skipped', why: err.message });
        continue;
      }
      const why = String(err.stderr || err.message).trim();
      console.error(`${name}: not counted: ${why}`);
      unread++;
      done({ name, kind: 'not read', why });
      continue;
    }
    const est = n => (n ? ` and about ${n} more estimated` : '');
    const line = `${name}: ${c.devices.length} devices, ${new Set(c.devices.map(v => v.person ?? v.id)).size} people, ` +
      `${c.devices.filter(v => v.account).length} matched to an account, ${c.totals.sessions} sessions${est(c.totals.est_sessions)}, ` +
      `${c.totals.messages} messages${est(c.totals.est_messages)}, ${c.totals.records} records of ${c.totals.bytes ?? 0} bytes, ` +
      `${c.contributors.length} contributors, ${c.days.length} days`;
    if (dry) {
      console.log(line);
      console.log(JSON.stringify(c.days));
      done({ name, kind: 'counted', why: 'not sent (--dry)', counts: c });
      continue;
    }
    try {
      await call('/v1/census', { method: 'POST', body: JSON.stringify(c) });
    } catch (err) {
      const why = String(err.message || err).trim();
      console.error(`${name}: not sent: ${why}`);
      unsent.push(name);
      done({ name, kind: 'not sent', why, counts: c });
      continue;
    }
    console.log(line);
    done({ name, kind: 'sent', counts: c });
  }
  write(summaryEnd(tally));
  if (unsent.length) throw new Error(`${unsent.length} of ${names.length} repos counted but not sent: ${unsent.join(', ')}`);
  if (unread + skipped.length === names.length && names.length) throw new Error('no repo could be counted');
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  main(process.argv.slice(2)).catch(err => {
    console.error(String(err.message || err));
    process.exit(1);
  });
}
