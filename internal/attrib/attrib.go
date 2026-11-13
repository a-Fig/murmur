// Package attrib finds the commits a command made in a worktree by comparing HEAD snapshots.
// Its git commands only read refs and objects; they never change the worktree or index.
package attrib

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/a-Fig/murmur/internal/gitx"
)

// committerSlack is how far a committer date may fall before the before snapshot's Time, or
// after the after snapshot's, and still count as made during the command. Git stores committer
// dates in whole seconds, truncated, so a commit made just after the snapshot can carry a date up
// to a second earlier, and clocks disagree a little. Two seconds covers both. The cost: a commit
// made in the two seconds before the snapshot counts too, so if the command also fetches such a
// commit, it is credited.
const committerSlack = 2 * time.Second

// Ref is a worktree's HEAD, plus what Head saw of the repository's refs and when it looked. SHA
// is empty before the first commit; Branch is empty when detached. Time is when Head started
// reading. Keep the whole value: NewCommits needs the tips and Time, and it refuses a Ref whose
// Time is zero rather than crediting every commit reachable from after.
type Ref struct {
	SHA    string
	Branch string
	Time   time.Time
	tips   []string // commit and annotated-tag object names, then detached HEADs, deduplicated
	gitDir string   // the worktree's git directory where Head found it itself, "" where git did
}

// Commit is a newly reachable commit. Branch is the after snapshot's branch, since git commits
// themselves have no branch membership; it is empty for a detached HEAD.
type Commit struct {
	SHA     string
	Subject string
	Branch  string
}

// Head reads this worktree's own HEAD, including in linked worktrees, and records every ref tip
// (local branches, remote-tracking branches and tags) and every worktree's detached HEAD with the
// time it started. for-each-ref lists no worktree's HEAD, and a call judged in another worktree of
// the clone than the one it started in (judgeEnd in the daemon) would otherwise credit a commit
// only that worktree's detached HEAD reached, made in the slack before the start (#60). Where it
// can read HEAD itself, from the worktree's git directory (see gitx.Discover), it runs one git
// process, the for-each-ref scan, which grows with the refs, and reads the other worktrees' HEAD
// files, a small read each. Elsewhere, such as a reftable repository, it asks git for HEAD and the
// worktrees too, and runs four. The hook waits for it before every tool call that could commit
// (#21), and on Windows each git process costs a tenth of a second or more.
func Head(ctx context.Context, worktree string) (Ref, error) {
	// Read the clock before any ref: a commit made after this and not on a tip is one the command made.
	ref := Ref{Time: time.Now()}
	if gitDir, commonDir, ok := gitx.Discover(worktree); ok {
		if symref, sha, ok := readHEAD(gitDir, commonDir); ok {
			return headFromFile(ctx, worktree, gitDir, commonDir, symref, sha, ref)
		}
	}
	return headByGit(ctx, worktree, ref)
}

// readHEAD reads a worktree's HEAD file: the ref it names (refs/heads/main), or the commit it is
// detached at. ok is false for anything else, and for a reftable repository, whose HEAD file only
// holds a placeholder.
func readHEAD(gitDir, commonDir string) (symref, sha string, ok bool) {
	if _, err := os.Stat(filepath.Join(commonDir, "reftable")); err == nil {
		return "", "", false
	}
	return readHEADFile(filepath.Join(gitDir, "HEAD"))
}

// readHEADFile is readHEAD for the HEAD file at path, in a repository that isn't reftable.
func readHEADFile(path string) (symref, sha string, ok bool) {
	if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() || fi.Size() > 1024 {
		return "", "", false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", false
	}
	text := strings.TrimSpace(string(b))
	if target, found := strings.CutPrefix(text, "ref:"); found {
		target = strings.TrimSpace(target)
		if !strings.HasPrefix(target, "refs/") || target == "refs/heads/.invalid" {
			return "", "", false
		}
		return target, "", true
	}
	if (len(text) == 40 || len(text) == 64) && strings.Trim(text, "0123456789abcdef") == "" {
		return "", text, true
	}
	return "", "", false
}

// headFromFile completes a Ref from what the HEAD file said, with one for-each-ref: the tips, and
// the commit HEAD's branch is at. A branch that isn't among the refs is unborn, as headByGit has
// it; a branch at anything but a commit, or one that is itself a symbolic ref (git names the branch
// at the end of the chain), is left to git to read.
func headFromFile(ctx context.Context, worktree, gitDir, commonDir, symref, sha string, ref Ref) (Ref, error) {
	head, err := scanRefs(ctx, worktree, symref, &ref)
	if err != nil {
		return Ref{}, err
	}
	addTips(&ref, detachedHeadFiles(commonDir))
	if symref == "" {
		ref.SHA = sha
	} else {
		ref.Branch = strings.TrimPrefix(symref, "refs/heads/")
		if head.symref != "" {
			return headByGit(ctx, worktree, Ref{Time: ref.Time})
		}
		switch head.objectType {
		case "":
			// Unborn: a symbolic HEAD whose branch does not exist yet.
		case "commit":
			ref.SHA = head.objectName
		default:
			return headByGit(ctx, worktree, Ref{Time: ref.Time})
		}
	}
	ref.gitDir = gitDir
	return ref, nil
}

