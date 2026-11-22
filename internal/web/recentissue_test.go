package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The harness builds Recent's entries from a GitHub listing and the daemon's activity, with the
// page's own code, and prints each line as text, newest first, with whom the Whose filter finds it
// under.
const recentIssueHarness = `
const vm = require("vm");
const fs = require("fs");
const src = fs.readFileSync(process.argv[2], "utf8");
const text = (x) => (x == null || x === false ? "" : Array.isArray(x) ? x.map(text).join("") : typeof x === "object" ? text(x.kids) : String(x));
const at = (min) => new Date(Date.UTC(2026, 10, 12, 10, min)).toISOString();
const gh = (number, what, min, author, url) => ({ id: "gh-issue-" + number + "-" + what, kind: "issue", what, at: at(min), number, title: "issue " + number, author,
  url: url === undefined ? "https://github.com/a-Fig/murmur/issues/" + number : url });
const act = (number, what, min, who, repo) => ({ id: "issue-" + what + "-" + number + "-" + who, kind: "issue", at: at(min), t: Date.parse(at(min)), addr: "laptop/" + who, who,
  repo: repo || "a-fig/murmur", issue: number, what });
const ctx = {
  ui: {
    messages: [], state: { devices: [] },
    github: [
      gh(37, "opened", 1, "tyler"),
      gh(38, "opened", 2, "tyler"), gh(38, "closed", 3, "tyler"),
      gh(39, "opened", 4, "tyler"), gh(39, "closed", 5, "tyler"),
      gh(40, "opened", 6, "tyler"), gh(40, "closed", 7, "tyler"),
      gh(42, "opened", 9, "tyler"), gh(43, "opened", 10, "tyler"), gh(43, "closed", 40, "tyler"),
      gh(44, "opened", 12, "tyler", ""),
    ],
    acts: [act(37, "opened", 1, "quiet-otter"), act(39, "opened", 4, "quiet-otter"), act(39, "closed", 5, "brave-heron"),
      act(40, "opened", 6, "quiet-otter"), act(41, "opened", 8, "quiet-otter"),
      // #42 and #41 of another repository than the listing's; #43 closed by brave-heron, reopened,
      // and closed again by someone; #44 listed without an address, so in the listing's repository.
      act(42, "opened", 9, "quiet-otter", "someone/else"), act(41, "opened", 9, "quiet-otter", "someone/else"),
      act(43, "closed", 11, "brave-heron"), act(44, "opened", 12, "quiet-otter")],
  },
  h: (tag, attrs, ...kids) => ({ kids }),
  icon: () => "", clockTime: () => "", plural: (n, w) => n + " " + w + (n === 1 ? "" : "s"),
  devices: () => [], liveGroups: () => [], inGroup: () => false, nameOf: (addr, shown) => shown,
  when: (s) => Date.parse(s), Date,
};
vm.createContext(ctx);
vm.runInContext(src + "\nthis.recentEntries = recentEntries; this.activityItem = activityItem; this.githubItem = githubItem;", ctx);
for (const e of ctx.recentEntries().sort((a, b) => b.t - a.t)) {
  console.log(text((e.a ? ctx.activityItem(e) : ctx.githubItem(e)).kids) + " | " + e.who.filter(Boolean).join(","));
}
`

// Recent names the session that filed an issue, where this device's hook saw it do so (#510), and
// the one that closed it, only on GitHub's event for the same number in the same repository, and
// for a close one about as late; it keeps GitHub's login where no session is known, and shows an
// issue a session filed in the listing's repository that GitHub hasn't listed yet on its own line.
func TestRecentNamesTheSessionThatFiledAnIssue(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "page.js")
	write := func(path, text string) {
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(src, pageFunctions(t, "RECENT_RUN", "ISSUE_NEAR", "ghRepo", "sessionOnBranch", "recentEntries", "activityLine", "actWho", "nameButton", "actBranch", "activityItem", "githubItem"))
	harness := filepath.Join(dir, "harness.js")
	write(harness, recentIssueHarness)
	b, err := exec.Command(node, harness, src).CombinedOutput()
	if err != nil {
		t.Fatalf("the harness: %v\n%s", err, b)
	}
	got := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(b), "\r\n", "\n")), "\n")
	want := []string{
		"Issue #43 was closed: issue 43 | ",
		"quiet-otter filed issue #44: issue 44 | quiet-otter",
		"brave-heron closed issue #43 | brave-heron",
		"tyler filed issue #43: issue 43 | ",
		"tyler filed issue #42: issue 42 | ",
		"quiet-otter filed issue #41 | quiet-otter",
		"quiet-otter's issue #40 was closed: issue 40 | quiet-otter",
		"quiet-otter filed issue #40: issue 40 | quiet-otter",
		"brave-heron closed issue #39: issue 39 | brave-heron,quiet-otter",
		"quiet-otter filed issue #39: issue 39 | quiet-otter",
		"Issue #38 was closed: issue 38 | ",
		"tyler filed issue #38: issue 38 | ",
		"quiet-otter filed issue #37: issue 37 | quiet-otter",
	}
	if len(got) != len(want) {
		t.Fatalf("the harness printed\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d:\n got %s\nwant %s", i, got[i], want[i])
		}
	}
}
