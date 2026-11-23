package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-Fig/murmur/internal/brand"
)

// ToolInfo is one tool as `mur help` lists it: its name, as an agent's client puts it after
// mcp__murmur__, and a line on what it does.
type ToolInfo struct{ Name, Summary string }

// toolList is every tool tools registers, in the order help shows them. A test checks it against
// what the server offers, so a tool added without a line here fails; `mur help` is built from it, so
// the line is all a new tool needs to appear there (#1103).
var toolList = []ToolInfo{
	{"who", "list the sessions in this repo and what each is doing"},
	{"lookup", "everything known about one session: its work, branches and messages"},
	{"status", "set the line others see in who for what you are working on"},
	{"tags", "add or remove your tags, and join or leave a group"},
	{"send", "message a session, a group, a device or everyone"},
	{"inbox", "read the messages that came as notices"},
	{"mute", "stop getting one session's messages"},
}

// toolNames are the tools tools lists, for taking them away again.
var toolNames = func() []string {
	names := make([]string, len(toolList))
	for i, t := range toolList {
		names[i] = t.Name
	}
	return names
}()

// Tools are the tools this server offers, in the order `mur help` lists them.
func Tools() []ToolInfo { return append([]ToolInfo(nil), toolList...) }

// ToolDescription is what the server tells an agent's client a tool does, as the client lists it:
// read from the registration itself, so `mur help TOOL` can't say something else.
func ToolDescription(name string) (string, error) {
	tools, err := registered()
	if err != nil {
		return "", err
	}
	for _, t := range tools {
		if t.Name == name {
			return t.Description, nil
		}
	}
	return "", fmt.Errorf("no tool %q", name)
}

// registered lists the tools tools registers, as a client of the server would see them.
func registered() ([]*mcp.Tool, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := mcp.NewServer(&mcp.Implementation{Name: brand.MCPServer}, nil)
	(&server{}).tools(srv)
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		return nil, err
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "help"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		return nil, err
	}
	defer cs.Close()
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		return nil, err
	}
	return res.Tools, nil
}
