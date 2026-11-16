package join

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/a-Fig/murmur/internal/brand"
)

// OpenCode 2 has no hooks of Claude Code's kind, so join writes a plugin of Murmur's own into its
// config directory's plugins/, which OpenCode loads in its server with no entry in opencode.json
// (#431, #205). It brings a session its messages as Murmur's hook does in Claude Code (#202): it
// runs `mur hook --agent opencode` at the same moments, with Claude Code's event names and
// OpenCode's session ID, which is the ID the shared MCP server registers the session by
// (internal/mcpserver, sharedAgent). Seen on OpenCode 2.2.0, 2026-11-12, with `opencode run
// --standalone` against a scratch config (the PR that added this says what was run):
//
//   - ctx.tool.hook("execute.after") fires after each tool call and is PostToolUse. What the hook
//     gives goes in with ctx.session.synthetic({sessionID, text, delivery: "steer"}): OpenCode puts
//     it after the tool's result and before the next model request, in the same execution, as a
//     message of its own that is kept in the session. Appended to the tool's result instead, a
//     model took it for text planted in the file it had read, and refused it (#431).
//   - ctx.event.subscribe({}) gives every event of the server, each with data.sessionID.
//     session.execution.started is UserPromptSubmit: the session is at work. So is every turn the
//     plugin itself starts.
//   - session.execution.succeeded is Stop. What the hook gives back goes in with the same call,
//     which in an idle session starts another execution, so the session goes on with it.
//     session.execution.interrupted (Escape, ctx.session.interrupt) is a Stop that was interrupted,
//     as Pi's is: the daemon takes the session for idle and hands over what is unread as a turn.
//     Any other session.execution.* event is taken the same way, a failed one among them, whose
//     name wasn't seen; one whose reason is "shutdown" is the server closing, and is left out.
//   - Each hook is a process, so the plugin runs one session's hooks one after another: a turn's
//     start that reached the daemon after its end (Escape pressed at once) would leave an idle
//     session looking busy until its next turn.
//   - session.created carries parentID for a session the `subagent` tool started (called `task`
//     before 2.2), and ctx.session.get gives it for one made before the plugin loaded. The plugin
//     says so on each request of that session's hook (child, parent_native), and at once when the
//     session is made, so the daemon has it off the mesh before its first call to one of Murmur's
//     tools registers it (#455, #452).
//
// A turn in an idle session needs nothing of the plugin: inside T3 it goes through T3, and outside
// it through `opencode api` (#135). The plugin is in every OpenCode session of the user, so it
// runs the hook only in a clone the user joined (its .git/murmur/joined.json), found from the
// server's directory, as Pi's extension does: elsewhere a tool call waits on no process of
// Murmur's. It looks again at each event until the clone is joined. Not covered: a session moved
// to another clone with `mur join -C` (#200) is looked for in the clone it works in, and
// OpenCode's commits aren't credited to its session, as no tool call is bracketed.
//
// OpenCode 1 gets a plugin of its own (#446, #205), since each version's shape is an error to the
// other: 1.19.5 calls a default export's server(input), and also runs the setup of a file that has
// both, and 2.3.0 refuses a file without setup or effect (packages/opencode/src/plugin/shared.ts
// at v1.19.5, packages/core/src/plugin/module.ts at v2.3.0). Join writes the one `opencode
// --version` names (opencode.go), at the same path, and the refresh as Murmur's server starts
// rewrites it for the OpenCode that started the server, so an upgrade from 1 to 2 (or both
// installed, sharing one config) moves the file to the version that loads it. Read in 1.19.5's
// source and seen with it, 2026-11-15, against a scratch config (the PR that added this says what
// was run):
//
//   - OpenCode 1 names no session in a call to an MCP server (_meta has only a progress token), so
//     the one server process in a directory is one session to Murmur, as it was before the plugin.
//     The plugin names it: its config hook, which OpenCode runs before it starts any MCP server,
//     adds MURMUR_OPENCODE_SESSION, a random ID, to Murmur's server's environment, and the hook's
//     requests carry the same ID, so the daemon finds the session by it as it does an OpenCode 2
//     session by OpenCode's. A session's notices and turns go to the OpenCode session that is at
//     work, or was last.
//   - "tool.execute.after" is PostToolUse. A session.status event of "busy" after none is
//     UserPromptSubmit, and session.idle is Stop, or an interrupted Stop where a session.error came
//     first (Escape, a failure). What the hook gives goes in with client.session.promptAsync, as a
//     synthetic text part, which OpenCode's terminal UI doesn't show and the model reads: in a
//     session at work it lands after the tool's result, and the running turn answers it before it
//     ends; in an idle one it starts a turn. Each goes in with the agent and model of the session's
//     last prompt, as OpenCode would otherwise take its default agent.
//   - A turn in an idle session, outside T3, comes through a socket the plugin opens, which takes
//     Claude Code's messaging-socket lines, as Pi's extension does: its address and token go to
//     the MCP server beside the ID (MURMUR_OPENCODE_MESSAGING_SOCKET, _TOKEN), and the message
//     goes in with promptAsync as a prompt the terminal UI shows, as OpenCode 2's `opencode api`
//     turn is shown.
//   - A session another one started (the `task` tool's) has a parentID, in session.created and
//     client.session.get. Its tool calls reach Murmur through its parent's server, as its
//     parent's, so it runs no hook of its own and is never given a turn: it never shows as a
//     session of its own.
//   - Under T3 (T3CODE_CLI_PATH in OpenCode's environment, as for the MCP server's route) the
//     plugin does nothing, so an OpenCode 1 thread is as it was before the plugin: T3 hands it a
//     message as a turn when its turn ends. Nobody has tried the plugin in T3, where its turn at an
//     idle session's Stop would be one T3 didn't start.
//
// Not covered: two sessions at work at once in one OpenCode 1 process (`opencode serve` with two
// clients) share one session's messages, and a turn goes to the one at work last. A `task` child's
// Murmur calls are its parent's.

