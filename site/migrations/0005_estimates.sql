-- Estimates of what an encrypted bus hides (#550). Since Murmur 0.3 every event is encrypted, with
-- its kind sealed inside, so the census can't count such a repo's sessions and messages exactly.
-- It estimates them (site/census/census.mjs says how, and how far off they are), and they are kept
-- apart from the exact counts: a repo's sessions are sessions + est_sessions, and the stats mark a
-- figure that includes an estimate. Rows from before this, and POSTs from a census before it, have 0.
ALTER TABLE repo_days ADD COLUMN est_sessions INTEGER NOT NULL DEFAULT 0;   -- sessions the name registry shows and no unencrypted start does
ALTER TABLE repo_days ADD COLUMN est_messages INTEGER NOT NULL DEFAULT 0;   -- encrypted records large enough to be messages
ALTER TABLE repo_census ADD COLUMN est_sessions INTEGER NOT NULL DEFAULT 0;
ALTER TABLE repo_census ADD COLUMN est_messages INTEGER NOT NULL DEFAULT 0;
