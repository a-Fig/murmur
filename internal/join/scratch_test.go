package join

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/clones"
)

// scratchSandbox is a sandbox whose temporary folder is <root>/tmp, so that <root>/real stands for
// the user's own folders outside it, which a test can't otherwise have: its folders are all in the
// temporary folder. The home is <root>/tmp/home.
func scratchSandbox(t *testing.T) *sandbox {
	sb := newSandbox(t, false)
	withAgents(sb)
	tmp := filepath.Join(sb.root, "tmp")
	mustMakeDir(t, tmp)
	was := tempRoots
	tempRoots = func() []string { return []string{tmp, resolved(tmp)} } // macOS's /var is /private/var
	t.Cleanup(func() { tempRoots = was })
	sb.Home = filepath.Join(tmp, "home")
	in := func(p string) string { return filepath.Join(sb.Home, filepath.Base(p)) }
	sb.ClaudeDir, sb.ClaudeJSON = in(sb.ClaudeDir), filepath.Join(in(sb.ClaudeDir), ".claude.json")
	sb.CodexHome, sb.OpenCodeDir, sb.MuseDir = in(sb.CodexHome), in(sb.OpenCodeDir), in(sb.MuseDir)
	sb.PiDir, sb.GrokHome, sb.GeminiHome = in(sb.PiDir), in(sb.GrokHome), in(sb.GeminiHome)
	return sb
}

func dirsOf(hs []agentHome) (out []string) {
	for _, h := range hs {
		out = append(out, h.Dir)
	}
	return out
}

// #995: a join in a home in the temporary folder, as a test's, followed an inherited
// CLAUDE_CONFIG_DIR and T3's settings to the user's own Claude Code and Antigravity, and pointed
// them at a test build. Such a home's join leaves everything outside the temporary folder alone,
// T3's settings included, and names the variables that led there.
func TestAScratchHomesJoinLeavesTheRealConfigurationsAlone(t *testing.T) {
	sb := scratchSandbox(t)
	real := filepath.Join(sb.root, "real")
	sb.ClaudeDir, sb.ClaudeJSON = filepath.Join(real, ".claude_personal"), filepath.Join(real, ".claude_personal", ".claude.json")
	sb.T3Settings = filepath.Join(real, ".t3", "userdata", "settings.json")
	write(t, sb.T3Settings, t3Settings(real))
	mustMakeDir(t, antigravityHomeOf(sb.T3Settings, "antigravity"))
	sb.Getenv = envWith(map[string]string{"CLAUDE_CONFIG_DIR": sb.ClaudeDir, "T3CODE_HOME": filepath.Join(real, ".t3"), "HOME": sb.Home}).Getenv

	byAgent, notes := sb.homes()
	for i, a := range agents {
		for _, h := range byAgent[i] {
			if h.Dir != "" && !strings.HasPrefix(h.Dir, sb.Home) {
				t.Errorf("%s home %s is outside the scratch home", a.label, h.Dir)
			}
		}
	}
	if got := dirsOf(byAgent[0]); len(got) != 1 || got[0] != "" {
		t.Errorf("Claude Code homes %q, want the default's place kept, empty", got)
	}
	if got := dirsOf(byAgent[agentIndex("codex")]); len(got) != 1 || got[0] != sb.CodexHome {
		t.Errorf("Codex homes %q, want the scratch one alone", got)
	}
	note := strings.Join(notes, "\n")
	for _, want := range []string{sb.Home, "Claude Code in " + sb.ClaudeDir + " (from CLAUDE_CONFIG_DIR)",
		sb.T3Settings + " (from T3CODE_HOME)", "`mur join --outside-home`"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note doesn't say %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, "personal claude") {
		t.Errorf("the note lists T3's instances, which it shouldn't have read:\n%s", note)
	}
	if strings.Contains(sb.removal(), real) {
		t.Errorf("leave's advice names a configuration it left alone: %s", sb.removal())
	}

	// --outside-home writes there all the same.
	sb.OutsideHome = true
	byAgent, notes = sb.homes()
	if got := dirsOf(byAgent[0]); !slices.Contains(got, sb.ClaudeDir) || !slices.Contains(got, filepath.Join(real, ".claude_personal")) {
		t.Errorf("with --outside-home, Claude Code homes %q", got)
	}
	if strings.Contains(strings.Join(notes, "\n"), "--outside-home") {
		t.Errorf("with --outside-home, notes %q", notes)
	}
}

// A scratch home's T3 settings, in the temporary folder, are read, and an instance whose home they
// put outside it is left alone and said to be from them. Uninstall names its own command.
func TestAScratchHomesUninstallLeavesAT3InstanceOutsideAlone(t *testing.T) {
	sb := scratchSandbox(t)
	sb.T3Settings = filepath.Join(sb.Home, ".t3", "userdata", "settings.json")
	write(t, sb.T3Settings, t3Settings(filepath.Join(sb.root, "real")))
	byAgent, notes := sb.homesWith(true)
	real := filepath.Join(sb.root, "real")
	if got := dirsOf(byAgent[0]); !slices.Contains(got, sb.ClaudeDir) || slices.ContainsFunc(got, func(d string) bool { return strings.HasPrefix(d, real) }) {
		t.Errorf("Claude Code homes %q", got)
	}
	note := strings.Join(notes, "\n")
	for _, want := range []string{"(from T3 Code's settings, in " + sb.T3Settings + ")", "personal claude", "`mur uninstall --outside-home`"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note doesn't say %q:\n%s", want, note)
		}
	}
}

