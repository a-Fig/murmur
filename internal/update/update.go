// Package update installs checksum-verified GitHub releases, and keeps the latest release's version
// so `mur version` can say when there is a newer one. It talks only to GitHub. While updates are off
// it still checks, to learn which release is out, and installs nothing unless a person asks for one
// install (Options.Install); MURMUR_NO_UPDATE stops the checks too. It takes stable releases only,
// unless the prerelease channel is chosen.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/gitx"
	"github.com/a-Fig/murmur/internal/proc"
)

type Options struct {
	Version, GitDir string
	URL             string // the repo's releases API; overridden only by tests
	Executable      string // resolved running executable; tests use a disposable file
	// Running is what Identity said of the executable when the process started. A different file
	// at that path by the time Run starts is a newer install, not this process's binary to replace.
	// Left nil, Run takes the identity itself.
	Running  os.FileInfo
	OS, Arch string
	Client   *http.Client
	Getenv   func(string) string
	Log      *log.Logger
	Every    time.Duration
	// Check asks for a check soon, as when a person changes the update setting: at once, or
	// checkGap after the last one. Nil: only the hourly ones.
	Check <-chan struct{}
	// Install asks for one install of the release it names, as a person's "Update now" does: a
	// check like Check's that installs even while updates are off in git config. It installs only
	// that release, the one the person saw: if the check finds another, it fails and says which.
	// It doesn't turn updates on, and MURMUR_NO_UPDATE still refuses it. Nil: none is asked for.
	Install <-chan string
	// Report, if set, hears the outcome of every check, so the page for humans can say whether
	// this version is out of date and what will happen about it (#240).
	Report func(Status)
}

// Status is what the last check found.
type Status struct {
	// Latest is the newest release the check saw, without the v; "" if the check didn't get that
	// far, or a newer binary was already installed by hand.
	Latest string
	// Off says updates are turned off (MURMUR_NO_UPDATE, murmur.update=false), so nothing is
	// installed without the user. With murmur.update false the check still reads which release is
	// out, and Latest says; with MURMUR_NO_UPDATE it asks GitHub nothing.
	Off bool
	// Failed says the check or the install failed: a release newer than this one isn't coming
	// on its own, at least until the next check.
	Failed bool
	// Installed says a newer binary is in place and the daemon restarts onto it when it can.
	Installed bool
	// Asked says the check was one Install asked for, so its outcome is the answer to a person.
	Asked bool
	// Error is why a check Install asked for failed, in words to show to the person who asked.
	Error string
}

// After says release version a comes after b. Development builds ("-dev") never do.
func After(a, b string) bool { return newer(a, b) }

// Off says updates are off in the clone at gitDir: MURMUR_NO_UPDATE, or murmur.update false.
func Off(ctx context.Context, getenv func(string) string, gitDir string) bool {
	return envOff(getenv) || last(updateValues(ctx, gitDir), "").value == "false"
}

// Prerelease says whether git config chooses the prerelease channel (`murmur.channel prerelease`),
// which takes the newest published release, prerelease or stable. Any other value is the stable
// channel. It is a key of its own because daemons from before it read murmur.update as a bool, and
// they ignore this one, so they keep taking stable releases until one of those brings the channel.
//
// While a return to stable holds, the value is heldNightly, which v0.5.3 and older read as stable.
func Prerelease(ctx context.Context, gitDir string) bool {
	return nightly(last(configValues(ctx, gitDir, channelKey), "").value)
}

// checkGap is the least time between a check and one a changed setting asks for (Options.Check),
// so that clicking through the choices can't spend GitHub's hourly allowance for this address.
var checkGap = 10 * time.Second

