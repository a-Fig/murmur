// Running the census from the stats page (#557): POST /stats/run-census, behind the same stats
// key as the rest of /stats (src/usage.js calls this only after its key check).
//
// Rate limit, from the census_runs table (migrations/0007) so it holds across Worker instances:
// at most one manual run per 15 minutes, and at most 10 per UTC day. The slot is reserved with
// one conditional INSERT, whose WHERE is the limit: SQLite serialises writes, so two clicks at
// once can't both slip past a check the other hasn't landed yet. A dispatch GitHub refuses (or
// that never answers) deletes its row again, so a failed start doesn't spend the slot.
//
// The 15-minute gap is shorter than the 30 minutes src/repos.js (RUN) takes counts within to be
// one census run, and the scheduled run isn't recorded here at all: two runs that close together
// read as one to the page's census health, so a repo the second of them missed isn't named as
// missed. Tyler set the gap at 15 minutes knowing that (#557); the counts themselves are exact
// either way, and the next run over 30 minutes later names what it misses again.
//
// The dispatch token is the CENSUS_RUN_TOKEN secret: a fine-grained GitHub token scoped to
// a-Fig/murmur with only the Actions permission dispatching a workflow needs. It is deliberately
// not the site's GITHUB_TOKEN, which only raises a read rate limit, so the read path never holds
// a token that can write. Without the secret this answers 503 and dispatches nothing.

const DISPATCH = 'https://api.github.com/repos/a-Fig/murmur/actions/workflows/census.yml/dispatches';
export const RUNS_PAGE = 'https://github.com/a-Fig/murmur/actions/workflows/census.yml';
export const PER_DAY = 10;
export const GAP_MS = 15 * 60e3;

const json = (status, body, headers) => Response.json(body, { status, headers });

export async function runCensus(request, env, headers, { now = Date.now() } = {}) {
  if (request.method !== 'POST') {
    return new Response('Only POST.\n', { status: 405, headers: { ...headers, Allow: 'POST' } });
  }
  const type = (request.headers.get('Content-Type') || '').split(';')[0].trim().toLowerCase();
  if (type !== 'application/json') {
    return new Response('Only application/json.\n', { status: 415, headers: { ...headers, 'Accept-Post': 'application/json' } });
  }
  const token = String(env.CENSUS_RUN_TOKEN || '');
  if (!token) {
    return json(503, { ok: false, message: 'Not set up (no CENSUS_RUN_TOKEN).' }, headers);
  }

  const at = new Date(now).toISOString();
  const dayStart = `${at.slice(0, 10)}T00:00:00.000Z`;
  const gapCutoff = new Date(now - GAP_MS).toISOString();
  const id = crypto.randomUUID();
  // Reserve the slot, or insert nothing: the day's 10 are spent, or one went in the last 15 minutes.
  await env.DB.prepare(
    `INSERT INTO census_runs (id, at) SELECT ?1, ?2
     WHERE (SELECT COUNT(*) FROM census_runs WHERE at >= ?3) < ?4
       AND NOT EXISTS (SELECT 1 FROM census_runs WHERE at > ?5)`,
  ).bind(id, at, dayStart, PER_DAY, gapCutoff).run();
  const mine = await env.DB.prepare('SELECT id FROM census_runs WHERE id = ?1').bind(id).first();
  if (!mine) {
    const { runsToday, last } = await state(env.DB, dayStart);
    if (runsToday >= PER_DAY) {
      const retryAfterSeconds = Math.max(1, Math.ceil((Date.parse(`${at.slice(0, 10)}T24:00:00.000Z`) - now) / 1000));
      return json(429, {
        ok: false, runsToday, retryAfterSeconds,
        message: 'Limit reached. Resets at midnight UTC.',
      }, { ...headers, 'Retry-After': String(retryAfterSeconds) });
    }
    // `last` can be gone by now: the run that blocked this one may have failed and freed its
    // slot between the refused INSERT and this read. Then there is nothing to wait for.
    const lastMs = last ? Date.parse(last) : NaN;
    const retryAfterSeconds = Number.isFinite(lastMs) ? Math.max(1, Math.ceil((lastMs + GAP_MS - now) / 1000)) : 1;
    const minutes = Math.ceil(retryAfterSeconds / 60);
    return json(429, {
      ok: false, runsToday, retryAfterSeconds,
      message: `Try again in ${minutes} min.`,
    }, { ...headers, 'Retry-After': String(retryAfterSeconds) });
  }

  let res;
  try {
    res = await fetch(DISPATCH, {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${token}`,
        Accept: 'application/vnd.github+json',
        'Content-Type': 'application/json',
        'User-Agent': 'murmur-site',
        'X-GitHub-Api-Version': '2022-11-28',
      },
      body: JSON.stringify({ ref: 'main' }),
      signal: AbortSignal.timeout(10000),
    });
  } catch (err) {
    console.log(`starting the census: ${err}`);
    await release(env.DB, id);
    // GitHub may have started the run before the answer was lost, so this doesn't claim none was.
    return json(502, { ok: false, message: "GitHub didn't answer." }, headers);
  }
  if (!res.ok) {
    // A 204 with no body is the only success; anything else means no run, so the slot is freed.
    console.log(`starting the census: GitHub answered ${res.status}`);
    await release(env.DB, id);
    return json(502, { ok: false, message: `GitHub refused (${res.status}).` }, headers);
  }

  const { runsToday } = await state(env.DB, dayStart);
  return json(202, {
    ok: true, startedAt: at, runsToday, perDay: PER_DAY, runsPage: RUNS_PAGE,
    message: 'Started.',
  }, headers);
}

async function state(db, dayStart) {
  const day = await db.prepare('SELECT COUNT(*) AS n FROM census_runs WHERE at >= ?1').bind(dayStart).first();
  const last = await db.prepare('SELECT MAX(at) AS at FROM census_runs').first();
  return { runsToday: day?.n ?? 0, last: last?.at ?? null };
}

async function release(db, id) {
  try {
    await db.prepare('DELETE FROM census_runs WHERE id = ?1').bind(id).run();
  } catch (err) {
    console.log(`freeing a census run slot: ${err}`);
  }
}