// openCodePlugin is OpenCode 2's plugin's text for the mur at exe.
func openCodePlugin(exe string) string { return fillOpenCode(openCodeTemplate, exe) }

// openCode1Plugin is OpenCode 1's.
func openCode1Plugin(exe string) string { return fillOpenCode(openCode1Template, exe) }

func fillOpenCode(template, exe string) string {
	cmd, _ := json.Marshal(exe)
	return strings.NewReplacer("{{product}}", brand.Product, "{{command}}", brand.Command, "{{server}}", brand.MCPServer,
		"{{state}}", brand.StateDir, "{{consent}}", consentFile, "{{exe}}", string(cmd)).Replace(template)
}

// openCodeTemplate is OpenCode 2's plugin, with its names left for package brand to fill in. It
// is plain JavaScript with no import of OpenCode's own: a default export of {id, setup} is all
// OpenCode asks.
const openCodeTemplate = `// Written by ` + "`{{command}} join`" + ` ({{product}}, https://github.com/a-Fig/murmur). ` + "`{{command}} join`" + ` writes it again when {{command}} moves,
// and {{command}} when it is updated, so edits here are lost; take this first line out to keep yours.
// It brings an OpenCode session its {{product}} messages: a notice after a tool call while it works,
// and what it hasn't read when its turn ends. It also tells {{product}} which sessions another
// session started, so that they stay out of {{product}}. Delete this file to take it out of OpenCode.
import { spawn } from "node:child_process";
import { existsSync, readFileSync, statSync } from "node:fs";
import { dirname, join, resolve } from "node:path";

const mur = {{exe}};

// joined says the user joined {{product}} in the clone that dir is in, as ` + "`{{command}} join`" + ` records it in
// the clone's .git: outside one, the plugin runs no hook. A few stats, no process.
const joined = (dir) => {
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

export default {
  id: "{{server}}",
  setup(ctx) {
    const dir = String(ctx?.location?.directory ?? process.cwd());
    let on = false;
    let stopped = false;
    // parents is, for each session seen, the session that started it, "" for one a person started.
    const parents = new Map();
    const parentOf = async (id) => {
      if (parents.has(id)) return parents.get(id);
      try {
        const s = await ctx.session.get({ sessionID: id });
        parents.set(id, String(s?.parentID ?? ""));
      } catch {
        return ""; // asked again next time
      }
      return parents.get(id);
    };

    // hook runs {{product}}'s hook for one of Claude Code's events and gives what it printed, parsed;
    // null on any trouble, or when the server isn't in a joined clone. One session's hooks run one
    // after another, in the order of their events: each is a process of its own, and a turn's start
    // that reached {{product}} after its end would leave an idle session looking busy.
    const chains = new Map();
    const hook = (event, sessionID, extra = {}) => {
      if (stopped || !sessionID) return Promise.resolve(null);
      const next = (chains.get(sessionID) ?? Promise.resolve()).then(() => run(event, sessionID, extra), () => null);
      chains.set(sessionID, next);
      next.then(() => {
        if (chains.get(sessionID) === next) chains.delete(sessionID);
      });
      return next;
    };
    const run = async (event, sessionID, extra) => {
      if (stopped) return null;
      if (!on && !(on = joined(dir))) return null;
      const parent = await parentOf(sessionID);
      return new Promise((done) => {
        let out = "";
        try {
          const child = spawn(mur, ["hook", "--agent", "opencode"], {
            cwd: dir, stdio: ["pipe", "pipe", "ignore"], windowsHide: true, timeout: 10000,
          });
          child.stdout.on("data", (b) => (out += b));
          child.on("error", () => done(null));
          child.on("close", () => {
            try {
              done(out.trim() ? JSON.parse(out) : null);
            } catch {
              done(null);
            }
          });
          child.stdin.on("error", () => {});
          child.stdin.end(JSON.stringify({
            session_id: sessionID, hook_event_name: event, cwd: dir, child: parent !== "", parent_native: parent, ...extra,
          }));
        } catch {
          done(null);
        }
      });
    };
    const notice = (r) => String(r?.hookSpecificOutput?.additionalContext ?? "");
    // say puts {{product}}'s words in the session as a message of their own, steered: in a session at
    // work they go in after its tool calls, before its next model request, and in an idle one they
    // start a turn.
    const say = async (sessionID, text) => {
      try {
        await ctx.session.synthetic({ sessionID, text, delivery: "steer" });
      } catch {}
    };

    ctx.tool.hook("execute.after", async (ev) => {
      const text = notice(await hook("PostToolUse", ev?.sessionID));
      if (text) await say(ev.sessionID, text);
    });

    const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
    (async () => {
      // A stream that ends or fails is asked for again: without it no turn's end is seen.
      for (; !stopped; await sleep(5000)) {
        try {
          for await (const ev of ctx.event.subscribe({})) {
            if (stopped) return;
            const type = String(ev?.type ?? "");
            const id = ev?.data?.sessionID;
            if (!id) continue;
            if (type === "session.created") {
              const parent = String(ev.data.parentID ?? "");
              parents.set(id, parent);
              // Said at once, before the session's first call to a tool of {{product}}'s registers it.
              if (parent) hook("SessionStart", id);
            } else if (type === "session.execution.started") {
              hook("UserPromptSubmit", id).then((r) => {
                const text = notice(r);
                if (text) return say(id, text);
              });
            } else if (type === "session.execution.succeeded") {
              hook("Stop", id).then((r) => {
                if (r?.decision === "block" && r?.reason) return say(id, String(r.reason));
              });
            } else if (type.startsWith("session.execution.") && ev.data.reason !== "shutdown") {
              // Interrupted, or failed: the work ended with no chance to go on. A server that is
              // closing interrupts too, and that session isn't idle: it is gone.
              hook("Stop", id, { interrupted: true });
            }
          }
        } catch {}
      }
    })();

    return () => {
      stopped = true;
    };
  },
};
`