// Run checks immediately and hourly. Network failures wait quietly until the next check.
// A successful install calls restart once; its caller waits for handoffs before stopping.
func Run(ctx context.Context, o Options, restart func()) {
	if strings.HasSuffix(o.Version, "-dev") {
		return
	}
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Log == nil {
		o.Log = log.New(io.Discard, "", 0)
	}
	if o.URL == "" {
		o.URL = "https://api.github.com/repos/a-Fig/murmur/releases"
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 2 * time.Minute}
	}
	if o.OS == "" {
		o.OS, o.Arch = runtime.GOOS, runtime.GOARCH
	}
	if o.Every == 0 {
		o.Every = time.Hour
	}
	var err error
	if o.Executable == "" {
		o.Executable, err = os.Executable()
		if err != nil {
			return
		}
	}
	o.Executable, err = filepath.EvalSymlinks(o.Executable)
	if err != nil {
		return
	}
	original := o.Running
	if original == nil {
		if original, err = Identity(o.Executable); err != nil {
			return
		}
	}
	wasOff := false // the last report's Off, so updates off show before the check ends
	report := func(s Status) {
		wasOff = s.Off
		if o.Report != nil {
			o.Report(s)
		}
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	var checked time.Time
	asked := "" // the release a person asked to install (Install), which the next check does
	for {
		select {
		case <-ctx.Done():
			return
		case <-o.Check:
			timer.Reset(max(0, time.Until(checked.Add(checkGap))))
			continue
		case asked = <-o.Install:
			timer.Reset(max(0, time.Until(checked.Add(checkGap))))
			continue
		case <-timer.C:
		}
		checked = time.Now()
		// At every check, not only as the daemon starts: a backup stays while a process still runs
		// it, an MCP server for days, and went only at the next daemon start after that process
		// ended, which an update's restart is the usual cause of (#1171).
		cleanup(o.Executable, o.OS)
		want, now := asked, asked != ""
		asked = ""
		if off := Off(ctx, o.Getenv, o.GitDir); off && envOff(o.Getenv) {
			// The environment's opt-out may mean the binary is managed by something else, so it
			// asks GitHub nothing, and refuses Update now.
			s := Status{Off: true, Asked: now}
			if now {
				s.Failed, s.Error = true, noUpdate+" is set for this daemon"
			}
			report(s)
		} else if off && !now {
			// Off only learns which release is out, so the page can offer Update now (#1099). A
			// failed check waits quietly for the next, as with updates on. Updates just turned off,
			// or off as the daemon starts, are reported first, so the page doesn't say "Updating"
			// while the check runs.
			if !wasOff {
				report(Status{Off: true})
			}
			latest, _ := look(ctx, o)
			if ctx.Err() != nil {
				return
			}
			report(Status{Latest: latest, Off: true})
		} else {
			done, latest, err := install(ctx, o, original, want)
			if done {
				report(Status{Latest: latest, Installed: true, Asked: now})
				restart()
				return
			}
			if ctx.Err() != nil {
				return
			}
			// Another release than the one asked for isn't a failed update: with updates on, the
			// next check installs it, and soon. Nor is updates turned off during the download.
			other := errors.Is(err, errNotAsked)
			turnedOff := errors.Is(err, errTurnedOff)
			off = off || turnedOff
			s := Status{Latest: latest, Off: off, Failed: err != nil && !other && !turnedOff, Asked: now}
			if err != nil && now {
				s.Error = err.Error()
			}
			report(s)
			if other && !off {
				timer.Reset(checkGap)
				continue
			}
			if errors.Is(err, os.ErrPermission) {
				o.Log.Printf("self-update disabled until restart: binary directory is not writable: %v", err)
				return
			}
			if errors.Is(err, errNoLink) {
				o.Log.Printf("self-update disabled until restart: %v", err)
				return
			}
		}
		timer.Reset(o.Every)
	}
}

