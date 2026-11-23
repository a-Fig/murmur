package bus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/gitx"
)

// ErrRejected means the remote refused a push because the ref wasn't what the push expected:
// someone else moved it first. Any other failure (network, auth, a server-side rule) is a
// different error.
var ErrRejected = errors.New("push rejected: the ref moved")

// ErrRefused means the server refused a push for a reason of its own, such as a protected branch
// or a hook; the error that wraps it carries the server's reason.
var ErrRefused = errors.New("refused by the server")

// Commit is a bus commit as read back.
type Commit struct {
	SHA     string
	Time    time.Time // committer time, the writer's clock
	Message string
	Size    int64 // the commit object's, headers and all
	// Oversize is a commit over the limit it was read with: it was left unread, so it has no Time
	// or Message.
	Oversize bool
	// Tip is, for a commit Recent read, the tip of the log it is in.
	Tip string
}

// PushMode chooses how a push may move the remote ref.
type PushMode int

const (
	// FastForward accepts only a descendant of the remote's current tip.
	FastForward PushMode = iota
	// Force replaces whatever is there (presence, which has no history).
	Force
	// Lease accepts only if the remote ref is exactly the expected value: a compare-and-swap.
	Lease
)

// Repo is the bus as seen from one clone.
type Repo struct {
	Git    gitx.Git
	Remote string
	// Name and Email sign bus commits as the device, so no commit-tree depends on the user's
	// git identity being configured.
	Name, Email string
	// Page is how many commits Log hands over at a time; zero is LogPage.
	Page int

	treeMu    sync.Mutex // the daemon's goroutines all write through one Repo
	emptyTree string

	reader *gitx.Reader // set by KeepReader; nil reads with a git for each read

	fetchMu  sync.Mutex
	fetching map[string]bool // the refs of each Fetch running, by their prefix (fetchPrefix)
	spent    []string        // the refs of Fetches that have ended, for Tidy to delete
	lost     bool            // a Fetch couldn't list what it wrote, so the next Tidy looks at all
	writes   uint64          // counts the Fetches and UpdateLocals begun, in the order they began

	setMu  ctxMutex          // held while bus refs are set (setRefs), so that setBy is as git has it
	setBy  map[string]uint64 // for each bus ref set, which write set it last, by its count in writes
	tidyMu sync.Mutex        // one Tidy at a time
}

// ReaderLinger is how long the git that KeepReader keeps stays after its last read. The daemon
// looks at the remote every 3 s while a conversation is going, so the reads of one conversation
// share a git, and a daemon nobody is writing to has none.
const ReaderLinger = 5 * time.Second

// KeepReader has the Repo read commits through one git that stays running between reads
// (gitx.Reader), in place of two started for every read. Call it before the Repo is used, and
// Close when done with it. Where the git installed can't do that, reads go on as they did.
func (r *Repo) KeepReader() { r.reader = r.Git.NewReader(ReaderLinger) }

// Close ends the git KeepReader keeps, if there is one, and waits for it to be gone.
func (r *Repo) Close() {
	if r.reader != nil {
		r.reader.Close()
	}
}

// ask puts a question to the kept reader: Info, or Contents of objects up to max bytes. With no
// reader, or one that can't answer (gitx.ErrNoReader), ok is false and the caller starts a git.
func (r *Repo) ask(ctx context.Context, names []string, contents bool, max int64) (objs []gitx.Object, ok bool, err error) {
	if r.reader == nil || len(names) == 0 {
		return nil, false, nil
	}
	if contents {
		objs, err = r.reader.Contents(ctx, names, max)
	} else {
		objs, err = r.reader.Info(ctx, names)
	}
	if errors.Is(err, gitx.ErrNoReader) {
		return nil, false, nil
	}
	return objs, true, err
}