// openCode1Line is a line of OpenCode 1's plugin and not of OpenCode 2's. Where the refresh can't
// learn the version of the OpenCode that started the server, it keeps a file with this line as
// OpenCode 1's. Never reword it: an older file would then be refreshed with OpenCode 2's plugin,
// which OpenCode 1 refuses.
const openCode1Line = "// For OpenCode 1."

// openCode1Re finds openCode1Line as a line of its own.
var openCode1Re = regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(openCode1Line) + `\r?$`)

// openCode1Template is OpenCode 1's plugin, with its names left for package brand to fill in. Its
// default export is {id, server}, server(input) giving OpenCode 1's hooks; input.client is
// OpenCode's own SDK client for this server.
const openCode1Template = `// Written by ` + "`{{command}} join`" + ` ({{product}}, https://github.com/a-Fig/murmur). ` + "`{{command}} join`" + ` writes it again when {{command}} moves,
// and {{command}} when it is updated, so edits here are lost; take this first line out to keep yours.
` + openCode1Line + `
// It brings an OpenCode session its {{product}} messages: a notice after a tool call while it works,
// what it hasn't read when its turn ends, and a turn when it is idle, through a socket it opens.
// Under T3 Code it does nothing. Delete this file to take it out of OpenCode.
import { spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { chmodSync, existsSync, readFileSync, rmSync, statSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";

const mur = {{exe}};
const windows = process.platform === "win32";

// joined says the user joined {{product}} in the clone that dir is in, as ` + "`{{command}} join`" + ` records it in
// the clone's .git: outside one, the plugin runs no hook and opens no socket. A few stats, no process.
const joined = (dir) => {
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

// sockPath is where the socket goes: a named pipe on Windows, else a file in the temporary
// directory, or in /tmp where that path would be too long for a socket's address.
const sockPath = (id) => {
  if (windows) return "\\\\.\\pipe\\{{server}}-opencode-" + id;
  const p = join(tmpdir(), "{{server}}-opencode-" + id + ".sock");
  return Buffer.byteLength(p) <= 100 ? p : join("/tmp", "{{server}}-opencode-" + id + ".sock");
};

export default {
  id: "{{server}}",
  async server(input) {
    // In T3 Code an OpenCode 1 thread is as it was before this plugin: T3 hands it its messages.
    if (process.env.T3CODE_CLI_PATH) return {};
    const dir = String(input?.directory ?? process.cwd());
    const client = input?.client;
    // OpenCode 1 names no session in a call to an MCP server, so the sessions of this directory are
    // one to {{product}}, and this is its ID: {{product}}'s server has it in its environment, and the
    // hook's requests carry it.
    const id = randomBytes(8).toString("hex");
    const sock = sockPath(id);
    const token = randomBytes(24).toString("hex");
    let on = false;
    let stopped = false;
    let server = null;
    let conns = new Set();
    // current is the OpenCode session at work, or the last one that was: what {{product}} hands over
    // as a turn goes to it.
    let current = "";
    // parents is, for each session seen, the session that started it, "" for one a person started.
    const parents = new Map();
    // asks is, for each session, the agent and model of its last prompt, which {{product}}'s words go
    // in with: without them OpenCode would take its default agent.
    const asks = new Map();
    const busy = new Set();
    const failed = new Set();

    const parentOf = async (sid) => {
      if (parents.has(sid)) return parents.get(sid);
      try {
        const r = await client.session.get({ path: { id: sid } });
        if (!r?.data) return "";
        parents.set(sid, String(r.data.parentID ?? ""));
      } catch {
        return ""; // asked again next time
      }
      return parents.get(sid);
    };

    // say puts {{product}}'s words in session sid as a prompt of its own: in a session at work they go
    // in after its tool calls, before its next model request, and in an idle one they start a turn.
    // Synthetic, they reach the model and the terminal UI doesn't show them.
    const say = async (sid, text, synthetic) => {
      if (stopped || !sid || !text) return;
      try {
        await client.session.promptAsync({
          path: { id: sid },
          body: { ...(asks.get(sid) ?? {}), parts: [{ type: "text", text, synthetic }] },
        });
      } catch {}
    };

    // gone takes the socket's file away, also as the process exits without disposing of its plugins.
    // One killed by a signal (opencode serve on SIGTERM) leaves the file, which no other run's
    // random name takes.
    const gone = () => {
      try {
        if (!windows) rmSync(sock, { force: true });
      } catch {}
    };
    const close = () => {
      try {
        if (server) server.close();
        for (const c of conns) c.destroy();
        if (server) gone();
      } catch {}
      process.off("exit", gone);
      conns = new Set();
      server = null;
    };
    // The socket takes what Claude Code's messaging socket does: a line {"type":"auth","token":...},
    // then lines {"type":"user","message":{"content":...}}. Without it, messages wait for the hook.
    const listen = () =>
      new Promise((done) => {
        const srv = createServer((conn) => {
          let buf = "";
          let authed = false;
          conns.add(conn);
          conn.on("close", () => conns.delete(conn));
          conn.setEncoding("utf8");
          conn.on("error", () => {});
          conn.on("data", (chunk) => {
            buf += chunk;
            if (buf.length > 4 << 20) return conn.destroy();
            for (let i = buf.indexOf("\n"); i >= 0; i = buf.indexOf("\n")) {
              let rec;
              try {
                rec = JSON.parse(buf.slice(0, i));
              } catch {
                return conn.destroy();
              }
              buf = buf.slice(i + 1);
              if (!authed) {
                if (rec?.type !== "auth" || rec?.token !== token) return conn.destroy();
                authed = true;
              } else if (rec?.type === "user" && typeof rec?.message?.content === "string" && rec.message.content) {
                say(current, rec.message.content, false);
              }
            }
          });
        });
        srv.on("error", () => done());
        srv.listen(sock, () => {
          try {
            if (!windows) chmodSync(sock, 0o600);
          } catch {}
          srv.unref();
          if (stopped) {
            srv.close();
            gone();
          } else {
            server = srv;
            process.once("exit", gone);
          }
          done();
        });
      });

    // hook runs {{product}}'s hook for one of Claude Code's events, for session sid, and gives what it
    // printed, parsed; null on any trouble, for a session another one started, or when the directory
    // isn't in a joined clone. The hooks run one after another, in the order of their events: each
    // is a process of its own, and a turn's start that reached {{product}} after its end would leave
    // an idle session looking busy.
    let chain = Promise.resolve();
    const hook = (event, sid, extra = {}) => {
      if (stopped || !sid) return Promise.resolve(null);
      const next = chain.then(() => run(event, sid, extra), () => null);
      chain = next;
      return next;
    };
    const run = async (event, sid, extra) => {
      if (stopped) return null;
      if (!on) {
        if (!joined(dir)) return null;
        on = true;
        await listen();
      }
      if ((await parentOf(sid)) !== "") return null;
      current = sid;
      return new Promise((done) => {
        let out = "";
        try {
          const child = spawn(mur, ["hook", "--agent", "opencode"], {
            cwd: dir, stdio: ["pipe", "pipe", "ignore"], windowsHide: true, timeout: 10000,
          });
          child.stdout.on("data", (b) => (out += b));
          child.on("error", () => done(null));
          child.on("close", () => {
            try {
              done(out.trim() ? JSON.parse(out) : null);
            } catch {
              done(null);
            }
          });
          child.stdin.on("error", () => {});
          child.stdin.end(JSON.stringify({ session_id: id, hook_event_name: event, cwd: dir, ...extra }));
        } catch {
          done(null);
        }
      });
    };
    const notice = (r) => String(r?.hookSpecificOutput?.additionalContext ?? "");

    return {
      // Run before OpenCode starts any MCP server, with the config it starts them from.
      config: async (cfg) => {
        const m = cfg?.mcp?.["{{server}}"];
        if (!m || typeof m !== "object") return;
        m.environment = {
          ...(m.environment ?? {}),
          MURMUR_OPENCODE_SESSION: id,
          MURMUR_OPENCODE_MESSAGING_SOCKET: sock,
          MURMUR_OPENCODE_MESSAGING_TOKEN: token,
        };
      },
      "tool.execute.after": async (i) => {
        const sid = i?.sessionID;
        const text = notice(await hook("PostToolUse", sid));
        if (text) await say(sid, text, true);
      },
      event: async ({ event }) => {
        const type = String(event?.type ?? "");
        const p = event?.properties ?? {};
        if (type === "session.created" || type === "session.updated") {
          if (p.info?.id) parents.set(p.info.id, String(p.info.parentID ?? ""));
          return;
        }
        if (type === "message.updated") {
          const m = p.info;
          if (m?.role === "user" && m.sessionID && m.agent && m.model?.providerID && m.model?.modelID) {
            asks.set(m.sessionID, { agent: m.agent, model: { providerID: m.model.providerID, modelID: m.model.modelID } });
          }
          return;
        }
        const sid = p.sessionID;
        if (!sid) return;
        if (type === "session.error") {
          failed.add(sid);
        } else if (type === "session.status" && p.status?.type === "busy" && !busy.has(sid)) {
          busy.add(sid);
          const text = notice(await hook("UserPromptSubmit", sid));
          if (text) await say(sid, text, true);
        } else if (type === "session.idle") {
          busy.delete(sid);
          if (failed.delete(sid)) {
            // Interrupted, or failed: the work ended with no chance to go on.
            await hook("Stop", sid, { interrupted: true });
            return;
          }
          const r = await hook("Stop", sid);
          if (r?.decision === "block" && r?.reason) await say(sid, String(r.reason), true);
        }
      },
      dispose: async () => {
        stopped = true;
        close();
      },
    };
  },
};
`

