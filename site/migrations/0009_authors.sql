-- The commit identity behind each device (#1244): the first 16 hex digits of the SHA-256 of the
-- account its commit fingerprint matched, 'login:<login>', or 'email:<email>' where GitHub links
-- the email to no account, as account is (site/census/census.mjs, authorAccounts()). Clones whose
-- owners commit as one person share it, in any repo, so the stats count them as one user within
-- limits (src/repos.js, users()). NULL for a clone whose fingerprint matches no commit, or that
-- writes none; once seen, it stays.
ALTER TABLE devices ADD COLUMN author TEXT;
