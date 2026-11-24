package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/client"
	"github.com/a-Fig/murmur/internal/join"
	"github.com/a-Fig/murmur/internal/proc"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/t3"
	"github.com/a-Fig/murmur/internal/t3db"
	"github.com/a-Fig/murmur/internal/view"
)

// The shell commands (#913, #1118): `mur who`, `mur send`, `mur inbox`, `mur status`, `mur lookup`, `mur tags` and `mur mute`, the
// tools of the same names for a harness Murmur has no tools in. Every harness can run a command, and one T3 Code
// runs gives the commands of its thread's shell the thread's ID (T3CODE_THREAD_ID): T3's Claude,
// Codex, Pi, Antigravity, Kiro, fx and ACP-registry drivers do; its Grok, OpenCode, Muse and Cursor
// drivers don't (T3 0.1.11, read 2026-11-21). The session is the thread's, at the address the
// thread's own server would register (threadID), and the daemon holds it while the thread is in use
// (proto.Hello.Command), so a session that only ever runs these keeps its name and gets messages as
// turns through `t3 thread send`, as any T3 thread does. Where the thread already has a session, its
// MCP server's, the commands act as that one.

// errNoThread is a command run where no T3 thread names its session.
var errNoThread = fmt.Errorf("this command finds its session from %s, which T3 Code sets in a thread's shell, and it isn't set here. "+
	"In an agent %s has tools in, use the tools instead", t3.ThreadEnv, brand.Product)

// errInheritedThread is a command run by an agent that another agent started in a T3 thread's
// shell: the variable is that thread's, and the command isn't.
var errInheritedThread = fmt.Errorf("%s here is the T3 thread of an agent above this one, which started it, so it doesn't name this session", t3.ThreadEnv)

// Command runs the shell command name with args, reading a message from stdin where asked and
// writing what the tool would answer to stdout, and a note on how it was asked to stderr.
func Command(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	req, err := commandRequest(name, args, stdin, stderr)
	if err != nil {
		return err
	}
	call, err := commandSession(ctx)
	if err != nil {
		return err
	}
	if name == "send" {
		to := recipients(req.To)
		if len(to) > 1 {
			out, sent := sendEachVia(call, to, req.Subject, req.Body, "", nil)
			fmt.Fprint(stdout, out)
			if !sent {
				return errors.New("not sent")
			}
			return nil
		}
		if len(to) == 1 {
			req.To = to[0]
		}
	}
	resp, err := call(req)
	if err != nil {
		return err
	}
	render := map[string]func(proto.Response) string{"who": view.Who, "send": view.Sent, "inbox": view.Inbox, "status": view.Note,
		"lookup": view.Lookup, "tags": view.Tags, "mute": view.Muted}[name]
	fmt.Fprint(stdout, render(resp))
	return nil
}

// commandRequest is the daemon request command name makes of its arguments.
func commandRequest(name string, args []string, stdin io.Reader, stderr io.Writer) (proto.Request, error) {
	switch name {
	case "who":
		return proto.Request{Op: proto.OpWho, Tags: args}, nil
	case "inbox":
		if len(args) > 0 {
			return proto.Request{}, errors.New("inbox takes no arguments")
		}
		return proto.Request{Op: proto.OpInbox}, nil
	case "status":
		doing, err := statusLine(strings.Join(args, " "))
		return proto.Request{Op: proto.OpStatus, Doing: doing}, err
	case "send":
		return sendRequest(args, stdin, stderr)
	case "lookup":
		return lookupRequest(args)
	case "tags":
		return tagsRequest(args)
	case "mute":
		return muteRequest(args)
	}
	return proto.Request{}, fmt.Errorf("unknown command %q", name)
}