// openCodeMarker starts every plugin join writes.
const openCodeMarker = "// Written by `" + brand.Command + " join`"

// openCodeExeLine is the line of a plugin join wrote that names the mur it runs.
var openCodeExeLine = regexp.MustCompile(`(?m)^const mur = ("(?:[^"\\]|\\.)*");\s*$`)

func openCodePluginPath(dir string) string {
	return filepath.Join(dir, "plugins", brand.MCPServer+".js")
}

// writeOpenCodePlugin writes Murmur's plugin, whose text is want, into OpenCode's config directory
// dir. It says whether it wrote one. A file there that join didn't write is left alone, with an
// error.
func writeOpenCodePlugin(dir, want string) (wrote bool, err error) {
	path := openCodePluginPath(dir)
	b, err := os.ReadFile(path)
	switch {
	case err == nil && strings.ReplaceAll(string(b), "\r\n", "\n") == want:
		return false, nil
	case err == nil && !strings.HasPrefix(string(b), openCodeMarker):
		return false, fmt.Errorf("%s is there and isn't the one join writes, so it was left alone", path)
	case err != nil && !os.IsNotExist(err):
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := writeAtomic(path, []byte(want), 0o644); err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return true, nil
}

// RefreshOpenCode brings Murmur's OpenCode plugin up to this binary's, as OpenCode's MCP server
// starts: an update replaces the binary and doesn't run join again, so a plugin from an older one
// would stay as it was (as RefreshPi does for Pi's extension, #205). It rewrites only a plugin
// join wrote, that runs this very binary, and whose text is another's, with this binary's plugin
// for the version of client, the OpenCode program that started the server. That moves the file
// after an upgrade from OpenCode 1 to 2, or for one of both installed with one config folder,
// where join asked the other. Where client is "" or its version can't be learned, the file keeps
// its own version. OpenCode 2 loads plugins again when the folder changes, OpenCode 1 at its next
// start. It says whether it rewrote it.
func RefreshOpenCode(ctx context.Context, client string) (bool, error) {
	return Env{Run: runProgram}.refreshOpenCode(ctx, client)
}

func (e Env) refreshOpenCode(ctx context.Context, client string) (bool, error) {
	exe, err := os.Executable()
	if err != nil {
		return false, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		config = filepath.Join(home, ".config")
	}
	major := 0
	if client != "" {
		major = e.openCodeMajorFor(ctx, agentHome{Bin: client})
	}
	return refreshOpenCodeAt(filepath.Join(config, "opencode"), exe, major)
}

// refreshOpenCodeAt is RefreshOpenCode for OpenCode's config directory dir, the mur at exe, and
// OpenCode's major version, 0 where it isn't known.
func refreshOpenCodeAt(dir, exe string, major int) (bool, error) {
	path := openCodePluginPath(dir)
	b, err := os.ReadFile(path)
	if err != nil {
		return false, nil // none: join writes one
	}
	text := strings.ReplaceAll(string(b), "\r\n", "\n")
	m := openCodeExeLine.FindStringSubmatch(text)
	if !strings.HasPrefix(text, openCodeMarker) || m == nil {
		return false, nil
	}
	var named string
	if json.Unmarshal([]byte(m[1]), &named) != nil || !sameFile(named, exe) {
		return false, nil
	}
	want := openCodePlugin(named)
	if major == 1 || (major == 0 && openCode1Re.MatchString(text)) {
		want = openCode1Plugin(named)
	}
	if text == want {
		return false, nil
	}
	if err := writeAtomic(path, []byte(want), 0o644); err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return true, nil
}

// removeOpenCodePlugin deletes Murmur's plugin from OpenCode's config directory dir, and the
// plugins folder if that leaves it empty. One join didn't write is a user's own, and stays.
func removeOpenCodePlugin(dir string) (gone bool, note string, err error) {
	path := openCodePluginPath(dir)
	gone, note, err = removeOwned(path, func(b []byte) bool { return bytes.HasPrefix(b, []byte(openCodeMarker)) })
	if gone {
		os.Remove(filepath.Dir(path)) // stays if anything else is in it
	}
	return gone, note, err
}