func assetName(goos, arch string) string {
	if (goos != "linux" && goos != "darwin" && goos != "windows") || (arch != "amd64" && arch != "arm64") {
		return ""
	}
	name := "mur-" + goos + "-" + arch
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// get fetches url, and never leaves https on the way: not to an asset URL the https API names, and
// not through a redirect, whatever client it was given. The binary and SHA256SUMS are fetched
// separately, so anyone on the path of a download that left https could swap both.
func get(ctx context.Context, o Options, url string, limit int64) ([]byte, error) {
	if strings.HasPrefix(o.URL, "https://") && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("refused %s: not https", url)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "mur/"+o.Version)
	client := *o.Client
	client.CheckRedirect = keepHTTPS
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download: %s", res.Status)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if int64(len(b)) > limit {
		return nil, errors.New("download too large")
	}
	return b, err
}

// keepHTTPS follows a redirect unless it goes from https to anything else. It doesn't limit hosts:
// GitHub has moved where it serves release assets before (objects.githubusercontent.com, now
// release-assets.githubusercontent.com), and a daemon that refused the next host could never update
// to a version that allowed it.
func keepHTTPS(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
		return fmt.Errorf("refused a redirect from https to %s://%s", req.URL.Scheme, req.URL.Host)
	}
	return nil
}

// errNotAsked says the release a person asked to install isn't the one the check found, or the one
// a check picked isn't the one to install by the time it has downloaded.
var errNotAsked = errors.New("another release is out")

// errTurnedOff says updates were turned off while the release downloaded, so it wasn't installed.
var errTurnedOff = errors.New("updates were turned off")

// attempt installs the latest release if it is newer than this one. latest is that release's
// version, once the check has read it.
func attempt(ctx context.Context, o Options, original os.FileInfo) (done bool, latest string, err error) {
	return install(ctx, o, original, "")
}

