package join

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/t3"
)

// A join run to test it, in a home made for the test in the temporary folder, still reads the
// variables of the shell it runs in. On 2026-11-21 one run by a review agent in T3 followed
// CLAUDE_CONFIG_DIR and T3's settings (found through T3CODE_HOME) out of its test home, and rewrote
// the user's own Claude Code and Antigravity configurations to run a test build that was deleted
// afterwards. Every new session then failed to join, with no error anywhere a person would look
// (#995). So while the home is inside the temporary folder, join and uninstall leave alone every
// configuration outside that folder, and say which variable pointed there. Nobody's real home is in
// the temporary folder, so a user never meets this; --outside-home lifts it for whoever does (a
// container whose HOME is under /tmp, say).

// scratchHome is the home this process was given when that is a folder inside the temporary
// folder, as a test's is, and "" otherwise or with OutsideHome. HOME and USERPROFILE count as well
// as Home: on Windows Home is USERPROFILE, so a test that set only HOME would still write in the
// real profile.
func (e Env) scratchHome() string {
	if e.OutsideHome {
		return ""
	}
	for _, h := range []string{e.Home, e.getenv("HOME"), e.getenv("USERPROFILE")} {
		if filepath.IsAbs(h) && inTemp(h, true) {
			return filepath.Clean(h)
		}
	}
	return ""
}

// tempRoots are the folders a test makes its home in: the system's temporary folder, and /tmp and
// /var/tmp, which mktemp and people use whatever TMPDIR says (macOS's is under /var/folders). Each
// is listed as written and with links resolved: macOS's /tmp is /private/tmp. A root that is a
// whole volume (TMPDIR=/) would hold every home, and isn't one. A variable, for the tests, whose
// own folders are all in the temporary folder.
var tempRoots = func() []string {
	var roots []string
	for _, r := range []string{os.TempDir(), "/tmp", "/var/tmp"} {
		if !filepath.IsAbs(r) { // "/tmp" on Windows
			continue
		}
		short, _ := filepath.EvalSymlinks(r) // which expands Windows' short names, as RUNNER~1
		for _, p := range []string{filepath.Clean(r), resolved(r), short} {
			if p != "" && filepath.Dir(p) != p {
				roots = append(roots, p)
			}
		}
	}
	return roots
}

// resolved is path with every link in it followed, as a write there would follow them: symbolic
// links, Windows junctions, which EvalSymlinks leaves as written (since Go 1.23), and links whose
// target isn't there yet. A part that doesn't exist is kept as named. "" past 40 links, a loop.
// Path components are walked before handling `..` so a target like `alias/../file` follows
// `alias` before stepping up, matching OS path resolution rather than lexical cleaning.
func resolved(path string) string { return resolveFrom("", path, 40) }

func resolveFrom(base, path string, hops int) string {
	path = filepath.FromSlash(path)
	sep := string(filepath.Separator)
	var cur, rest string
	if filepath.IsAbs(path) {
		vol := filepath.VolumeName(path)
		cur = vol
		rest = path[len(vol):]
		if strings.HasPrefix(rest, sep) {
			cur += sep
		}
	} else if strings.HasPrefix(path, sep) {
		// Drive-rooted path on Windows (e.g. `\profiles\codex`): preserve the leading separator
		// (and base's volume when resolving a link target).
		cur = filepath.VolumeName(base) + sep
		rest = path
	} else {
		cur = base
		rest = path
	}
	for _, part := range strings.Split(rest, sep) {
		switch part {
		case "", ".":
			continue
		case "..":
			if cur == "" || cur == "." || cur == ".." || strings.HasSuffix(cur, sep+"..") {
				cur = filepath.Join(cur, "..")
			} else {
				cur = filepath.Dir(cur)
			}
			continue
		}
		next := filepath.Join(cur, part)
		fi, err := os.Lstat(next)
		if err == nil && fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			if target, err := os.Readlink(next); err == nil {
				if hops == 0 {
					return ""
				}
				if next = resolveFrom(cur, target, hops-1); next == "" {
					return ""
				}
			}
		}
		cur = next
	}
	return filepath.Clean(cur)
}

