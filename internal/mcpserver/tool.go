package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/hook"
	"github.com/a-Fig/murmur/internal/join"
	"github.com/a-Fig/murmur/internal/proto"
)

// `mur tool` runs one of Murmur's tools for an agent whose plugin gives it the tools itself, one
// process a call: Hermes Agent (#975, #1183). Every Hermes session runs in one gateway process,
// which would share one MCP server among all its sessions and repos, and no MCP call says which
// session made it; a plugin's tool handler knows its session, and runs `mur tool` with the
// session's ID in hermesSessionEnv and the session's working directory as its own.
//
// The tool is the MCP server's own: `mur tool --list` lists the tools as the server does, which the
// plugin registers, and a call goes through the server's handler for it, in this process over an
// in-memory transport, so its arguments, checks and text are the MCP tool's and can't drift from
// them. Only where the handler reaches the daemon is different: the session is registered as the
// shell commands' sessions are (#913), by a hello that closes as the call ends, and the daemon
// holds it while it is in use (proto.Hello.Command). It is keyed by its native ID,
// "hermes:<session ID>", hashed as other agents' IDs are (nativeID), so each call finds the same
// session, at the same address and name.

// hermesSessionEnv is the variable Murmur's Hermes plugin gives `mur tool` the session's ID in.
// hermesSocketEnv and hermesTokenEnv are the socket it opened for the session and the token that
// opens it (#1184), which `mur tool` and `mur hook` give the daemon in the session's route, as
// Claude Code's messaging socket is given.
const (
	hermesSessionEnv = "MURMUR_HERMES_SESSION"
	hermesSocketEnv  = "MURMUR_HERMES_SOCKET"
	hermesTokenEnv   = "MURMUR_HERMES_TOKEN"
	// hermesNewSocketEnv says the plugin opened the socket for this hook, so the hook registers the
	// session with it, whatever its event.
	hermesNewSocketEnv = "MURMUR_HERMES_NEW_SOCKET"
)

// maxToolArgs is the most of a call's arguments read: a send's body is the largest, and the daemon
// refuses one far smaller, with its reason.
const maxToolArgs = 4 << 20

// toolAgents are the agents `mur tool --agent` takes.
var toolAgents = map[string]bool{"hermes": true}

// ToolList writes Murmur's tools as JSON, a list of objects with name, description and
// inputSchema, as the MCP server lists them.
func ToolList(ctx context.Context, stdout io.Writer) error {
	cs, done, err := inMemory(ctx, &server{direct: func(proto.Request) (proto.Response, error) {
		return proto.Response{}, errors.New("listing only")
	}})
	if err != nil {
		return err
	}
	defer done()
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		return err
	}
	type tool struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		InputSchema any    `json:"inputSchema"`
	}
	out := make([]tool, 0, len(res.Tools))
	for _, t := range res.Tools {
		out = append(out, tool{t.Name, t.Description, t.InputSchema})
	}
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(out)
}

// Tool runs tool name for agent, its arguments a JSON object on stdin, and writes the tool's text
// to stdout. A call the tool answers with an error writes that text too, and returns errToolFailed.
func Tool(ctx context.Context, agent, name string, stdin io.Reader, stdout io.Writer) error {
	if !toolAgents[agent] {
		return fmt.Errorf("`%s tool` is for an agent whose plugin runs %s's tools (hermes), not %q", brand.Command, brand.Product, agent)
	}
	b, err := io.ReadAll(io.LimitReader(stdin, maxToolArgs+1))
	if err != nil {
		return fmt.Errorf("reading the arguments: %w", err)
	}
	if len(b) > maxToolArgs {
		return fmt.Errorf("the arguments are over %d MB", maxToolArgs>>20)
	}
	args := json.RawMessage("{}")
	if strings.TrimSpace(string(b)) != "" {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(b, &obj); err != nil || obj == nil {
			return errors.New("the arguments must be a JSON object")
		}
		args = b
	}
	register, err := hermesSession(ctx)
	if err != nil {
		return err
	}
	// The session is registered when the tool's handler first reaches the daemon, not before: a
	// call the tool refuses (an unknown tool, arguments it can't take) registers nothing.
	var call func(proto.Request) (proto.Response, error)
	direct := func(req proto.Request) (proto.Response, error) {
		if call == nil {
			c, err := register()
			if err != nil {
				return proto.Response{}, err
			}
			call = c
		}
		return call(req)
	}
	cs, done, err := inMemory(ctx, &server{direct: direct})
	if err != nil {
		return err
	}
	defer done()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return err
	}
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			fmt.Fprint(stdout, t.Text)
		}
	}
	if res.IsError {
		return errToolFailed
	}
	return nil
}

// errToolFailed is a tool call the tool answered with an error, whose text is on stdout.
var errToolFailed = errors.New("the tool call failed")