// install is attempt, and if want isn't "", installs only that release: a person asked for it.
func install(ctx context.Context, o Options, original os.FileInfo, want string) (done bool, latest string, err error) {
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	// Serialize daemons from different clones sharing this install. Never replace a binary
	// another updater has already installed with a release selected by this older process.
	lock, err := proc.Lock(o.Executable + ".update.lock")
	if err != nil {
		return false, latest, err
	}
	defer lock.Close()
	current, err := Identity(o.Executable)
	if err != nil {
		return false, latest, err
	}
	if !os.SameFile(original, current) {
		return true, latest, nil
	}
	// Going back to stable from a prerelease (Back) takes the latest stable release, older or not.
	// After that, no prerelease installs that isn't newer than the one gone back from, on either
	// channel. Stable releases still do, so the machine never stays below the latest of them.
	skip := skipped(ctx, o.GitDir, o.Version)
	clearSkip(ctx, o, o.Version)
	if skip != "" && same(skip, o.Version) && wentBack(ctx, o.GitDir, skip, o.Executable) {
		// This install went back from this prerelease once, and it runs again: the release gone
		// back to took it again, so whatever that release reads chose it (#1197). v0.5.3 and older
		// know no hold, and take it whenever any git config file says murmur.channel prerelease,
		// a repo's own as `git config murmur.channel prerelease` writes it, or a person setting it
		// again. Going back again would loop, a download and a restart each time, so the hold
		// comes off and the prerelease stays.
		o.Log.Printf("self-update: back on %s after going back from it, so it was chosen again: %s comes off", o.Version, skipKey)
		takeHoldOff(ctx, o)
		skip = ""
	}
	rel, back, err := target(ctx, o, skip)
	if err != nil {
		return false, latest, err
	}
	if rel == nil {
		if want != "" {
			return false, "", fmt.Errorf("%s isn't a release to install now", want)
		}
		return false, "", nil
	}
	recordLatest(o.GitDir, rel.Tag)
	latest = strings.TrimPrefix(rel.Tag, "v")
	if want != "" && !same(want, rel.Tag) {
		return false, latest, fmt.Errorf("%w: %s is the release to install now, not %s", errNotAsked, latest, want)
	}
	if !back && !newer(rel.Tag, o.Version) {
		return false, latest, nil
	}
	name := assetName(o.OS, o.Arch)
	if name == "" {
		return false, latest, fmt.Errorf("no release binary for %s/%s", o.OS, o.Arch)
	}
	urls := map[string]string{}
	for _, a := range rel.Assets {
		urls[a.Name] = a.URL
	}
	if urls[name] == "" || urls["SHA256SUMS"] == "" {
		return false, latest, errors.New("missing release assets")
	}
	sums, err := get(ctx, o, urls["SHA256SUMS"], 1<<20)
	if err != nil {
		return false, latest, err
	}
	data, err := get(ctx, o, urls[name], 128<<20)
	if err != nil {
		return false, latest, err
	}
	if err = verify(data, sums, name); err != nil {
		return false, latest, err
	}
	if ctx.Err() != nil {
		return false, latest, ctx.Err()
	}
	// An install.ps1 from before #1198 takes no lock, and a binary swapped by hand takes none. Do
	// not overwrite a reinstall that completed while the release was downloading; restart onto it
	// instead.
	current, err = Identity(o.Executable)
	if err != nil {
		return false, latest, err
	}
	if !os.SameFile(original, current) {
		return true, latest, nil
	}
	// A person may have gone back to stable from this version while the release downloaded: that
	// choice wins over a release picked before it, and the next check, which Back asks for, goes back.
	if !back && same(skipped(ctx, o.GitDir, o.Version), o.Version) {
		return false, latest, nil
	}
	// Or turned updates off, or moved to stable releases, which the check read before the download
	// (#1197). Off doesn't stop an install a person asked for (want), which is for updates off; a
	// prerelease after a move to stable isn't installed even then, as the person asked for it
	// on the channel they have since left.
	if want == "" && Off(ctx, o.Getenv, o.GitDir) {
		return false, latest, errTurnedOff
	}
	if !back && prerelease(rel.Tag) && !Prerelease(ctx, o.GitDir) {
		return false, latest, fmt.Errorf("%w: updates moved to stable releases while %s downloaded", errNotAsked, latest)
	}
	if back {
		if err = holdChannel(ctx, o.GitDir); err != nil {
			return false, latest, err
		}
	}
	if err = replace(o.Executable, data, o.OS, osFiles); err != nil {
		return false, latest, err
	}
	if back {
		// Whatever happens to ctx now: the new binary is in place, and a daemon stopping just
		// then would otherwise leave no record, and cost a trip more.
		if _, err := (gitx.Git{Dir: o.GitDir}).Run(context.WithoutCancel(ctx), "", "config", "--global", "--add", backKey, skip+" "+o.Executable); err != nil {
			o.Log.Printf("self-update: couldn't record going back in %s: %v", backKey, err)
		}
	}
	clearSkip(ctx, o, latest)
	o.Log.Printf("self-update: %s -> %s", o.Version, latest)
	return true, latest, nil
}

// target is the release a check would install, nil for none, given skip, the release gone back to
// stable from (skipped). back says this check goes back to stable, so takes rel even if it is older.
func target(ctx context.Context, o Options, skip string) (rel *release, back bool, err error) {
	back = skip != "" && same(skip, o.Version)
	rel, err = pick(ctx, o, !back && Prerelease(ctx, o.GitDir))
	if err == nil && rel != nil && skip != "" && prerelease(rel.Tag) && !newer(rel.Tag, skip) {
		rel, err = pick(ctx, o, false) // held: the latest stable release, if it is newer
	}
	return rel, back, err
}

// look is a check with updates off (#1099): it learns the release a check with them on would
// install, records it for the page and `mur version`, and returns its version. It downloads no
// binary, takes no lock and writes no config; it can't install, by construction, since nothing it
// calls replaces the executable.
func look(ctx context.Context, o Options) (latest string, err error) {
	rel, _, err := target(ctx, o, skipped(ctx, o.GitDir, o.Version))
	if err != nil || rel == nil {
		return "", err
	}
	recordLatest(o.GitDir, rel.Tag)
	return strings.TrimPrefix(rel.Tag, "v"), nil
}

