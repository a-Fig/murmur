package update

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/a-Fig/murmur/internal/gitx"
)

// A person's choice of how Murmur updates itself on this machine (#1045, Tyler, 2026-11-22).
const (
	ChoiceOff     = "off"     // murmur.update false: nothing installs on its own
	ChoiceOn      = "on"      // the default: stable releases
	ChoiceNightly = "nightly" // murmur.channel prerelease: prereleases and nightlies too
)

const (
	updateKey  = "murmur.update"
	channelKey = "murmur.channel"
	// skipKey holds a prerelease a person went back to stable from (#1045): the updater installs
	// no prerelease that isn't newer than it, and takes the key off once a newer version is in.
	skipKey  = "murmur.updateSkip"
	noUpdate = "MURMUR_NO_UPDATE"
)

// Setting is the machine's update choice and what applies in one clone.
type Setting struct {
	// Choice is the machine's, from the global git config: one binary serves every clone, so the
	// choice is made for all of them.
	Choice string
	// Effective is what this clone's updater does, once the repo's own git config, the system's
	// and MURMUR_NO_UPDATE have had their say.
	Effective string
	// Why says what overrides Choice, in words to show, when Effective differs from it.
	Why string
	// Skip is the prerelease this machine went back to stable from, if it is still held back.
	Skip string
	// Hold says, in words to show, what Skip does while updates are on.
	Hold string
	// CanGoBack says Back would do something: a prerelease is running, updates are on here, and
	// it isn't already going back from this one.
	CanGoBack bool
}

// ValidChoice says whether c is one of the three choices.
func ValidChoice(c string) bool {
	return c == ChoiceOff || c == ChoiceOn || c == ChoiceNightly
}

// configValue is one value git config holds for a key, with the scope it came from (global,
// local, system, worktree or command).
type configValue struct{ scope, value string }

// configValues is every value of key in the order git reads them, so the last one is the one
// that applies; none if it isn't set, or git can't read it.
func configValues(ctx context.Context, gitDir, key string) []configValue {
	vals, _ := configValuesErr(ctx, gitDir, key)
	return vals
}

// configValuesErr is configValues with git's options (a --type) and its error, nil when the key
// isn't set.
func configValuesErr(ctx context.Context, gitDir, key string, opts ...string) ([]configValue, error) {
	args := append(append([]string{"config", "--show-scope"}, opts...), "--get-all", key)
	out, err := (gitx.Git{Dir: gitDir}).Run(ctx, "", args...)
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return nil, nil // not set
		}
		return nil, err
	}
	var vals []configValue
	for line := range strings.Lines(out) {
		scope, value, ok := strings.Cut(strings.TrimRight(line, "\r\n"), "\t")
		if ok {
			vals = append(vals, configValue{scope, strings.TrimSpace(value)})
		}
	}
	return vals, nil
}

// updateValues is murmur.update's values as git reads a bool: "true", "false", or "" for one it
// can't read. Off has always taken an unreadable last value for updates on. git reads them all
// (it alone tells `update =`, false, from a bare `update`, true); only when one value fails it
// and so the lot, are they read one by one here.
func updateValues(ctx context.Context, gitDir string) []configValue {
	if vals, err := configValuesErr(ctx, gitDir, updateKey, "--type=bool"); err == nil {
		return vals
	}
	vals := configValues(ctx, gitDir, updateKey)
	for i := range vals {
		v := strings.ToLower(vals[i].value)
		n, nerr := strconv.ParseInt(v, 0, 64)
		switch {
		case v == "false" || v == "no" || v == "off" || nerr == nil && n == 0:
			vals[i].value = "false"
		case v == "" || v == "true" || v == "yes" || v == "on" || nerr == nil:
			vals[i].value = "true"
		default:
			vals[i].value = ""
		}
	}
	return vals
}

// last is the value that applies: the last one, or the last from scope if it isn't "".
func last(vals []configValue, scope string) configValue {
	for i := len(vals) - 1; i >= 0; i-- {
		if scope == "" || vals[i].scope == scope {
			return vals[i]
		}
	}
	return configValue{}
}

func envOff(getenv func(string) string) bool {
	v := strings.TrimSpace(getenv(noUpdate))
	return v != "" && v != "0" && !strings.EqualFold(v, "false")
}

func choiceOf(update, channel string) string {
	switch {
	case update == "false":
		return ChoiceOff
	case strings.EqualFold(channel, "prerelease"):
		return ChoiceNightly
	}
	return ChoiceOn
}