// options takes the named options out of a command's arguments and returns the words left. A name
// in valued takes the next argument or what follows an = ("--messages 5", "--messages=5", and with
// one dash too, as send's --subject does); a name in flags takes nothing. A valued option given
// twice has both values. -- ends the options. An argument that starts with a dash and is no option
// is refused, so that a mistyped option isn't taken for a name.
func options(args []string, valued, flags []string) (words []string, values map[string][]string, set map[string]bool, err error) {
	values, set = map[string][]string{}, map[string]bool{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			words = append(words, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			words = append(words, a)
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(a, "-"), "-"), "=")
		switch {
		case slices.Contains(valued, name):
			if !hasValue {
				if i+1 == len(args) {
					return nil, nil, nil, fmt.Errorf("--%s needs a value after it", name)
				}
				i++
				value = args[i]
			}
			values[name] = append(values[name], value)
		case slices.Contains(flags, name) && !hasValue:
			set[name] = true
		default:
			return nil, nil, nil, fmt.Errorf("unknown option %s", a)
		}
	}
	return words, values, set, nil
}

// lookupRequest is `mur lookup NAME [--messages N]`: the lookup tool's name and messages.
func lookupRequest(args []string) (proto.Request, error) {
	usage := fmt.Errorf("usage: %s lookup NAME [--messages N]", brand.Command)
	words, values, _, err := options(args, []string{"messages"}, nil)
	if err != nil {
		return proto.Request{}, fmt.Errorf("%w; %v", err, usage)
	}
	if len(words) != 1 || len(values["messages"]) > 1 {
		return proto.Request{}, usage
	}
	req := proto.Request{Op: proto.OpLookup, Name: words[0]}
	if v := values["messages"]; len(v) == 1 {
		n, err := strconv.Atoi(v[0])
		if err != nil || n < 1 {
			return proto.Request{}, fmt.Errorf("--messages takes a number of messages, 1 or more, not %q", v[0])
		}
		req.Limit = n
	}
	return req, nil
}

// sendRequest is `mur send TO TEXT`: the message as one text, whose subject subjectFor makes, as
// the page does for a person (#1027). Agents wrote `mur send TO "<paragraph>"` to the old
// `mur send TO SUBJECT [BODY]` anyway (#1021), so the one text is the default and --subject S, the
// option, gives a subject of the agent's own, with the text as the body.
//
// The old form, with three words after send, is the one v0.5.1 documents and agents and scripts
// already run, and the new form has two, so three still mean SUBJECT BODY, as they did. A note on
// stderr gives the new form. Only `TO -`, which now reads stdin, and a call with a word that is --
// or the option, which now mean what they say here, are taken differently than before.
func sendRequest(args []string, stdin io.Reader, stderr io.Writer) (proto.Request, error) {
	var words []string
	subject, given := "", false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			words = append(words, args[i+1:]...)
			break
		}
		if v, ok := strings.CutPrefix(a, "--subject="); ok {
			subject, given = v, true
			continue
		}
		if v, ok := strings.CutPrefix(a, "-subject="); ok {
			subject, given = v, true
			continue
		}
		if a == "--subject" || a == "-subject" {
			if i+1 == len(args) {
				return proto.Request{}, errors.New("--subject needs the subject after it")
			}
			subject, given = args[i+1], true
			i++
			continue
		}
		words = append(words, a)
	}
	if given && oneLine(subject) == "" {
		// The daemon's refusal would say the message needs a subject, which reads as wrong when one
		// was given.
		return proto.Request{}, errors.New("--subject is empty: give a subject, or leave --subject out and the text's opening words are the subject")
	}
	switch {
	case given && (len(words) == 1 || len(words) == 2):
		body := ""
		var err error
		if len(words) == 2 {
			if body, err = sendText(words[1], stdin); err != nil {
				return proto.Request{}, err
			}
		}
		req := proto.Request{Op: proto.OpSend, To: words[0]}
		req.Subject, req.Body, err = subjectAndBody(subject, body)
		return req, err
	case !given && len(words) == 2:
		text, err := sendText(words[1], stdin)
		if err != nil {
			return proto.Request{}, err
		}
		if strings.TrimSpace(bus.Text(text, math.MaxInt)) == "" { // judged whole: a malformed byte cleans to 3 (#1037)
			return proto.Request{}, errors.New("nothing to send: the message is empty")
		}
		req := proto.Request{Op: proto.OpSend, To: words[0]}
		req.Subject, req.Body, err = subjectFor(text)
		return req, err
	case !given && len(words) == 3:
		body, err := sendText(words[2], stdin)
		if err != nil {
			return proto.Request{}, err
		}
		fmt.Fprintf(stderr, "%[1]s send TO SUBJECT BODY is the old form, and still works. "+
			"The new one takes the message as one text: %[1]s send TO TEXT, or %[1]s send TO --subject SUBJECT TEXT.\n", brand.Command)
		req := proto.Request{Op: proto.OpSend, To: words[0]}
		req.Subject, req.Body, err = subjectAndBody(words[1], body)
		return req, err
	}
	return proto.Request{}, fmt.Errorf("usage: %[1]s send TO TEXT, or %[1]s send TO --subject SUBJECT [TEXT], with TEXT in quotes; - as TEXT reads it from standard input", brand.Command)
}

