-- The machine behind each device (#1245): the first 16 hex digits of the SHA-256 of the hostname
-- slug its device ID begins with, for IDs made before #330 (site/census/census.mjs, machine()).
-- Clones on one machine, in any repo, share it, so the stats count them as one user within limits
-- (src/repos.js, users()). NULL for a new ID, which names no machine, or a slug many machines
-- share; once seen, it stays.
ALTER TABLE devices ADD COLUMN machine TEXT;

-- Murmur's releases on GitHub, as the census reads them each day (#1245), for the stats page's
-- machines on the latest release. Every clone on a machine shares one binary, and the updater lets
-- only one of them download, so each machine running Murmur takes each release once.
CREATE TABLE releases (
  tag        TEXT PRIMARY KEY,           -- v0.5.4
  published  TEXT,                       -- when GitHub published it, an ISO time in UTC
  prerelease INTEGER NOT NULL DEFAULT 0, -- 1 for a nightly or a release candidate
  latest     INTEGER NOT NULL DEFAULT 0  -- 1 for GitHub's latest release, the newest stable one
);

-- Each release's binaries' download counts, as GitHub gave them on each UTC day the census read
-- them. A count is a running total, so the newest day's is the release's whole count.
CREATE TABLE release_downloads (
  day       TEXT NOT NULL,              -- YYYY-MM-DD, UTC, the day it was read
  tag       TEXT NOT NULL,              -- releases.tag
  asset     TEXT NOT NULL,              -- mur-<os>-<arch>, with .exe on Windows
  downloads INTEGER NOT NULL DEFAULT 0, -- GitHub's download_count, raw: 1 is no machine (census.mjs)
  PRIMARY KEY (day, tag, asset)
);
