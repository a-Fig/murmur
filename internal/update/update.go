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
func Prerelease(ctx context.Context, gitDir string) bool {
	return strings.EqualFold(last(configValues(ctx, gitDir, channelKey), "").value, "prerelease")
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
	cleanup(o.Executable)
	reported, wasOff := false, false // the last report, so a change to Off shows before the check ends
	report := func(s Status) {
		reported, wasOff = true, s.Off
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
			// failed check waits quietly for the next, as with updates on. Updates just turned off
			// are reported first, so the page doesn't say "Updating" while the check runs.
			if reported && !wasOff {
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
			// next check installs it, and soon.
			other := errors.Is(err, errNotAsked)
			s := Status{Latest: latest, Off: off, Failed: err != nil && !other, Asked: now}
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

// errNotAsked says the release a person asked to install isn't the one the check found.
var errNotAsked = errors.New("another release is out")

// attempt installs the latest release if it is newer than this one. latest is that release's
// version, once the check has read it.
func attempt(ctx context.Context, o Options, original os.FileInfo) (done bool, latest string, err error) {
	return install(ctx, o, original, "")
}

// install is attempt, and if want isn't "", installs only that release: a person asked for it.
func install(ctx context.Context, o Options, original os.FileInfo, want string) (done bool, latest string, err error) {
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
	// Manual installers do not take our lock. Do not overwrite a reinstall that
	// completed while the release was downloading; restart onto it instead.
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
	if err = replace(o.Executable, data, o.OS, osFiles); err != nil {
		return false, latest, err
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

// clearSkip takes murmur.updateSkip off once version, the one running or just installed, has
// passed it, so a later prerelease that misbehaves starts from no hold. It reads the key again
// each time: a person may have gone back from another prerelease since the check began. It is the
// global key Back writes; a repo's own is left alone.
func clearSkip(ctx context.Context, o Options, version string) {
	v := last(configValues(ctx, o.GitDir, skipKey), "global").value
	if v == "" || validVersion(v) && !newer(version, v) {
		return
	}
	if err := unsetGlobal(ctx, o.GitDir, skipKey); err != nil {
		o.Log.Printf("self-update: couldn't take %s off: %v", skipKey, err)
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
// and backups no process runs any more. It takes the update lock first, so as not to take another
// updater's staged file, and does nothing if another holds it.
func cleanup(path string) {
	dir := filepath.Dir(path)
	entries, _ := os.ReadDir(dir)
	var stale []string
	for _, e := range entries {
		n := e.Name()
		backup, isBackup := strings.CutPrefix(n, filepath.Base(path)+".old-")
		// Backups from before #944 have no binary name in them.
		if !e.IsDir() && (staged(n, path) || isBackup && (staged(backup, path) || numbered(backup, ".mur-update-"))) {
			stale = append(stale, filepath.Join(dir, n))
		}
	}
	if len(stale) == 0 {
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
}

// stagedPrefix starts the name of the file an update of the binary at path stages beside it. A
// Windows backup is the binary's name, ".old-" and that file's name, which client.installedExecutable
// maps back to the binary.
func stagedPrefix(path string) string { return ".mur-update-" + filepath.Base(path) + "-" }

// staged says name is a file an update of the binary at path staged. The number os.CreateTemp
// puts after the prefix keeps mur's apart from mur-nightly's, whose prefix starts the same.
func staged(name, path string) bool { return numbered(name, stagedPrefix(path)) }

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
// not a prerelease held back by a return to stable (Back), else "".
func Newer(gitDir, version string) string {
	var l latest
	if b, err := os.ReadFile(latestPath(gitDir)); err == nil {
		json.Unmarshal(b, &l)
	}
	if l.Latest == "" || !newer(l.Latest, version) {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if skip := skipped(ctx, gitDir, version); skip == "" || !prerelease(l.Latest) || newer(l.Latest, skip) {
		return l.Latest
	}
	return ""
}
