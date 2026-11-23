package setup

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/gitx"
)

// FallbackBranch is where init pushes its commit when the server refuses it on the default branch,
// for a pull request.
const FallbackBranch = brand.Name + "-init"

// pushTries is how many times Push builds and pushes its commit when the default branch keeps moving
// between its fetch and its push.
const pushTries = 3

// beforePush runs between building the commit and pushing it; a test moves the branch there.
var beforePush = func(try int) {}

// Pushed is what Push did.
type Pushed struct {
	Remote, Branch string   // the remote, and its default branch
	SHA            string   // the commit init made; "" if the default branch already had the section
	Changed        []string // what the commit changes, one line per file
	// Fallback is set when the server refused the commit on the default branch, with git's reason
	// in Refused: the commit went to FallbackBranch instead, unless Pending says that branch already
	// had the section from an earlier run. PullURL opens a pull request from it on GitHub.
	Fallback, Refused, PullURL string
	Pending                    bool
	// Checkout says what became of the checkout init ran in: FastForwarded to the commit, or left
	// alone, for the reason in Checkout.
	FastForwarded bool
	Checkout      string
}

// ErrNoBranch says the remote has no branch, so there is no commit for the section to go on.
var ErrNoBranch = errors.New("the remote has no branch yet, so there is no commit to add the section to")

// Push makes init's edits as a commit on the default branch of the clone's remote (the one its bus
// is on, bus.RemoteFor) and pushes it there, never forced. The commit is built from the branch's
// fetched tip with a temporary index, so neither the working tree nor the index changes. When the
// branch moves before the push lands, Push fetches and builds again, up to pushTries times; when the
// server refuses the push for a reason of its own (a protected branch, a hook), the commit goes to
// FallbackBranch instead, for a pull request. Last, a checkout on the default branch, behind the
// commit and with no local changes to the files init edits, is fast-forwarded to it.
func Push(ctx context.Context, dir string) (Pushed, error) {
	var p Pushed
	top, err := Top(ctx, dir)
	if err != nil {
		return p, err
	}
	g, own, err := gitIn(ctx, top)
	if err != nil {
		return p, err
	}
	if p.Remote, err = bus.RemoteFor(ctx, g); err != nil {
		return p, err
	}
	if heads, err := g.Run(ctx, "", "ls-remote", "--heads", p.Remote); err == nil && strings.TrimSpace(heads) == "" {
		return p, ErrNoBranch
	}
	if p.Branch, err = defaultBranch(ctx, g, p.Remote); err != nil {
		return p, err
	}
	repo := &bus.Repo{Git: g, Remote: p.Remote}
	ref := "refs/heads/" + p.Branch
	tracking := "refs/remotes/" + p.Remote + "/" + p.Branch
	var paths []string
	for try := 1; ; try++ {
		tip, err := fetchTip(ctx, g, p.Remote, p.Branch)
		if err != nil {
			return p, err
		}
		var sha string
		sha, p.Changed, paths, err = buildCommit(ctx, g, own, tip)
		if err != nil {
			return p, err
		}
		if sha == "" {
			// The default branch already says the repo uses Murmur (if this was a retry, a
			// teammate's push did), and nothing of init's is pushed.
			p.SHA = ""
			setRemoteHead(ctx, g, p.Remote, tracking)
			if !markedTree(top) {
				p.Checkout = fmt.Sprintf("this checkout gets the section once %s is merged into its branch", p.Branch)
			}
			return p, nil
		}
		p.SHA = sha
		beforePush(try)
		err = repo.Push(ctx, sha, ref, bus.FastForward, "")
		if err == nil {
			// The remote-tracking branch is the default branch as this clone's other worktrees see it.
			g.Run(ctx, "", "update-ref", tracking, sha)
			setRemoteHead(ctx, g, p.Remote, tracking)
			break
		}
		if errors.Is(err, bus.ErrRejected) {
			if try < pushTries {
				continue
			}
			return p, fmt.Errorf("%s's %s kept moving while init pushed to it (%d tries); run init again", p.Remote, p.Branch, pushTries)
		}
		if !errors.Is(err, bus.ErrRefused) {
			return p, fmt.Errorf("pushing to %s's %s: %w", p.Remote, p.Branch, err)
		}
		p.Refused = err.Error()
		return p, fallback(ctx, g, repo, &p)
	}
	p.FastForwarded, p.Checkout = fastForward(ctx, top, repo, p.Branch, p.SHA, paths)
	return p, nil
}

