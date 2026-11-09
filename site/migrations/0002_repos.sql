-- One row per public repo that uses Murmur (#199). Murmur sends a repo's address once, and only if
-- the repo is public (internal/publicrepo); it is the only thing Murmur sends. A row is stored only
-- once GitHub confirms the repo public, and nothing else is stored: no IP addresses. The checkins
-- table stays as history and gets no new rows.
CREATE TABLE repos (
  repo  TEXT PRIMARY KEY, -- github.com/<owner>/<name>, lowercased
  name  TEXT NOT NULL,    -- the same, as GitHub spells it
  first TEXT NOT NULL     -- YYYY-MM-DD, UTC, the server's: when it was first reported
);