// sendText is a send's text: the word given, or standard input for -.
func sendText(word string, stdin io.Reader) (string, error) {
	if word != "-" {
		return word, nil
	}
	// A little over the most the daemon takes, so that a longer text is refused by it, with its
	// reason, and not cut here.
	b, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil {
		return "", fmt.Errorf("reading the message: %w", err)
	}
	return string(b), nil
}

// commandSession registers the session of the T3 thread the command runs in, and returns how to
// make a request as it. The hello's connection closes as soon as it is answered: the daemon holds
// the session (proto.Hello.Command).
//
// The thread's own MCP server can register between the hello and a request, at an ID of its own,
// as Codex's does, and the daemon then lets the command's session go (#953). A request the daemon
// refuses as not registered (proto.NotRegistered) is made again, once, after a new hello, which
// finds the thread's session as it is then. That refusal comes before the request does anything,
// so the second try can't send a message twice. A request that fails any other way, a lost
// connection included, isn't made again: it may have done what it asked.
func commandSession(ctx context.Context) (func(proto.Request) (proto.Response, error), error) {
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
	thread := os.Getenv(threadEnv)
	if thread == "" {
		return nil, errNoThread
	}
	if commandAgentsAbove() > 1 {
		return nil, errInheritedThread
	}
	cli := os.Getenv("T3CODE_CLI_PATH")
	if cli == "" {
		cli = t3Above()
	}
	if cli == "" {
		return nil, errors.New("can't find T3's command line (t3), which hands this thread its messages: T3CODE_CLI_PATH isn't set, and no t3 is above this command or on PATH")
	}
	home := os.Getenv("T3CODE_HOME")
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	_, err = commandThreadState(sctx, cli, home, thread)
	cancel()
	if errors.Is(err, t3db.ErrNotFound) {
		return nil, fmt.Errorf("T3's database has no thread %s, which %s names", thread, threadEnv)
	} // any other failure to read it leaves the daemon to find out. The daemon reads the thread's
	// state itself as the session registers, so a delegate is one from its first command (#945).
	h := proto.Hello{ID: threadID(thread), Agent: "agent in T3", Cwd: cwd, ByThread: true, Command: true,
		Route: proto.Route{T3CLI: cli, T3Home: home, T3Thread: thread}}
	return registerCommand(ctx, place, h)
}

// registerCommand says hello h for a command in clone place, and returns how to make a request as
// the session it registers (see commandSession).
func registerCommand(ctx context.Context, place join.Place, h proto.Hello) (func(proto.Request) (proto.Response, error), error) {
	c := &client.Client{GitDir: place.GitDir, Spawn: true, MaySpawn: place.Joined}
	hello := func() (string, error) {
		hctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		conn, resp, err := c.Hello(hctx, h)
		cancel()
		if err != nil {
			// A daemon kept as older (#1197) refuses what it doesn't know, such as a hello keyed by
			// Native (`mur tool`): why it was kept says more than its refusal.
			if why := c.Kept(); why != "" {
				return "", fmt.Errorf("not registered with the %s daemon: %s", brand.Product, why)
			}
			return "", fmt.Errorf("not registered with the %s daemon: %w", brand.Product, err)
		}
		conn.Close()
		if _, id := bus.SplitAddr(resp.Self); id != "" {
			return id, nil
		}
		return h.ID, nil
	}
	id, err := hello()
	if err != nil {
		return nil, err
	}
	retried := false
	return func(req proto.Request) (proto.Response, error) {
		req.Session = id
		resp, err := c.Do(ctx, req)
		if err == nil && resp.Error == proto.NotRegistered && !retried {
			retried = true // once a command: a session that won't stay registered is for the daemon to explain
			if id, err = hello(); err != nil {
				return resp, err
			}
			req.Session = id
			resp, err = c.Do(ctx, req)
		}
		if err != nil {
			return resp, fmt.Errorf("can't reach the %s daemon: %w", brand.Product, err)
		}
		if resp.Error != "" {
			return resp, errors.New(resp.Error)
		}
		return resp, nil
	}, nil
}

