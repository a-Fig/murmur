// This file is the tools as clients see them: their names, descriptions and input schemas, and
// the handler each one runs.

package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/view"
)

// stringList is a tool argument that holds a list of strings. A harness may send it as a
// string instead of an array — Muse sends the JSON array written into a string
// (#698) — and the SDK validates arguments against the tool's schema before the handler
// runs, so the affected tools carry a schema that lets a string through (listArgSchema)
// and this type does the parsing the validation cannot: a JSON array in the string, or
// values separated by commas, or one value, parsed as recipients parses send's to.
type stringList []string

func (l *stringList) UnmarshalJSON(b []byte) error {
	var arr []string
	if err := json.Unmarshal(b, &arr); err == nil {
		*l = arr
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*l = recipients(s)
	return nil
}

// pathList is a tool argument that holds send's files. As stringList, but a path may
// hold a comma, so a string that is not a JSON array is one path, never values to split.
type pathList []string

func (l *pathList) UnmarshalJSON(b []byte) error {
	var arr []string
	if err := json.Unmarshal(b, &arr); err == nil {
		*l = arr
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if t := strings.TrimSpace(s); strings.HasPrefix(t, "[") && json.Unmarshal([]byte(t), &arr) == nil {
		*l = arr
	} else if s != "" {
		*l = pathList{s}
	}
	return nil
}

// listArgSchema is the schema of a stringList or pathList argument: an array of strings,
// or the same list as a string, or null, which the generated schema allowed and the
// types above read as no list. The description stays the argument's own, so an agent
// reading the tools learns both shapes from it.
func listArgSchema(description string) *jsonschema.Schema {
	return &jsonschema.Schema{
		Description: description,
		AnyOf: []*jsonschema.Schema{
			{Type: "array", Items: &jsonschema.Schema{Type: "string"}},
			{Type: "string"},
			{Type: "null"},
		},
	}
}

// objectSchema is an input schema that refuses any property it doesn't name, as the
// generated schemas do: a typo in an argument is an error, not quietly dropped.
func objectSchema(properties map[string]*jsonschema.Schema, required ...string) *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:                 "object",
		Properties:           properties,
		Required:             required,
		AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
	}
}

type (
	noInput struct{}
	// who, tags and send name their argument descriptions in their tools' schemas (see
	// tools), not here, so the two can't drift apart (#700 review).
	whoIn struct {
		Tags stringList `json:"tags,omitempty"`
	}
	tagsIn struct {
		Add    stringList `json:"add,omitempty"`
		Remove stringList `json:"remove,omitempty"`
		For    stringList `json:"for,omitempty"`
	}
	muteIn struct {
		Name   string `json:"name,omitempty" jsonschema:"the name of the session to mute (quiet-otter, from who); leave it out to list whom you have muted"`
		Unmute bool   `json:"unmute,omitempty" jsonschema:"true to undo your mute of that session"`
	}
	statusIn struct {
		Doing string `json:"doing" jsonschema:"what you are working on now, in one line"`
	}
	lookupIn struct {
		Name     string `json:"name" jsonschema:"a session's name from who (quiet-otter), or its device/session address"`
		Messages int    `json:"messages,omitempty" jsonschema:"how many of its latest messages to show, 10 if not given"`
	}
	sendIn struct {
		To      string   `json:"to"`
		Subject string   `json:"subject"`
		Body    string   `json:"body,omitempty"`
		TellMe  string   `json:"tell_me,omitempty"`
		Files   pathList `json:"files,omitempty"`
	}
)

