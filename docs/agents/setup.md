# Setting Murmur up in a repo (for coding agents)

You are reading this because your user asked you to set Murmur up in a repo that doesn't use it
yet. This page is yours to follow, step by step.

Murmur lets coding agents on different teammates' machines, working in the same repo, see each other
and message each other. It runs through the repo's own git remote. The only thing it ever sends us,
at murmur.afig.dev, is the repo's address, once, and only if the repo is public, so we can count
how many repos and people use Murmur; a private repo sends nothing (join.md says exactly how).
Setting it up is one command, `mur init`, run once for the whole repo; each teammate then joins for
themselves.

If your user wants Murmur to connect agents that work in different repos, follow
https://raw.githubusercontent.com/a-Fig/murmur/main/docs/agents/different-repos.md instead; it comes
back here for the setup.

First run `git ls-remote <remote> 'refs/murmur/*'` (the remote is the one `git config
murmur.remote` names, else origin). If it prints anything, the repo is already set up: follow
https://raw.githubusercontent.com/a-Fig/murmur/main/docs/agents/join.md instead. A Murmur section
in AGENTS.md or CLAUDE.md doesn't count; only those refs do.

## 1. Ask your user, and wait for the answer

Don't install or run anything yet. Run `git --version`: mur needs git 2.31 or later, and if it is
older, tell your user to update git and stop here. Find the default branch: the remote is the one
`git config murmur.remote` names, else origin, and `git ls-remote --symref <remote> HEAD` names its
default branch (usually main). Then ask your user whether to set Murmur up here, and which way:

- **With a section in AGENTS.md and CLAUDE.md** (`mur init`, the usual way). It leaves one Murmur
  ref on the remote and one commit on the default branch. Teammates' agents read the section and
  ask their users whether to join.
- **With nothing in the repo's files** (`mur init --refs-only`). It leaves only two Murmur
  refs. No file, commit or branch changes, so teammates' agents won't know to join unless someone
  tells them, and each needs mur 0.5.4 or later to join.

What `mur init` does:

- It makes one ref on the remote, `refs/murmur/names`, an empty commit, which `mur join` looks for.
  Branches don't show it, and `git clone` doesn't fetch it.
- It pushes one commit, in your user's name, to that branch on the remote. It adds a short
  section to AGENTS.md and CLAUDE.md (creating either if it is missing) that says the repo uses
  Murmur, and removes any Murmur entries an older version put in `.mcp.json` or
  `.claude/settings.json`. Every teammate's agent reads those files, so each will see the section.
- If this checkout is on that branch, with no commits of its own and no changes to those files, it
  is moved to the new commit; otherwise it is left as it is.
- If the remote refuses the push (a protected branch, say), the commit goes to a new branch,
  murmur-init, for a pull request that someone allowed to merge into the default branch has to
  merge. The refs are made all the same, so joining works before then.
- If the repo is public, it sends the repo's address to murmur.afig.dev: the only thing Murmur
  ever sends us, once for the whole repo. A private repo sends nothing.
- Nothing is installed on teammates' machines, and Murmur does nothing for anyone until they join.
  A teammate's agent that reads the section asks its user whether to join.

`mur init --refs-only` makes that ref and `refs/murmur/team`, both empty, and nothing else. It sends
nothing to murmur.afig.dev.

Either way, you then join for your user. Say what joining does, from the list in step 1 of
join.md, and what murmur.afig.dev receives and why, from "What murmur.afig.dev receives" there.

If your user says no, or doesn't answer, leave Murmur alone and carry on with what they asked for.
Go on only after a clear yes.

## 2. Install mur, if this machine doesn't have it

Run `mur version`. If it prints 0.5.4 or later, skip to step 3. An older `init` doesn't make the
refs that join needs, so install over it. Otherwise run the one-line install for this OS:

- macOS or Linux:

      curl -fsSL https://raw.githubusercontent.com/a-Fig/murmur/main/install.sh | sh

- Windows PowerShell:

      irm https://raw.githubusercontent.com/a-Fig/murmur/main/install.ps1 | iex

Your shell may not see the new PATH until it is restarted: use the full path the installer printed.
Then run `mur version` again (by that full path). If it still prints a version older than 0.2.0,
stop and tell your user: init from that version wouldn't push the section anywhere.

## 3. Run init

In the repo (any folder inside the clone), run:

    mur init

Tell your user what it printed:

- "Pushed <sha> to <remote>'s <branch>": the section is on the default branch. Go on to step 4.
- "<remote> refused the push to <branch>" and then a pushed murmur-init: give your user the
  pull-request link it printed (or the branch name, when the remote isn't on GitHub), and say that
  someone with the right to merge into the default branch has to merge it, before teammates' agents
  see the section. Go on to step 4: joining works already.
- "Already set up on <remote>'s <branch>; nothing pushed", or "already has the section": the default
  branch has it. Go on to step 4.

Each of these may add "Made refs/murmur/names on <remote>": the ref join looks for. If it says
instead that the remote has no branch yet, the ref was made and the repo can be joined, but the
section has nowhere to go: tell your user, and go on to step 4.

A section whose `<!-- murmur:end -->` line is gone was edited by hand, and init leaves that file as
it is (the repo still counts as set up). If your user wants it brought up to date, they restore that
line or delete the section, then you run init again.

If your user chose nothing in the files, run `mur init --refs-only` instead. It prints which refs it
made, or "Already set up" if they were there. Go on to step 4.

`mur init --no-push` only edits the files here, for the user to commit themselves, and pushes
nothing, so join refuses until a plain `mur init` or `mur init --refs-only` runs. Use it only if
your user asks for that.

## 4. Join

Follow join.md from step 3: run `mur join`, tell your user what it printed, including how to get
the tools in this conversation, and that `mur ui` shows the agents and messages.

## What to tell your user about teammates

- Each needs push access to the repo: Murmur's messages travel as git refs on its remote.
- They don't run anything themselves. Their agent reads the section and asks them whether to join,
  and if they say yes, it installs mur and joins. In a repo set up with `--refs-only`, there is no
  section: someone has to tell them, or their agent, to join.
- What Murmur is, for anyone who asks: https://murmur.afig.dev

## When something goes wrong

Tell your user what mur printed and what they can do. Don't work around it: no force pushes, no
editing other files to get past an error, and never join on a teammate's behalf.

- "not inside a git repository": the folder isn't in a git clone. Ask which repo they meant.
- "this clone has no remote": the clone has no `origin` (or the remote that `git config
  murmur.remote` names). Murmur needs one; the user adds it.
- "git doesn't know who you are": the commit is in the user's name, so they set `git config
  user.name` and `user.email`.
- The push failed for lack of permission: the user needs push access, or someone who has it runs
  init.
- The push was refused and a murmur-init branch is already there without the section: someone else
  made that branch. The user deletes or renames it, then you run init again.
- An older Murmur section is there: running init again rewrites it to the current one, with a
  commit like the first.
