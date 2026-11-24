package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/proto"
)

// toolRepo is a joined repo with a fake daemon, the working directory, and the Hermes session the
// plugin names (#975).
func toolRepo(t *testing.T, f *fakeDaemon) {
	t.Helper()
	p := repo(t, true, f)
	if _, err := p.RecordConsent(); err != nil {
		t.Fatal(err)
	}
	t.Chdir(p.Top)
	t.Setenv(hermesSessionEnv, "local-0123abcd")
}

// --list lists the seven tools as the MCP server does, each with its schema.
func TestToolListIsTheServersTools(t *testing.T) {
	var out strings.Builder
	if err := ToolList(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	var got []struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		InputSchema map[string]any `json:"inputSchema"`
	}
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	var names []string
	for _, tl := range got {
		names = append(names, tl.Name)
		if tl.Description == "" || tl.InputSchema["type"] != "object" {
			t.Errorf("%s: description %q, schema %v", tl.Name, tl.Description, tl.InputSchema)
		}
	}
	slices.Sort(names)
	if want := []string{"inbox", "lookup", "mute", "send", "status", "tags", "who"}; !slices.Equal(names, want) {
		t.Errorf("tools %v, want %v", names, want)
	}
}

// A call registers the Hermes session by its own ID, as a command's session with no thread, runs
// the MCP tool's handler as it, and prints the tool's text.
func TestAToolCallRegistersTheHermesSession(t *testing.T) {
	f := &fakeDaemon{}
	toolRepo(t, f)
	var out strings.Builder
	args := `{"to": "quiet-otter", "subject": "the subject", "body": "the body", "tell_me": "read"}`
	if err := Tool(context.Background(), "hermes", "send", strings.NewReader(args), &out); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	h := f.lastHello()
	if !h.Command || h.ByThread || h.T3Thread != "" || h.Native != "hermes:local-0123abcd" ||
		h.ID != nativeID("hermes:local-0123abcd") || h.Agent != "hermes" {
		t.Errorf("hello %+v, want a command's, keyed by the Hermes session", h)
	}
	got := f.requestsOf(proto.OpSend)
	if len(got) != 1 || got[0].Session != h.ID || got[0].To != "quiet-otter" || got[0].Subject != "the subject" ||
		got[0].Body != "the body" || got[0].TellMe != "read" {
		t.Errorf("sends %+v, want the tool's, as %s", got, h.ID)
	}
	if out.Len() == 0 {
		t.Error("the call printed nothing")
	}
}

// The tool's own checks run: a call it refuses prints why and fails, having asked the daemon nothing
// but the hello.
func TestAToolCallTheToolRefusesFails(t *testing.T) {
	f := &fakeDaemon{}
	toolRepo(t, f)
	var out strings.Builder
	err := Tool(context.Background(), "hermes", "send", strings.NewReader(`{"to": "a,b,c,d,e,f,g,h,i,j,k", "subject": "eleven"}`), &out)
	if !ToolFailed(err) || out.Len() == 0 {
		t.Fatalf("error %v, printed %q", err, out.String())
	}
	if got := f.requestsOf(proto.OpSend); len(got) != 0 {
		t.Errorf("sends %+v", got)
	}
}

// who with no arguments, or with an empty stdin, is the tool's who.
func TestAToolCallWithNoArguments(t *testing.T) {
	f := &fakeDaemon{}
	toolRepo(t, f)
	var out strings.Builder
	if err := Tool(context.Background(), "hermes", "who", strings.NewReader(""), &out); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	if len(f.requestsOf(proto.OpWho)) != 1 || !strings.Contains(out.String(), "You are") {
		t.Errorf("printed %q", out.String())
	}
}

// Without the session, outside a joined clone, for another agent, or with arguments that aren't an
// object, the call stops before any hello.
func TestAToolCallThatCantRunStops(t *testing.T) {
	f := &fakeDaemon{}
	toolRepo(t, f)
	for _, c := range []struct {
		name, agent, args, want string
		set                     func(*testing.T)
	}{
		{"no session", "hermes", "{}", hermesSessionEnv + " isn't set", func(t *testing.T) { t.Setenv(hermesSessionEnv, "") }},
		{"bad session", "hermes", "{}", "isn't a Hermes session ID", func(t *testing.T) { t.Setenv(hermesSessionEnv, "a b") }},
		{"other agent", "pi", "{}", "not \"pi\"", nil},
		{"not an object", "hermes", "[1]", "JSON object", nil},
		{"not joined", "hermes", "{}", errPassive.Error(), func(t *testing.T) { t.Chdir(repo(t, false, nil).Top) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.set != nil {
				c.set(t)
			}
			err := Tool(context.Background(), c.agent, "who", strings.NewReader(c.args), &strings.Builder{})
			if err == nil || ToolFailed(err) || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %v, want %q", err, c.want)
			}
		})
	}
	if n := f.helloCount(); n != 0 {
		t.Errorf("%d hellos", n)
	}
}

