// Stand-ins for D1 with the methods the site calls: prepare, bind, first, all, run and batch.
import { readFileSync, readdirSync } from 'node:fs';

const MIGRATIONS = new URL('../migrations/', import.meta.url);

// Keeps no tables: `listed` is the repos table, and each batch is kept in `batches` as its
// statements ({sql, args}), so a test can see what would have been written.
export function recordingD1(listed = []) {
  const repos = new Set(listed.map(r => r.toLowerCase()));
  const batches = [];
  const statement = (sql, args = []) => ({
    sql, args,
    bind: (...a) => statement(sql, a),
    // A listed repo, looked up by its key or its name (#1250): the stand-in's keys are the names.
    async first() { return /FROM repos (r )?WHERE (repo|lower\(r?\.?name\)) = \?1/.test(sql) && repos.has(args[0]) ? { 1: 1, repo: args[0] } : null; },
    async all() { return { results: /SELECT name FROM repos/.test(sql) ? listed.map(name => ({ name })) : [] }; },
    async run() { return { success: true }; },
  });
  return {
    batches,
    prepare: sql => statement(sql),
    async batch(stmts) {
      batches.push(stmts);
      // The census's listing (#1250): each repo by its name, with no ID and no other names.
      return stmts.map(s => ({ results: /SELECT repo, name, github_id FROM repos/.test(s.sql) ? listed.map(name => ({ repo: name.toLowerCase(), name, github_id: null })) : [] }));
    },
  };
}

// Answers repoNumbers' queries with `repos`, rows as its first query makes them, and the newest of
// their `counted` times as its MAX(counted); its other queries find nothing. For what the stats say
// of given rows, without SQLite.
export function statsD1(repos) {
  const at = repos.map(r => r.counted).filter(Boolean).sort().at(-1) ?? null;
  const answer = sql => (/LEFT JOIN repo_census/.test(sql) && /AS week/.test(sql) ? repos : /MAX\(counted\)/.test(sql) ? [{ at }] : []);
  const statement = (sql, args = []) => ({ sql, args, bind: (...a) => statement(sql, a) });
  return {
    prepare: sql => statement(sql),
    async batch(stmts) { return stmts.map(s => ({ results: answer(s.sql) })); },
  };
}

// D1 is SQLite, so node's own runs the site's SQL as D1 would, on a database made from migrations/.
// node:sqlite loads without a flag from Node 22.13 and 23.4 (22.5 to 22.12 need
// --experimental-sqlite). null where it doesn't: the tests that need it skip, and node --test
// counts them as skipped, with the reason, rather than passed.
export async function sqliteD1() {
  let sqlite;
  try {
    sqlite = await import('node:sqlite');
  } catch {
    return null;
  }
  const db = new sqlite.DatabaseSync(':memory:');
  for (const f of readdirSync(MIGRATIONS).filter(f => f.endsWith('.sql')).sort()) db.exec(readFileSync(new URL(f, MIGRATIONS), 'utf8'));
  const statement = (sql, args = []) => ({
    bind: (...a) => statement(sql, a),
    async first() { return db.prepare(sql).get(...args) ?? null; },
    async all() { return { results: db.prepare(sql).all(...args) }; },
    async run() { db.prepare(sql).run(...args); return { success: true }; },
    now() {
      const s = db.prepare(sql);
      if (/^\s*(SELECT|WITH)\b/i.test(sql)) return { results: s.all(...args) };
      s.run(...args);
      return { results: [] };
    },
  });
  return {
    sql: db,
    batches: 0,
    prepare: sql => statement(sql),
    // A batch is one transaction, as D1's is.
    async batch(stmts) {
      this.batches++;
      db.exec('BEGIN');
      try {
        const out = stmts.map(s => s.now());
        db.exec('COMMIT');
        return out;
      } catch (err) {
        db.exec('ROLLBACK');
        throw err;
      }
    },
  };
}