func (s *server) tools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{Name: "who", Description: "Show the agents working in this repo across the team: each device and " +
		"session, its name, agent and branch, when it was last active and what it is doing, and which one is you. " +
		"Lists the 20 most recently active sessions, with their tags. Use it to find who to message; give tags to list " +
		"only the sessions that have them.",
		InputSchema: objectSchema(map[string]*jsonschema.Schema{
			"tags": listArgSchema("list only sessions with every one of these tags; -tag for those without it, and a * at the end for any tag that starts so (model:opus*). An array, or the same list as a string."),
		})},
		func(ctx context.Context, req *mcp.CallToolRequest, in whoIn) (*mcp.CallToolResult, any, error) {
			resp, err := s.callFrom(ctx, req, proto.Request{Op: proto.OpWho, Tags: []string(in.Tags)})
			return result(view.Who, resp, err)
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "lookup", Description: "Look up everything known about a session, including your own, by its name from who " +
		"(or device/session): what it is doing, its branch and the branches it has committed to, when it " +
		"started and was last active, whether a message would reach it, its known file settings, and its latest messages to and from " +
		"others. Use it before messaging a session you don't know, to tell whether it is working on what you need " +
		"and is still there, or when a session you sent to has gone quiet. For a session on this device it " +
		"also gives its worktree folder and the commits it made."},
		func(ctx context.Context, req *mcp.CallToolRequest, in lookupIn) (*mcp.CallToolResult, any, error) {
			resp, err := s.callFrom(ctx, req, proto.Request{Op: proto.OpLookup, Name: in.Name, Limit: in.Messages})
			return result(view.Lookup, resp, err)
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "status", Description: "Set the one line other agents see in who for what you are working on. " +
		"Update it when you switch tasks. Until you set one, your conversation's title is what they see; " +
		"an empty status brings the title back."},
		func(ctx context.Context, req *mcp.CallToolRequest, in statusIn) (*mcp.CallToolResult, any, error) {
			doing, err := statusLine(in.Doing)
			if err != nil {
				return result(nil, proto.Response{}, err)
			}
			resp, err := s.callFrom(ctx, req, proto.Request{Op: proto.OpStatus, Doing: doing})
			return result(view.Note, resp, err)
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "tags", Description: "Add or remove your own tags: short labels others see in who and lookup, " +
		"and can search who by. model:<your model> says which model you are (a session has one). do-not-disturb keeps messages to " +
		"you queued in your inbox: none comes as a turn or a notice until you read your inbox or take the tag off, " +
		"when they come the usual way; your user's, from this device's page, still come. A tag that starts with group: puts you in that group: send takes the tag as an address and reaches every " +
		"session that has it, so add it to join a group and remove it to leave. To start a group or bring others into one, give their names in for: " +
		"they are added, and can leave the same way. A tag a human set on the page stands over yours, but you can always leave a group.",
		InputSchema: objectSchema(map[string]*jsonschema.Schema{
			"add":    listArgSchema("tags to put on yourself: lowercase letters, digits and . _ : / - (reviewing, model:opus-5.5, do-not-disturb, group:ui). An array, or the same list as a string."),
			"remove": listArgSchema("tags to take off yourself. An array, or the same list as a string."),
			"for":    listArgSchema("other sessions to add to a group, by name from who: add then holds group: tags only, and goes to them, not to you unless you list your own name too. An array, or the same list as a string."),
		})},
		func(ctx context.Context, req *mcp.CallToolRequest, in tagsIn) (*mcp.CallToolResult, any, error) {
			resp, err := s.callFrom(ctx, req, proto.Request{Op: proto.OpTags, Add: []string(in.Add), Remove: []string(in.Remove), For: []string(in.For)})
			return result(view.Tags, resp, err)
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "send", Description: "Send a message to another agent by its name (quiet-otter, from who), to a group (group:ui: " +
		"every session with that tag), to all sessions on a device (its group, group:device-…, from who), or to everyone. It usually reaches the recipient as a turn of its own, headed \"Message from <your name> (...) via " + brand.Product + "\", " +
		"with your subject and then your body. A recipient that is at work, or that " + brand.Product + " can't start a turn in, gets a one-line notice with " +
		"your subject after its next tool call and reads the body with inbox; what it hasn't read when it finishes is usually given to it then, and its turn goes on. " +
		"To send the same message to several sessions, name them all, separated by commas: each gets an ordinary message from you, " +
		"not told who else got it, and the result says what became of each. files sends files along, which the recipient gets as paths on its own machine: " +
		"files are off by default until your user turns them on in " + brand.Product + "'s Settings, and " +
		"each file's text is checked for secrets as the body is, but nothing compressed, as an image or a zip, is unpacked to check. Message to coordinate, not to chat.",
		InputSchema: objectSchema(map[string]*jsonschema.Schema{
			"to":      {Type: "string", Description: "a session's name from who (quiet-otter); device/session works too; a group (group:ui) for every session with that tag, a device's group (group:device-…, from who) for all its sessions, or * for everyone; or several names separated by commas (quiet-otter, brisk-finch), each sent the message on its own"},
			"subject": {Type: "string", Description: "one line of at most 72 characters that stands on its own: it is the first thing the recipient reads, and all they see if the message comes as a notice"},
			"body":    {Type: "string", Description: "the rest of the message"},
			"tell_me": {Type: "string", Description: "read or delivered, to be told once, in your inbox, when every recipient has read the message (or been told of it), or after 30 minutes with where it is; not for a message to everyone. Leave it out unless you need to know, as when handing off work"},
			"files":   listArgSchema("paths of files to send with it, such as a screenshot or a log, in this repo or the temp folder: at most 8, 10 MB each, 20 MB together. An array, or the same list as a string."),
		}, "to", "subject")},
		func(ctx context.Context, req *mcp.CallToolRequest, in sendIn) (*mcp.CallToolResult, any, error) {
			to := recipients(in.To)
			files := fullPaths([]string(in.Files))
			tell := strings.TrimSpace(in.TellMe)
			subject, body, err := subjectAndBody(in.Subject, in.Body)
			if errors.Is(err, errTooLong) {
				err = fmt.Errorf("%w, or put the text in a file and send that with files", err)
			}
			if err != nil {
				return result(nil, proto.Response{}, err)
			}
			if len(to) > 1 {
				return s.sendEach(ctx, req, to, subject, body, tell, files)
			}
			if len(to) == 1 {
				in.To = to[0]
			}
			resp, err := s.callFrom(ctx, req, proto.Request{Op: proto.OpSend, To: in.To, Subject: subject, Body: body, TellMe: tell, Files: files})
			return result(view.Sent, untold(resp, err, tell), err)
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "inbox", Description: "Read your unread messages from other agents, with the name of who sent them. " +
		"A message that arrived as a turn is already read and isn't here; this is for the ones that came as a notice. " +
		"Reading removes them from the inbox."},
		func(ctx context.Context, req *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, any, error) {
			resp, err := s.callFrom(ctx, req, proto.Request{Op: proto.OpInbox})
			return result(view.Inbox, resp, err)
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "mute", Description: "Stop getting anything another session sends: its messages to you, to your device and to everyone. " +
		"For a session whose messages keep interrupting your work after you have asked it to stop; do-not-disturb (tags) is for holding everyone's for a while. " +
		"The muted session isn't notified unless it sends to you again, when " + brand.Product + " tells it you have muted it. Mutes show in lookup and on the page, where your user can undo one."},
		func(ctx context.Context, req *mcp.CallToolRequest, in muteIn) (*mcp.CallToolResult, any, error) {
			resp, err := s.callFrom(ctx, req, proto.Request{Op: proto.OpMute, Name: in.Name, Unmute: in.Unmute})
			return result(view.Muted, resp, err)
		})
}
