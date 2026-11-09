package names

import (
	"strings"
	"testing"
)

func TestValidPerson(t *testing.T) {
	for _, name := range []string{"ada", "ada-lovelace", "a1", "tyler-2", strings.Repeat("a", MaxPerson)} {
		if !ValidPerson(name) {
			t.Errorf("%q should be valid", name)
		}
	}
	bad := []string{
		"", "a", "Ada", "ada lovelace", "ada_lovelace", "-ada", "ada-", "ada--lovelace", "zoë", "ada/x", "*",
		strings.Repeat("a", MaxPerson+1), "human", "everyone", "you", "cli", adjectives[0] + "-" + nouns[0],
	}
	for _, name := range bad {
		if ValidPerson(name) {
			t.Errorf("%q should not be valid", name)
		}
	}
}

func TestPersonSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Ada Lovelace":                  "ada-lovelace",
		"smash":                         "smash",
		"Tyler  O'Brien, Jr":            "tyler-o-brien-jr",
		"Zoë":                           "zo",
		"  dash-ed  ":                   "dash-ed",
		"":                              "",
		"日本語":                           "",
		"x":                             "",
		"Human":                         "",
		strings.Repeat("long name ", 8): "long-name-long-name-long-name-lo",
	} {
		if got := PersonSlug(in); got != want {
			t.Errorf("PersonSlug(%q) = %q, want %q", in, got, want)
		}
	}
}