// EmptyTree returns the ID of git's empty tree, which every bus commit carries: bus commits
// hold no files, so fetching them brings nothing else along.
func (r *Repo) EmptyTree(ctx context.Context) (string, error) {
	r.treeMu.Lock()
	defer r.treeMu.Unlock()
	if r.emptyTree != "" {
		return r.emptyTree, nil
	}
	out, err := r.Git.Run(ctx, "", "hash-object", "-t", "tree", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	r.emptyTree = strings.TrimSpace(out)
	return r.emptyTree, nil
}

// NewCommit writes a commit with the empty tree and the given parent ("" for a root commit).
// It writes an object and nothing else: no ref moves until a push succeeds.
func (r *Repo) NewCommit(ctx context.Context, parent, message string) (string, error) {
	tree, err := r.EmptyTree(ctx)
	if err != nil {
		return "", err
	}
	// Unsigned on purpose: a signing prompt on every message would make the bus unusable, and
	// these commits never reach a branch.
	args := []string{"-c", "user.name=" + r.Name, "-c", "user.email=" + r.Email,
		"commit-tree", "--no-gpg-sign", tree}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	args = append(args, "-F", "-")
	out, err := r.Git.Run(ctx, message, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// ListRemote returns every bus ref on the remote and its SHA. It is one round trip and
// transfers no objects, which is why the daemon can afford it every few seconds.
func (r *Repo) ListRemote(ctx context.Context) (map[string]string, error) {
	out, err := r.Git.Run(ctx, "", "ls-remote", r.Remote, brand.RefRoot+"*")
	if err != nil {
		return nil, err
	}
	refs := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		sha, ref, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if ok && strings.HasPrefix(ref, brand.RefRoot) && !strings.HasPrefix(ref, fetchPrefix) {
			refs[ref] = sha
		}
	}
	return refs, nil
}

// RemoteRef returns one ref's SHA on the remote, or "" if it doesn't exist there.
func (r *Repo) RemoteRef(ctx context.Context, ref string) (string, error) {
	out, err := r.Git.Run(ctx, "", "ls-remote", r.Remote, ref)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if sha, name, ok := strings.Cut(strings.TrimSpace(line), "\t"); ok && name == ref {
			return sha, nil
		}
	}
	return "", nil
}

// Fetch copies the given remote refs to the same names locally. The local copies are a cache
// for reading history; what the daemon has processed is tracked separately, so a fetch that
// lands late and rewinds a local copy does no harm.
//
// It offers the server only the bus commits it has (--negotiation-tip), as no commit Murmur writes
// has a branch's commit in its history. By default git offers every local ref, and for a presence
// commit, which has no parent, the server is never ready, so the clone lists the user's whole
// history a batch per request (docs/design/transport.md, "A fetch offers only bus commits"). The
// cost: a commit built on a branch and pushed onto a bus ref, junk to Murmur, brings its branch's
// whole history, since the server isn't told the clone has it; on a large repo #15's slow fetch
// takes it. A clone with no bus refs yet offers nothing, which git warns of on stderr, where it
// leads the error of a fetch that fails.
//
// It writes only the refs it names (--refmap=). Otherwise git also writes wherever the remote's
// own fetch refspecs map each ref it brings, and a refspec can map a bus ref onto a branch, which
// a fetch would then move under whoever has it checked out (#950).
//
// Nor does it write through a symbolic ref (#965). git fetch does: were refs/murmur/x a symbolic
// ref to refs/heads/main, it would set main to the bus commit, under the checkout and index, and
// the user's next commit would sit on bus history. So the fetch writes refs of its own, under a
// name no other ref has (fetchPrefix), and a transaction then sets each bus ref itself
// (--no-deref) to what the fetch brought. A symbolic bus ref is replaced, as UpdateLocal says. All
// of a fetch's refs are set, or none, which costs little: the daemon fetches each ref of a batch
// that failed on its own.
//
// It returns what it brought, each ref's commit, and that, not the ref, is what the caller reads:
// by then another fetch may have set the ref (#979). Nor can an older write overwrite a newer one.
// Of two writes to a ref, Fetches and UpdateLocals, the one begun later stands, whichever ends
// first, so a slow fetch begun before a quick one can't rewind the ref when it lands. Writes are
// put in order within this Repo; the daemon writes the bus refs through one.
//
// It removes none of its own refs, as a delete locks packed-refs, which another git can hold, and
// on Git for Windows a delete waits for that lock even for a ref that isn't there; a sync's
// fetches would each wait (#978). Tidy removes them. A ctx already done starts nothing, and leaves
// nothing to remove.
func (r *Repo) Fetch(ctx context.Context, refs []string) (map[string]string, error) {
	if len(refs) == 0 {
		return map[string]string{}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	own := fmt.Sprintf("%s%016x/", fetchPrefix, rand.Uint64())
	r.fetchMu.Lock()
	if r.fetching == nil {
		r.fetching = map[string]bool{}
	}
	r.fetching[own] = true
	r.writes++
	write := r.writes
	r.fetchMu.Unlock()
	args := []string{"-c", "gc.auto=0", "-c", "maintenance.auto=false", "fetch", "--quiet",
		"--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", "--refmap=",
		"--negotiation-tip=" + brand.RefRoot + "*", r.Remote}
	of := map[string]string{} // each of the fetch's own refs, to the bus ref it is for
	for _, ref := range refs {
		at := own + strings.TrimPrefix(ref, brand.RefRoot)
		args = append(args, "+"+ref+":"+at)
		of[at] = ref
	}
	_, err := r.Git.Run(ctx, "", args...)
	// The rest is local and quick, and has a deadline of its own: the fetch may have used up its
	// own, and on Windows a git stopped by one is killed, and can leave a lock behind.
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	// What the fetch wrote is read whether it worked or not, as one that failed may have written
	// some; Tidy deletes those it finds.
	tips, wrote, lerr := r.fetched(lctx, own, of)
	if err == nil {
		err = lerr
	}
	if err == nil {
		for _, ref := range refs {
			if tips[ref] == "" {
				err = fmt.Errorf("the fetch of %s brought nothing", ref)
				break
			}
		}
	}
	if err == nil {
		if testBeforeSet != nil {
			testBeforeSet(refs)
		}
		err = r.setRefs(lctx, write, tips)
	}
	r.fetchMu.Lock()
	delete(r.fetching, own)
	r.spent = append(r.spent, wrote...)
	r.lost = r.lost || lerr != nil
	r.fetchMu.Unlock()
	if err != nil {
		return nil, err
	}
	return tips, nil
}

// fetched says what a fetch wrote under own, its prefix: the commit each bus ref came in at, by
// the bus ref, and which of the fetch's own refs are there. of gives the bus ref each of those is
// for.
//
// The kept reader is asked where there is one: a git for-each-ref for every fetch was 5% of git's
// time in the daemon's tests on Windows (#1008), and the reader answers without a process being
// started. Each of the fetch's refs has a name no fetch before it had, with 64 random bits that
// never leave this clone, so the answer can't be an old one or another's ref, and a ref the reader
// calls missing is confirmed by a new git (gitx.Reader.Refs). With no reader, a git too old for one
// (before 2.36), a name it won't take, or any failure of it, the refs are listed with for-each-ref,
// as they were. The reader has readerWait of the time, so that the listing has time left if the
// reader is busy with another read: the rest, less what a git stopped mid-answer takes to end.
func (r *Repo) fetched(ctx context.Context, own string, of map[string]string) (tips map[string]string, wrote []string, err error) {
	tips = map[string]string{}
	if r.reader != nil {
		names := make([]string, 0, len(of))
		for at := range of {
			names = append(names, at)
		}
		slices.Sort(names)
		rctx, cancel := context.WithTimeout(ctx, readerWait)
		objs, rerr := r.reader.Refs(rctx, names)
		cancel()
		if rerr == nil && len(objs) == len(names) {
			for i, o := range objs {
				if !o.Missing {
					tips[of[names[i]]] = o.ID
					wrote = append(wrote, names[i])
				}
			}
			return tips, wrote, nil
		}
	}
	out, err := r.Git.Run(ctx, "", "for-each-ref", "--format=%(objectname) %(refname)", strings.TrimSuffix(own, "/"))
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) == 2 && of[f[1]] != "" {
			tips[of[f[1]]] = f[0]
			wrote = append(wrote, f[1])
		}
	}
	return tips, wrote, err
}

