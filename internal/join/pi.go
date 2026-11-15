package join

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/jsonedit"
)

// Pi (the pi coding agent, 0.99) has MCP built in: servers in mcp.json in its agent directory
// (~/.pi/agent, or $PI_CODING_AGENT_DIR), one server process per session, with Pi's environment.
// But nothing in that process says which session it is: Pi gives the session's ID and file only to
// its own shell tools, and T3 Code knows a Pi thread by the session's file. So join registers
// Murmur's server through a Pi extension instead, a file of Murmur's own in the agent directory's
// extensions/, which Pi loads in every session: it registers the server as Pi loads it, which makes
// it a server Pi waits for before the first prompt, and again at each session's start with the
// session's file in MURMUR_PI_SESSION_FILE (pi.registerMcpServer, docs/extensions.md and
// docs/mcp.md of Pi's package, read 2026-11-01; Pi 0.99.0 showed it connected with its five tools,
// 2026-11-01). "exposure": "direct" lists the tools themselves; Pi's
// default hides them behind a tool of its own. Pi asks before no tool call; in T3, T3's own Pi
// extension asks before each one outside full-access mode. A server named murmur in mcp.json would
// win over the extension's, and its sessions would all look like one.

// The extension also brings a session its messages, as Murmur's hook does in Claude Code (#202,
// #205): Pi has no hooks of that kind, so the extension runs `mur hook --agent pi` at the same
// moments, with Claude Code's event names and the session's file for its ID (package hook), and
// opens a socket of its own that takes Claude Code's messaging-socket lines (package msgsock),
// whose address and token it gives the MCP server in its environment as Claude Code does. From
// Pi's docs/extensions.md and the types of @earendil-works/pi-coding-agent 1.5.0, read
// 2026-11-09, and run with Pi 1.5.0 against a stub model the same day, in RPC mode and in its
// terminal UI (the PR that added this says what was run):
//   - before_agent_start, which every prompt and every turn the extension starts goes through, is
//     UserPromptSubmit: the session is at work, and a notice goes in as the run's message;
//   - tool_result is PostToolUse: a notice is sent with deliverAs "steer", which Pi puts in after
//     the tool calls and before the next model request;
//   - agent_before_settle, the last point at which Pi lets an extension add entries and continue,
//     is Stop: what the hook gives back keeps the work going, as a custom_message entry and
//     continue: true. After an error, which ends Pi's run whatever it asks, and after an abort
//     (Escape), which skips agent_before_settle and shows as agent_settled's aborted, the hook's
//     Stop says it was interrupted instead: the daemon takes the session for idle, gives nothing
//     back, and hands over what is unread as a turn, as for a T3 turn the user stopped;
//   - a message on the socket is sent with triggerTurn, which starts a turn in an idle session, and
//     deliverAs "followUp", which holds it until a busy one finishes rather than steering it.
// Everything Murmur says goes in as a custom message with customType "murmur", shown in the
// session, which the model reads in the user's role but which Pi doesn't take for the user's
// typing, as it would sendUserMessage's (#205). The extension is in every Pi session, so it runs
// the hook and opens the socket only in a clone the user joined (its .git/murmur/joined.json):
// elsewhere a tool call doesn't wait on a process of Murmur's. It looks for the consent as the
// session starts, then each second and at each prompt and tool call until it is there, since the
// user often joins from inside a running session; the socket's address and token are chosen at the
// start and given to the MCP server, which registers only once the clone is joined. Under T3
// the socket is there too, and unused: the daemon reaches a T3 session through T3
// (internal/daemon, viaOf). An update doesn't run join again, so Pi's MCP server rewrites an
// older extension (RefreshPi), which Pi loads from its next session. A session without a file
// (`pi --no-session`) has nothing to give the hook, so it gets no notices, only turns through the
// socket, which a busy one takes when it finishes. Not covered: Pi's commits aren't credited to
// its session, as no tool call is bracketed (Claude Code's hook snapshots before and after each).
//
// Pi has no subagents of its own (its docs and source, 0.99.0, read 2026-11-11). Packages add
// them. pi-subagents (0.31.0, the one `pi install npm:pi-subagents` gives) runs each subagent as a
// child `pi --mode json -p`, with the user's extensions unless the agent's definition lists its
// own, and PI_SUBAGENT_CHILD=1 in its environment: such a child would load this extension,
// register Murmur's server, and show in who as a session, one per subagent (#457). The extension
// does nothing in a process with that variable, so the child has no Murmur tools, hook or socket
// and never reaches the daemon; a T3 delegate does, and the daemon keeps it off (#188). Not covered: @gotgenes/pi-subagents (16.6.0), which makes its
// subagents inside the parent's process with Pi's SDK and binds the parent's extensions to each.
// Nothing in the environment tells such a session from its parent, and the parentSession its file
// records is what a /fork records too.