// defaultBranch is the branch the remote's HEAD points at, or, if the remote doesn't say, the one
// this clone's refs/remotes/<remote>/HEAD points at (which git clone sets).
func defaultBranch(ctx context.Context, g gitx.Git, remote string) (string, error) {
	out, err := g.Run(ctx, "", "ls-remote", "--symref", remote, "HEAD")
	if err != nil {
		return "", fmt.Errorf("can't reach %s to find its default branch: %w", remote, err)
	}
	for _, line := range strings.Split(out, "\n") {
		if target, ok := strings.CutPrefix(strings.TrimSpace(line), "ref: "); ok {
			if name, head, ok := strings.Cut(target, "\t"); ok && head == "HEAD" {
				if b, ok := strings.CutPrefix(name, "refs/heads/"); ok {
					return b, nil
				}
			}
		}
	}
	if out, err := g.Run(ctx, "", "symbolic-ref", "-q", "refs/remotes/"+remote+"/HEAD"); err == nil {
		if b, ok := strings.CutPrefix(strings.TrimSpace(out), "refs/remotes/"+remote+"/"); ok && b != "" {
			return b, nil
		}
	}
	return "", fmt.Errorf("%s doesn't say what its default branch is, and this clone doesn't know either; "+
		"set it with `git remote set-head %s <branch>`, or use --no-push", remote, remote)
}

// fetchTip fetches the default branch into its remote-tracking ref and returns its tip.
func fetchTip(ctx context.Context, g gitx.Git, remote, branch string) (string, error) {
	tracking := "refs/remotes/" + remote + "/" + branch
	if _, err := g.Run(ctx, "", "fetch", "--quiet", "--no-tags", "--no-recurse-submodules", remote,
		"+refs/heads/"+branch+":"+tracking); err != nil {
		return "", fmt.Errorf("fetching %s from %s: %w", branch, remote, err)
	}
	out, err := g.Run(ctx, "", "rev-parse", "--verify", "--quiet", tracking+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("%s has no commit on %s to build on: %w", remote, branch, err)
	}
	return strings.TrimSpace(out), nil
}

// setRemoteHead points refs/remotes/<remote>/HEAD at the default branch init found: one nothing set,
// or one left naming a branch that is no longer the default. Until #830 the MCP server read the
// marker there; now it is only git's own record of the default branch, kept right.
func setRemoteHead(ctx context.Context, g gitx.Git, remote, tracking string) {
	head := "refs/remotes/" + remote + "/HEAD"
	if cur, err := g.Run(ctx, "", "symbolic-ref", "-q", head); err != nil || strings.TrimSpace(cur) != tracking {
		g.Run(ctx, "", "symbolic-ref", head, tracking)
	}
}

// buildCommit applies init's steps to the files of the commit tip and returns a commit of the
// result with tip as its parent, what it changes, and the paths it changes; sha is "" when nothing
// would change. It writes objects and nothing else: no ref, no index but a temporary one.
//
// The commit is made in own, the checkout's own git dir, so it is by whoever git says the user is
// there; everything else is in g, the clone's.
func buildCommit(ctx context.Context, g, own gitx.Git, tip string) (sha string, changed, paths []string, err error) {
	t := &treeFiles{ctx: ctx, g: g, tip: tip, files: map[string]*treeFile{}}
	for _, s := range steps {
		real, b, exists, err := t.read(s.name)
		if err != nil {
			if s.optional {
				continue
			}
			return "", nil, nil, err
		}
		e := s.edit(b, exists)
		if e.change && !e.remove && len(e.data) > maxRepoFile {
			// As writeOut refuses for a working tree: a file the edit took past what read reads would
			// count as unmarked, and the next init would stop on it (Opus review of 29cf0f5d).
			return "", nil, nil, fmt.Errorf("%s would be over %d MiB, more than %s reads", s.name, maxRepoFile>>20, brand.Command)
		}
		if e.change {
			f := t.files[real]
			f.data, f.exists, f.changed = e.data, !e.remove, true
		}
		if e.msg != "" {
			changed = append(changed, s.name+": "+e.msg)
		}
	}
	for name, f := range t.files {
		if f.changed {
			paths = append(paths, name)
		}
	}
	if len(paths) == 0 {
		return "", changed, nil, nil
	}

	tmp, err := os.MkdirTemp("", brand.Name+"-init")
	if err != nil {
		return "", nil, nil, err
	}
	defer os.RemoveAll(tmp)
	ig := gitx.Git{Dir: g.Dir, Env: []string{"GIT_INDEX_FILE=" + filepath.Join(tmp, "index")}}
	if _, err := ig.Run(ctx, "", "read-tree", tip); err != nil {
		return "", nil, nil, err
	}
	for _, name := range paths {
		f := t.files[name]
		if !f.exists {
			if _, err := ig.Run(ctx, "", "update-index", "--force-remove", "--", name); err != nil {
				return "", nil, nil, err
			}
			continue
		}
		blob, err := ig.Run(ctx, string(f.data), "hash-object", "-w", "--stdin")
		if err != nil {
			return "", nil, nil, err
		}
		mode := f.mode
		if mode != "100755" {
			mode = "100644"
		}
		if _, err := ig.Run(ctx, "", "update-index", "--add", "--cacheinfo", mode+","+strings.TrimSpace(blob)+","+name); err != nil {
			return "", nil, nil, err
		}
	}
	tree, err := ig.Run(ctx, "", "write-tree")
	if err != nil {
		return "", nil, nil, err
	}
	out, err := own.Run(ctx, commitMessage(changed), "commit-tree", strings.TrimSpace(tree), "-p", tip, "-F", "-")
	if err != nil {
		if strings.Contains(err.Error(), "identity") || strings.Contains(err.Error(), "who you are") {
			return "", nil, nil, fmt.Errorf("git doesn't know who you are, so it can't make init's commit; set user.name and user.email: %w", err)
		}
		return "", nil, nil, err
	}
	return strings.TrimSpace(out), changed, paths, nil
}