// detachedHeadFiles returns the commits the clone's worktrees are detached at, from their HEAD
// files: the main worktree's in the common directory, and each linked one's under worktrees/. A
// HEAD on a branch is left out, as for-each-ref has the branch. One that can't be read is skipped:
// a missing tip only lets in what Head always let in before (#60).
//
// The files are read by up to headReaders goroutines at once. Each read is a file open, and a clone
// can hold hundreds of worktree entries, stale ones included until `git worktree prune`: in a
// Windows clone with 159 (2026-11-12) reading them one at a time took 22 ms of every Head, and 8 at
// once took 5 ms (#507).
func detachedHeadFiles(commonDir string) []string {
	paths := []string{filepath.Join(commonDir, "HEAD")}
	entries, _ := os.ReadDir(filepath.Join(commonDir, "worktrees"))
	for _, e := range entries {
		paths = append(paths, filepath.Join(commonDir, "worktrees", e.Name(), "HEAD"))
	}
	found := make([]string, len(paths))
	next := make(chan int)
	var wg sync.WaitGroup
	for range min(headReaders, len(paths)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				if _, sha, ok := readHEADFile(paths[i]); ok {
					found[i] = sha
				}
			}
		}()
	}
	for i := range paths {
		next <- i
	}
	close(next)
	wg.Wait()
	var shas []string
	for _, sha := range found {
		if sha != "" {
			shas = append(shas, sha)
		}
	}
	return shas
}

// headReaders is how many HEAD files detachedHeadFiles reads at once. More than 8 measured no faster.
const headReaders = 8

// detachedHeadsByGit is detachedHeadFiles with git listing the worktrees: one more process.
func detachedHeadsByGit(ctx context.Context, worktree string) ([]string, error) {
	out, err := gitx.RunIn(ctx, worktree, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	// Each worktree is a block of lines ended by a blank one; a detached one has a "detached" line
	// after its "HEAD <sha>". -z would keep a path with a newline in it from reading as lines of
	// its own, but needs git 2.36, and mur supports 2.31; such a path could at most add a tip,
	// which only keeps more out.
	var shas []string
	var sha string
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		switch {
		case line == "":
			sha = ""
		case strings.HasPrefix(line, "HEAD "):
			sha = strings.TrimPrefix(line, "HEAD ")
		case line == "detached" && sha != "":
			shas = append(shas, sha)
		}
	}
	return shas, nil
}

// addTips adds detached HEADs to ref's tips, sorted, so both ways of finding them record the same
// list, and leaving out any already there.
func addTips(ref *Ref, shas []string) {
	slices.Sort(shas)
	for _, sha := range slices.Compact(shas) {
		// git before 2.44 writes the all-zero ID into a new worktree's HEAD for a moment during
		// `git worktree add`. It names no commit, and as a tip it would make NewCommits's git log
		// fail with "bad object", so the call would be credited nothing.
		if strings.Trim(sha, "0") == "" {
			continue
		}
		if !slices.Contains(ref.tips, sha) {
			ref.tips = append(ref.tips, sha)
		}
	}
}

// headByGit is Head with git reading HEAD and the worktrees: four processes.
func headByGit(ctx context.Context, worktree string, ref Ref) (Ref, error) {
	// RunIn uses exec.CommandContext and the repository's git error/cancellation conventions.
	branch, err := gitx.RunIn(ctx, worktree, "symbolic-ref", "--quiet", "HEAD")
	if err != nil && !exitCode(err, 1) {
		return Ref{}, err
	}
	branch = strings.TrimSpace(branch)
	ref.Branch = strings.TrimPrefix(branch, "refs/heads/")
	head, err := scanRefs(ctx, worktree, branch, &ref)
	if err != nil {
		return Ref{}, err
	}
	detached, err := detachedHeadsByGit(ctx, worktree)
	if err != nil {
		return Ref{}, err
	}
	addTips(&ref, detached)
	sha, err := gitx.RunIn(ctx, worktree, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil {
		if exitCode(err, 1) && branch != "" && head.objectType == "" {
			return ref, nil // A symbolic HEAD whose branch does not exist yet is unborn.
		}
		return Ref{}, err
	}
	ref.SHA = strings.TrimSpace(sha)
	return ref, nil
}

// refLine is one ref as for-each-ref lists it. symref is the ref it names, if it is a symbolic ref;
// its object is then the one at the end of the chain.
type refLine struct{ objectType, objectName, symref string }

// scanRefs records every ref tip in ref, and returns the line of the ref named branch, if there is
// one (empty when branch is "" or doesn't exist).
func scanRefs(ctx context.Context, worktree, branch string, ref *Ref) (refLine, error) {
	out, err := gitx.RunIn(ctx, worktree, "for-each-ref", "--format=%(refname)%00%(objecttype)%00%(objectname)%00%(symref)")
	if err != nil {
		return refLine{}, err
	}
	seen := make(map[string]bool)
	var head refLine
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(strings.TrimSuffix(line, "\r"), "\x00")
		if len(fields) != 4 {
			return refLine{}, fmt.Errorf("unexpected git for-each-ref output: %q", line)
		}
		if branch != "" && fields[0] == branch {
			head = refLine{fields[1], fields[2], fields[3]}
		}
		switch fields[1] {
		case "commit", "tag":
			// An annotated tag is kept as the tag object, not peeled here: git log peels it,
			// tags of tags included, and ignores one that ends at a tree or blob. Peeling each
			// with rev-parse cost a process per tag, over a minute for 1,000 release tags.
			if !seen[fields[2]] {
				seen[fields[2]] = true
				ref.tips = append(ref.tips, fields[2])
			}
		}
	}
	return head, nil
}

