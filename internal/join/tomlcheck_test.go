package join

import (
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// Join's trust entry keeps config.toml valid TOML, with its line endings and its final newline or
// lack of one, whether the hook's table is there or not (#946). A CRLF file with no final newline
// whose table lacked trusted_hash used to end in a lone \r, which TOML forbids, and Codex then
// refused all of its config.
func TestEditCodexTrustKeepsTheFilesForm(t *testing.T) {
	trust := []codexTrust{ // the first one's table may be there; the second one's never is
		{`C:\h\hooks.json:stop:0:0`, "sha256:new"},
		{`C:\h\hooks.json:user_prompt_submit:0:0`, "sha256:other"},
	}
	header := `[hooks.state.'C:\h\hooks.json:stop:0:0']`
	for name, body := range map[string]string{
		"no table":              "model = \"gpt\"\nx = 1", // two lines, so a CRLF file without a final newline shows its CRLF
		"the table, empty":      "model = \"gpt\"\n\n" + header,
		"the table, other keys": "model = \"gpt\"\n\n" + header + "\nenabled = true",
		"the table, old hash":   "model = \"gpt\"\n\n" + header + "\ntrusted_hash = \"sha256:old\"",
		"the table, then more":  header + "\nenabled = true\n\n[tui]\nx = 1",
		"a comment last":        header + "\nenabled = true\n# mine",
		"a blank line last":     header + "\nenabled = true\n   ",
	} {
		for _, nl := range []string{"\n", "\r\n"} {
			for _, final := range []bool{true, false} {
				before := strings.ReplaceAll(body, "\n", nl)
				if final {
					before += nl
				}
				out, changed, err := editCodexTrust(before, trust)
				label := name + map[string]string{"\n": ", LF", "\r\n": ", CRLF"}[nl] + map[bool]string{true: ", final newline", false: ", none"}[final]
				if err != nil || !changed {
					t.Errorf("%s: changed=%v err=%v", label, changed, err)
					continue
				}
				var cfg struct {
					Hooks struct {
						State map[string]struct {
							TrustedHash string `toml:"trusted_hash"`
						}
					}
				}
				if _, err := toml.Decode(out, &cfg); err != nil {
					t.Errorf("%s: not valid TOML: %v\n%q", label, err, out)
					continue
				}
				for _, tr := range trust {
					if got := cfg.Hooks.State[tr.key].TrustedHash; got != tr.hash {
						t.Errorf("%s: trusted_hash of %s = %q\n%q", label, tr.key, got, out)
					}
				}
				if nl == "\r\n" && strings.Count(out, "\n") != strings.Count(out, "\r\n") {
					t.Errorf("%s: a line without CRLF: %q", label, out)
				}
				if nl == "\n" && strings.Contains(out, "\r") {
					t.Errorf("%s: a CR in an LF file: %q", label, out)
				}
				if strings.HasSuffix(out, "\n") != final {
					t.Errorf("%s: final newline %v, want %v: %q", label, !final, final, out)
				}
			}
		}
	}
}

// An empty file gets the trust tables, each line ending in LF.
func TestEditCodexTrustEmptyFile(t *testing.T) {
	out, _, err := editCodexTrust("", []codexTrust{{"k1", "sha256:a"}, {"k2", "sha256:b"}})
	want := "[hooks.state.'k1']\ntrusted_hash = \"sha256:a\"\n\n[hooks.state.'k2']\ntrusted_hash = \"sha256:b\"\n"
	if err != nil || out != want {
		t.Errorf("%v\n%q", err, out)
	}
	if err := checkTOMLEdit("", out); err != nil {
		t.Error(err)
	}
}

// An edit that would leave a file that parsed as one that doesn't is refused, and one of a file
// that didn't parse to begin with isn't judged: join's edits are by line, and the file may use
// TOML that the parser doesn't read.
func TestCheckTOMLEdit(t *testing.T) {
	valid, invalid := "a = 1\n", "a = 1\na = 2\n"
	if err := checkTOMLEdit(valid, valid+"b = 2\n"); err != nil {
		t.Errorf("a valid edit: %v", err)
	}
	if err := checkTOMLEdit(valid, invalid); err == nil || !strings.Contains(err.Error(), "the file is as it was") {
		t.Errorf("an edit that breaks the file: %v", err)
	}
	if err := checkTOMLEdit(invalid, invalid+"b = 2\n"); err != nil {
		t.Errorf("a file that was invalid already: %v", err)
	}
}

// Grok's allow rule keeps config.toml valid TOML, with its line endings and its final newline or lack
// of one, wherever [permission] is (#969). A CRLF file with no final newline whose last table was
// [permission], without allow, used to end in a lone \r, so checkTOMLEdit refused it and join failed
// on a valid file.
func TestAddTOMLListItemKeepsTheFilesForm(t *testing.T) {
	server := "[mcp_servers.murmur]\ncommand = \"/opt/mur\"\nargs = [\"mcp\", \"--agent\", \"grok\"]\n\n"
	for name, body := range map[string]string{
		"no table":                  server + "x = 1",
		"the table, empty":          server + "[permission]",
		"the table, other keys":     server + "[permission]\nask = [\"y\"]",
		"the table, an allow":       server + "[permission]\nallow = [\"y\"]",
		"the table, then more":      "[permission]\nask = [\"y\"]\n\n[tui]\nx = 1",
		"the table, a comment last": server + "[permission]\nask = [\"y\"]\n# mine",
		"the table, a blank last":   server + "[permission]\nask = [\"y\"]\n   ",
		"the table, a string last":  server + "[permission]\nnote = \"\"\"\nhi\n\"\"\"",
	} {
		for _, nl := range []string{"\n", "\r\n"} {
			for _, final := range []bool{true, false} {
				before := strings.ReplaceAll(body, "\n", nl)
				if final {
					before += nl
				}
				label := name + map[string]string{"\n": ", LF", "\r\n": ", CRLF"}[nl] + map[bool]string{true: ", final newline", false: ", none"}[final]
				out, changed, err := addTOMLListItem(before, "permission", "allow", grokAllow, "config.toml")
				if err == nil {
					err = checkTOMLEdit(before, out)
				}
				if err != nil || !changed {
					t.Errorf("%s: changed=%v err=%v\n%q", label, changed, err, out)
					continue
				}
				var cfg struct{ Permission struct{ Allow []string } }
				if _, err := toml.Decode(out, &cfg); err != nil {
					t.Errorf("%s: not valid TOML: %v\n%q", label, err, out)
					continue
				}
				if !slices.Contains(cfg.Permission.Allow, grokAllow) {
					t.Errorf("%s: allow = %q\n%q", label, cfg.Permission.Allow, out)
				}
				if nl == "\r\n" && strings.Count(out, "\n") != strings.Count(out, "\r\n") {
					t.Errorf("%s: a line without CRLF: %q", label, out)
				}
				if nl == "\n" && strings.Contains(out, "\r") {
					t.Errorf("%s: a CR in an LF file: %q", label, out)
				}
				if strings.HasSuffix(out, "\n") != final && strings.Contains(body, "[permission]") {
					t.Errorf("%s: final newline %v, want %v: %q", label, !final, final, out)
				}
			}
		}
	}
}

// A valid one-line allow list with brackets inside quoted rules (or comments) is edited in place
// and round-trips through uninstall, rather than being rejected as a multi-line array (#983).
func TestTOMLListItemHandlesBracketsInsideStrings(t *testing.T) {
	for name, before := range map[string]string{
		"open bracket in rule":          "[permission]\nallow = [\"Bash(printf [)\"]\n",
		"close bracket in literal rule": "[permission]\nallow = ['Bash(echo ])']\n",
		"brackets and comment":          "[permission]\nallow = [\"Bash(printf [)\", 'Bash(ls)'] # [note\n",
		"escaped quote and bracket":     "[permission]\nallow = [\"Bash(printf \\\"[\\\")\"]\n",
		"trailing comma":                "[permission]\nallow = [\"Bash(printf [)\",]\n",
	} {
		out, changed, err := addTOMLListItem(before, "permission", "allow", grokAllow, "config.toml")
		if err != nil || !changed {
			t.Fatalf("%s: add changed=%v err=%v\n%s", name, changed, err, out)
		}
		if err := checkTOMLEdit(before, out); err != nil {
			t.Fatalf("%s: checkTOMLEdit: %v\n%s", name, err, out)
		}
		var cfg struct{ Permission struct{ Allow []string } }
		if _, err := toml.Decode(out, &cfg); err != nil {
			t.Fatalf("%s: invalid TOML: %v\n%s", name, err, out)
		}
		if !slices.Contains(cfg.Permission.Allow, grokAllow) {
			t.Fatalf("%s: missing %q in %q", name, grokAllow, cfg.Permission.Allow)
		}
		if again, changed, err := addTOMLListItem(out, "permission", "allow", grokAllow, "config.toml"); err != nil || changed || again != out {
			t.Fatalf("%s: second add changed=%v err=%v", name, changed, err)
		}
		removed, changed, err := removeTOMLListItem(out, "permission", "allow", grokAllow, "config.toml")
		if err != nil || !changed {
			t.Fatalf("%s: remove changed=%v err=%v", name, changed, err)
		}
		if err := checkTOMLEdit(out, removed); err != nil {
			t.Fatalf("%s: checkTOMLEdit after remove: %v\n%s", name, err, removed)
		}
		var after struct{ Permission struct{ Allow []string } }
		if _, err := toml.Decode(removed, &after); err != nil {
			t.Fatalf("%s: invalid TOML after remove: %v\n%s", name, err, removed)
		}
		if slices.Contains(after.Permission.Allow, grokAllow) || len(after.Permission.Allow) != len(cfg.Permission.Allow)-1 {
			t.Fatalf("%s: after remove allow = %q", name, after.Permission.Allow)
		}
	}

	// Multiline lists, triple-quoted strings, and malformed lists are still refused.
	for name, bad := range map[string]string{
		"multiline":     "[permission]\nallow = [\n  \"Bash(ls)\",\n]\n",
		"triple basic":  "[permission]\nallow = [\"\"\"Bash(ls)\"\"\"]\n",
		"triple single": "[permission]\nallow = ['''Bash(ls)''']\n",
		"missing comma": "[permission]\nallow = [\"a\" \"b\"]\n",
		"trailing junk": "[permission]\nallow = [\"a\"] extra\n",
	} {
		if out, _, err := addTOMLListItem(bad, "permission", "allow", grokAllow, "config.toml"); err == nil {
			t.Errorf("%s: unexpectedly edited:\n%s", name, out)
		}
	}
}