func commitMessage(changed []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Say in AGENTS.md and CLAUDE.md that this repo uses %s\n\n", brand.Product)
	fmt.Fprintf(&b, "`%s init` adds a short section to both files, so that a teammate's coding agent that\n"+
		"reads them knows the repo uses %[2]s and asks its user whether to join. It installs nothing;\n"+
		"%[2]s does nothing in a clone until its user joins there.\n\n", brand.Command, brand.Product)
	for _, c := range changed {
		b.WriteString("- " + c + "\n")
	}
	return b.String()
}

// treeFiles reads the files of one commit for init's steps, following a symbolic link to the file
// in the commit it names (CLAUDE.md is often a link to AGENTS.md), and holds what the steps make of
// them.
type treeFiles struct {
	ctx   context.Context
	g     gitx.Git
	tip   string
	files map[string]*treeFile // by real path
}

type treeFile struct {
	mode    string
	data    []byte
	exists  bool
	changed bool
}

// read returns the file the path name leads to in the commit, after the edits made so far.
func (t *treeFiles) read(name string) (real string, data []byte, exists bool, err error) {
	real = name
	for hops := 0; ; hops++ {
		if f, ok := t.files[real]; ok {
			return real, f.data, f.exists, nil
		}
		mode, blob, size, err := t.entry(real)
		if err != nil {
			return "", nil, false, err
		}
		if (mode == "100644" || mode == "100755" || mode == "120000") && size > maxRepoFile {
			// Read whole, it would sit in memory, as readRepoFile keeps a working tree's file from
			// doing (#947). ls-tree says its size, so the blob isn't read at all.
			return "", nil, false, fmt.Errorf("%s is over %d MiB in %s, more than any file an agent reads", real, maxRepoFile>>20, t.tip)
		}
		switch mode {
		case "":
			t.files[real] = &treeFile{}
			return real, nil, false, nil
		case "100644", "100755":
			out, err := t.g.Run(t.ctx, "", "cat-file", "blob", blob)
			if err != nil {
				return "", nil, false, err
			}
			t.files[real] = &treeFile{mode: mode, data: []byte(out), exists: true}
			return real, []byte(out), true, nil
		case "120000":
			target, err := t.g.Run(t.ctx, "", "cat-file", "blob", blob)
			if err != nil {
				return "", nil, false, err
			}
			next := path.Clean(path.Join(path.Dir(real), target))
			if path.IsAbs(target) || next == ".." || strings.HasPrefix(next, "../") || hops >= 8 {
				return "", nil, false, fmt.Errorf("%s is a symbolic link to %s, outside the repo, so init can't edit it in a commit; use --no-push", name, target)
			}
			real = next
		default:
			return "", nil, false, fmt.Errorf("%s isn't a file in %s, so init can't edit it in a commit; use --no-push", real, t.tip)
		}
	}
}

// entry is the mode, object and size of the path in the commit, or "" and 0 when it has none. A
// size ls-tree doesn't give (a submodule's commit) is 0.
func (t *treeFiles) entry(name string) (mode, object string, size int64, err error) {
	out, err := t.g.Run(t.ctx, "", "ls-tree", "--full-tree", "-l", "-z", t.tip, "--", name)
	if err != nil {
		return "", "", 0, err
	}
	for _, rec := range strings.Split(out, "\x00") {
		meta, p, ok := strings.Cut(rec, "\t")
		if !ok || p != name {
			continue
		}
		if fields := strings.Fields(meta); len(fields) == 4 {
			size, _ = strconv.ParseInt(fields[3], 10, 64)
			return fields[0], fields[2], size, nil
		}
	}
	return "", "", 0, nil
}

