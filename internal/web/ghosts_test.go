package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// The page names each session's ghost and accessory from two lists in its scripts (#579); every name
// has to have a file that scripts/ghosts.py drew, or a session's face loses a layer.
func TestGhostFilesExist(t *testing.T) {
	src := []byte(pageSource(t))
	lists := map[string]string{"GHOSTS": "ghost-", "GHOST_ACCESSORIES": "acc-"}
	for list, prefix := range lists {
		m := regexp.MustCompile(`const ` + list + ` = \[([^\]]*)\]`).FindSubmatch(src)
		if m == nil {
			t.Fatalf("the page has no %s list", list)
		}
		names := make(map[string]bool)
		for _, q := range strings.Split(string(m[1]), ",") {
			n := strings.Trim(strings.TrimSpace(q), `"`)
			if n == "" || names[n] {
				t.Errorf("%s has an empty or repeated name %q", list, n)
				continue
			}
			names[n] = true
		}
		if len(names) != 16 {
			t.Errorf("%s has %d distinct names, want 16", list, len(names))
		}
		for n := range names {
			if _, err := fs.Stat(static, "static/assets/ghosts/"+prefix+n+".svg"); err != nil {
				t.Errorf("%s names %q, but there is no %s%s.svg", list, n, prefix, n)
			}
		}
	}
}
