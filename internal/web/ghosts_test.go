package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// The page names each person's ghost and accessory from two lists in app.js (#579); every name has
// to have a file that scripts/ghosts.py drew, or a session's face loses a layer.
func TestGhostFilesExist(t *testing.T) {
	src, err := fs.ReadFile(static, "static/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	lists := map[string]string{"GHOSTS": "ghost-", "GHOST_ACCESSORIES": "acc-"}
	for list, prefix := range lists {
		m := regexp.MustCompile(`const ` + list + ` = \[([^\]]*)\]`).FindSubmatch(src)
		if m == nil {
			t.Fatalf("app.js has no %s list", list)
		}
		var names []string
		for _, q := range strings.Split(string(m[1]), ",") {
			if n := strings.Trim(strings.TrimSpace(q), `"`); n != "" {
				names = append(names, n)
			}
		}
		// ghostOf picks from 16 of each: an empty accessory is the 16th, "none".
		if want := map[string]int{"GHOSTS": 16, "GHOST_ACCESSORIES": 15}[list]; len(names) != want {
			t.Errorf("%s has %d names, want %d", list, len(names), want)
		}
		for _, n := range names {
			if _, err := fs.Stat(static, "static/assets/ghosts/"+prefix+n+".svg"); err != nil {
				t.Errorf("%s names %q, but there is no %s%s.svg", list, n, prefix, n)
			}
		}
	}
}
