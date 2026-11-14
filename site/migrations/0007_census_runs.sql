-- Manual census runs started from the stats page (#557): when each one was dispatched. The page's
-- run button is rate-limited from these rows (src/census-run.js), so the limit holds across Worker
-- instances and restarts; the scheduled daily run is not recorded and doesn't spend the quota.
-- Rows are kept: at most 10 a day can be added, and the day's count is read back from them.
CREATE TABLE census_runs (
  id TEXT PRIMARY KEY, -- a UUID for the attempt, so a run can tell its own row from another's
  at TEXT NOT NULL     -- when it was dispatched, an ISO time in UTC
);
CREATE INDEX census_runs_at ON census_runs (at);
