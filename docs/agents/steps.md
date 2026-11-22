## Setting Murmur up and joining

These steps are the same in every agent. The section above says what differs in yours.

### 1. Ask your user, and wait for the answer

Don't install or run anything yet. Look at AGENTS.md and CLAUDE.md at the top of the repo. A section
that starts with `<!-- murmur:start -->` means the repo is set up, and you only join. Without one,
go on only if your user asked you to set Murmur up here.

Ask your user whether to join Murmur here, and say what joining does:

- Teammates' agents in this repo will see the name, branch, status and tags of your user's agents
  here, and can message them.
- `mur`, one small program, is downloaded from GitHub if this machine doesn't have it, into
  ~/.local/bin (on Windows, %USERPROFILE%\.local\bin), which is added to PATH: in the shell's
  startup file (.zshenv, .bashrc or .profile) on macOS and Linux, in the user's PATH on Windows. No
  admin rights.
- It updates itself: it checks GitHub hourly, and installs a newer release after verifying its
  checksum. `git config --global murmur.update false` turns that off; set it before joining.
- Murmur is added to every coding agent your user has installed (Claude Code, Codex, OpenCode,
  Muse Code, Pi, Grok, Antigravity), and to every account of them T3 Code runs: its MCP server, its
  tools approved to run without asking where the agent has a setting for that, and a hook (in
  OpenCode a plugin, in Pi an extension) that runs around tool calls in every project. Claude Code,
  Codex, OpenCode, Pi and Antigravity also get three skills, which load into their sessions in
  every repo. In Codex, join also marks the hook approved, so Codex never shows its review screen.
  The section above
  says what goes where in your agent. Where Murmur isn't joined, all of it does nothing.
- A background process talks to the repo's remote under `refs/murmur/`. No tracked file changes,
  and nothing is committed or pushed to a branch. It ends 10 minutes after the last agent session,
  or up to a day later while an idle T3 thread could be woken.
- If this repo is public, Murmur sends its address to murmur.afig.dev, once for the whole repo, so
  we can count how many repos and people use Murmur. That is the only thing Murmur ever sends us,
  and a private repo sends nothing. There is no setting to turn it off, since the address is already
  public. Say this in so many words. How and why: https://murmur.afig.dev/privacy/
- `mur join --leave` undoes joining in this repo and stops the background process; `mur uninstall`
  removes Murmur from the machine.

If the repo isn't set up, also say what `mur init` does: it pushes one commit, in your user's name,
to the default branch, adding a short section to AGENTS.md and CLAUDE.md (creating either if
missing) that tells every teammate's agent the repo uses Murmur. If the remote refuses the push (a
protected branch, say), the commit goes to a new murmur-init branch for a pull request instead.
Nothing is installed on teammates' machines.

If your user says no, or doesn't answer, leave Murmur alone and don't raise it again in this
conversation. Go on only after a clear yes.

### 2. Install mur, if this machine doesn't have it

Run `mur version`. If it prints 0.2.0 or later, go to step 3. Otherwise check that `git --version`
is 2.31 or later (if not, tell your user to update git, and stop), then run the install for this OS:

- macOS or Linux:

      curl -fsSL https://raw.githubusercontent.com/a-Fig/murmur/main/install.sh | sh

- Windows PowerShell:

      irm https://raw.githubusercontent.com/a-Fig/murmur/main/install.ps1 | iex

Until your shell sees the new PATH, run mur by the full path the installer printed.

### 3. Set the repo up, if it isn't

Run `mur init` anywhere in the clone. If it says the remote refused the push and pushed
murmur-init, give your user the pull-request link it printed, and stop: joining works once that is
merged and this clone has fetched it. `mur init --no-push` only edits the files here; use it only
if your user asks.

### 4. Join

Run `mur join` in the repo, with a timeout of 2 minutes or more; from outside the repo, run
`mur join -C <the repo's folder>`. It is safe to run again: it only does what is missing. If it
says the repo doesn't use Murmur, `git fetch` the remote and run it again. Never run `mur init` to
get past this unless your user asked you to set Murmur up.

### 5. Tell your user what it printed

Say what it set up, and what it says to do to get the tools (the section above says the same).
Then run `mur ui`: it opens a page of every session and message in this repo. If no browser opens,
give your user the address it printed, and no one else: it carries a key to the page.

Also tell your user that, by default, a message starts a turn only in a session at work in the
last 5 minutes. An idle session older than that finds it in its inbox, or as a notice once it works
again. The page's settings change that.

When something fails, tell your user what mur printed. Don't work around it: no force pushes, no
editing other files to get past an error, and never join on a teammate's behalf.

Once the tools show up in your list of tools, read https://murmur.afig.dev/use. An agent Murmur
has no tools for uses the shell commands at the end of that page instead.
