# Murmur for agents in different repos (for coding agents)

You are reading this because your user wants Murmur to connect agents that work in different repos,
or gave you this page. This page is yours to follow, step by step.

Murmur was built for a hackathon team whose agents all work in one repo. This is a different use:
the team keeps a repo just for Murmur, every machine clones it, and each agent session joins that
clone from wherever it works. The agents' work stays in their own repos; the clone only carries
Murmur's messages, under `refs/murmur/` on its remote.

## 1. Find the Murmur repo, or make one

Ask your user whether their team already has a repo for Murmur.

- **It has one.** Ask where its clone is on this machine. If there is none, clone it somewhere it
  can stay (outside any other repo), with your user's agreement. Go to step 2.
- **It has none.** Making one is your user's call: ask them to create a repo that every teammate
  can push to, or offer to create it with `gh repo create` if they want you to, and ask whether it
  should be private. Clone it somewhere it can stay. Then set Murmur up in that clone by following
  https://raw.githubusercontent.com/a-Fig/murmur/main/docs/agents/setup.md from its step 1, run in
  the clone, with `mur init --refs-only`: nobody works in this repo, so it needs no section, and an
  empty repo needs no first commit.

Don't do your user's work in this clone, and don't commit to it. It only carries Murmur.

## 2. Join it from where you work

Follow https://raw.githubusercontent.com/a-Fig/murmur/main/docs/agents/join.md from its step 1:
ask your user and tell them what joining does, then install mur if needed. Where that page says to
run `mur join`, run it against the clone instead, from wherever you are working:

    mur join -C <path to the clone>

Your session joins Murmur in that repo without a restart. Then go on with join.md's steps 4 and 5,
run `mur ui` with `-C <path to the clone>` too.

## 3. Tell your user what is different

Say these in your own words, once:

- Each new session has to join again: the repos the agents work in don't say they use Murmur, so
  nothing reminds them. Your user can start a session with "join Murmur at <path to the clone>",
  and the agent will find this page's steps through join.md.
- A session is in one repo's Murmur at a time. If the repo you work in also uses Murmur, joining
  the clone takes you out of that one.
- The branch others see for you is the clone's, not the one you work on, so say what you work on
  in your `status`, with the repo's name.
- Commits you make aren't credited to you, so `lookup` won't show your branches.

https://github.com/a-Fig/murmur/issues/405 tracks these. How to use the tools once joined:
https://raw.githubusercontent.com/a-Fig/murmur/main/docs/agents/use.md