// commandThreadState reads the thread in T3's database; a variable, so that tests needn't have one.
var commandThreadState = t3db.StateOf

// t3Above is the t3 above the command, for T3's headless server, which sets no T3CODE_CLI_PATH;
// a variable, for tests.
var t3Above = func() string { return t3.Above(os.Getppid(), 12, exec.LookPath) }

// commandAgentsAbove counts the agents' programs above the command, through shells and the like
// (see agentAbove). The thread's own agent is one. Two say the command's agent was started by
// another, in whose thread's shell T3CODE_THREAD_ID was set, and which may be another clone's or
// none of T3's. A variable, for tests.
var commandAgentsAbove = func() int {
	pids := proc.Ancestors(os.Getppid(), 12)
	return agentsInChain(len(pids), func(i int) (exe, comm string, ok bool) {
		exe, err := proc.Exe(pids[i])
		return exe, commOf(pids[i]), err == nil
	})
}

// agentsInChain is commandAgentsAbove's walk over n processes, the command's parent first, each
// read by at. It stops at a program that is neither an agent's nor one it looks past. Agents'
// programs one above the other count once: Antigravity's agy_acp_server runs its commands through
// localharness_external (see agentPrograms), and an agent another starts runs under a shell.
func agentsInChain(n int, at func(i int) (exe, comm string, ok bool)) int {
	agents, last := 0, false
	for i := range n {
		exe, comm, ok := at(i)
		if !ok {
			break
		}
		name := programName(exe)
		agent := agentPrograms[name] || agentPrograms[comm]
		if !agent && !passThrough[name] {
			break
		}
		if agent && !last {
			agents++
		}
		last = agent
	}
	return agents
}

// tagsRequest is `mur tags [--add TAG...] [--remove TAG...] [--for NAME...]`: the tags tool's add,
// remove and for. Each takes tags or names separated by commas, and may be given more than once;
// with none, the command only says what your tags are, as the tool does.
func tagsRequest(args []string) (proto.Request, error) {
	usage := fmt.Errorf("usage: %s tags [--add TAG,...] [--remove TAG,...] [--for NAME,...]", brand.Command)
	words, values, _, err := options(args, []string{"add", "remove", "for"}, nil)
	if err != nil {
		return proto.Request{}, fmt.Errorf("%w; %v", err, usage)
	}
	if len(words) > 0 {
		return proto.Request{}, usage
	}
	list := func(name string) []string {
		var out []string
		for _, v := range values[name] {
			out = append(out, recipients(v)...)
		}
		return out
	}
	return proto.Request{Op: proto.OpTags, Add: list("add"), Remove: list("remove"), For: list("for")}, nil
}

// muteRequest is `mur mute [NAME] [--unmute]`: the mute tool's name and unmute. With no name it
// only lists whom you have muted, as the tool does.
func muteRequest(args []string) (proto.Request, error) {
	usage := fmt.Errorf("usage: %s mute [NAME] [--unmute]", brand.Command)
	words, _, set, err := options(args, nil, []string{"unmute"})
	if err != nil {
		return proto.Request{}, fmt.Errorf("%w; %v", err, usage)
	}
	if len(words) > 1 {
		return proto.Request{}, usage
	}
	if len(words) == 0 && set["unmute"] {
		return proto.Request{}, fmt.Errorf("--unmute needs the name of the session to unmute; %v", usage)
	}
	req := proto.Request{Op: proto.OpMute, Unmute: set["unmute"]}
	if len(words) == 1 {
		req.Name = words[0]
	}
	return req, nil
}