// inTemp says path is in one of the temporary folders, or, unless strict, is one. A home (strict)
// counts as written or resolved, as either is how a test made it. A configuration counts by where
// its links lead: a link in a scratch home to the user's own ~/.t3 would otherwise be followed.
func inTemp(path string, strict bool) bool {
	paths := []string{filepath.Clean(path), resolved(path)}
	if !strict && paths[1] != "" {
		paths = paths[1:]
	}
	for _, p := range paths {
		for _, root := range tempRoots() {
			if p != "" && within(p, root) && !(strict && samePath(p, root)) {
				return true
			}
		}
	}
	return false
}

// within says path is root or inside it, as the file system compares names.
func within(path, root string) bool {
	for {
		if samePath(path, root) {
			return true
		}
		up := filepath.Dir(path)
		if up == path {
			return false
		}
		path = up
	}
}

// homeVar is the variable the user's home directory comes from: USERPROFILE where Home is it, as on
// Windows, else HOME.
func (e Env) homeVar() string {
	if e.Home != "" && samePath(e.getenv("USERPROFILE"), e.Home) {
		return "USERPROFILE"
	}
	return "HOME"
}

// t3Vars are the variables that led join to T3's settings: those of T3CODE_HOME, T3CODE_CLI_PATH and
// T3CODE_THREAD_ID that are set, else the home's (T3's default folder is ~/.t3).
func (e Env) t3Vars() []string {
	var out []string
	for _, v := range []string{t3HomeEnv, t3CLIEnv, t3.ThreadEnv} {
		if e.getenv(v) != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		out = []string{e.homeVar()}
	}
	return out
}

// homeFrom is the variable that put h, one of agent a's homes, where it is: the agent's own variable
// for its default home; the home's for one in the home, as the shared ~/.gemini and the defaults T3
// runs instances in are; else T3's settings, which named it.
func (e Env) homeFrom(a agent, h agentHome, readT3 bool) string {
	if a.driver == "cursor" && h.JSON != "" && !inTemp(h.JSON, false) {
		// Its cli-config.json, which these move (cursorConfigDir).
		for _, v := range []string{"CURSOR_CONFIG_DIR", "XDG_CONFIG_HOME"} {
			if strings.TrimSpace(e.getenv(v)) != "" {
				return v
			}
		}
	}
	if samePath(h.Dir, e.defaultHome(a).Dir) {
		v := a.t3Var
		if a.driver == "antigravity" {
			v = "GEMINI_HOME"
		}
		if v != "" && e.getenv(v) != "" {
			return v
		}
		if a.driver == "hermes" && runtime.GOOS == "windows" && e.getenv("LOCALAPPDATA") != "" {
			return "LOCALAPPDATA" // Hermes' default there (hermesHomeDir)
		}
		return e.homeVar()
	}
	if readT3 && (e.Home == "" || !within(h.Dir, e.Home)) {
		return "T3 Code's settings, in " + e.T3Settings
	}
	return e.homeVar()
}

// keepToScratch empties, in byAgent, each home outside the temporary folder while the home is a
// scratch one (scratchHome), keeping the others' places, and says which it left alone. readT3 says
// T3's settings were read. skippedT3 is set where they weren't, being outside it too. cmd is the
// command that would write them anyway.
func (e Env) keepToScratch(byAgent [][]agentHome, readT3, skippedT3 bool, cmd string) (note string) {
	scratch := e.scratchHome()
	if scratch == "" {
		return ""
	}
	var left []string
	if skippedT3 {
		left = append(left, "the agents T3 Code runs, listed in "+e.T3Settings+" (from "+strings.Join(e.t3Vars(), ", ")+")")
	}
	for i, a := range agents {
		for j, h := range byAgent[i] {
			if h.Dir == "" || inTemp(h.Dir, false) && (h.JSON == "" || inTemp(h.JSON, false)) && (a.driver != "kiro" || kiroPathsInTemp(h.Dir)) {
				continue
			}
			left = append(left, h.Label+" in "+h.Dir+" (from "+e.homeFrom(a, h, readT3)+")")
			byAgent[i][j] = agentHome{Label: h.Label}
		}
	}
	if len(left) == 0 {
		return ""
	}
	return "Left these alone, because your home, " + scratch + ", is in the temporary folder, as a test's is, and they are outside it: " +
		strings.Join(left, "; ") + ". To test in that home alone, unset the variables named here or point them into it. " +
		"To change these anyway, run `" + brand.Command + " " + cmd + " --outside-home`."
}