// readerWait is how long a Fetch gives the kept reader to say what it wrote, of the 15 s it has for
// that and for setting the bus refs.
const readerWait = 5 * time.Second

// testBeforeSet, set only by tests, is called as a Fetch that worked is about to set the bus refs.
var testBeforeSet func(refs []string)

// setRefs sets each bus ref in tips to its commit, in one transaction, for the write'th write
// (Fetch, UpdateLocal). A ref that a write begun later has set already is left as it is.
//
// Every Fetch and every send's UpdateLocal waits here, whatever ref it sets, so neither the wait
// nor git's may be long (#998): the wait for setMu ends with ctx, and git waits at most 100 ms
// (refLockWait) for a ref another git holds locked, not for as long as the clone's config says,
// which -1 makes forever. A ref that stays locked fails its write: a Fetch's is tried again with
// the next fetch, and an UpdateLocal's is logged, leaving the local copy as it was until the next
// write.
func (r *Repo) setRefs(ctx context.Context, write uint64, tips map[string]string) error {
	if err := r.setMu.lock(ctx); err != nil {
		return err
	}
	defer r.setMu.unlock()
	var set strings.Builder
	var refs []string
	for ref, sha := range tips {
		if r.setBy[ref] > write {
			continue
		}
		fmt.Fprintf(&set, "update %s %s\n", ref, sha)
		refs = append(refs, ref)
	}
	if len(refs) == 0 {
		return nil
	}
	if _, err := r.Git.Run(ctx, set.String(), append(slices.Clone(refLockWait), "update-ref", "--no-deref", "--stdin")...); err != nil {
		return err
	}
	if r.setBy == nil {
		r.setBy = map[string]uint64{}
	}
	for _, ref := range refs {
		r.setBy[ref] = write
	}
	return nil
}

// refLockWait is how long setRefs's git waits for a ref another git holds locked: git's own default,
// 100 ms, which a clone's config may have changed. A clone that keeps its refs in a reftable locks
// the whole table, under a setting of its own, which a git older than that setting ignores.
var refLockWait = []string{"-c", "core.filesRefLockTimeout=100", "-c", "reftable.lockTimeout=100"}

// ctxMutex is a mutex whose wait ends with a context. Its zero value is unlocked.
type ctxMutex struct {
	once sync.Once
	ch   chan struct{}
}

func (m *ctxMutex) lock(ctx context.Context) error {
	m.once.Do(func() { m.ch = make(chan struct{}, 1) })
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case m.ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *ctxMutex) unlock() { <-m.ch }

// fetchPrefix begins the refs a fetch writes before it sets the bus refs (Fetch). Each fetch takes
// a name of its own under it, so none of its refs is one that was there before, symbolic or not.
// It is under brand.RefRoot, so that `mur uninstall`'s advice removes any left behind and the
// daemon's repairs of broken refs and stale locks reach them. Neither listing of bus refs, local
// or remote, gives one.
const fetchPrefix = brand.RefRoot + "fetch-"