// A user's own home is never a scratch one, whatever their variables point at: a CLAUDE_CONFIG_DIR
// elsewhere, T3's settings, a temporary folder that is the home itself or holds it.
func TestARealHomeWritesWhereItsVariablesSay(t *testing.T) {
	sb := scratchSandbox(t)
	sb.Home = filepath.Join(sb.root, "real", "home")
	other := filepath.Join(sb.root, "real", "D", "claude")
	sb.ClaudeDir, sb.ClaudeJSON = other, filepath.Join(other, ".claude.json")
	sb.T3Settings = filepath.Join(sb.Home, ".t3", "userdata", "settings.json")
	write(t, sb.T3Settings, t3Settings(sb.Home))
	sb.Getenv = envWith(map[string]string{"CLAUDE_CONFIG_DIR": other, "HOME": sb.Home, "T3CODE_HOME": filepath.Join(sb.Home, ".t3")}).Getenv
	byAgent, notes := sb.homes()
	if got := dirsOf(byAgent[0]); !slices.Contains(got, other) || !slices.Contains(got, filepath.Join(sb.Home, ".claude_personal")) {
		t.Errorf("Claude Code homes %q", got)
	}
	if strings.Contains(strings.Join(notes, "\n"), "--outside-home") {
		t.Errorf("notes %q", notes)
	}
	mustMakeDir(t, sb.Home)
	tempRoots = func() []string { return []string{sb.Home, resolved(sb.Home)} } // TMPDIR=$HOME
	if got := sb.scratchHome(); got != "" {
		t.Errorf("a home that is the temporary folder was taken for a scratch one: %s", got)
	}
}

// On Windows the home is USERPROFILE, so a test that moved only HOME into the temporary folder
// would write in the real profile: its HOME makes the home a scratch one too.
func TestAScratchHOMEWithTheRealProfile(t *testing.T) {
	sb := scratchSandbox(t)
	profile := filepath.Join(sb.root, "real", "profile")
	sb.Home = profile
	sb.ClaudeDir, sb.ClaudeJSON = filepath.Join(profile, ".claude"), filepath.Join(profile, ".claude.json")
	sb.Getenv = envWith(map[string]string{"HOME": filepath.Join(sb.root, "tmp", "home"), "USERPROFILE": profile}).Getenv
	byAgent, notes := sb.homes()
	if got := dirsOf(byAgent[0]); len(got) != 1 || got[0] != "" {
		t.Errorf("Claude Code homes %q", got)
	}
	if note := strings.Join(notes, "\n"); !strings.Contains(note, "Claude Code in "+sb.ClaudeDir+" (from USERPROFILE)") {
		t.Errorf("notes:\n%s", note)
	}
}

// The whole join, with Claude Code's command installed: the home left alone isn't handed to it
// (an empty home would be a problem), the scratch ones are written, and the report says why.
func TestAScratchHomesJoinRuns(t *testing.T) {
	sb := scratchSandbox(t)
	sb.LookPath = newSandbox(t, true).LookPath
	real := filepath.Join(sb.root, "real", ".claude_personal")
	sb.ClaudeDir, sb.ClaudeJSON = real, filepath.Join(real, ".claude.json")
	mustMakeDir(t, real)
	mustMakeDir(t, sb.CodexHome)
	sb.Getenv = envWith(map[string]string{"CLAUDE_CONFIG_DIR": real}).Getenv
	t.Setenv(clones.DirEnv, filepath.Join(sb.root, "clones"))
	p := startJoinTestDaemon(t)
	r, err := Join(t.Context(), sb.Env, p.Top)
	if err != nil {
		t.Fatalf("join: %v\n%s", err, r)
	}
	if exists(sb.ClaudeJSON) || exists(filepath.Join(real, "settings.json")) {
		t.Error("join wrote in the Claude Code home outside the temporary folder")
	}
	if !strings.Contains(read(t, filepath.Join(sb.CodexHome, "config.toml")), "[mcp_servers.murmur]") {
		t.Error("join didn't register in the scratch Codex home")
	}
	if !strings.Contains(r.String(), "(from CLAUDE_CONFIG_DIR)") {
		t.Errorf("report:\n%s", r)
	}
}

// A link in a scratch home to the user's own folders leads outside it: T3's settings through it
// aren't read, and a configuration through it, one not made yet included, is left alone.
func TestAScratchHomesLinkOutIsOutside(t *testing.T) {
	sb := scratchSandbox(t)
	real := filepath.Join(sb.root, "real")
	write(t, filepath.Join(real, ".t3", "userdata", "settings.json"), t3Settings(real))
	mustMakeDir(t, sb.Home)
	linkOrSkip(t, filepath.Join(real, ".t3"), filepath.Join(sb.Home, ".t3"))
	linkOrSkip(t, real, filepath.Join(sb.Home, "real"))
	sb.T3Settings = filepath.Join(sb.Home, ".t3", "userdata", "settings.json")
	sb.CodexHome = filepath.Join(sb.Home, "real", "codex-not-made-yet")
	byAgent, notes := sb.homes()
	if got := dirsOf(byAgent[agentIndex("codex")]); len(got) != 1 || got[0] != "" {
		t.Errorf("Codex homes %q, want the one through the link left alone", got)
	}
	if note := strings.Join(notes, "\n"); !strings.Contains(note, "listed in "+sb.T3Settings) || strings.Contains(note, "personal claude") {
		t.Errorf("notes:\n%s", note)
	}
}