// A call of a tool there isn't fails and registers nothing.
func TestAToolCallOfNoToolRegistersNothing(t *testing.T) {
	f := &fakeDaemon{}
	toolRepo(t, f)
	if err := Tool(context.Background(), "hermes", "nosuch", strings.NewReader("{}"), &strings.Builder{}); err == nil {
		t.Error("ran")
	}
	if n := f.helloCount(); n != 0 {
		t.Errorf("%d hellos", n)
	}
}

// The plugin names the socket it opened for the session (#1184), and the session's hello carries
// it, so the daemon can start a turn there.
func TestAToolCallNamesTheSessionsSocket(t *testing.T) {
	f := &fakeDaemon{}
	toolRepo(t, f)
	t.Setenv(hermesSocketEnv, "/tmp/murmur-hermes-x/s")
	t.Setenv(hermesTokenEnv, "tok-1")
	var out strings.Builder
	if err := Tool(context.Background(), "hermes", "who", strings.NewReader("{}"), &out); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	if h := f.lastHello(); h.MsgSocket != "/tmp/murmur-hermes-x/s" || h.MsgToken != "tok-1" {
		t.Errorf("hello route %+v, want the plugin's socket", h.Route)
	}
}

// The hook registers the session its payload names, with its socket, before it asks the daemon
// anything, so a session that hasn't called a tool yet still gets its messages; then it asks as the
// hook does, by the same Native.
func TestAHermesHookRegistersItsSessionFirst(t *testing.T) {
	f := &fakeDaemon{}
	toolRepo(t, f)
	t.Setenv(hermesSocketEnv, "/tmp/murmur-hermes-x/s")
	t.Setenv(hermesTokenEnv, "tok-1")
	cwd, _ := os.Getwd()
	payload, _ := json.Marshal(map[string]string{"session_id": "local-9", "hook_event_name": "UserPromptSubmit", "cwd": cwd, "prompt": "hi"})
	HermesHook("", strings.NewReader(string(payload)), io.Discard)
	h := f.lastHello()
	if !h.Command || h.Native != "hermes:local-9" || h.ID != nativeID("hermes:local-9") || h.MsgSocket != "/tmp/murmur-hermes-x/s" || h.Cwd != cwd {
		t.Errorf("hello %+v, want the payload's session, with its socket", h)
	}
	got := f.requestsOf(proto.OpNotices)
	if len(got) != 1 || got[0].Native != "hermes:local-9" {
		t.Errorf("notices requests %+v, want one for hermes:local-9", got)
	}
}

// A hook for a tool call asks the daemon as the hook does without registering the session again: the
// turn's start and each `mur tool` did, and registering takes a few git processes.
func TestAHermesToolCallsHookDoesntRegister(t *testing.T) {
	f := &fakeDaemon{}
	toolRepo(t, f)
	cwd, _ := os.Getwd()
	payload, _ := json.Marshal(map[string]string{"session_id": "local-9", "hook_event_name": "PostToolUse", "tool_name": "read_file", "cwd": cwd})
	HermesHook("", strings.NewReader(string(payload)), io.Discard)
	if n := f.helloCount(); n != 0 {
		t.Errorf("%d hellos, want none", n)
	}
	if got := f.requestsOf(proto.OpNotices); len(got) != 1 || got[0].Native != "hermes:local-9" {
		t.Errorf("notices requests %+v, want one for hermes:local-9", got)
	}
}

// Outside a joined repo the hook registers nothing and asks nothing, as `mur tool` does.
func TestAHermesHookOutsideAJoinedRepoDoesNothing(t *testing.T) {
	f := &fakeDaemon{}
	p := repo(t, true, f) // marked, never joined here
	payload, _ := json.Marshal(map[string]string{"session_id": "local-9", "hook_event_name": "UserPromptSubmit", "cwd": p.Top})
	var out strings.Builder
	HermesHook("", strings.NewReader(string(payload)), &out)
	if n := f.helloCount(); n != 0 || out.Len() != 0 || len(f.requestsOf(proto.OpNotices)) != 0 {
		t.Errorf("%d hellos, printed %q", n, out.String())
	}
}
