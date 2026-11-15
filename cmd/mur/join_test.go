package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-Fig/murmur/internal/clones"
)

func runCount(t *testing.T, b *box) int {
	t.Helper()
	data, err := os.ReadFile(b.log)
	if err != nil {
		return 0
	}
	return strings.Count(string(data), "\n")
}

// sameFile reports whether two paths are one file; a temp directory can be reached by two names.
func sameFile(a, b string) bool {
	sa, errA := os.Stat(a)
	sb, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(sa, sb)
}

func jsonAt(t *testing.T, path string, keys ...string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(readFile(t, path)), &v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

// init says the repo uses Murmur in both files, and puts nothing else in the repo; join, run by an agent
// after its user agreed, does the rest on the user's side only. Running it again changes nothing,
// and --leave takes this repo's part back out.
func TestInitJoinAndLeave(t *testing.T) {
	b := newBox(t)
	b.extraEnv = []string{"CLAUDECODE=1"} // the agent running these is Claude Code

	out := b.mustRun(b.clone, "init")
	sha := strings.TrimSpace(git(t, b.remote, "rev-parse", "--short=7", "main"))
	if !strings.Contains(out, "Pushed "+sha+" to origin's main") || !strings.Contains(out, "is now at "+sha) {
		t.Errorf("init doesn't say it pushed %s to main and moved this checkout there:\n%s", sha, out)
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		got := readFile(t, filepath.Join(b.clone, name))
		for _, want := range []string{"<!-- murmur:start -->", "ask your user first", "docs/agents/join.md", "docs/agents/use.md", "<!-- murmur:end -->"} {
			if !strings.Contains(got, want) {
				t.Errorf("%s after init doesn't say %q:\n%s", name, want, got)
			}
		}
	}
	for _, name := range []string{".mcp.json", ".claude", "murmur"} {
		if exists(filepath.Join(b.clone, name)) {
			t.Errorf("init wrote %s into the repo", name)
		}
	}
	if exists(b.claudeJSON()) || exists(b.codexConfig()) || exists(filepath.Join(b.clone, ".git", "murmur")) {
		t.Fatal("init touched the user's settings or started something")
	}

	out = b.mustRun(b.clone, "join")
	if !strings.Contains(out, "Tell your user") || !strings.Contains(out, "claude --continue") {
		t.Errorf("join's output should tell the agent what to pass on, and that this conversation needs a restart:\n%s", out)
	}

	// On the record of this machine's clones, for the page's repo picker (#348), named for its remote.
	record := filepath.Join(b.root, "clones")
	joined := func(string) bool { return true }
	if list, err := clones.List(record, joined); err != nil || len(list) != 1 || list[0].Name != "remote" {
		t.Errorf("the record after join: %+v, %v", list, err)
	}

	// Registered once for the user, with this binary's full path, and its tools allowed.
	server, _ := jsonAt(t, b.claudeJSON(), "mcpServers", "murmur").(map[string]any)
	if cmd, _ := server["command"].(string); server == nil || !sameFile(cmd, b.mur) || !reflect.DeepEqual(server["args"], []any{"mcp"}) {
		t.Errorf("Claude Code's server: %v", server)
	}
	if allow, _ := jsonAt(t, b.claudeSettings(), "permissions", "allow").([]any); !reflect.DeepEqual(allow, []any{"mcp__murmur"}) {
		t.Errorf("Claude Code's allow list: %v", allow)
	}
	toml := readFile(t, b.codexConfig())
	if !strings.Contains(toml, "[mcp_servers.murmur]") || !strings.Contains(toml, `args = ["mcp", "--agent", "codex"]`) || !strings.Contains(toml, `default_tools_approval_mode = "approve"`) {
		t.Errorf("Codex's config:\n%s", toml)
	}

	// The skills, for each agent that reads them, beside its config.
	for _, dir := range []string{filepath.Dir(b.claudeSettings()), filepath.Dir(b.codexConfig())} {
		for _, name := range []string{"murmur", "murmur-pro", "murmur-manager"} {
			if !strings.Contains(readFile(t, filepath.Join(dir, "skills", name, "SKILL.md")), "name: "+name) {
				t.Errorf("no %s skill in %s", name, dir)
			}
		}
	}

	// Not for agents this machine doesn't have. The box's PATH is the machine's own after mur and
	// claude, so an agent installed on the machine running the tests is found, and set up, as on a
	// user's: only those it can't find are checked.
	for bin, dir := range map[string]string{"opencode": filepath.Join(b.home, ".config", "opencode"), "pi": filepath.Join(b.home, ".pi")} {
		if _, err := exec.LookPath(bin); err == nil {
			continue
		}
		if exists(dir) {
			t.Errorf("join made %s for an agent that isn't installed", dir)
		}
	}

	// The hook is for this user, in Claude Code's own settings, and nothing goes in the repo (#422).
	hook := readFile(t, b.claudeSettings())
	missing := false
	for _, event := range []string{"PreToolUse", "PostToolUse", "PostToolUseFailure", "UserPromptSubmit", "Stop"} {
		missing = missing || !strings.Contains(hook, `"`+event+`"`)
	}
	if missing || !strings.Contains(hook, ` hook`) {
		t.Errorf("Claude Code's settings.json:\n%s", hook)
	}
	if exists(filepath.Join(b.clone, ".claude")) {
		t.Error("join wrote in the repo's .claude")
	}
	status := git(t, b.clone, "status", "--porcelain")
	if strings.Contains(status, ".claude") || strings.Contains(status, ".mcp.json") {
		t.Errorf("join left files that a commit would pick up:\n%s", status)
	}
	if !exists(filepath.Join(b.clone, ".git", "murmur", "joined.json")) || !exists(filepath.Join(b.clone, ".git", "murmur", "daemon.json")) {
		t.Error("join didn't record consent and start the daemon")
	}

	// Again: nothing changes, and claude isn't asked anything.
	before, runs := snapshot(t, b.home, b.clone), runCount(t, b)
	out = b.mustRun(b.clone, "join")
	if !strings.Contains(out, "Nothing to do") {
		t.Errorf("joining again should say so:\n%s", out)
	}
	if after := snapshot(t, b.home, b.clone); !reflect.DeepEqual(before, after) || runCount(t, b) != runs {
		t.Error("joining twice changed something")
	}
	// Now the server is registered, a session that started after that has it already.
	if !strings.Contains(out, "within a few seconds") {
		t.Errorf("a repeat join in Claude Code should say the tools are coming:\n%s", out)
	}

	// --leave takes this repo's part out: the consent and the daemon. The per-user registration
	// and hook stay, as other repos may have joined.
	out = b.mustRun(b.clone, "join", "--leave")
	if !strings.Contains(out, "Removed the record") {
		t.Errorf("leave's output:\n%s", out)
	}
	if exists(filepath.Join(b.clone, ".git", "murmur", "joined.json")) || exists(filepath.Join(b.clone, ".git", "murmur", "daemon.json")) {
		t.Error("consent or the daemon survived --leave")
	}
	if !strings.Contains(readFile(t, b.claudeSettings()), ` hook`) {
		t.Error("--leave took the hook out for every repo")
	}
	if !strings.Contains(out, filepath.Join(filepath.Dir(b.claudeSettings()), "skills", "murmur-pro")) {
		t.Errorf("leave should say how to delete the skills:\n%s", out)
	}
	if jsonAt(t, b.claudeJSON(), "mcpServers", "murmur") == nil || !strings.Contains(readFile(t, b.codexConfig()), "[mcp_servers.murmur]") {
		t.Error("--leave unregistered the server for every repo")
	}
	if !strings.Contains(readFile(t, filepath.Join(b.clone, "AGENTS.md")), "<!-- murmur:start -->") {
		t.Error("--leave touched the repo's files")
	}
	if list, _ := clones.List(record, joined); len(list) != 0 {
		t.Errorf("the record after --leave: %+v", list)
	}
	if out = b.mustRun(b.clone, "join", "--leave"); !strings.Contains(out, "Nothing to undo") {
		t.Errorf("leaving twice should say so:\n%s", out)
	}

	// And back in.
	b.mustRun(b.clone, "join")
	if !exists(filepath.Join(b.clone, ".git", "murmur", "joined.json")) {
		t.Error("joining again after leaving didn't join")
	}
}

// --no-push is init as it was before it pushed: it edits this working tree, commits nothing and
// leaves the remote alone.
func TestInitNoPushOnlyEditsTheWorkingTree(t *testing.T) {
	b := newBox(t)
	before := git(t, b.remote, "rev-parse", "main")
	out := b.mustRun(b.clone, "init", "--no-push")
	if !strings.Contains(out, "Review and commit these files") || strings.Contains(out, "Pushed") {
		t.Errorf("init --no-push said:\n%s", out)
	}
	if got := git(t, b.remote, "rev-parse", "main"); got != before {
		t.Errorf("init --no-push moved the remote's main")
	}
	if status := git(t, b.clone, "status", "--porcelain"); !strings.Contains(status, "AGENTS.md") || !strings.Contains(status, "CLAUDE.md") {
		t.Errorf("status after init --no-push:\n%s", status)
	}
	if git(t, b.clone, "rev-parse", "HEAD") != before {
		t.Error("init --no-push committed")
	}
}

// What the user already had in Claude Code's and Codex's files stays as it was, in its order.
func TestJoinKeepsTheUsersConfig(t *testing.T) {
	b := newBox(t)
	b.mustRun(b.clone, "init")
	writeFile(t, b.claudeJSON(), "{\n  \"numStartups\": 7,\n  \"mcpServers\": {\n    \"other\": {\"type\": \"stdio\", \"command\": \"o\", \"args\": []}\n  },\n  \"theme\": \"dark\"\n}\n")
	writeFile(t, b.claudeSettings(), "{\n  \"model\": \"x\",\n  \"permissions\": {\"allow\": [\"Bash(ls)\"]}\n}\n")
	codex := "# my codex\nmodel = \"gpt\"\n\n[mcp_servers.other]\ncommand = \"o\"  # mine\n"
	writeFile(t, b.codexConfig(), codex)
	// The repo's own settings file, with the hook a join before #422 put there.
	writeFile(t, filepath.Join(b.clone, ".claude", "settings.local.json"), "{\n  \"permissions\": {\"allow\": [\"Bash(git status)\"]},\n"+
		"  \"hooks\": {\"PostToolUse\": [{\"matcher\": \"*\", \"hooks\": [{\"type\": \"command\", \"command\": \"\\\"/old/bin/mur\\\" hook\"}]}]}\n}\n")

	out := b.mustRun(b.clone, "join")

	cfg := readFile(t, b.claudeJSON())
	if !(strings.Index(cfg, "numStartups") < strings.Index(cfg, "mcpServers") && strings.Index(cfg, `"other"`) < strings.Index(cfg, `"murmur"`) && strings.Index(cfg, `"murmur"`) < strings.Index(cfg, "theme")) {
		t.Errorf(".claude.json lost the order of the user's keys:\n%s", cfg)
	}
	if allow, _ := jsonAt(t, b.claudeSettings(), "permissions", "allow").([]any); !reflect.DeepEqual(allow, []any{"Bash(ls)", "mcp__murmur"}) {
		t.Errorf("allow list: %v", allow)
	}
	if got := readFile(t, b.codexConfig()); !strings.HasPrefix(got, codex) {
		t.Errorf("config.toml no longer starts with what the user wrote:\n%s", got)
	}
	if settings := readFile(t, b.claudeSettings()); !strings.HasPrefix(settings, "{\n  \"model\": \"x\",") || !strings.Contains(settings, "PostToolUse") {
		t.Errorf("settings.json:\n%s", settings)
	}
	// The older hook comes out of the repo's file, which keeps what else it had.
	local := readFile(t, filepath.Join(b.clone, ".claude", "settings.local.json"))
	if !strings.Contains(local, "Bash(git status)") || strings.Contains(local, "hook") || !strings.Contains(out, "Removed the hook an older") {
		t.Errorf("settings.local.json:\n%s\njoin's output:\n%s", local, out)
	}
	b.mustRun(b.clone, "join", "--leave")
	if got := readFile(t, filepath.Join(b.clone, ".claude", "settings.local.json")); got != local {
		t.Errorf("settings.local.json after --leave:\n%s", got)
	}
}

// A join that can't write the hook in Claude Code's settings leaves the older one in the clone's
// working tree, which is then the only hook the user's sessions have (Sol's review of #426). The
// next join, with the settings mended, moves it.
func TestJoinKeepsTheOlderHookWhereTheNewOneFailed(t *testing.T) {
	b := newBox(t)
	b.mustRun(b.clone, "init")
	local := filepath.Join(b.clone, ".claude", "settings.local.json")
	writeFile(t, local, "{\n  \"hooks\": {\"PostToolUse\": [{\"matcher\": \"*\", \"hooks\": [{\"type\": \"command\", \"command\": \"\\\"/old/bin/mur\\\" hook\"}]}]}\n}\n")
	writeFile(t, b.claudeSettings(), "{\"hooks\": 1}\n") // not an object: join won't replace it

	out, code := b.run(b.clone, "join")
	if code == 0 || !strings.Contains(out, "Problems:") || !strings.Contains(out, `"hooks" isn't an object`) {
		t.Errorf("join over settings it can't edit: exit %d\n%s", code, out)
	}
	if got := readFile(t, local); !strings.Contains(got, " hook") {
		t.Errorf("the older hook went though the new one isn't in:\n%s", got)
	}

	writeFile(t, b.claudeSettings(), "{}\n")
	out = b.mustRun(b.clone, "join")
	if exists(local) || !strings.Contains(out, "Removed the hook an older") || !strings.Contains(readFile(t, b.claudeSettings()), " hook") {
		t.Errorf("the next join didn't move the hook:\n%s", out)
	}
	b.mustRun(b.clone, "join", "--leave")
}

// A repo that commits .claude/settings.local.json is nothing to a join, which writes no hook in
// the repo (#422): it says nothing about the file and leaves it as it is.
func TestJoinLeavesAHookFileTheRepoTracksAlone(t *testing.T) {
	b := newBox(t)
	b.mustRun(b.clone, "init")
	local := filepath.Join(b.clone, ".claude", "settings.local.json")
	const settings = "{\n  \"permissions\": {\"allow\": [\"Bash(ls)\"]}\n}\n"
	writeFile(t, local, settings)
	git(t, b.clone, "add", "-f", "--", ".claude/settings.local.json")
	git(t, b.clone, "commit", "-q", "-m", "settings")

	out := b.mustRun(b.clone, "join")
	if strings.Contains(out, "Not done:") || strings.Contains(out, "Problems:") {
		t.Errorf("join's output:\n%s", out)
	}
	if got := readFile(t, local); got != settings {
		t.Errorf("join edited the tracked hook file:\n%s", got)
	}
	b.mustRun(b.clone, "join", "--leave")
}

// A Codex table an older join wrote forwards T3CODE_CLI_PATH only; joining again adds CODEX_HOME
// to the list and keeps the user's own names, and --leave leaves the per-user table as it is.
func TestJoinForwardsCodexHome(t *testing.T) {
	b := newBox(t)
	b.mustRun(b.clone, "init")
	head := "model = \"gpt\"\n\n[mcp_servers.murmur]\ncommand = " + strconv.Quote(b.mur) + "\nargs = [\"mcp\", \"--agent\", \"codex\"]\n"
	tail := "default_tools_approval_mode = \"approve\"\n"
	writeFile(t, b.codexConfig(), head+"env_vars = [\"HTTP_PROXY\", \"T3CODE_CLI_PATH\"] # mine\n"+tail)

	b.mustRun(b.clone, "join")
	joined := readFile(t, b.codexConfig())
	// The server's table, then the trust of Murmur's Codex hook, one table for each of its three
	// hooks (#225).
	server, trust, _ := strings.Cut(joined, "\n[hooks.state.")
	if want := head + "env_vars = [\"HTTP_PROXY\", \"T3CODE_CLI_PATH\", \"CODEX_HOME\"] # mine\n" + tail; server != want || strings.Count(trust, "trusted_hash") != 3 {
		t.Errorf("config.toml:\n%s\nwant:\n%s\nand the hook's trust", joined, want)
	}
	b.mustRun(b.clone, "join")
	if again := readFile(t, b.codexConfig()); again != joined {
		t.Errorf("a second join changed config.toml:\n%s", again)
	}
	b.mustRun(b.clone, "join", "--leave")
	if left := readFile(t, b.codexConfig()); left != joined {
		t.Errorf("--leave changed config.toml:\n%s", left)
	}
}

// A Codex thread in T3 that began before Murmur was added never gets its tools, so what join prints
// for it is to open a new thread (Tyler, 2026-10-30), in these words.
func TestJoinInACodexThreadInT3SaysToOpenANewThread(t *testing.T) {
	b := newBox(t)
	b.extraEnv = []string{"CODEX_THREAD_ID=019a", "T3CODE_CLI_PATH=" + filepath.Join(b.root, "t3", "bin", "t3")}
	b.mustRun(b.clone, "init")
	out := b.mustRun(b.clone, "join")
	if want := "open a new thread in this project to talk to other agents through Murmur"; !strings.Contains(out, want) {
		t.Errorf("join's output doesn't say %q:\n%s", want, out)
	}
}

// join does nothing in a repo that doesn't say it uses Murmur: the user's yes was to this repo's invitation.
func TestJoinNeedsTheMarker(t *testing.T) {
	b := newBox(t)
	out, code := b.run(b.clone, "join")
	if code == 0 || !strings.Contains(out, "doesn't say it uses Murmur") {
		t.Errorf("exit %d:\n%s", code, out)
	}
	if exists(b.claudeJSON()) || exists(b.codexConfig()) || exists(filepath.Join(b.clone, ".git", "murmur")) || exists(filepath.Join(b.clone, ".claude")) {
		t.Error("join changed something in a repo that doesn't use Murmur")
	}
}

// The commands for people are gone, and the help doesn't list them: agents message through MCP,
// people through the page.
func TestTheMessagingCommandsAreGone(t *testing.T) {
	b := newBox(t)
	for _, cmd := range []string{"who", "send", "inbox", "log"} {
		out, code := b.run(b.clone, cmd)
		if code != 2 || !strings.Contains(out, "unknown command") {
			t.Errorf("mur %s: exit %d, want 2 and an unknown command\n%s", cmd, code, out)
		}
	}
	help := b.mustRun(b.clone, "help")
	for _, gone := range []string{"mur who", "mur send", "mur inbox", "mur log"} {
		if strings.Contains(help, gone) {
			t.Errorf("help still lists %q:\n%s", gone, help)
		}
	}
	for _, kept := range []string{"mur init", "mur join", "mur ui", "mur stop"} {
		if !strings.Contains(help, kept) {
			t.Errorf("help doesn't list %q:\n%s", kept, help)
		}
	}
	for _, kept := range []string{"version", "stop"} {
		if _, code := b.run(b.clone, kept); code != 0 {
			t.Errorf("mur %s: exit %d", kept, code)
		}
	}
}

// A daemon publishes this machine's presence to the team, which only a user who joined agreed to,
// so one started in a clone that hasn't joined exits.
func TestDaemonWillNotRunInAClonethatHasNotJoined(t *testing.T) {
	b := newBox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, b.mur, "daemon", "-C", b.clone)
	cmd.Env = b.env()
	out, err := cmd.CombinedOutput()
	if err == nil || ctx.Err() != nil || !strings.Contains(string(out), "hasn't joined") {
		t.Errorf("the daemon ran (err %v, timed out %v):\n%s", err, ctx.Err() != nil, out)
	}
	if exists(filepath.Join(b.clone, ".git", "murmur", "daemon.json")) {
		t.Error("it left a daemon.json")
	}
}