// Tidy deletes the refs of the Fetches that have ended. With all, it deletes every ref under
// fetchPrefix but those of Fetches still running: what a daemon stopped mid-fetch left, or another
// process. A Fetch that couldn't list what it wrote makes the next Tidy look at all of them too.
// Whoever fetches calls it now and then, apart from the fetches (the daemon, on a timer), as a
// delete locks packed-refs: another git can hold that lock for a moment, or, if it was killed,
// until someone removes it. Then Tidy fails, and what it was to delete waits for the next one.
// Another daemon's fetch, in the moment two run in one clone, can lose its refs to an all, and
// fail, to be tried again.
func (r *Repo) Tidy(ctx context.Context, all bool) error {
	r.tidyMu.Lock()
	defer r.tidyMu.Unlock()
	r.fetchMu.Lock()
	all = all || r.lost
	r.lost = false
	r.fetchMu.Unlock()
	var listed []string
	if all {
		out, err := r.Git.Run(ctx, "", "for-each-ref", "--format=%(refname)", brand.RefRoot)
		if err != nil {
			r.fetchMu.Lock()
			r.lost = true
			r.fetchMu.Unlock()
			return err
		}
		listed = strings.Fields(out)
	}
	// What has ended is taken after the listing: a Fetch that ended before it was listed has its
	// refs there, and one that ended after it has them in spent.
	r.fetchMu.Lock()
	drop := r.spent
	r.spent = nil
	seen := map[string]bool{}
	for _, ref := range drop {
		seen[ref] = true
	}
	for _, ref := range listed {
		rest, ok := strings.CutPrefix(ref, fetchPrefix)
		name, _, _ := strings.Cut(rest, "/")
		// A transaction that names a ref twice fails.
		if ok && !seen[ref] && !r.fetching[fetchPrefix+name+"/"] {
			drop = append(drop, ref)
			seen[ref] = true
		}
	}
	r.fetchMu.Unlock()
	if len(drop) == 0 {
		return nil
	}
	var del strings.Builder
	for _, ref := range drop {
		fmt.Fprintf(&del, "delete %s\n", ref)
	}
	// A delete waits for packed-refs.lock as long as core.packedRefsTimeout says, which a user's
	// config can make forever; a second is git's own default, and keeps a Tidy short.
	if _, err := r.Git.Run(ctx, del.String(), "-c", "core.packedRefsTimeout=1000", "update-ref", "--no-deref", "--stdin"); err != nil {
		r.fetchMu.Lock()
		r.spent = append(r.spent, drop...)
		r.fetchMu.Unlock()
		return err
	}
	return nil
}

// Push sets a remote ref to sha, or deletes it if sha is "". With Lease, expect is the value
// the remote ref must have ("" meaning it must not exist), checked atomically by the server.
//
// Pushes skip the repo's hooks: a pre-push hook that runs the test suite on every message would
// make the bus unusable, and these commits never reach a branch. Nor does a push of a bus ref
// write a local ref (#950). After a push git sets, or deletes, the local ref that the remote's
// fetch refspecs map the pushed ref to, and a refspec can map a bus ref onto a branch. So each
// such push adds two refspecs of its own (noTracking).
//
// A push the server refuses as busy (see busyReasons) is sent again as it was, after a second or
// so, up to pushTries times in all. Sending the same commit again can't write anything twice: if an
// earlier try landed after all, the next one lands on what is already there, or is refused and the
// look that follows every failure finds the ref at sha.
func (r *Repo) Push(ctx context.Context, sha, ref string, mode PushMode, expect string) error {
	for try := 1; ; try++ {
		busy, err := r.pushOnce(ctx, sha, ref, mode, expect)
		switch {
		case !busy:
			return err
		case try == pushTries:
			return fmt.Errorf("%w (%d tries)", err, try)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w, waiting to try again a push refused as busy: %v", ctx.Err(), err)
		case <-time.After(busyWait(try)):
		}
	}
}

// pushTries is how many times Push sends a push that the server refuses as busy.
const pushTries = 3

// busyReasons are the reasons GitHub gives, now and then, for refusing a push that met another push
// to the same repository, from the same device or another ("[remote rejected] (failed)"). They say
// nothing about the push itself, and the same push sent a moment later lands. What was seen, and
// when, is under "Pushes" in docs/design/transport.md.
var busyReasons = map[string]bool{"failed": true, "failure": true, "fatal error in commit_refs": true}

// busyWait is how long Push waits after its try'th try was refused as busy: half a second to a
// second after the first, a second to two after the second. It is random within that, so that two
// devices refused together don't meet again.
func busyWait(try int) time.Duration {
	d := 500 * time.Millisecond << (try - 1)
	return d + rand.N(d)
}

