package join

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestTOMLLineEnd(t *testing.T) {
	for _, tc := range []struct{ line, open, want string }{
		{`model = "x"`, "", ""},
		{`# wrap long prompts in """ quotes`, "", ""},
		{`# or ''' ones`, "", ""},
		{`developer_instructions = "prefer ''' strings"`, "", ""},
		{`x = '"""'`, "", ""},
		{`x = "say \"\"\" now"`, "", ""},
		{`path = "C:\\" # a """ in a comment`, "", ""},
		{`x = 'C:\' # a ''' in a comment`, "", ""}, // no escapes in a literal string
		{`notes = """`, "", `"""`},
		{`notes = '''`, "", `'''`},
		{`notes = """with ''' inside`, "", `"""`},
		{`notes = '''with """ inside`, "", `'''`},
		{`w = """one line"""`, "", ""},
		{`z = """a "quote"""""`, "", ""}, // the two quotes before the closing three are the string's
		{`text, still`, `"""`, `"""`},
		{`a \""" b`, `"""`, `"""`}, // an escaped quote doesn't close it
		{`a \''' b`, `'''`, ""},    // a literal string has no escapes
		{`end """`, `"""`, ""},
		{`""" # done, and a """ in a comment`, `"""`, ""},
		{`''' + '''`, `'''`, `'''`},
		{`"""" and on`, `"""`, ""},
	} {
		if got := tomlLineEnd(tc.line, tc.open); got != tc.want {
			t.Errorf("tomlLineEnd(%q, %q) = %q, want %q", tc.line, tc.open, got, tc.want)
		}
	}
}

// tomlBefore is text that may come before Murmur's table in a config.toml, with quotes in it that
// open no multi-line string, or that open one and close it. Each is valid TOML (checked with
// Python's tomllib, 2026-11-09).
var tomlBefore = map[string]string{
	"triple quotes in a comment":           "model = \"o5\"\n# tip: wrap long prompts in \"\"\" quotes\n",
	"literal triple quotes in a comment":   "# or ''' ones\n",
	"literal triple quotes in a string":    "developer_instructions = \"prefer ''' strings\"\n",
	"triple quotes in a literal string":    "x = '\"\"\"'\n",
	"escaped quotes in a string":           `x = "say \"\"\" now"` + "\n",
	"an escaped backslash, then a comment": `path = "C:\\" # a """ in a comment` + "\n",
	"an escaped quote in a multi-line one": "x = \"\"\"\na \\\"\"\" b\n\"\"\"\n",
	"literal quotes opening a basic one":   "x = \"\"\"with '''\n\"\"\"\n",
	"a header in a multi-line literal":     "y = '''\n[mcp_servers.murmur]\n'''\n",
	"quotes before the closing three":      "z = \"\"\"a \"quote\"\"\"\"\"\n",
	"a multi-line string on one line":      "w = \"\"\"one\"\"\"\n",
}

// Quotes in a comment or in a one-line string open no multi-line string, so the table join wrote
// last time is found again, not appended a second time: TOML forbids that, and Codex then refuses
// its whole config.
func TestCodexEditFindsItsTablePastQuotes(t *testing.T) {
	for name, before := range tomlBefore {
		first, _ := edit(t, before)
		if want := before + "\n" + tomlTable; first != want {
			t.Errorf("%s: first edit:\n%s", name, first)
			continue
		}
		second, changed, err := editTOMLServer(first, "/opt/mur", codexServer)
		if err != nil || changed || second != first {
			t.Errorf("%s: the second edit changed=%v err=%v:\n%s", name, changed, err, second)
		}
		moved, _, err := editTOMLServer(first, "/new/mur", codexServer)
		if err != nil || strings.Count(moved, "[mcp_servers.murmur]") != strings.Count(first, "[mcp_servers.murmur]") || !strings.Contains(moved, `"/new/mur"`) {
			t.Errorf("%s: after the binary moved (err %v):\n%s", name, err, moved)
		}
	}
}

// Grok's allow list is found the same way.
func TestAddTOMLListItemFindsItsTablePastQuotes(t *testing.T) {
	for name, before := range tomlBefore {
		text := before + "\n[permission]\nallow = [\"x\"]\n"
		if out, changed, err := addTOMLListItem(text, "permission", "allow", "x", "config.toml"); err != nil || changed || out != text {
			t.Errorf("%s: changed=%v err=%v:\n%s", name, changed, err, out)
		}
	}
}

// A line of a multi-line string in Murmur's table is the value's, whatever it starts with: one that
// reads `args = ...` or `command = ...` isn't taken for the key and rewritten (#968). Both versions
// of the file parse, so checkTOMLEdit can't catch it, and the user's value would change unseen.
func TestCodexEditLeavesKeysInAMultilineStringAlone(t *testing.T) {
	for name, value := range map[string]string{
		"basic":   "description = \"\"\"\nargs = [\"keep\", \"me\"]\ncommand = \"keep\"\nenv_vars = [\"KEEP\"]\n\"\"\"\n",
		"literal": "description = '''\nargs = [\"keep\", \"me\"]\ncommand = \"keep\"\nenv_vars = [\"KEEP\"]\n'''\n",
	} {
		for _, nl := range []string{"\n", "\r\n"} {
			before := strings.ReplaceAll("[mcp_servers.murmur]\n"+value, "\n", nl)
			out, changed, err := editCodexConfig(before, "/opt/mur")
			if err != nil || !changed {
				t.Errorf("%s: changed=%v err=%v", name, changed, err)
				continue
			}
			if !strings.Contains(out, strings.ReplaceAll(value, "\n", nl)) {
				t.Errorf("%s: the string changed:\n%q", name, out)
			}
			var cfg struct {
				MCPServers map[string]struct {
					Command, Description string
					Args                 []string
					EnvVars              []string `toml:"env_vars"`
				} `toml:"mcp_servers"`
			}
			if _, err := toml.Decode(out, &cfg); err != nil {
				t.Errorf("%s: not valid TOML: %v\n%q", name, err, out)
				continue
			}
			if s := cfg.MCPServers["murmur"]; s.Command != "/opt/mur" || len(s.Args) != 3 || len(s.EnvVars) != 3 {
				t.Errorf("%s: the table join wrote: %+v\n%q", name, s, out)
			}
			if again, changed, err := editCodexConfig(out, "/opt/mur"); err != nil || changed || again != out {
				t.Errorf("%s: the second edit changed=%v err=%v:\n%q", name, changed, err, again)
			}
		}
	}
}

// Grok's allow list isn't taken from a multi-line string in [permission] either.
func TestAddTOMLListItemLeavesAMultilineStringAlone(t *testing.T) {
	value := "note = \"\"\"\nallow = [\"keep\"]\n\"\"\"\n"
	out, changed, err := addTOMLListItem("[permission]\n"+value, "permission", "allow", "x", "config.toml")
	if err != nil || !changed || !strings.Contains(out, value) || !strings.Contains(out, "\nallow = [\"x\"]") {
		t.Errorf("changed=%v err=%v:\n%q", changed, err, out)
	}
}