// ToolFailed says err is a call the tool answered with an error, already written out.
func ToolFailed(err error) bool { return errors.Is(err, errToolFailed) }

// inMemory serves s's tools over an in-memory transport and connects a client to them; done closes
// both.
func inMemory(ctx context.Context, s *server) (*mcp.ClientSession, func(), error) {
	srv := mcp.NewServer(&mcp.Implementation{Name: brand.MCPServer}, nil)
	s.tools(srv)
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		return nil, nil, err
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: brand.Command + "-tool"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		ss.Close()
		return nil, nil, err
	}
	return cs, func() { cs.Close(); ss.Close() }, nil
}

// hermesSession checks the Hermes session the plugin names and the clone the working directory is
// in, and returns how to register the session, which returns how to make a request as it (see
// registerCommand).
func hermesSession(ctx context.Context) (func() (func(proto.Request) (proto.Response, error), error), error) {
	id := os.Getenv(hermesSessionEnv)
	if err := hermesSessionOK(id); err != nil {
		return nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	place, err := join.Locate(ctx, cwd)
	if err != nil {
		return nil, err
	}
	if !place.Joined() {
		return nil, errPassive
	}
	return func() (func(proto.Request) (proto.Response, error), error) {
		return registerCommand(ctx, place, hermesHello(id, cwd))
	}, nil
}

// hermesHello is the hello of Hermes session id, at work in cwd, with the socket the plugin opened
// for it, if it names one.
func hermesHello(id, cwd string) proto.Hello {
	native := hook.HermesNative(id)
	r := proto.Route{Native: native}
	if sock, tok := os.Getenv(hermesSocketEnv), os.Getenv(hermesTokenEnv); sock != "" && tok != "" {
		r.MsgSocket, r.MsgToken = sock, tok
	}
	return proto.Hello{ID: nativeID(native), Agent: hook.Hermes, Cwd: cwd, Command: true, Route: r}
}

// HermesHook is `mur hook --agent hermes`, which Murmur's Hermes plugin runs as Hermes' hooks fire
// (#1184). As a turn starts and ends, and for a socket the plugin has just opened, it registers the
// session its payload names, as `mur tool` does, so a session that has called no tool yet is on the
// mesh and has its socket known; then, as for every other hook, it runs the hook for it. In a
// clone that hasn't joined it does nothing, and as any hook it never fails: what goes wrong means no
// answer. registered is false only where it tried to register the session and couldn't, so the
// plugin knows the daemon may not have the socket yet (`mur hook` exits 3; Hermes never sees it).
func HermesHook(event string, stdin io.Reader, stdout io.Writer) (registered bool) {
	registered = true
	defer func() { recover() }()
	b, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil {
		return
	}
	var p struct {
		SessionID string `json:"session_id"`
		Cwd       string `json:"cwd"`
		Event     string `json:"hook_event_name"`
	}
	if json.Unmarshal(b, &p) != nil || hermesSessionOK(p.SessionID) != nil || p.Cwd == "" {
		return
	}
	// Registered as each turn starts and ends, by each `mur tool`, and when the plugin opens a new
	// socket: a daemon that restarted mid-turn knows the session again by the turn's end, and a socket
	// the plugin opened again is known at once. A hook for a tool call otherwise doesn't spend the
	// few git processes registering takes. The plugin names the event in the payload; a hook command
	// may name it instead.
	if event == "" {
		event = p.Event
	}
	if event != "UserPromptSubmit" && event != "Stop" && os.Getenv(hermesNewSocketEnv) == "" {
		hook.RunForEvent(hook.Hermes, event, bytes.NewReader(b), stdout)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), hermesHookRegister)
	defer cancel()
	place, err := join.Locate(ctx, p.Cwd)
	if err != nil {
		return false
	}
	if !place.Joined() {
		return
	}
	if _, err := registerCommand(ctx, place, hermesHello(p.SessionID, p.Cwd)); err != nil {
		return false
	}
	hook.RunForEvent(hook.Hermes, event, bytes.NewReader(b), stdout)
	return
}

// hermesHookRegister bounds the hook's registration, which may start the clone's daemon. The plugin
// gives a hook longer than this and the hook's own requests together.
const hermesHookRegister = 15 * time.Second

// hermesSessionOK checks the session ID the plugin gave: Hermes' are local-<hex>, ses_<...> or a
// gateway's own (gateway-local-sessions.md, read 2026-11-23), all short and printable.
func hermesSessionOK(id string) error {
	if id == "" {
		return fmt.Errorf("%s isn't set: `%s tool` runs for %s's Hermes plugin, which names the session in it", hermesSessionEnv, brand.Command, brand.Product)
	}
	if len(id) > 200 || strings.IndexFunc(id, func(r rune) bool { return !unicode.IsPrint(r) || unicode.IsSpace(r) }) >= 0 {
		return fmt.Errorf("%s isn't a Hermes session ID", hermesSessionEnv)
	}
	return nil
}