// remoteRefs lists the Murmur refs on the remote, where teammates would see them.
func remoteRefs(t *testing.T, b *box) string {
	t.Helper()
	return strings.TrimSpace(git(t, b.remote, "for-each-ref", "refs/murmur"))
}

// mcpSession starts `mur mcp` in the clone, as an agent's client does, and connects to it.
func (b *box) mcpSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	return b.mcpSessionIn(t, b.clone)
}

// mcpSessionIn is mcpSession with the agent started in dir.
func (b *box) mcpSessionIn(t *testing.T, dir string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.Command(b.mur, "mcp", "-C", dir)
	cmd.Env = append(b.env(), b.extraEnv...)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil).Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close(); cancel() })
	return cs
}

// offersTools reports whether the names include the tools every agent gets. Tools added since
// (lookup, #21) are not a reason for the tests of when the server is on to change.
func offersTools(names []string) bool {
	for _, want := range []string{"who", "status", "send", "inbox"} {
		if !slices.Contains(names, want) {
			return false
		}
	}
	return true
}

func tools(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(30 * time.Second); !cond(); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func quiet(t *testing.T, b *box, what string) {
	t.Helper()
	time.Sleep(3 * time.Second) // longer than it waits between looks, and than a daemon takes to start
	if exists(filepath.Join(b.clone, ".git", "murmur")) {
		t.Errorf("%s: a daemon's directory exists", what)
	}
	if refs := strings.TrimSpace(git(t, b.clone, "for-each-ref", "refs/murmur")) + remoteRefs(t, b); refs != "" {
		t.Errorf("%s: Murmur refs exist: %s", what, refs)
	}
}

// The user-scope server starts in every repo the user opens. Where the repo hasn't invited Murmur,
// or this user hasn't joined, it has no tools and no instructions, starts no daemon and writes no
// ref, locally or on the remote.
func TestTheMCPServerIsPassiveWhereItIsNotInvited(t *testing.T) {
	b := newBox(t)

	// A repo that doesn't use Murmur.
	cs := b.mcpSession(t)
	if names := tools(t, cs); len(names) != 0 {
		t.Errorf("tools in a repo that doesn't use Murmur: %q", names)
	}
	quiet(t, b, "no marker")
	cs.Close()

	// A repo that does, where this user hasn't joined.
	b.mustRun(b.clone, "init")
	cs = b.mcpSession(t)
	if names := tools(t, cs); len(names) != 0 {
		t.Errorf("tools before joining: %q", names)
	}
	if cs.InitializeResult().Instructions != "" {
		t.Error("instructions before joining")
	}
	quiet(t, b, "marker, not joined")
}

// Joining while a session is open turns its server on, and leaving turns it off again, without
// restarting the daemon that --leave stopped.
func TestTheMCPServerFollowsJoinAndLeave(t *testing.T) {
	b := newBox(t)
	b.mustRun(b.clone, "init")
	cs := b.mcpSession(t)
	if names := tools(t, cs); len(names) != 0 {
		t.Fatalf("tools before joining: %q", names)
	}

	b.mustRun(b.clone, "join")
	eventually(t, "the tools to appear", func() bool { return offersTools(tools(t, cs)) })
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "who"})
	if err != nil || res.IsError {
		t.Fatalf("who after joining: %v %+v", err, res)
	}

	b.mustRun(b.clone, "join", "--leave")
	eventually(t, "the tools to go", func() bool { return len(tools(t, cs)) == 0 })
	time.Sleep(3 * time.Second)
	if exists(filepath.Join(b.clone, ".git", "murmur", "daemon.json")) {
		t.Error("a daemon is running after --leave")
	}
}