// NewCommits returns the commits a command made, oldest first in topological order. A commit is
// returned when all three hold:
//
//  1. it is reachable from after;
//  2. it is not reachable from any tip in before, whether or not the branch changed;
//  3. its committer date is no earlier than before.Time less committerSlack, and no later than
//     after.Time plus committerSlack.
//
// Rule 2 keeps existing history out, including history a branch switch, merge, fast-forward or
// rebase brings in from a branch that already existed. Rule 3 keeps out history fetched by the
// command, such as a pull's teammate commits, which were committed before the command began.
// Its upper end keeps out a fetched commit from a teammate whose clock runs ahead, dated after
// the command ended; this machine's clock dated every commit the command made while it ran.
// Amend, rebase, cherry-pick and pull --rebase all stamp a new committer date, so their results
// pass rule 3. A push in the same command does not hide a commit: rule 2 looks only at before.
//
// Known limits. A commit that another writer makes to this HEAD's history during the command, and
// that the same command fetches, passes all three rules and is credited. Writers that commit to
// other branches are not credited, because their commits are not reachable from after. A fetched
// commit whose date, from a clock running ahead, lands inside the command's span still passes
// rule 3. A commit the command makes with a date set ahead (GIT_COMMITTER_DATE, or a container or
// VM whose clock runs fast) fails it and is not credited. A rewrite that reproduces an existing
// commit exactly (same parent, tree, author and committer second) yields that commit's SHA, which
// is a tip in before, so rule 2 drops it. All rare, and none handled.
func NewCommits(ctx context.Context, worktree string, before, after Ref) ([]Commit, error) {
	if before.Time.IsZero() {
		return nil, errors.New("attrib: before was not read by Head")
	}
	if after.Time.IsZero() {
		return nil, errors.New("attrib: after was not read by Head")
	}
	if after.SHA == "" {
		return nil, nil
	}
	// The tips go to git log on stdin, one per line. On the command line, about 780 refs at
	// distinct commits pass Windows' limit of about 32K characters, and git cannot start.
	// gitx.RunIn takes no stdin, so this finds the worktree's git directory and runs log there;
	// that costs one more short process.
	gitDir := after.gitDir
	if gitDir == "" {
		out, err := gitx.RunIn(ctx, worktree, "rev-parse", "--absolute-git-dir")
		if err != nil {
			return nil, err
		}
		gitDir = strings.TrimSpace(out)
	}
	var exclude strings.Builder
	if before.SHA != "" {
		exclude.WriteString("^" + before.SHA + "\n")
	}
	for _, tip := range before.tips {
		exclude.WriteString("^" + tip + "\n")
	}
	repo := gitx.Git{Dir: strings.TrimSpace(gitDir)}
	out, err := repo.Run(ctx, exclude.String(), "log", "--stdin", "--reverse", "--topo-order", "--no-show-signature", "--encoding=UTF-8", "--format=%H%x00%ct%x00%s", "-z", "--end-of-options", after.SHA, "--")
	if err != nil {
		return nil, err
	}
	var commits []Commit
	if out == "" {
		return commits, nil
	}
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	if len(fields)%3 != 0 {
		return nil, fmt.Errorf("unexpected git log output: %q", out)
	}
	earliest := before.Time.Add(-committerSlack)
	latest := after.Time.Add(committerSlack)
	for i := 0; i < len(fields); i += 3 {
		committed, err := strconv.ParseInt(fields[i+1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("unexpected committer date in git log output: %q", fields[i+1])
		}
		if at := time.Unix(committed, 0); at.Before(earliest) || at.After(latest) {
			continue
		}
		commits = append(commits, Commit{SHA: fields[i], Subject: fields[i+2], Branch: after.Branch})
	}
	return commits, nil
}

func exitCode(err error, code int) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == code
}
