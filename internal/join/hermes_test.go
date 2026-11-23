package join

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/brand"
)

// The edit adds the name to plugins.enabled in each form a person or Hermes writes, and keeps every
// other line; taking it out gives back what was there, but for an emptied block list.
func TestAddYAMLPluginKeepsTheFile(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"empty file", "", "plugins:\n  enabled:\n    - murmur\n"},
		{"no plugins", "# mine\nmodel:\n  default: x  # keep\n", "# mine\nmodel:\n  default: x  # keep\n\nplugins:\n  enabled:\n    - murmur\n"},
		{"block list", "plugins:\n  enabled:\n    - disk-cleanup  # yes\n  disabled: []\nmemory: {}\n",
			"plugins:\n  enabled:\n    - disk-cleanup  # yes\n    - murmur\n  disabled: []\nmemory: {}\n"},
		{"items at the key's indent", "plugins:\n  enabled:\n  - a\n  - 'b'\nx: 1\n", "plugins:\n  enabled:\n  - a\n  - 'b'\n  - murmur\nx: 1\n"},
		{"flow list", "plugins:\n  enabled: [a, \"b\"]  # two\n", "plugins:\n  enabled: [a, b, murmur] # two\n"},
		{"empty flow list", "plugins:\n  enabled: []\n", "plugins:\n  enabled: [murmur]\n"},
		{"null list", "plugins:\n  enabled:\n  disabled:\n    - x\n", "plugins:\n  enabled:\n    - murmur\n  disabled:\n    - x\n"},
		{"no enabled", "plugins:\n    disabled: [x]\n", "plugins:\n    enabled:\n      - murmur\n    disabled: [x]\n"},
		{"empty plugins", "plugins: {}  # none\nz: 2\n", "plugins: # none\n  enabled:\n    - murmur\nz: 2\n"},
		{"crlf", "plugins:\r\n  enabled:\r\n    - a\r\n", "plugins:\r\n  enabled:\r\n    - a\r\n    - murmur\r\n"},
		{"there already", "plugins:\n  enabled:\n    - murmur\n", "plugins:\n  enabled:\n    - murmur\n"},
		{"a nested enabled isn't it", "other:\n  enabled:\n    - a\n", "other:\n  enabled:\n    - a\n\nplugins:\n  enabled:\n    - murmur\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, changed, err := addYAMLPlugin(c.in, "murmur")
			if err != nil || got != c.want || changed != (c.in != c.want) {
				t.Fatalf("got %q changed=%v err=%v, want %q", got, changed, err, c.want)
			}
			on, err := yamlListHas(got, "enabled", "murmur")
			if err != nil || !on {
				t.Errorf("not read back: %v %v", on, err)
			}
			back, changed, err := removeYAMLPlugin(got, "murmur")
			if err != nil || !changed {
				t.Fatalf("remove: changed=%v err=%v", changed, err)
			}
			if on, _ := yamlListHas(back, "enabled", "murmur"); on {
				t.Errorf("still on after removal: %q", back)
			}
		})
	}
}

// Taking the name out leaves the rest of the list, and an emptied list as [].
func TestRemoveYAMLPlugin(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"plugins:\n  enabled:\n    - a\n    - murmur # x\n    - b\n", "plugins:\n  enabled:\n    - a\n    - b\n"},
		{"plugins:\n  enabled:  # on\n    - murmur\n", "plugins:\n  enabled: [] # on\n"},
		{"plugins:\n  enabled: [a, murmur]\n", "plugins:\n  enabled: [a]\n"},
		{"plugins:\n  enabled: [a]\n", "plugins:\n  enabled: [a]\n"},
		{"x: 1\n", "x: 1\n"},
	} {
		got, _, err := removeYAMLPlugin(c.in, "murmur")
		if err != nil || got != c.want {
			t.Errorf("%q: got %q err=%v, want %q", c.in, got, err, c.want)
		}
	}
}

// A form the edit can't be sure of is refused, never guessed at: above all, one where adding the
// key could give the mapping the key twice.
func TestAddYAMLPluginRefusesFormsItCantRead(t *testing.T) {
	for name, in := range map[string]string{
		"quoted key":          "\"plugins\":\n  enabled: []\n",
		"quoted child":        "plugins:\n  \"enabled\": []\n",
		"flow mapping":        "plugins: {enabled: [a]}\n",
		"flow mapping below":  "plugins:\n  {enabled: [a]}\n",
		"no space":            "plugins:\n  enabled:[a]\n",
		"multi-line flow":     "plugins:\n  enabled: [a,\n    b]\n",
		"anchor":              "plugins:\n  enabled: &on\n    - a\n",
		"alias":               "plugins:\n  enabled: *on\n",
		"mapping item":        "plugins:\n  enabled:\n    - name: a\n",
		"tab":                 "plugins:\n\tenabled: []\n",
		"second document":     "a: 1\n---\nplugins: {}\n",
		"scalar":              "plugins:\n  enabled: murmur\n",
		"plugins scalar":      "plugins: off\n",
		"whole file is JSON":  "{\"plugins\": {}}\n",
		"item with odd chars": "plugins:\n  enabled:\n    - \"a\\\"b\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			got, changed, err := addYAMLPlugin(in, "murmur")
			if !errors.Is(err, errYAMLForm) || changed || got != in {
				t.Errorf("got %q changed=%v err=%v", got, changed, err)
			}
		})
	}
}