// piExtension is the extension's text for the mur at exe.
func piExtension(exe string) string {
	cmd, _ := json.Marshal(exe)
	return strings.NewReplacer("{{product}}", brand.Product, "{{command}}", brand.Command, "{{server}}", brand.MCPServer,
		"{{state}}", brand.StateDir, "{{consent}}", consentFile, "{{exe}}", string(cmd)).Replace(piTemplate)
}

// piTemplate is the extension, with its names left for package brand to fill in.
const piTemplate = `// Written by ` + "`{{command}} join`" + ` ({{product}}, https://github.com/a-Fig/murmur). ` + "`{{command}} join`" + ` writes it again when {{command}} moves,
// and {{command}} when it is updated, so edits here are lost; take this first line out to keep yours.
// It registers {{product}}'s MCP server for each Pi session, with the session's file in its
// environment: that is how {{product}} tells Pi sessions apart, and finds a session's T3 Code thread.
// It also brings the session its messages: a notice after a tool call while it works, what it hasn't
// read when its work ends, and a turn when it is idle, through a socket it opens for the session.
// Delete this file to take {{product}} out of Pi.
import { spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { chmodSync, existsSync, readFileSync, rmSync, statSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";

const mur = {{exe}};
const windows = process.platform === "win32";

// joined says the user joined {{product}} in the clone that dir is in, as ` + "`{{command}} join`" + ` records it in
// the clone's .git: outside one, the extension runs no hook and opens no socket. A few stats, no process.
const joined = (dir: string): boolean => {
  try {
    for (let d = resolve(dir); ; d = dirname(d)) {
      const dot = join(d, ".git");
      if (existsSync(dot)) {
        let git = dot;
        if (!statSync(dot).isDirectory()) {
          const m = /^gitdir:\s*(.+)$/m.exec(readFileSync(dot, "utf8"));
          if (!m) return false;
          git = resolve(d, m[1].trim());
          const common = join(git, "commondir");
          if (existsSync(common)) git = resolve(git, readFileSync(common, "utf8").trim());
        }
        return existsSync(join(git, "{{state}}", "{{consent}}"));
      }
      if (dirname(d) === d) return false;
    }
  } catch {
    return false;
  }
};

// sockPath is where the session's socket goes: a named pipe on Windows, else a file in the
// temporary directory, or in /tmp where that path would be too long for a socket's address.
const sockPath = (id: string): string => {
  if (windows) return "\\\\.\\pipe\\murmur-pi-" + id;
  const p = join(tmpdir(), "murmur-pi-" + id + ".sock");
  return Buffer.byteLength(p) <= 100 ? p : join("/tmp", "murmur-pi-" + id + ".sock");
};

export default function (pi: any) {
  // A subagent stays off {{product}}: the pi-subagents package runs each one as a pi of its own, which
  // loads this extension as any pi does, and would be a session of its own to everyone else. Its
  // parent is the one on {{product}}, and says what the subagent needs said.
  if (process.env.PI_SUBAGENT_CHILD === "1") return;
  let file = "";
  let cwd = "";
  // on says the session is in a joined clone: the hook runs and the socket is open.
  let on = false;
  let sock = "";
  let token = "";
  let server: any = null;
  // conns are the open server's connections, which close ends too: server.close() only stops new
  // ones, and a connection to an earlier session's socket would otherwise start a turn in this one.
  let conns = new Set<any>();
  let watch: any = null;
  // ended says the end of the work has been told to the daemon since it last started.
  let ended = false;
  const register = () =>
    pi.registerMcpServer("{{server}}", {
      command: mur,
      args: ["mcp", "--agent", "pi"],
      exposure: "direct",
      env: { MURMUR_PI_SESSION_FILE: file, MURMUR_PI_MESSAGING_SOCKET: sock, MURMUR_PI_MESSAGING_TOKEN: token },
    });
  // Registered as Pi loads, the server is one Pi waits for before the first prompt; registered
  // again when the session starts, it has the session's file and socket.
  register();

  // What {{product}} says: a message of its own, shown in the session.
  const said = (content: string) => ({ customType: "{{server}}", content, display: true });

  // hook runs {{product}}'s hook for one of Claude Code's events and gives what it printed, parsed;
  // null on any trouble, or when the session isn't in a joined clone or has no file to be known by.
  // Only a notice's hook stops with the work (Escape): the daemon counts what the end of the work
  // gives as read once it answers, so that hook is left to finish.
  const hook = (event: string, ctx: any, extra: any = {}): Promise<any> =>
    new Promise((resolve) => {
      if (!on || !file) return resolve(null);
      let out = "";
      try {
        const child = spawn(mur, ["hook", "--agent", "pi"], {
          cwd: ctx?.cwd, stdio: ["pipe", "pipe", "ignore"], windowsHide: true, timeout: 10000,
          signal: event === "Stop" ? undefined : ctx?.signal,
        });
        child.stdout.on("data", (b: any) => (out += b));
        child.on("error", () => resolve(null));
        child.on("close", () => {
          try {
            resolve(out.trim() ? JSON.parse(out) : null);
          } catch {
            resolve(null);
          }
        });
        child.stdin.on("error", () => {});
        child.stdin.end(JSON.stringify({ session_id: file, hook_event_name: event, cwd: ctx?.cwd, ...extra }));
      } catch {
        resolve(null);
      }
    });
  const notice = (r: any) => String(r?.hookSpecificOutput?.additionalContext ?? "");
  // interrupted tells the daemon the work ended without a chance to go on (Escape, an error), so the
  // session is idle and a message can go in as a turn.
  const interrupted = async (ctx: any) => {
    if (ended) return;
    ended = true;
    await hook("Stop", ctx, { interrupted: true });
  };

  const close = () => {
    if (watch) clearInterval(watch);
    try {
      if (server) server.close();
      for (const c of conns) c.destroy();
      if (server && !windows) rmSync(sock, { force: true });
    } catch {}
    conns = new Set();
    watch = server = null;
    on = false;
    sock = token = "";
  };
  // The socket takes what Claude Code's messaging socket does: a line {"type":"auth","token":...},
  // then lines {"type":"user","message":{"content":...}}. Without it, messages wait for the hook.
  const listen = (path: string, key: string): Promise<void> =>
    new Promise((resolve) => {
      const mine = new Set<any>();
      const srv = createServer((conn: any) => {
        let buf = "";
        let authed = false;
        mine.add(conn);
        conn.on("close", () => mine.delete(conn));
        conn.setEncoding("utf8");
        conn.on("error", () => {});
        conn.on("data", (chunk: string) => {
          buf += chunk;
          if (buf.length > 4 << 20) return conn.destroy();
          for (let i = buf.indexOf("\n"); i >= 0; i = buf.indexOf("\n")) {
            let rec: any;
            try {
              rec = JSON.parse(buf.slice(0, i));
            } catch {
              return conn.destroy();
            }
            buf = buf.slice(i + 1);
            if (!authed) {
              if (rec?.type !== "auth" || rec?.token !== key) return conn.destroy();
              authed = true;
            } else if (server !== srv) {
              return conn.destroy(); // the session it was opened for has ended
            } else if (rec?.type === "user" && typeof rec?.message?.content === "string" && rec.message.content) {
              // A turn if the session is idle; held until it finishes if it is busy, not steered into it.
              pi.sendMessage(said(rec.message.content), { triggerTurn: true, deliverAs: "followUp" });
            }
          }
        });
      });
      srv.on("error", () => resolve());
      srv.listen(path, () => {
        try {
          if (!windows) chmodSync(path, 0o600);
        } catch {}
        srv.unref();
        server = srv;
        conns = mine;
        resolve();
      });
    });

  // turnOn starts the hook and the socket once the session's clone is joined, which may be after the
  // session started: the user often joins from inside it. Until then it is looked for again each
  // second, as the MCP server looks for the consent every 2, and at each prompt and tool call.
  const turnOn = async () => {
    if (on || !sock || !joined(cwd)) return;
    on = true;
    if (watch) clearInterval(watch);
    watch = null;
    await listen(sock, token);
  };

  pi.on("session_start", async (_event: any, ctx: any) => {
    close();
    file = String(ctx?.sessionManager?.getSessionFile?.() ?? "");
    cwd = String(ctx?.cwd ?? process.cwd());
    // Chosen now and given to the MCP server, which registers once the clone is joined: a message
    // that comes before the socket opens waits for the hook.
    sock = sockPath(randomBytes(8).toString("hex"));
    token = randomBytes(24).toString("hex");
    await turnOn();
    if (!on) {
      watch = setInterval(turnOn, 1000);
      watch.unref();
    }
    register();
  });
  pi.on("session_shutdown", () => close());
  pi.on("before_agent_start", async (_event: any, ctx: any) => {
    ended = false;
    await turnOn();
    const text = notice(await hook("UserPromptSubmit", ctx));
    if (text) return { message: said(text) };
  });
  pi.on("tool_result", async (_event: any, ctx: any) => {
    await turnOn();
    const text = notice(await hook("PostToolUse", ctx));
    if (text) pi.sendMessage(said(text), { deliverAs: "steer" });
  });
  pi.on("agent_before_settle", async (event: any, ctx: any) => {
    // After an error Pi ends the run whatever an extension asks.
    if (event?.outcome !== "completed") return interrupted(ctx);
    const r = await hook("Stop", ctx);
    if (r?.decision !== "block" || !r?.reason) {
      ended = true;
      return;
    }
    return { entries: [...(event.entries ?? []), { type: "custom_message", ...said(String(r.reason)) }], continue: true };
  });
  // Pi skips agent_before_settle after an abort.
  pi.on("agent_settled", async (event: any, ctx: any) => {
    if (event?.aborted) await interrupted(ctx);
  });
}
`