// fallback pushes the commit to FallbackBranch, after the server refused it on the default branch.
// A FallbackBranch that is already there and has the section is left as it is; one without it is
// someone else's, and init stops rather than replace it.
func fallback(ctx context.Context, g gitx.Git, repo *bus.Repo, p *Pushed) error {
	p.Fallback = FallbackBranch
	p.PullURL = pullURL(repo.Config(ctx, "remote."+p.Remote+".url"), p.Branch, FallbackBranch)
	ref := "refs/heads/" + FallbackBranch
	existing, err := repo.RemoteRef(ctx, ref)
	if err != nil {
		return err
	}
	if existing != "" {
		if _, err := fetchTip(ctx, g, p.Remote, FallbackBranch); err != nil {
			return err
		}
		if markedAt(ctx, g, existing) {
			p.Pending, p.SHA = true, existing
			return nil
		}
		return fmt.Errorf("%s refused the push to %s (%s), and %s already has a branch %s without the section; "+
			"delete or rename that branch, then run init again", p.Remote, p.Branch, p.Refused, p.Remote, FallbackBranch)
	}
	if err := repo.Push(ctx, p.SHA, ref, bus.Lease, ""); err != nil {
		return fmt.Errorf("%s refused the push to %s (%s), and the push to a new branch %s failed too: %w",
			p.Remote, p.Branch, p.Refused, FallbackBranch, err)
	}
	return nil
}

// pullURL is the page that opens a pull request from head into base, when the remote is on GitHub;
// "" for any other remote.
func pullURL(remoteURL, base, head string) string {
	repoPath := ""
	switch {
	case strings.HasPrefix(remoteURL, "git@github.com:"):
		repoPath = strings.TrimPrefix(remoteURL, "git@github.com:")
	default:
		u, err := url.Parse(remoteURL)
		if err != nil || !strings.EqualFold(u.Hostname(), "github.com") {
			return ""
		}
		repoPath = strings.TrimPrefix(u.Path, "/")
	}
	repoPath = strings.TrimSuffix(strings.TrimSuffix(repoPath, "/"), ".git")
	if strings.Count(repoPath, "/") != 1 {
		return ""
	}
	return "https://github.com/" + repoPath + "/compare/" + url.PathEscape(base) + "..." + url.PathEscape(head) + "?expand=1"
}

// fastForward moves the checkout at top to sha, the commit just pushed, when that is safe: it is on
// the default branch, its HEAD is an ancestor of sha, and the files the commit changes have no local
// changes, staged, unstaged, untracked or ignored (merge would overwrite an ignored file). git pull would refuse a file with an uncommitted change
// even when it matches what the pull brings (checked 2026-11-01), so a checkout left with init's
// edits uncommitted would be stuck. Otherwise it says why the checkout was left alone.
func fastForward(ctx context.Context, top string, repo *bus.Repo, branch, sha string, paths []string) (bool, string) {
	later := fmt.Sprintf("this checkout gets it once %s is merged into its branch", branch)
	head, err := gitx.RunIn(ctx, top, "symbolic-ref", "-q", "HEAD")
	if err != nil || strings.TrimSpace(head) != "refs/heads/"+branch {
		return false, "this checkout isn't on " + branch + ", so " + later
	}
	cur, err := gitx.RunIn(ctx, top, "rev-parse", "HEAD")
	if err != nil {
		return false, later
	}
	if ok, err := repo.IsAncestor(ctx, strings.TrimSpace(cur), sha); err != nil || !ok {
		return false, "this checkout's " + branch + " has commits " + repo.Remote + " doesn't, so " + later
	}
	// --no-optional-locks: looking must not write the index, which status otherwise refreshes.
	status, err := gitx.RunIn(ctx, top, append([]string{"--no-optional-locks", "status", "--porcelain", "--untracked-files=all", "--ignored", "--"}, paths...)...)
	if err != nil {
		return false, later
	}
	if strings.TrimSpace(status) != "" {
		return false, "this checkout has local changes to " + strings.Join(paths, ", ") + ", so " + later
	}
	if _, err := gitx.RunIn(ctx, top, "merge", "--ff-only", "--no-overwrite-ignore", "--quiet", sha); err != nil {
		return false, "fast-forwarding this checkout failed (" + err.Error() + "), so " + later
	}
	return true, ""
}