// Hermes' home: HERMES_HOME, else %LOCALAPPDATA%\hermes on Windows and ~/.hermes elsewhere, with
// the suffix.
func TestHermesHome(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	home := t.TempDir()
	for _, c := range []struct {
		vars    map[string]string
		windows bool
		want    string
	}{
		{map[string]string{"HERMES_HOME": filepath.Join(home, "x")}, true, filepath.Join(home, "x")},
		{map[string]string{"HERMES_HOME": "~/h"}, false, filepath.Join(home, "h")},
		{map[string]string{"LOCALAPPDATA": filepath.Join("l", "a")}, true, filepath.Join("l", "a", "hermes")},
		{nil, true, filepath.Join(home, "AppData", "Local", "hermes")},
		{nil, false, filepath.Join(home, ".hermes")},
		{map[string]string{"HERMES_DATA_DIR_SUFFIX": "-dev"}, false, filepath.Join(home, ".hermes-dev")},
	} {
		if got := hermesHomeFor(env(c.vars), home, c.windows); got != c.want {
			t.Errorf("%v windows=%v: %q, want %q", c.vars, c.windows, got, c.want)
		}
	}
}

// hermesSandbox is a sandbox with Hermes set up: its home, with a config.yaml, and a profile.
func hermesSandbox(t *testing.T) *sandbox {
	t.Helper()
	sb := newSandbox(t, false)
	withAgents(sb)
	write(t, filepath.Join(sb.HermesHome, "config.yaml"), "# my settings\nmodel:\n  default: stub\nplugins:\n  enabled:\n    - disk-cleanup\n")
	write(t, filepath.Join(sb.HermesHome, "profiles", "work", "SOUL.md"), "work\n")
	// Not a profile: Hermes counts only a folder with one of its files.
	if err := os.MkdirAll(filepath.Join(sb.HermesHome, "profiles", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	return sb
}

// Join writes the plugin and turns it on in the home and in each profile, keeping the user's
// settings; a second join changes nothing; uninstall takes it all out again.
func TestHermesJoinAndUninstall(t *testing.T) {
	sb := hermesSandbox(t)
	byAgent, _ := sb.homes()
	hs := byAgent[agentIndex("hermes")]
	if len(hs) != 2 || hs[1].Dir != filepath.Join(sb.HermesHome, "profiles", "work") {
		t.Fatalf("homes %+v, want the home and the work profile", hs)
	}
	cfg := filepath.Join(sb.HermesHome, "config.yaml")
	before := read(t, cfg)
	for _, h := range hs {
		did, fresh, err := sb.registerHermesAt(h)
		if err != nil || !fresh || len(did) != 2 {
			t.Fatalf("%s: did=%q fresh=%v err=%v", h.Label, did, fresh, err)
		}
		init := read(t, filepath.Join(hermesPluginDir(h.Dir), "__init__.py"))
		if !strings.HasPrefix(init, hermesMarker) || !strings.Contains(init, `"tool", "--agent", "hermes"`) {
			t.Errorf("%s: the plugin is\n%s", h.Label, init)
		}
		if !strings.Contains(read(t, filepath.Join(hermesPluginDir(h.Dir), "plugin.yaml")), "name: "+brand.MCPServer) {
			t.Errorf("%s: plugin.yaml", h.Label)
		}
		if dir := skillsDir(agents[agentIndex("hermes")], h); dir != filepath.Join(h.Dir, "skills") {
			t.Errorf("%s: skills in %q", h.Label, dir)
		}
	}
	if got := read(t, cfg); got != "# my settings\nmodel:\n  default: stub\nplugins:\n  enabled:\n    - disk-cleanup\n    - murmur\n" {
		t.Errorf("config.yaml:\n%s", got)
	}
	if got := read(t, filepath.Join(hs[1].Dir, "config.yaml")); got != "plugins:\n  enabled:\n    - murmur\n" {
		t.Errorf("the profile's config.yaml:\n%s", got)
	}
	for _, h := range hs {
		if did, fresh, err := sb.registerHermesAt(h); err != nil || fresh || len(did) != 0 {
			t.Errorf("again: did=%q fresh=%v err=%v", did, fresh, err)
		}
	}
	if r := hermesRemoval(sb.Env, hs[0]); !strings.Contains(r, hermesPluginDir(hs[0].Dir)) || !strings.Contains(r, cfg) {
		t.Errorf("removal: %q", r)
	}
	// Python's cache, made as Hermes loads the plugin, goes with it.
	write(t, filepath.Join(hermesPluginDir(hs[0].Dir), "__pycache__", "__init__.cpython-314.pyc"), "x")
	for _, h := range hs {
		did, notes, err := sb.unregisterHermesAt(context.Background(), []agentHome{h})
		if err != nil || len(did) != 2 || len(notes) != 0 {
			t.Fatalf("%s: uninstall did=%q notes=%q err=%v", h.Label, did, notes, err)
		}
		if exists(hermesPluginDir(h.Dir)) {
			t.Errorf("%s: the plugin's folder is still there", h.Label)
		}
	}
	if got := read(t, cfg); got != before {
		t.Errorf("config.yaml after uninstall:\n%s\nwant\n%s", got, before)
	}
}

// A machine without Hermes' home gets nothing, though hermes may be on PATH: Hermes makes its home
// on its first run.
func TestHermesSkipsAMachineWithoutItsHome(t *testing.T) {
	sb := newSandbox(t, false)
	withAgents(sb)
	h := sb.defaultHome(agents[agentIndex("hermes")])
	if did, fresh, err := sb.registerHermesAt(h); err != nil || fresh || did != nil || exists(sb.HermesHome) {
		t.Errorf("did=%q fresh=%v err=%v", did, fresh, err)
	}
}

// A plugin the user disabled stays off, a config.yaml join can't read is left as it was, and a
// plugin folder of someone else's is left alone: each a problem that says what to do.
func TestHermesLeavesWhatIsntJoinsToMake(t *testing.T) {
	for _, c := range []struct{ name, cfg, plugin, want string }{
		{"disabled", "plugins:\n  disabled: [murmur]\n", "", "plugins.disabled"},
		{"unreadable", "plugins: {enabled: [x]}\n", "", "hermes plugins enable"},
		{"someone else's", "", "def register(ctx): pass\n", "isn't the one join writes"},
	} {
		t.Run(c.name, func(t *testing.T) {
			sb := hermesSandbox(t)
			cfg := filepath.Join(sb.HermesHome, "config.yaml")
			if c.cfg != "" {
				write(t, cfg, c.cfg)
			}
			init := filepath.Join(hermesPluginDir(sb.HermesHome), "__init__.py")
			if c.plugin != "" {
				write(t, init, c.plugin)
			}
			before := read(t, cfg)
			_, _, err := sb.registerHermesAt(sb.defaultHome(agents[agentIndex("hermes")]))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %v, want %q", err, c.want)
			}
			if read(t, cfg) != before {
				t.Errorf("config.yaml changed:\n%s", read(t, cfg))
			}
			if c.plugin != "" && read(t, init) != c.plugin {
				t.Error("the user's plugin changed")
			}
		})
	}
}

// An update rewrites a plugin join wrote for this binary, and leaves one for another binary, or the
// user's, alone.
func TestRefreshHermes(t *testing.T) {
	sb := hermesSandbox(t)
	write(t, sb.Exe, "binary")
	other := filepath.Join(sb.root, "other", exeName("mur"))
	write(t, other, "another binary")
	dir := hermesPluginDir(sb.HermesHome)
	init := filepath.Join(dir, "__init__.py")
	if _, err := writeHermesPlugin(dir, sb.Exe); err != nil {
		t.Fatal(err)
	}
	current := read(t, init)
	write(t, init, strings.Replace(current, "def register(ctx):", "def register(ctx):  # older", 1))
	if did, err := refreshHermesAt(sb.HermesHome, sb.Exe); err != nil || !did || read(t, init) != current {
		t.Errorf("did=%v err=%v", did, err)
	}
	if did, err := refreshHermesAt(sb.HermesHome, other); err != nil || did {
		t.Errorf("another binary's: did=%v err=%v", did, err)
	}
	write(t, init, "# mine\n")
	if did, err := refreshHermesAt(sb.HermesHome, sb.Exe); err != nil || did || read(t, init) != "# mine\n" {
		t.Errorf("the user's: did=%v err=%v", did, err)
	}
}

// Run from a Hermes session, join says to restart the gateway and resume this session where it just
// added the plugin, and names the tools where it was there already; another agent's join says the
// restart for Hermes too.
func TestHermesAdvice(t *testing.T) {
	e := envWith(map[string]string{"HERMES_SESSION_ID": "local-abc"})
	if got := strings.Join(e.advice(false, false, false, false, true, false), "\n"); !strings.Contains(got, "`hermes gateway restart`") ||
		!strings.Contains(got, "`hermes --resume local-abc`") {
		t.Errorf("fresh: %s", got)
	}
	if got := strings.Join(e.advice(false, false, false, false, false, false), "\n"); !strings.Contains(got, "murmur_who") {
		t.Errorf("on already: %s", got)
	}
	if got := othersAdvice([]string{"Hermes", "Muse Code"}); !strings.Contains(got, "`hermes gateway restart`") || strings.Contains(got, "Hermes, Muse Code load") {
		t.Errorf("others: %s", got)
	}
	// A Claude Code started from Hermes' terminal has HERMES_SESSION_ID too, and gets Claude Code's.
	if envWith(map[string]string{"HERMES_SESSION_ID": "x", "CLAUDECODE": "1"}).looksLikeHermes() {
		t.Error("Claude Code under Hermes taken for Hermes")
	}
}
