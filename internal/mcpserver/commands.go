package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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

// The shell commands (#913): `mur who`, `mur send`, `mur inbox` and `mur status`, the tools of the
// same names for a harness Murmur has no tools in. Every harness can run a command, and one T3 Code
// runs gives the commands of its thread's shell the thread's ID (T3CODE_THREAD_ID): T3's Claude,
// Codex, Pi, Antigravity, Kiro, fx and ACP-registry drivers do; its Grok, OpenCode, Muse and Cursor
// drivers don't (T3 0.1.11, read 2026-11-21). The session is the thread's, at the address the
// thread's own server would register (threadID), and the daemon holds it while the thread is in use
// (proto.Hello.Command), so a session that only ever runs these keeps its name and gets messages as
// turns through `t3 thread send`, as any T3 thread does. Where the thread already has a session, its
// MCP server's, the commands act as that one. Only these four: lookup, tags and mute stay tools.

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
	render := map[string]func(proto.Response) string{"who": view.Who, "send": view.Sent, "inbox": view.Inbox, "status": view.Note}[name]
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
		return proto.Request{Op: proto.OpStatus, Doing: oneLine(strings.Join(args, " "))}, nil
	case "send":
		return sendRequest(args, stdin, stderr)
	}
	return proto.Request{}, fmt.Errorf("unknown command %q", name)
}

// sendRequest is `mur send TO TEXT`: the message as one text, whose subject subjectFor makes, as
// the page does for a person (#1027). Agents wrote `mur send TO "<paragraph>"` to the old
// `mur send TO SUBJECT [BODY]` anyway (#1021), so the one text is the default and --subject S, the
// option, gives a subject of the agent's own, with the text as the body.
//
// The old form, with three words after send, is the one v0.5.1 documents and agents and scripts
// already run, and the new form has two, so three still mean SUBJECT BODY, as they did. A note on
// stderr gives the new form. Only a call whose text was a literal -, -- or --subject sends
// something different now.
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
		if len(words) == 2 {
			var err error
			if body, err = sendText(words[1], stdin); err != nil {
				return proto.Request{}, err
			}
		}
		req := proto.Request{Op: proto.OpSend, To: words[0]}
		req.Subject, req.Body = subjectAndBody(subject, body)
		return req, nil
	case !given && len(words) == 2:
		text, err := sendText(words[1], stdin)
		if err != nil {
			return proto.Request{}, err
		}
		if strings.TrimSpace(bus.Text(text, len(text))) == "" {
			return proto.Request{}, errors.New("nothing to send: the message is empty")
		}
		req := proto.Request{Op: proto.OpSend, To: words[0]}
		req.Subject, req.Body = subjectFor(text)
		return req, nil
	case !given && len(words) == 3:
		body, err := sendText(words[2], stdin)
		if err != nil {
			return proto.Request{}, err
		}
		fmt.Fprintf(stderr, "%[1]s send TO SUBJECT BODY is the old form, and still works. "+
			"The new one takes the message as one text: %[1]s send TO TEXT, or %[1]s send TO --subject SUBJECT TEXT.\n", brand.Command)
		req := proto.Request{Op: proto.OpSend, To: words[0]}
		req.Subject, req.Body = subjectAndBody(words[1], body)
		return req, nil
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
func commandSession(ctx context.Context) (func(proto.Request) (proto.Response, error), error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	place, err := join.Locate(ctx, cwd)
	if err != nil {
		return nil, err
	}
	if !place.Active() {
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
	c := &client.Client{GitDir: place.GitDir, Spawn: true, MaySpawn: place.Active}
	hctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	conn, resp, err := c.Hello(hctx, h)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("not registered with the %s daemon: %w", brand.Product, err)
	}
	conn.Close()
	_, id := bus.SplitAddr(resp.Self)
	if id == "" {
		id = h.ID
	}
	return func(req proto.Request) (proto.Response, error) {
		req.Session = id
		resp, err := c.Do(ctx, req)
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
