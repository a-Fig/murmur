-- Repos by GitHub's repository ID (#1250). A rename or a transfer changes a repo's address but not
-- its ID, so the ID is what says two rows are one repo. Each row keeps the key it was first stored
-- under, in repos.repo and in every table that counts it, so no count moves. For the rows stored
-- before, that key is their address then; repos.name is the current address, shown on the page.

-- GitHub's numeric ID, from /v1/repo when GitHub's API answered it, or from the census. NULL until
-- one of them has it. Two rows found to share an ID are merged into one (src/repos.js, merge()).
ALTER TABLE repos ADD COLUMN github_id INTEGER;
CREATE UNIQUE INDEX repos_github_id ON repos (github_id) WHERE github_id IS NOT NULL;

-- Every address each repo has had, lowercased: the one it was stored under, and each name GitHub
-- has given it since. The census hashes commit identities under each of them (#1244), since a
-- clone on an old URL names the repo by it. After a rename GitHub lets a new repo take the old
-- address, so one address can belong to two repos here: it says where to look, and the ID decides.
CREATE TABLE repo_names (
  name TEXT NOT NULL, -- github.com/<owner>/<name>, lowercased
  repo TEXT NOT NULL, -- repos.repo
  PRIMARY KEY (name, repo)
);
CREATE INDEX repo_names_repo ON repo_names (repo);
INSERT INTO repo_names (name, repo) SELECT repo, repo FROM repos;