// pushOnce is one try of Push. busy says the server refused it as busy, so it may be sent again.
func (r *Repo) pushOnce(ctx context.Context, sha, ref string, mode PushMode, expect string) (busy bool, err error) {
	args := append(r.noTracking(ref), "push", "--porcelain", "--no-verify", "--no-follow-tags", "--no-signed",
		"--recurse-submodules=no", r.Remote)
	switch mode {
	case Force:
		args = append(args, "--force")
	case Lease:
		args = append(args, "--force-with-lease="+ref+":"+expect)
	}
	args = append(args, sha+":"+ref)
	out, err := r.Git.Run(ctx, "", args...)
	var refused string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "!\t") {
			refused = strings.TrimSpace(line)
		}
	}
	if err == nil && refused == "" {
		return false, nil
	}
	// A failed push may still have landed: the server can update the ref and then fail to
	// report it ("remote failed to report status"), or the connection can drop after. Calling
	// that a failure would make the caller write the same event twice, so look.
	// The push's own deadline may be what failed it, so look with a fresh one.
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	now, lerr := r.RemoteRef(lctx, ref)
	if lerr == nil && now == sha {
		return false, nil
	}
	if refused == "" || strings.Contains(refused, "[remote failure]") {
		// Network, auth: nothing was decided. Nor by a remote that took the pack and then failed
		// to say what it did with it ("remote failed to report status"), which is the push most
		// likely to have landed: it is no refusal (#745).
		if err == nil {
			err = fmt.Errorf("push to %s: %s", ref, refused)
		}
		return false, err
	}
	_, reason, rejected := strings.Cut(refused, "[remote rejected]")
	if !rejected {
		return false, ErrRejected // refused before sending: the ref had already moved
	}
	// The server refused. When two leased pushes race, the loser can pass the client-side
	// check and still lose on the server, which reports it as a rejection ("reference already
	// exists", "cannot lock ref"). If the ref moved, that is what happened.
	if mode == Lease && lerr == nil && now != expect {
		return false, ErrRejected
	}
	reason = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(reason), "("), ")")
	return busyReasons[reason], fmt.Errorf("push to %s %w: %s", ref, ErrRefused, refused)
}

// noTracking are the options that keep a push of a bus ref from writing a local ref (#950). git
// asks the remote's fetch refspecs which local ref tracks each ref the server says it updated, and
// a negative refspec makes the answer none. git checks a negative refspec against what the other
// refspecs map that ref to, not against the ref itself: git 2.31 to 2.53 do, and a pattern refspec
// such as refs/murmur/*:refs/heads/x/* then got past one alone (seen 2026-11-21). The identity
// refspec beside it maps every ref to itself, so the negative one rules each out first; a git that
// checked the ref itself would rule it out too. Both cover all of refs/, not just the bus refs: a
// server with a proc-receive hook can report the push under another name, such as a branch's
// (Opus review). Other pushes, `mur init`'s to a branch, keep git's update of the tracking ref.
func (r *Repo) noTracking(ref string) []string {
	if !strings.HasPrefix(ref, brand.RefRoot) {
		return nil
	}
	key := "remote." + r.Remote + ".fetch="
	return []string{"-c", key + "refs/*:refs/*", "-c", key + "^refs/*"}
}

// LocalRefs returns the local copies of bus refs. A symbolic ref is no copy (#965): git would give
// the commit of the ref it names, a branch's perhaps, and the daemon would build its log on the
// user's history and push that to the bus.
func (r *Repo) LocalRefs(ctx context.Context) (map[string]string, error) {
	out, err := r.Git.Run(ctx, "", "for-each-ref", "--format=%(objectname) %(refname) %(symref)", brand.RefRoot)
	if err != nil {
		return nil, err
	}
	refs := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) == 2 && !strings.HasPrefix(f[1], fetchPrefix) {
			refs[f[1]] = f[0]
		}
	}
	return refs, nil
}

// UpdateLocal points a local bus ref at sha. A symbolic ref there is replaced, and the ref it
// names left alone (--no-deref, #965); otherwise git would move that ref, which could be the
// checked-out branch. Murmur replaces it rather than refusing: refs/murmur/ holds copies of the
// remote's refs, which no symbolic ref is, and a refusal would leave that peer unread, or this
// device unable to send, until someone found the ref and removed it by hand.
// A Fetch begun earlier that ends later leaves it as this sets it (see Fetch).
func (r *Repo) UpdateLocal(ctx context.Context, ref, sha string) error {
	r.fetchMu.Lock()
	r.writes++
	write := r.writes
	r.fetchMu.Unlock()
	return r.setRefs(ctx, write, map[string]string{ref: sha})
}

