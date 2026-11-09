-- What the census (#259) reads from each public repo in the repos table: its bus (refs/murmur/*)
-- and its commit history, both public. site/census/census.mjs counts them in a GitHub Action each
-- day and sends them to /v1/census. Only counts and hashes are kept: no names, no message text, no
-- device IDs.
--
-- Nothing is taken away. A bus log can be rewritten and a daemon deletes its presence when it stops,
-- so a count is only ever raised, and a device or contributor once seen stays.

-- One row per repo per UTC day with anything in its devices' logs.
CREATE TABLE repo_days (
  repo     TEXT NOT NULL,              -- repos.repo
  day      TEXT NOT NULL,              -- YYYY-MM-DD, UTC
  sessions INTEGER NOT NULL DEFAULT 0, -- agent sessions that started that day, each counted once
  messages INTEGER NOT NULL DEFAULT 0, -- messages between agents, where the bus shows the kind
  records  INTEGER NOT NULL DEFAULT 0, -- everything the devices wrote to their logs
  PRIMARY KEY (repo, day)
);

-- Each clone running Murmur in a repo, and the person behind it once the census has seen one.
CREATE TABLE devices (
  repo   TEXT NOT NULL, -- repos.repo
  device TEXT NOT NULL, -- the first 16 hex digits of the SHA-256 of its device ID
  person TEXT,          -- the same of its owner, the git user name its presence shows; NULL until seen
  PRIMARY KEY (repo, device)
);

-- The days each device wrote anything to its repo's bus: an event or its presence.
CREATE TABLE device_days (
  repo   TEXT NOT NULL, -- repos.repo
  device TEXT NOT NULL, -- devices.device
  day    TEXT NOT NULL, -- YYYY-MM-DD, UTC
  PRIMARY KEY (repo, day, device)
);

-- One row per repo: its totals from the last census.
CREATE TABLE repo_census (
  repo      TEXT PRIMARY KEY,           -- repos.repo
  sessions  INTEGER NOT NULL DEFAULT 0,
  messages  INTEGER NOT NULL DEFAULT 0,
  records   INTEGER NOT NULL DEFAULT 0,
  first_day TEXT,                       -- the first day with anything on its bus
  last_day  TEXT,                       -- the last
  counted   TEXT NOT NULL               -- when the census last read it, an ISO time
);

-- Everyone who has committed to a repo's default branch, by a hash of their GitHub login or email.
CREATE TABLE contributors (
  repo  TEXT NOT NULL, -- repos.repo
  id    TEXT NOT NULL, -- the first 16 hex digits of the SHA-256 of the login or email
  first TEXT NOT NULL, -- YYYY-MM-DD, UTC: their first commit there
  PRIMARY KEY (repo, id)
);
