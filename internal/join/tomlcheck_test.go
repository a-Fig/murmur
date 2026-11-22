package join

import (
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
