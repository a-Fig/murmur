# Murmur: notes for Claude Code

Murmur lets coding agents on different teammates' machines, in the same repo, discover and
message each other, with git refs on GitHub as the transport. V1 exists (2026-10-26): one Go
binary, `cmd/mur`, with its packages in `internal/`; `go test ./...` runs it all, on Linux, macOS and
Windows in CI; most pull requests leave `internal/daemon` on macOS and Windows to main's run
(`docs/testing.md`).
`docs/VISION.md` is the direction and its reasons; read it before proposing architecture. How each
part works is in `docs/design/`, one file per topic, mapped in its README. Tyler's rule (2026-11-14,
#578): a PR adds to VISION only when it changes a direction; how a feature works goes in its design
file or in code comments. The name lives in `internal/brand`, so a rename is that file plus the
module path.
The logo files, palette and type, and the renders a palette change means redoing, are in
`docs/brand.md`.

## Writing for whoever comes next

Docs and comments inform rather than direct: what is true, why, what it cost. Directions are
Tyler's rules, marked as his. Facts that can go stale carry a date. Each fact lives in one place:
this file loads into every session, so it stays short and points into `docs/` for detail.

Tyler's rule (2026-11-16): prose people read (the site, the README, the `mur ui` page, terminal
output) is written with the writing-for-humans skill:
https://github.com/a-Fig/figs-skills/blob/main/writing-for-humans-101/SKILL.md

## Words that mean two things

- **Murmur, mur, t4.** Murmur is the product and the repo (`a-Fig/murmur`); `mur` is the command
  people type. The repo and the tool were t4 until 2026-11-01 (`docs/names.md`).
- **session.** One agent conversation (a T3 thread, a Claude Code or Codex session). One device
  runs many.
- **device.** One clone's mur daemon; the clone's worktrees share it, and a second clone on the
  same machine is a second device. It owns one ref, `refs/murmur/mesh/<deviceId>`.
- **mesh, bus.** The set of `refs/murmur/*` refs on the remote that carry events.

## Traps

- **Never touch the working tree or index when writing to the bus.** Humans and agents are editing
  that checkout at the same moment. Bus writes use plumbing (`commit-tree`, `push <sha>:<ref>`);
  a checkout of a bus ref, even briefly, would wreck someone's work.
- On Windows, Git Bash rewrites POSIX-looking arguments into Windows paths unless
  `MSYS_NO_PATHCONV=1` is set; refs like `refs/murmur/...` are safe, absolute paths are not.

## Issue labels

Every issue carries at least a kind label.

- `bug` / `Feature` / `Task` / `question`: the kind, spelled exactly so. `question` is an open
  design question.
- `priority:` has two scales, and an open issue carries one label, from either: whoever files it
  picks the scale that says more about it (Tyler, 2026-11-16, #751).
  - `priority: now` / `soon` / `future` / `maybe never`: when it should be done.
  - `priority: low` / `med` / `high` / `xhigh`: how much it matters.
- `PR needed` / `non-PR needed`: where the work lives. `non-PR needed` means some of it is done by
  hand: a decision, an account, a test in the world.
- `from: <Model>`: see Workflow.

Why an issue closed is its `state_reason` (`completed` / `not_planned` / `duplicate`), not a label.
Tyler's rule: no new label without a reason.

## Workflow

Tyler's rules:

- Keep what agents and users outside this repo read current and lean: the skills
  (`internal/skills`), `docs/agents/`, the MCP server's instructions and tool descriptions, and the
  README. A change to behavior updates them in the same PR. They load into other people's agents,
  next to real work, so each line earns its place: cut what an agent doesn't need to act.
- Commit messages and PR bodies explain *why*.
- Every PR closes at least one issue, and its body starts with only its `Closes #N` lines; work no
  issue covers gets one filed first.
- PR bodies follow `.github/pull_request_template.md`. GitHub pre-fills it only in the web UI, so an
  agent opening a PR through `gh` writes those sections itself.
- Every PR gets an Opus, Fable or Sol review before it is handed over, and merges with every CI
  check that ran green, except as the next rule says. The PR body says which commits the review
  covered.
- **PRs can skip CI and code review in these situations:**
  - **Docs, templates and images only** (Markdown outside `internal/` and `docs/agents/` other
    than `README.md`, `.github/` templates, images): skip both. No check reads these files.
  - **UI, styling or assets** (2026-11-10): skip review. CI still runs, because the page in
    `internal/web/static` is built into the binary and tested.
  - **The website only** (`site/`, plus anything in the first item): skip review. The Go tests
    never read `site/`; its own tests run in `test-linux`, or locally in seconds:
    `node --test --import ./site/test/register.mjs site/test/*.test.mjs`. Either one passing is
    enough.
  - **A merge of main that only resolves conflicts:** skip review of that commit.
  - **With permission from Tyler.**

  Anything else waits for both. `README.md` and `docs/agents/` (a setup test reads them),
  `internal/skills`, `go.mod` and `.github/workflows` are tested, so even a small change to them
  waits for CI. Since 2026-11-12 CI applies its half of these itself (#512): it runs only the jobs
  the changed files need (`scripts/ci-level.sh`), so there is no job to skip by hand.
- A PR that changes something a person sees (the `mur ui` page, the site, terminal output) shows
  it in its body: before and after screenshots, and a GIF when it moves or is interactive.
  `gh` can't upload images; `scripts/pr-media.sh` can (`docs/pr-media.md`).
- Before a release is tagged, a Codex (Sol) agent reviews `main` for bugs and security holes
  (`docs/releasing.md`).
- Issues a model files on its own initiative get `(from <model>)` in the title and the matching
  `from: <Model>` label. An issue Tyler asks for is `from: Tyler`, even though a model typed it.

<!-- murmur:start -->
## Murmur
This repo uses Murmur, a cross-device, intra-repo communication channel for contributing coding agents like yourself. If you have not joined Murmur yet, you should.

- To join Murmur (ask your user first): https://raw.githubusercontent.com/a-Fig/murmur/main/docs/agents/join.md
- To use Murmur (no tools? in T3 Code, `mur who` and `mur send` work in your shell): https://raw.githubusercontent.com/a-Fig/murmur/main/docs/agents/use.md
<!-- murmur:end -->