// IsCommit says whether sha names a commit here. Anything else (a tree pushed by hand, or an
// object that isn't here) is not, and nothing can be built on it. Any other failure is an error:
// callers build over what isn't a commit, which on a passing fault would drop what is there.
func (r *Repo) IsCommit(ctx context.Context, sha string) (bool, error) {
	if objs, ok, err := r.ask(ctx, []string{sha}, false, 0); ok {
		return err == nil && objs[0].Type == "commit", err
	}
	out, err := r.Git.Run(ctx, "", "cat-file", "-t", sha)
	if err == nil {
		return strings.TrimSpace(out) == "commit", nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	// cat-file -e exits 1, and only 1, for an object that isn't here (or a git killed on
	// Windows when ctx is cancelled).
	_, cerr := r.Git.Run(ctx, "", "cat-file", "-e", sha)
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(cerr, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// IsAncestor reports whether a is an ancestor of b (or equal to it). A commit that isn't
// present locally counts as not an ancestor. Any other failure is an error, so that a passing
// fault isn't taken for rewritten history, which callers handle by starting over.
func (r *Repo) IsAncestor(ctx context.Context, a, b string) (bool, error) {
	_, err := r.Git.Run(ctx, "", "merge-base", "--is-ancestor", a, b)
	if err == nil {
		return true, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	for _, c := range []string{a, b} {
		// cat-file -e exits 1, and only 1, for an object that isn't here (or a git killed on
		// Windows when ctx is cancelled).
		_, cerr := r.Git.Run(ctx, "", "cat-file", "-e", c)
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if errors.As(cerr, &exit) && exit.ExitCode() == 1 {
			return false, nil
		}
		if out, terr := r.Git.Run(ctx, "", "cat-file", "-t", c); terr == nil && strings.TrimSpace(out) != "commit" {
			return false, nil // here, but not a commit, so no history to share
		}
	}
	return false, err
}

// recordSep and fieldSep cannot appear in what Murmur writes: subjects are collapsed to one line
// and the JSON body escapes control characters.
const (
	recordSep = "\x1e"
	fieldSep  = "\x00"
)

// MaxRecord and MaxSnapshot bound the commits a reader reads: from a device's log or presence, and
// from the names registry. Git stores a commit compressed, so a fetch that is quick can still bring
// a message of any size, and reading one whole, as git hands it over, can run a reader out of
// memory: a 64 MB message packed to 64 KB, and reading it took 448 MB (2026-11-09). A larger commit
// isn't read, and is passed over as junk is. Neither limit comes near what Murmur writes. An event's
// body is held to MaxBody, which JSON escapes to six times that at most, and a presence lists one
// device's sessions. A registry snapshot lists every name its writer has seen, about 45 bytes each,
// so MaxSnapshot holds some 700,000 of the 1.25 million names there are.
const (
	MaxRecord   = 1 << 20
	MaxSnapshot = 32 << 20
)

// LogPage is how many commits Log hands over at a time, unless Repo.Page says otherwise. A page
// also ends once it holds pageBytes, or after one commit that alone is more.
const (
	LogPage   = 500
	pageBytes = 8 << 20
)

// ErrTooLarge is a commit over the limit it was read with.
var ErrTooLarge = errors.New("commit too large to read")

// Log reads the commits after from (exclusive; "" for the start) up to to, oldest first, and calls
// page with them a page at a time, so a caller can let go of what it holds between pages and note
// how far it got. A commit over max bytes isn't read: it comes with Oversize set and no message, so
// a caller passes over it as junk and its cursor moves past it. An error from page stops the read
// and is returned.
//
// Only first parents are followed. Murmur writes every log commit with one parent, so a log is a
// chain, and a cursor at the last commit of a page then stands for everything read before it. A
// merge, which only someone writing by hand could put in a log, would break that: with a page
// ending on one side of it, a read resumed from there would list the other side again, and its
// messages would be delivered twice. Followed by first parents, a merge's other side isn't read.
func (r *Repo) Log(ctx context.Context, from, to string, max int64, page func([]Commit) error) error {
	args := []string{"rev-list", "--reverse", "--first-parent", to}
	if from != "" {
		args = append(args, "^"+from)
	}
	out, err := r.Git.Run(ctx, "", append(args, "--")...)
	if err != nil {
		return err
	}
	n := r.Page
	if n <= 0 {
		n = LogPage
	}
	return r.readPaged(ctx, strings.Fields(out), n, max, page)
}

// readPaged reads the commits named, in order, and calls page with them a page at a time, as Log
// says: n commits to a page at most, and no more than pageBytes. Git is asked the sizes of n
// commits at a time, and then for each page.
func (r *Repo) readPaged(ctx context.Context, shas []string, n int, max int64, page func([]Commit) error) error {
	for len(shas) > 0 {
		batch := shas[:min(n, len(shas))]
		sizes, err := r.sizes(ctx, batch)
		if err != nil {
			return err
		}
		for len(batch) > 0 {
			end, bytes := 0, int64(0)
			for ; end < len(batch); end++ {
				if s := sizes[batch[end]]; s <= max {
					if end > 0 && bytes+s > pageBytes {
						break
					}
					bytes += s
				}
			}
			commits, err := r.readCommits(ctx, batch[:end], sizes, max)
			if err != nil {
				return err
			}
			if err := page(commits); err != nil {
				return err
			}
			shas, batch = shas[end:], batch[end:]
		}
	}
	return nil
}

// errEnough ends Recent's read when its caller has what it wanted.
var errEnough = errors.New("enough")

// Recent reads the newest commits of the logs that end at tips, newest first by when each was
// written, whichever log it is in, and calls page with them a page at a time: until page returns
// false, limit commits have been read, or the logs run out. Each commit says which log it is in
// (Commit.Tip). It is for looking back over what the logs hold, and Log is for reading them on: the
// order across logs goes by their writers' clocks, so a device whose clock runs ahead comes first.
// A tip that isn't a commit here is passed over, and one that isn't here at all is returned as
// missing, so a caller can come back when it has been fetched. An oversize commit comes marked, and
// only first parents are followed, as in Log.
//
// It takes four git commands however many commits it reads, where Log takes two for every LogPage
// of them: a page here is all the commits that fit in pageBytes, about 10,000 of Murmur's, as
// nothing waits on a cursor between pages. Repo.Page, if set, still bounds a page.
func (r *Repo) Recent(ctx context.Context, tips []string, limit int, max int64, page func([]Commit) bool) (missing []string, err error) {
	if tips, missing, err = r.commitsAmong(ctx, tips); err != nil {
		return nil, err
	}
	if len(tips) == 0 || limit <= 0 {
		return missing, nil
	}
	// One walk over every log: git hands the commits over newest first, each log's from its tip
	// back, and with each its first parent, which says whose log it is in.
	out, err := r.Git.Run(ctx, strings.Join(tips, "\n")+"\n", "rev-list", "--first-parent", "--parents",
		"-n", strconv.Itoa(limit), "--stdin")
	if err != nil {
		return missing, err
	}
	var shas []string
	parent := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		shas = append(shas, f[0])
		parent[f[0]] = ""
		if len(f) > 1 {
			parent[f[0]] = f[1]
		}
	}
	tipOf := make(map[string]string, len(shas))
	for _, tip := range tips {
		for c := tip; ; c = parent[c] {
			if _, listed := parent[c]; !listed || tipOf[c] != "" {
				break // past what was listed, or into a log that another tip's shares
			}
			tipOf[c] = tip
		}
	}
	n := r.Page
	if n <= 0 {
		n = len(shas)
	}
	err = r.readPaged(ctx, shas, n, max, func(commits []Commit) error {
		for i := range commits {
			commits[i].Tip = tipOf[commits[i].SHA]
		}
		if !page(commits) {
			return errEnough
		}
		return nil
	})
	if errors.Is(err, errEnough) {
		err = nil
	}
	return missing, err
}

// commitsAmong is the object names in shas that are commits here, each once, in the order given,
// and those that name nothing here. Anything that isn't a whole object name is left out of both:
// these go to git on its standard input, where a line could otherwise be read as an option.
func (r *Repo) commitsAmong(ctx context.Context, shas []string) (commits, missing []string, err error) {
	var names []string
	seen := map[string]bool{}
	for _, sha := range shas {
		if !seen[sha] && gitx.IsObjectName(sha) {
			seen[sha] = true
			names = append(names, sha)
		}
	}
	if len(names) == 0 {
		return nil, nil, nil
	}
	kind := map[string]string{}
	if objs, ok, err := r.ask(ctx, names, false, 0); err != nil {
		return nil, nil, err
	} else if ok {
		for _, o := range objs {
			if kind[o.Name] = o.Type; o.Missing {
				kind[o.Name] = "missing"
			}
		}
	} else {
		out, err := r.Git.Run(ctx, strings.Join(names, "\n")+"\n", "cat-file", "--batch-check=%(objectname) %(objecttype)")
		if err != nil {
			return nil, nil, err
		}
		for _, line := range strings.Split(out, "\n") {
			if f := strings.Fields(line); len(f) == 2 {
				kind[f[0]] = f[1] // for an object that isn't here, git prints its name and "missing"
			}
		}
	}
	for _, sha := range names {
		switch kind[sha] {
		case "commit":
			commits = append(commits, sha)
		case "missing":
			missing = append(missing, sha)
		}
	}
	return commits, missing, nil
}

// sizes asks git how large each commit is, without reading it.
func (r *Repo) sizes(ctx context.Context, shas []string) (map[string]int64, error) {
	if objs, ok, err := r.ask(ctx, shas, false, 0); err != nil {
		return nil, err
	} else if ok {
		sizes := make(map[string]int64, len(objs))
		for _, o := range objs {
			if o.Type != "commit" {
				return nil, fmt.Errorf("not a commit here: %s", o.Name)
			}
			sizes[o.Name] = o.Size
		}
		return sizes, nil
	}
	out, err := r.Git.Run(ctx, strings.Join(shas, "\n")+"\n", "cat-file",
		"--batch-check=%(objectname) %(objecttype) %(objectsize)")
	if err != nil {
		return nil, err
	}
	sizes := make(map[string]int64, len(shas))
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || f[1] != "commit" {
			return nil, fmt.Errorf("not a commit here: %q", line)
		}
		size, err := strconv.ParseInt(f[2], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("reading a commit's size: %q", line)
		}
		sizes[f[0]] = size
	}
	for _, sha := range shas {
		if _, ok := sizes[sha]; !ok {
			return nil, fmt.Errorf("no size for commit %s", sha)
		}
	}
	return sizes, nil
}

// readCommits reads the commits named, in order, but for those over max, which come back marked.
func (r *Repo) readCommits(ctx context.Context, shas []string, sizes map[string]int64, max int64) ([]Commit, error) {
	var small []string
	for _, sha := range shas {
		if sizes[sha] <= max {
			small = append(small, sha)
		}
	}
	read := map[string]Commit{}
	var byLog []string // those git log is asked for
	objs, kept, err := r.ask(ctx, small, true, max)
	if err != nil {
		return nil, err
	}
	for _, o := range objs {
		if o.Type != "commit" {
			return nil, fmt.Errorf("unreadable commit %s", o.Name)
		}
		at, message, ok := parseCommit(o.Data)
		if !ok {
			byLog = append(byLog, o.Name)
			continue
		}
		read[o.Name] = Commit{SHA: o.Name, Time: at, Message: message}
	}
	if !kept {
		byLog = small
	}
	if len(byLog) > 0 {
		out, err := r.Git.Run(ctx, strings.Join(byLog, "\n")+"\n", "log", "--stdin", "--no-walk=unsorted",
			"--no-show-signature", "--no-color", "--encoding=UTF-8", "--format=%H%x00%ct%x00%B%x1e", "--")
		if err != nil {
			return nil, err
		}
		for _, rec := range strings.Split(out, recordSep) {
			rec = strings.TrimLeft(rec, "\r\n")
			parts := strings.SplitN(rec, fieldSep, 3)
			if len(parts) != 3 {
				continue
			}
			secs, _ := strconv.ParseInt(parts[1], 10, 64)
			read[parts[0]] = Commit{SHA: parts[0], Time: time.Unix(secs, 0), Message: parts[2]}
		}
	}
	commits := make([]Commit, 0, len(shas))
	for _, sha := range shas {
		c, ok := read[sha]
		switch {
		case sizes[sha] > max:
			c = Commit{SHA: sha, Oversize: true}
		case !ok:
			return nil, fmt.Errorf("unreadable commit %s", sha)
		}
		c.Size = sizes[sha]
		commits = append(commits, c)
	}
	return commits, nil
}

// parseCommit takes from a commit object, as git stores it, what `git log` prints as %ct and %B:
// when it was committed by its writer's clock (the Unix epoch if it doesn't say), and its message,
// which is everything after the first empty line.
//
// Two kinds of commit git log reads differently, and for them ok is false, so that the caller asks
// git log: one that names an encoding, which log converts its message from (a record written by
// hand in Latin-1 still decodes, to other text, if it isn't converted), and one with a NUL in it,
// which log cuts the message at, as readCommits does at the byte it parts log's records with.
// Murmur writes none of these.
//
// git log is asked for UTF-8 (--encoding), which is what it gives unless the user's
// i18n.logOutputEncoding says otherwise: with that set to Latin-1, a message read by log came
// converted to it, and one read here didn't.
func parseCommit(raw []byte) (at time.Time, message string, ok bool) {
	if bytes.IndexByte(raw, 0) >= 0 || bytes.Contains(raw, []byte(recordSep)) {
		return time.Time{}, "", false
	}
	head, body, _ := strings.Cut(string(raw), "\n\n")
	var secs int64
	for _, line := range strings.Split(head, "\n") {
		if strings.HasPrefix(line, "encoding ") {
			return time.Time{}, "", false
		}
		if who, found := strings.CutPrefix(line, "committer "); found && secs == 0 {
			// "Name <email> 1794789106 -0800", and a name can hold anything but '<', '>' and a newline.
			if f := strings.Fields(who[strings.LastIndex(who, ">")+1:]); len(f) > 0 {
				secs, _ = strconv.ParseInt(f[0], 10, 64)
			}
		}
	}
	return time.Unix(secs, 0), body, true
}

// Roots returns the commits with no parent in sha's history that aren't in not's ("" for none).
// Murmur writes no merges, so a history it wrote has one root, and what it adds to one it read brings
// none; a merge pushed by hand can bring in others, by either parent.
func (r *Repo) Roots(ctx context.Context, sha, not string) ([]string, error) {
	args := []string{"rev-list", "--max-parents=0", sha}
	if not != "" {
		args = append(args, "^"+not)
	}
	out, err := r.Git.Run(ctx, "", append(args, "--")...)
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// ReadCommit returns one commit, or ErrTooLarge, without reading it, if it is over max bytes.
func (r *Repo) ReadCommit(ctx context.Context, sha string, max int64) (Commit, error) {
	sizes, err := r.sizes(ctx, []string{sha})
	if err != nil {
		return Commit{}, err
	}
	if size := sizes[sha]; size > max {
		return Commit{}, fmt.Errorf("%w: %s is %d bytes, over %d", ErrTooLarge, sha, size, max)
	}
	commits, err := r.readCommits(ctx, []string{sha}, sizes, max)
	if err != nil {
		return Commit{}, err
	}
	return commits[0], nil
}

// RemoteFor names the remote a clone's bus lives on: the one `git config murmur.remote` names,
// else origin. A clone without that remote is an error. `mur init` pushes its commit to the same
// remote, so a repo's section is where its bus is.
func RemoteFor(ctx context.Context, g gitx.Git) (string, error) {
	r := &Repo{Git: g}
	name := r.Config(ctx, brand.ConfigRemote)
	if name == "" {
		name = "origin"
	}
	if r.Config(ctx, "remote."+name+".url") == "" {
		return name, fmt.Errorf("this clone has no remote %q; add one, or name the bus's remote with `git config %s <name>`",
			name, brand.ConfigRemote)
	}
	return name, nil
}

// Config returns a git config value, or "" if it isn't set.
func (r *Repo) Config(ctx context.Context, key string) string {
	out, err := r.Git.Run(ctx, "", "config", "--get", key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}
