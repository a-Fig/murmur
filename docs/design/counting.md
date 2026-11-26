# Counting use

Tyler's goal for the project is 100 users ever (2026-11-25, #1245; it said 100 active users from
2026-11-02), and the transport hides them: every team's traffic stays in its own repo, and release
downloads count installs, not use. Maintainers count: Tyler is a user, and the goal is 100 users,
not 100 besides us (Tyler, 2026-11-25).

Tyler's rule (2026-11-08, #199): **the only thing Murmur sends us is a public repo's address.** A
public repo's bus is readable by anyone, so once we know the address, its devices and records can be
counted from the bus itself, and the address is already public, so there is no opt-out. A private
repo sends nothing and isn't counted. Weighed and turned down: a GitHub topic on each repo as the
marker (it changes how the user's repo looks), and an ID for private repos (a hash of the address
can't be salted for real, since the salt would ship in the public binary, and a random ID is still
data from a private repo).

## Reporting a repo

`internal/publicrepo` does it, from `mur init`, `mur join` and daemon start:

1. If `refs/murmur/reported` is on the remote, stop: some clone has reported the repo. A daemon's
   sync doesn't fetch that ref, so one `ls-remote` asks, and `publicrepo.json` in the state
   directory remembers the answer until the remote changes.
2. Reduce the remote's URL to `github.com/<owner>/<name>`; anything else is not reported.
3. Ask `GET api.github.com/repos/<owner>/<name>` with no credentials; a private repo answers 404.
   Private, or not on GitHub: look again the next UTC day, so a repo made public later is reported
   within a day.
4. POST `{"repo": "github.com/<owner>/<name>"}` to `https://murmur.afig.dev/v1/repo`, with no
   version in the User-Agent. The site stores it only once GitHub confirms the repo public: one row
   per repo, its address and first day, no IP addresses (`site/src/usage.js`). A body not sent as
   `application/json` is refused, so a web page can't have its visitors' browsers send these POSTs
   without a CORS preflight, which the site doesn't answer (#299).
5. Once the site has answered 200, push `refs/murmur/reported`, leased so it is created only if
   absent, and retry the push at every later look without sending the address again. Two clones
   racing both send, and the site keeps one row.

So each public repo reaches us once in its life. A `-dev` build never reports. Earlier versions
(#148) sent a daily check-in to `/v1/checkin` (a random user ID, a hash of the remote, the version,
OS and architecture); since 2026-11-08 the site stores none but still answers with the latest
release, and the rows already stored are kept at `/stats/archive`.

## The census

