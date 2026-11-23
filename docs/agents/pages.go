// Package agents is the pages in this folder, built into mur, so `mur docs` can print them for an
// agent with no internet (#831). They are embedded, not copied, so the printed page is always the
// one in the installed version's source.
package agents

import (
	"embed"
	"sort"
	"strings"
)

//go:embed *.md
var pages embed.FS

// Names are the pages' names, without .md, in order.
func Names() []string {
	entries, _ := pages.ReadDir(".")
	var names []string
	for _, e := range entries {
		names = append(names, strings.TrimSuffix(e.Name(), ".md"))
	}
	sort.Strings(names)
	return names
}

// Page is the named page's text, and whether there is one.
func Page(name string) (string, bool) {
	b, err := pages.ReadFile(strings.TrimSuffix(name, ".md") + ".md")
	return string(b), err == nil
}