// An agent whose session started outside the repo, here the folder that holds it, runs join in the
// repo: its own session moves in, gets the tools and registers, without a restart (#200). The
// session's server is this test's child, as an agent's is the agent's, and so is the join.
func TestJoinBringsInASessionStartedOutside(t *testing.T) {
	b := newBox(t)
	b.mustRun(b.clone, "init")
	cs := b.mcpSessionIn(t, b.root)
	if names := tools(t, cs); len(names) != 0 {
		t.Fatalf("tools outside the repo: %q", names)
	}
	out := b.mustRun(b.clone, "join")
	if !strings.Contains(out, "Moved this session into Murmur here") || !strings.Contains(out, "It started outside this repo") {
		t.Errorf("join didn't say it moved the session:\n%s", out)
	}
	eventually(t, "the tools to appear", func() bool { return offersTools(tools(t, cs)) })
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "who"})
	if err != nil || res.IsError {
		t.Fatalf("who: %v %+v", err, res)
	}

	// Running join again finds the session already there, and says nothing about moving it.
	if out := b.mustRun(b.clone, "join"); strings.Contains(out, "Moved this session") {
		t.Errorf("a second join moved the session again:\n%s", out)
	}

	// Claude Code runs the hook where the session started, outside the repo. It finds the clone the
	// session joined (#422): that clone's daemon hears the hook's conversation ID, which is news to
	// it, and moves the session to it.
	payload, _ := json.Marshal(map[string]string{"session_id": "claude-outside", "hook_event_name": "UserPromptSubmit", "cwd": b.root})
	cmd := exec.Command(b.mur, "hook")
	cmd.Dir = b.root
	// The session's server is this test's child, so this test is its Claude Code.
	cmd.Env = append(b.env(), "CLAUDE_PID="+strconv.Itoa(os.Getpid()))
	cmd.Stdin = bytes.NewReader(payload)
	if out, err := cmd.CombinedOutput(); err != nil || len(out) != 0 {
		t.Fatalf("mur hook: %v\n%s", err, out)
	}
	eventually(t, "the joined clone's daemon to hear the hook", func() bool {
		return strings.Contains(readFile(t, filepath.Join(b.clone, ".git", "murmur", "daemon.log")), "followed Claude Code to another conversation")
	})
}