// piMarker starts every extension join writes.
const piMarker = "// Written by `" + brand.Command + " join`"

// piExeLine is the line of an extension join wrote that names the mur it runs: `const mur = ...;`
// now, `command: ...,` before #205.
var piExeLine = regexp.MustCompile(`(?m)^\s*(?:const mur = |command: )("(?:[^"\\]|\\.)*")[;,]\s*$`)

// RefreshPi brings Murmur's Pi extension up to this binary's, as Pi's MCP server starts (#205):
// an update replaces the binary and doesn't run join again, so an extension from an older one
// would stay as it was. It rewrites only an extension join wrote, that runs this very binary, and
// whose text is another's. Pi reads extensions as a session starts, so the session whose server
// rewrote it has the old one until its next session. It says whether it rewrote it.
func RefreshPi() (bool, error) {
	exe, err := os.Executable()
	if err != nil {
		return false, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	return refreshPiAt(piDir(os.Getenv, home), exe)
}

func refreshPiAt(dir, exe string) (bool, error) {
	path := piExtensionPath(dir)
	b, err := os.ReadFile(path)
	if err != nil {
		return false, nil // none: join writes one
	}
	text := strings.ReplaceAll(string(b), "\r\n", "\n")
	m := piExeLine.FindStringSubmatch(text)
	if !strings.HasPrefix(text, piMarker) || m == nil {
		return false, nil
	}
	var named string
	if json.Unmarshal([]byte(m[1]), &named) != nil || !sameFile(named, exe) {
		return false, nil
	}
	want := piExtension(named)
	if text == want {
		return false, nil
	}
	if err := writeAtomic(path, []byte(want), 0o644); err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return true, nil
}

func sameFile(a, b string) bool {
	x, err := os.Stat(a)
	if err != nil {
		return false
	}
	y, err := os.Stat(b)
	return err == nil && os.SameFile(x, y)
}

// piDir is Pi's agent directory: PI_CODING_AGENT_DIR, whose leading ~ Pi reads as the home
// directory (Codex's review of #136), else ~/.pi/agent.
func piDir(getenv func(string) string, home string) string {
	dir := getenv("PI_CODING_AGENT_DIR")
	if strings.HasPrefix(dir, "~") {
		dir = expandHome(dir, home)
	}
	if dir == "" {
		dir = filepath.Join(home, ".pi", "agent")
	}
	return dir
}

func piExtensionPath(dir string) string {
	return filepath.Join(dir, "extensions", brand.MCPServer+".ts")
}

// registerPiAt writes Murmur's extension into Pi's agent directory h. Pi isn't touched where neither
// its command nor the directory is there.
func (e Env) registerPiAt(h agentHome) (did []string, fresh bool, err error) {
	if h.Dir == "" {
		return nil, false, nil
	}
	if _, lerr := e.look("pi"); lerr != nil && h.Bin == "" && !isDir(h.Dir) {
		return nil, false, nil
	}
	path := piExtensionPath(h.Dir)
	want := piExtension(e.Exe)
	b, err := os.ReadFile(path)
	switch {
	case err == nil && strings.ReplaceAll(string(b), "\r\n", "\n") == want:
		return nil, false, piMCPConflict(h.Dir)
	case err == nil && !strings.HasPrefix(string(b), piMarker):
		return nil, false, fmt.Errorf("%s is there and isn't the one join writes, so it was left alone", path)
	case err != nil && !os.IsNotExist(err):
		return nil, false, err
	}
	if err := writeAtomic(path, []byte(want), 0o644); err != nil {
		return nil, false, fmt.Errorf("%s: %w", path, err)
	}
	return []string{h.Label + ": registered the " + brand.MCPServer + " MCP server for you, in every project, through an extension (" + path + ")"}, true, piMCPConflict(h.Dir)
}

// piMCPConflict is an error if Pi's mcp.json in dir has a server named murmur, which Pi runs instead
// of the extension's: with whatever command it names, and without the session's file (Codex's
// review of #136). join leaves the user's file alone and says what to delete.
func piMCPConflict(dir string) error {
	path := filepath.Join(dir, "mcp.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	if _, found, _ := jsonedit.GetIn(string(b), []string{"mcpServers", brand.MCPServer}); found {
		return fmt.Errorf("%s has a server named %s, which Pi runs instead of %s's extension: delete it from there", path, brand.MCPServer, brand.Product)
	}
	return nil
}

func piRemoval(_ Env, h agentHome) string {
	if path := piExtensionPath(h.Dir); exists(path) {
		return "delete " + path
	}
	return ""
}