// wentBack says the install at exe has gone back to stable from skip before: backKey has it, as
// the updater records it once the release gone back to is in place.
func wentBack(ctx context.Context, gitDir, skip, exe string) bool {
	for _, v := range configValues(ctx, gitDir, backKey) {
		version, path, ok := strings.Cut(v.value, " ")
		if ok && same(version, skip) && path == exe {
			return true
		}
	}
	return false
}

// clearSkip takes murmur.updateSkip off once version, the one running or just installed, has
// passed it, so a later prerelease that misbehaves starts from no hold. It reads the key again
// each time: a person may have gone back from another prerelease since the check began. It is the
// global key Back writes; a repo's own is left alone.
func clearSkip(ctx context.Context, o Options, version string) {
	v := last(configValues(ctx, o.GitDir, skipKey), "global").value
	if v != "" && validVersion(v) && !newer(version, v) {
		return
	}
	takeHoldOff(ctx, o)
}

// takeHoldOff takes murmur.updateSkip off, with the record of going back from it (backKey), and
// makes a held channel "prerelease" again.
func takeHoldOff(ctx context.Context, o Options) {
	// The record first: a hold left without it is a hold Back wrote, while a record left without
	// the hold would take the next Back for one already gone back from (Opus review).
	for _, key := range []string{backKey, skipKey} {
		if last(configValues(ctx, o.GitDir, key), "global").value == "" {
			continue // as at almost every check: git is asked only to read
		}
		if err := unsetGlobal(ctx, o.GitDir, key); err != nil {
			o.Log.Printf("self-update: couldn't take %s off: %v", key, err)
			return
		}
	}
	// With the hold off, the channel Back held reads "prerelease" again, as `mur updates nightly`
	// writes it (#1197).
	if strings.EqualFold(last(configValues(ctx, o.GitDir, channelKey), "global").value, heldNightly) {
		if _, err := (gitx.Git{Dir: o.GitDir}).Run(ctx, "", "config", "--global", "--replace-all", channelKey, "prerelease"); err != nil {
			o.Log.Printf("self-update: couldn't set %s back to prerelease: %v", channelKey, err)
		}
	}
}

type release struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// pick returns the release to compare against the running version, or nil if there is none. The
// stable channel asks for GitHub's latest release, which is never a prerelease. The prerelease
// channel lists every release and takes the highest version, not the newest date, so a stable
// release published after a higher prerelease doesn't pull the channel back. It reads every page,
// since an old candidate can outrank the newest releases; old nightlies are pruned, so that is
// usually one request. A failed page, or more than ten, fails the check rather than choosing from
// part of the list.
func pick(ctx context.Context, o Options, prerelease bool) (*release, error) {
	if !prerelease {
		b, err := get(ctx, o, o.URL+"/latest", 1<<20)
		if err != nil {
			return nil, err
		}
		var r release
		if err = json.Unmarshal(b, &r); err != nil {
			return nil, err
		}
		if r.Draft || r.Prerelease {
			return nil, nil
		}
		return &r, nil
	}
	const perPage, maxPages = 100, 10
	var best *release
	for page := 1; ; page++ {
		if page > maxPages {
			return nil, errors.New("more releases than the prerelease channel reads")
		}
		b, err := get(ctx, o, fmt.Sprintf("%s?per_page=%d&page=%d", o.URL, perPage, page), 16<<20)
		if err != nil {
			return nil, err
		}
		var all []release
		if err = json.Unmarshal(b, &all); err != nil {
			return nil, err
		}
		for i := range all {
			r := &all[i]
			if r.Draft {
				continue
			}
			if _, _, ok := version(r.Tag); ok && (best == nil || newer(r.Tag, best.Tag)) {
				best = r
			}
		}
		if len(all) < perPage {
			return best, nil
		}
	}
}

func verify(data, sums []byte, name string) error {
	sum := sha256.Sum256(data)
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			if strings.EqualFold(fields[0], hex.EncodeToString(sum[:])) {
				return nil
			}
			return errors.New("release checksum mismatch")
		}
	}
	return errors.New("release checksum entry missing")
}