// Read says what this machine's update choice is, and what applies in the clone at gitDir to the
// running version. The updater reads the same keys on each check (Off, Prerelease, skipped), so
// what this says is what it does.
func Read(ctx context.Context, getenv func(string) string, gitDir, running string) Setting {
	upd := updateValues(ctx, gitDir)
	ch := configValues(ctx, gitDir, channelKey)
	s := Setting{
		Choice:    choiceOf(last(upd, "global").value, last(ch, "global").value),
		Effective: choiceOf(last(upd, "").value, last(ch, "").value),
	}
	env := envOff(getenv)
	if env {
		s.Effective = ChoiceOff
	}
	s.Skip = skipped(ctx, gitDir, running)
	if s.Effective != ChoiceOff {
		s.CanGoBack = prerelease(running) && !same(running, s.Skip)
		switch {
		case s.Skip == "":
		case same(running, s.Skip):
			s.Hold = "Going back to the latest stable release."
		case s.Effective == ChoiceNightly:
			on := "stable"
			if prerelease(running) {
				on = strings.TrimPrefix(running, "v")
			}
			s.Hold = "On " + on + " until the next nightly after " + s.Skip + "."
		}
	}
	if s.Effective == s.Choice {
		return s
	}
	label := map[string]string{ChoiceOff: "Off", ChoiceOn: "On", ChoiceNightly: "Nightly"}[s.Effective]
	switch {
	case env:
		s.Why = "Off here: " + noUpdate + " is set."
	case s.Effective == ChoiceOff || s.Choice == ChoiceOff:
		s.Why = label + " here: " + where(last(upd, "").scope) + " sets " + updateKey + "."
	default:
		s.Why = label + " here: " + where(last(ch, "").scope) + " sets " + channelKey + "."
	}
	return s
}

// where names a git config scope that isn't the global one.
func where(scope string) string {
	switch scope {
	case "local", "worktree":
		return "this repo's git config"
	case "system":
		return "the system's git config"
	case "command":
		return "git config from the environment"
	}
	return "git config"
}

// settingMu keeps this process's changes from interleaving: each is two git commands.
var settingMu sync.Mutex

// Set makes choice this machine's, in the global git config, and returns the setting as it now
// reads. It writes murmur.update and murmur.channel and no other key. Off keeps no channel, so a
// later On is stable. Leaving prereleases doesn't go back to an older stable release: the updater
// installs only a version newer than the one running, so a nightly stays until a stable passes it.
func Set(ctx context.Context, getenv func(string) string, gitDir, running, choice string) (Setting, error) {
	if !ValidChoice(choice) {
		return Setting{}, fmt.Errorf("updates are off, on or nightly, not %q", choice)
	}
	settingMu.Lock()
	defer settingMu.Unlock()
	g := gitx.Git{Dir: gitDir}
	unset := func(key string) error { return unsetGlobal(ctx, gitDir, key) }
	var err error
	switch choice {
	case ChoiceOff:
		if _, err = g.Run(ctx, "", "config", "--global", updateKey, "false"); err == nil {
			err = unset(channelKey)
		}
	case ChoiceOn:
		if err = unset(updateKey); err == nil {
			err = unset(channelKey)
		}
	case ChoiceNightly:
		if err = unset(updateKey); err == nil {
			_, err = g.Run(ctx, "", "config", "--global", channelKey, "prerelease")
		}
	}
	if err != nil {
		return Setting{}, err
	}
	s := Read(ctx, getenv, gitDir, running)
	if s.Choice != choice {
		// git writes one global file, and another (an include, ~/.config/git/config) still decides.
		return s, fmt.Errorf("another global git config file still sets %s or %s", updateKey, channelKey)
	}
	return s, nil
}

func unsetGlobal(ctx context.Context, gitDir, key string) error {
	_, err := (gitx.Git{Dir: gitDir}).Run(ctx, "", "config", "--global", "--unset-all", key)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 5 {
		return nil // it wasn't set
	}
	return err
}

// Back goes back to stable from the prerelease running (#1045, Tyler, 2026-11-22): the updater's
// next check installs the latest stable release, older or not, through the same verified download
// and restart as any update. From then on it installs no prerelease that isn't newer than the one
// gone back from; stable releases still come, so the machine never stays below the latest one. It
// writes murmur.updateSkip, in the global git config.
func Back(ctx context.Context, getenv func(string) string, gitDir, running string) (Setting, error) {
	settingMu.Lock()
	defer settingMu.Unlock()
	s := Read(ctx, getenv, gitDir, running)
	if !s.CanGoBack {
		switch {
		case strings.HasSuffix(running, "-dev"):
			return s, errors.New("a development build doesn't update itself")
		case !prerelease(running):
			return s, errors.New("this is a stable release already")
		case s.Effective == ChoiceOff:
			return s, errors.New("updates are off here, so nothing would install")
		}
		return s, nil // already going back
	}
	if _, err := (gitx.Git{Dir: gitDir}).Run(ctx, "", "config", "--global", skipKey, strings.TrimPrefix(running, "v")); err != nil {
		return s, err
	}
	s = Read(ctx, getenv, gitDir, running)
	if !same(s.Skip, running) {
		return s, fmt.Errorf("another git config file still sets %s", skipKey)
	}
	return s, nil
}

// skipped is the release this machine went back to stable from, while it still holds updates back:
// "" once a newer one runs, or if the key holds no version.
func skipped(ctx context.Context, gitDir, running string) string {
	v := last(configValues(ctx, gitDir, skipKey), "").value
	if !validVersion(v) || newer(running, v) {
		return ""
	}
	return strings.TrimPrefix(v, "v")
}

func validVersion(v string) bool {
	_, _, ok := version(v)
	return ok
}

// prerelease says v is a release's version with a prerelease part: an -rc.N or a -nightly, not
// a development build's -dev, which never updates itself.
func prerelease(v string) bool {
	_, pre, ok := version(v)
	return ok && pre != "" && pre != "dev"
}

// same says a and b are one release's version.
func same(a, b string) bool {
	_, _, aok := version(a)
	_, _, bok := version(b)
	return aok && bok && !newer(a, b) && !newer(b, a)
}
