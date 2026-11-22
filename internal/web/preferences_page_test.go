package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The harness loads the page's preferences in a browser that still has a theme and a filter from
// before #942, against a daemon whose import (the POST) fails and whose read (the GET) works or
// doesn't.
const preferencesHarness = `
const vm = require("vm");
const fs = require("fs");
const src = fs.readFileSync(process.argv[2], "utf8");
async function load(getWorks) {
  const store = new Map([["Murmur:theme", "sky"], ["Murmur:only-mine", "0"]]);
  const calls = [];
  const ctx = {
    NAME: "Murmur",
    THEMES: [["dim"], ["sky"], ["light"]],
    legacyKeys: { theme: ":theme", onlyMine: ":only-mine", tagsFolded: ":tags-folded" },
    localStorage: { getItem: (k) => store.get(k) ?? null, removeItem: (k) => store.delete(k) },
    document: { documentElement: { dataset: { theme: "sky" } } },
    AbortSignal,
    AuthError: class extends Error {},
    deny: () => calls.push("deny"),
    tokenEra: 1,
    initialTheme: "dim",
    theme: "sky",
    onlyMine: false,
    tagsFolded: false,
    api: async (path, init = {}) => {
      const method = init.method || "GET";
      calls.push(method);
      if (method === "POST" || !getWorks) throw new Error("couldn't save page preferences");
      return { theme: "light", onlyMine: true };
    },
  };
  vm.createContext(ctx);
  vm.runInContext(src + "\nthis.loadPreferences = loadPreferences;", ctx);
  await ctx.loadPreferences(1);
  return [calls.join(","), "onlyMine=" + ctx.onlyMine, "tagsFolded=" + ctx.tagsFolded, "theme=" + ctx.theme + "/" + ctx.document.documentElement.dataset.theme,
    "kept=" + [...store.keys()].join(",")].join(" ");
}
(async () => {
  console.log("the read works: " + (await load(true)));
  console.log("the read fails: " + (await load(false)));
})();
`

// When the import of a browser's old choices fails, the page still reads what the clone has saved,
// so a stale filter in the browser doesn't hide it for that load, and it keeps the old keys, the
// theme's with them, for the next load to import (#999). If the read fails too, the browser's old
// choices stand in, as before.
func TestAFailedImportStillReadsTheSavedPreferences(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node")
	}
	dir := t.TempDir()
	write := func(name, text string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	src := write("page.js", pageFunctions(t, "legacyPreferences", "loadPreferences"))
	b, err := exec.Command(node, write("harness.js", preferencesHarness), src).CombinedOutput()
	if err != nil {
		t.Fatalf("the harness: %v\n%s", err, b)
	}
	got := strings.TrimSpace(strings.ReplaceAll(string(b), "\r\n", "\n"))
	want := "the read works: POST,GET onlyMine=true tagsFolded=false theme=light/light kept=Murmur:theme,Murmur:only-mine\n" +
		"the read fails: POST,GET onlyMine=false tagsFolded=false theme=sky/sky kept=Murmur:theme,Murmur:only-mine"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}