// files are the calls replace makes to rename and link, so tests can fail them or stop between them.
type files struct {
	rename, link func(oldname, newname string) error
}

var osFiles = files{os.Rename, os.Link}

// errNoLink says the binary's volume can't give it a second name, which Windows needs before it
// lets a new binary take its place, so updates there would always fail.
var errNoLink = errors.New("can't hard-link the running binary, so it can't be replaced safely")

// replace stages beside the destination, closes and syncs, then renames the staged file over it.
// That one rename is the whole swap, so a kill at any moment leaves the path naming a whole binary,
// the old release or the new one (#944); cleanup takes what a kill leaves beside it.
//
// Windows refuses that rename while the path is a running program's only name, but not while the
// program has a second one (tested on Windows 11, 2026-11-21). So replace first links the running
// binary to a backup name, unique because a still-running old MCP server may hold an earlier backup
// open. The backup goes once nothing runs it. Renaming the running binary aside instead, as the
// updater did before, left no binary at the path for a moment, and a kill in that moment left none
// at all. Where the volume has no links (FAT), replace refuses rather than open that gap again.
//
// The staged file's name carries the binary's, so cleanup takes only what this binary's updates
// left, under the lock they hold.
func replace(path string, data []byte, goos string, fs files) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), stagedPrefix(path)+"*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(data); err == nil {
		err = f.Chmod(st.Mode().Perm())
	}
	if err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	if goos != "windows" {
		return fs.rename(tmp, path)
	}
	old := path + ".old-" + filepath.Base(tmp)
	if err = fs.link(path, old); err != nil {
		// FAT answers ERROR_INVALID_FUNCTION (1), a share that can't link ERROR_NOT_SUPPORTED. Any
		// other failure, such as install.ps1 having just moved mur.exe aside, is worth a retry.
		var errno syscall.Errno
		if errors.Is(err, errors.ErrUnsupported) || runtime.GOOS == "windows" && errors.As(err, &errno) && errno == 1 {
			return fmt.Errorf("%w: %w", errNoLink, err)
		}
		return err
	}
	err = fs.rename(tmp, path)
	os.Remove(old) // fails harmlessly while the old exe is running; cleanup takes it later
	return err
}

// cleanup removes what an update left beside the binary at path: a staged file a kill left behind,
// and backups no process runs any more, the updater's and, on Windows (goos), install.ps1's.
// Windows won't remove one a process still runs, so those stay for a later call. It takes the
// update lock first, so as not to take another updater's staged file, and does nothing if another
// holds it.
//
// The lock covers the updater's own backups: one is made and done with under it, so any found here
// is left over. install.ps1 takes the same lock for its swap since #1198, so this check never sees
// a swap half done, but an install killed mid-swap leaves what a swap leaves, and an installer
// older than that takes no lock. Its backup is its hard link to mur.exe from the
// moment it makes one until it renames the new file over mur.exe. Taken in between, with nothing
// running mur.exe to make Windows refuse, the link's loss sends the installer to its two-step
// swap, which a held file can leave with no mur.exe at all (Sol's review of #1173). Where it can't
// link, it moves mur.exe aside under the backup's name, and for a moment that is the only copy.
// So an installer's backup that is still a second name of the binary at path stays, and so does
// every one while there is no binary at path: once a swap is done its backup is an older binary.
// One left as a link to the binary by an install that was stopped stays until the binary is next
// replaced.
func cleanup(path, goos string) {
	dir := filepath.Dir(path)
	entries, _ := os.ReadDir(dir)
	var stale, installers []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		backup, isBackup := strings.CutPrefix(n, filepath.Base(path)+".old-")
		switch {
		// Backups from before #944 have no binary name in them.
		case staged(n, path) || isBackup && (staged(backup, path) || numbered(backup, ".mur-update-")):
			stale = append(stale, filepath.Join(dir, n))
		// install.ps1's have eight hex digits (mur.exe.old-1a2b3c4d): it removes them itself, but
		// only when it next runs, and a machine that updates itself may never run it again
		// (#1171). Only Windows has an install.ps1: elsewhere a file of that name is somebody's
		// own, and removing a file there works even while it runs.
		case isBackup && goos == "windows" && installerBackup(backup):
			installers = append(installers, filepath.Join(dir, n))
		}
	}
	if len(stale)+len(installers) == 0 {
		return
	}
	lock, err := proc.Lock(path + ".update.lock")
	if err != nil {
		return
	}
	defer lock.Close()
	for _, p := range stale {
		os.Remove(p)
	}
	for _, p := range installers {
		// Asked just before each removal, so that as little as can be happens between, and by
		// opening each file (Identity): os.Stat on Windows keeps only the path, and SameFile
		// then opens it again later and says "not the same" of a path that has gone meanwhile,
		// which is the moment a backup is the only copy (Opus's review of #1173).
		current, err := Identity(path)
		if err != nil {
			return
		}
		if fi, err := Identity(p); err != nil || os.SameFile(current, fi) {
			continue
		}
		os.Remove(p)
	}
}

