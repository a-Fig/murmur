package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

var (
	importRe = regexp.MustCompile(`(?m)^import \{([^}]*)\} from "\./([a-z]+)\.js";$`)
	exportRe = regexp.MustCompile(`(?m)^export \{([^}]*)\};$`)
)

func names(list string) []string {
	var out []string
	for _, n := range strings.Split(list, ",") {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// The page is native ES modules (#1015), loaded from the files embedded here. A name one imports
// that the other doesn't export, or a file that nothing loads, shows only when a browser runs the
// page, as a blank one, so the module graph is checked here: app.js is the page's entry, every
// module is reachable from it, and every import names something its module exports.
func TestPageModulesFitTogether(t *testing.T) {
	files, err := fs.Glob(static, "static/assets/*.js")
	if err != nil {
		t.Fatal(err)
	}
	src := map[string]string{}
	for _, f := range files {
		b, err := static.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src[strings.TrimSuffix(strings.TrimPrefix(f, "static/assets/"), ".js")] = string(b)
	}
	delete(src, "icons") // a module of its own, which exports as it declares
	exports := map[string]map[string]bool{}
	for m, text := range src {
		exports[m] = map[string]bool{}
		for _, e := range exportRe.FindAllStringSubmatch(text, -1) {
			for _, n := range names(e[1]) {
				exports[m][n] = true
			}
		}
	}
	reached := map[string]bool{"app": true}
	queue := []string{"app"}
	for len(queue) > 0 {
		m := queue[0]
		queue = queue[1:]
		for _, imp := range importRe.FindAllStringSubmatch(src[m], -1) {
			from := imp[2]
			if from == "icons" {
				continue
			}
			if _, ok := src[from]; !ok {
				t.Errorf("%s imports ./%s.js, which isn't a page module", m, from)
				continue
			}
			for _, n := range names(imp[1]) {
				if !exports[from][n] {
					t.Errorf("%s imports %s from %s, which doesn't export it", m, n, from)
				}
			}
			if !reached[from] {
				reached[from] = true
				queue = append(queue, from)
			}
		}
	}
	for m := range src {
		if !reached[m] {
			t.Errorf("%s.js is not imported by anything the page loads", m)
		}
	}
	if n := strings.Count(src["app"], "\nexport "); n != 0 {
		t.Errorf("app.js is the entry and exports nothing, found %d exports", n)
	}
}