The counts come from the census (#259), which reads what each reported repo shows anyone. Once a day
a GitHub Action (`.github/workflows/census.yml`, `site/census/census.mjs`) fetches the commits,
without their files, of each repo's bus refs and default branch, and sends the site counts and
hashes, never content: per UTC day, the sessions started, the messages, all records and their bytes,
a hash of each device that wrote anything and of each device's owner, and for each contributor a
hash of their GitHub login or email and the day of their first commit, so one person in two repos
counts once. The site only ever raises a count, and keeps every device and owner it has seen, since
a bus log can be rewritten and a daemon deletes its presence when it stops. It runs from GitHub, not
the Worker, because counting needs git, and a fetch of the whole bus is one request where the API
would need one per commit.

- **Estimates, for encrypted buses.** Since 0.3 each event's kind is sealed inside it
  ([encryption](encryption.md)), so the census counts an encrypted log's records exactly but can't
  read which are messages or session starts. Tyler's decision (2026-11-13, #550): estimate them,
  since 0 is wrong and "unknown" says too little, and say which figures hold an estimate (an
  asterisk on the page, ≈ in the report). The census decrypts nothing and holds no key. It uses two
  things any reader of a public repo sees: each record's size, and the name registry. A size gives
  away roughly what kind of event a record is, never what it says.
  - *Messages, from size.* An encrypted record's length gives the event's length exactly, and a
    message is longer than a session's start or stop: an event 180 bytes or more longer than its
    device's ID counts as a message. Measured 2026-11-13 against the unencrypted history of two
    public repos (155 messages, 1,094 starts and stops), that cut missed 8 messages, 5%, each with
    no body and a short subject, and took no session event for a message. It can run high where that
    history had nothing to measure: a start whose agent and branch together pass about 55 characters
    (the longest seen was 47), or a tags event with some 40 characters of tags. Checked the same day
    on four buses Tyler's machine could read whole (868 messages and 1,598 other records), it missed
    19 messages, 2.2% (0 to 5.2% by repo), and took no other record for a message.
  - *Sessions, from the name registry*, which isn't encrypted and records each session that takes a
    name. Size can't count them: a start announced again after a restart, a tags event and a group
    add are all the size of a start, and start-sized records came to 1.3 to 3 times the sessions on
    the buses measured. The registry held 445 of the 446 sessions one history started, and gave 569
    of the four buses' 570. A session another session started takes a name but, since #487, writes
    no start, so the registry counts it too.
  - Estimates are sent and kept apart from exact counts (`est_sessions`, `est_messages`), and
    `census.mjs` has the measurements. The site only raises them, so a lower estimate later needs a
    hand edit in D1.
- **What the stats page leads with** (Tyler, 2026-11-13). Encryption doesn't hide how many records a
  device wrote and how large, so the page is built around those. Bytes are each record's event as
  JSON, the event inside the box for an encrypted one, so a log weighs the same either way, and they
  grow with what agents say to each other: the exact measure of a bus's traffic. The page shows
  users ever first, with those active in the last 7 days beside them (#1245, the goal's figure),
  then repos, then records, bytes, devices and contributors, then sessions and messages, estimated
  where a bus is encrypted.
- **Users.** Murmur sends nothing that names a person, and since 0.3 a clone's owner is sealed in
  its encrypted presence, which a daemon deletes when it stops. On 2026-11-25 the page said 36
  users, 31 of them counted one per clone; matching by hand put the real figure nearer 23 to 27.
  So a user is the best of what the census can tie a clone to (`users()` in `site/src/repos.js`):
  the GitHub account its owner's git name matches in the repo's commits, else that git name, and,
  since #1245, its machine. A device ID made before 0.5 (#330) begins with a slug of the hostname,
  which every clone on that machine shares in any repo. The census sends the slug hashed, and the
  site joins clones that share it, except for slugs strangers share (`macbook-pro`, `ubuntu`, one a
  network assigns, and one the old IDs may have cut short at 24 characters, `census.mjs`'s
  `machine()`), and for a slug that would join two accounts into one user or spans more than 10
  clones or 6 repos (`JOIN`): a wrong merge costs more than a missed one. A new
  ID names no machine, so a clone made since counts one per clone unless its owner is seen; #1244's
  commit fingerprint is the lasting fix. The count from before, roughly one user per clone, stays
  on the page as the old count (Tyler, 2026-11-25), and the users charts mark each day the method
  changed, since every day's figure is worked out again by the new method.
- **Repos by GitHub's ID** (#1250). A rename or a transfer changes a repo's address, and a clone
  that reports the new one used to store the repo twice, splitting its counts. So a repo is one
  GitHub repository ID: `/v1/repo` stores the ID when GitHub's API gives it, and each census run
  asks GitHub where each repo is now (by its ID, else by the name it was listed under, which GitHub
  redirects), reads the bus from the current name, and sends the ID and name back. The row takes
  the new name, and two rows found to share an ID merge, taking the larger of each day's counts
  (they counted one bus twice) and joining clones and contributors as sets. Each row keeps the key
  it was first stored under, so no count moves; `repo_names` holds every address each repo has
  had, which #1244's fingerprint is matched under, since a clone on an old URL names the repo by
  it. After a rename GitHub lets a new repo take the old address, so an address is only where to
  look, and the ID decides: `/v1/repo` answers from D1 only for a repo's current name, and a new
  repo at a taken address is keyed `gh:<id>`. A row with no ID yet takes GitHub's answer for its
  address only if that repo was made before the row was stored; a newer one is another repo, and
  is counted on its own row. A repo GitHub says is gone keeps its counts.
- **Machines on the latest release** (#1245), the only figure that covers private repos. Each day
  the census reads the download counts of every release's `mur-<os>-<arch>` binaries from GitHub's
  API, and the site keeps them by day. Every clone on a machine shares one binary and the updater's
  lock lets only one download, so each machine running Murmur downloads each release once. Every
  binary of every release shows at least 1 download from something that fetches each file once
  (seen 2026-11-25), so 1 comes off each. Only GitHub's latest release counts: prereleases go almost
  only to Tyler's machines. Releases don't add up, since a machine that took two is in both. It
  misses machines with updates off and installs from a package manager, and counts one person on
  two machines twice. It needs nothing from clients, so #199 holds.
- **Limits.** Each repo is counted on its own, since anyone can list one (#298), and one that can't
  be read keeps the counts it had. A repo gets 3 minutes and 2,000 device refs, so one with huge
  objects or 100,000 refs can't hold the 30-minute job. One over either is skipped with a warning
  and the run stays green: a skipped repo is skipped every day until someone takes it out of D1, and
  a run red every day teaches people to ignore red. Counts the site refuses do fail the run.
- **Saying what wasn't counted.** `/stats/report.md` ends with when the census last ran, a warning
  once that is over 30 hours ago, and the repos that run didn't count.
- **Clocks.** A time on the bus or a branch is its writer's clock, and can say any year up to 9999.
  A day after tomorrow (UTC) is never believed, since one would keep a repo active and its people
  new for ever, nor a bus day before 2026-10-01, when there was no bus; such a record is dated by
  its commit, or isn't counted.
- `/stats` shows the repos and the counts over time, and needs a key: Tyler's, or one he gives an
  agent, which reads it as markdown at `/stats/report.md` or JSON at `/stats/data` (#150).