// stagedPrefix starts the name of the file an update of the binary at path stages beside it. A
// Windows backup is the binary's name, ".old-" and that file's name, which client.installedExecutable
// maps back to the binary.
func stagedPrefix(path string) string { return ".mur-update-" + filepath.Base(path) + "-" }

// staged says name is a file an update of the binary at path staged. The number os.CreateTemp
// puts after the prefix keeps mur's apart from mur-nightly's, whose prefix starts the same.
func staged(name, path string) bool { return numbered(name, stagedPrefix(path)) }

// installerBackup says suffix is what install.ps1 puts after ".old-" in a backup's name: the first
// eight digits of a GUID, in lower-case hex.
func installerBackup(suffix string) bool {
	return len(suffix) == 8 && strings.Trim(suffix, "0123456789abcdef") == ""
}

func numbered(name, prefix string) bool {
	n, ok := strings.CutPrefix(name, prefix)
	return ok && n != "" && strings.Trim(n, "0123456789") == ""
}

// File.Stat captures Windows file IDs now; os.Stat can defer loading the ID
// until SameFile, by which time the path may already name the replacement.
func Identity(path string) (os.FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Stat()
}

// latest is the latest release last seen on the clone's channel, kept in its state dir.
type latest struct {
	Latest string `json:"latest"` // its version, without the v
}

func latestPath(gitDir string) string { return filepath.Join(gitDir, brand.StateDir, "latest.json") }

func recordLatest(gitDir, tag string) {
	if gitDir == "" {
		return
	}
	if _, _, ok := version(tag); !ok {
		return
	}
	b, _ := json.Marshal(latest{Latest: strings.TrimPrefix(tag, "v")})
	path := latestPath(gitDir)
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, path)
	}
}

// Checked is when a daemon of the clone last learned from GitHub which release is the latest, zero
// if none has: the time latest.json was written. A check that failed, or found no release, doesn't
// write it.
func Checked(gitDir string) time.Time {
	fi, err := os.Stat(latestPath(gitDir))
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// Newer returns the latest release the clone's daemon last saw, if it is newer than version and
// not a prerelease held back by a return to stable (Back), else "". It reads a file, and for a
// prerelease asks git config what is held back.
func Newer(gitDir, version string) string {
	var l latest
	if b, err := os.ReadFile(latestPath(gitDir)); err == nil {
		json.Unmarshal(b, &l)
	}
	if l.Latest == "" || !newer(l.Latest, version) {
		return ""
	}
	// Only a prerelease can be held back, so only for one is git asked what is: `mur version` and
	// the page call this, and a stable release, the usual answer, costs them no process (#1164).
	if !prerelease(l.Latest) {
		return l.Latest
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if skip := skipped(ctx, gitDir, version); skip == "" || newer(l.Latest, skip) {
		return l.Latest
	}
	return ""
}
