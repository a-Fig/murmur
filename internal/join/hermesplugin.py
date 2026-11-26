# Written by `{{command}} join` ({{product}}, https://github.com/a-Fig/murmur). `{{command}} join` writes it again when {{command}} moves,
# and {{command}} when it is updated, so edits here are lost; take this first line out to keep yours.
# It gives each Hermes session {{product}}'s tools ({{server}}_who, {{server}}_send and the rest), and brings it its
# messages: a notice as a turn starts and after a tool's result, and a turn of its own when it is idle,
# through a socket it opens for the session. Each call runs `{{command}} tool`,
# and each hook `{{command}} hook`, with the session's ID and working directory.
# `{{command}} uninstall` takes it out, or delete this folder and {{server}} from plugins.enabled and plugins.entries in config.yaml.
"""{{product}}'s tools and messages for Hermes Agent."""

import json
import logging
import os
import secrets
import subprocess
import tempfile
import threading

MUR = {{exe}}
log = logging.getLogger(__name__)

# A subagent acts as the session that started it: its calls go as its parent's, and it runs no hook,
# so it never shows to others as a session of its own.
_parents = {}
# Each session's key, which inject_message takes, as Hermes binds it while the session is at work.
_keys = {}
# Each session's socket, while the session is open in a clone the user joined.
_sockets = {}
_lock = threading.Lock()


def _run(args, stdin="", cwd=None, env=None, timeout=120):
    extra = {"creationflags": subprocess.CREATE_NO_WINDOW} if os.name == "nt" else {}
    return subprocess.run([MUR, *args], input=stdin, capture_output=True, text=True, encoding="utf-8",
                          errors="replace", cwd=cwd or None, env=env, timeout=timeout, **extra)


def _joined(folder):
    """Says the user joined {{product}} in the clone folder is in, as `{{command}} join` records it in the
    clone's .git: outside one the plugin runs no hook and opens no socket. A few stats, no process."""
    try:
        d = os.path.abspath(folder)
        while True:
            dot = os.path.join(d, ".git")
            if os.path.exists(dot):
                git = dot
                if not os.path.isdir(dot):
                    with open(dot, encoding="utf-8") as f:
                        line = f.read().strip()
                    if not line.startswith("gitdir:"):
                        return False
                    git = os.path.normpath(os.path.join(d, line[len("gitdir:"):].strip()))
                    common = os.path.join(git, "commondir")
                    if os.path.exists(common):
                        with open(common, encoding="utf-8") as f:
                            git = os.path.normpath(os.path.join(git, f.read().strip()))
                return os.path.exists(os.path.join(git, "{{state}}", "{{consent}}"))
            up = os.path.dirname(d)
            if up == d:
                return False
            d = up
    except Exception:
        return False


def _session(session_id):
    sid = session_id or ""
    if not sid:
        try:
            from gateway.session_context import get_session_env
            sid = get_session_env("HERMES_SESSION_ID", "")
        except Exception:
            sid = ""
    with _lock:
        seen = set()
        while sid in _parents and sid not in seen:
            seen.add(sid)
            sid = _parents[sid]
    return sid


def _note_key(sid):
    """Keeps the key Hermes binds for the session at work, which only a call in its context can read."""
    try:
        from gateway.session_context import bound_session_key
        key = bound_session_key()
    except Exception:
        key = ""
    if key:
        with _lock:
            _keys[sid] = key


class _Socket:
    """A socket for one session that takes Claude Code's messaging-socket lines, as {{product}}'s
    daemon writes them: an auth line with the token, then a user line, whose text goes into the
    session with inject_message, as a turn of its own when it is idle and its next turn when it is
    busy. It answers each user line with a line of its own, {"type":"accepted"} or
    {"type":"refused"}, and the daemon takes the message out of the inbox only once it has
    accepted it. A named pipe on Windows, where Python has no unix sockets. It answers while the
    session can take a turn, and the daemon holds the session for as long as it does."""

    def __init__(self, ctx, sid):
        self.ctx, self.sid, self.token = ctx, sid, secrets.token_hex(16)
        self.closed = False
        # Whether a hook has run with this socket new, which registers it with the daemon. A tool
        # call that never reaches the daemon doesn't, so each run says it is new until a hook has.
        self.announced = False
        if os.name == "nt":
            from multiprocessing.connection import Listener
            self.addr = "\\\\.\\pipe\\{{server}}-hermes-" + secrets.token_hex(8)
            self._ln = Listener(self.addr, family="AF_PIPE")
        else:
            import socket
            self._dir = tempfile.mkdtemp(prefix="murh")  # short: macOS limits a unix socket's path
            self.addr = os.path.join(self._dir, "s")
            self._ln = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            self._ln.bind(self.addr)
            os.chmod(self.addr, 0o600)
            self._ln.listen(8)
            self._ln.settimeout(1)
        threading.Thread(target=self._serve, name="{{server}}-socket", daemon=True).start()

    def _serve(self):
        while not self.closed:
            try:
                conn = self._ln.accept()
            except OSError:
                if self.closed:
                    return
                continue
            if isinstance(conn, tuple):
                conn = conn[0]
            if self.closed:
                conn.close()
                return
            threading.Thread(target=self._take, args=(conn,), daemon=True).start()

    def _lines(self, conn):
        buf = b""
        while True:
            try:
                if os.name == "nt":
                    chunk = conn.recv_bytes(1 << 21)
                else:
                    conn.settimeout(10)
                    chunk = conn.recv(1 << 16)
            except (EOFError, OSError):
                chunk = b""
            if not chunk:
                break
            buf += chunk
            while b"\n" in buf:
                line, buf = buf.split(b"\n", 1)
                yield line
            if len(buf) > 1 << 21:
                return
        if buf.strip():
            yield buf

    def _take(self, conn):
        try:
            authed = False
            for raw in self._lines(conn):
                try:
                    msg = json.loads(raw.decode("utf-8"))
                except Exception:
                    return
                if not authed:
                    if msg.get("type") != "auth" or not secrets.compare_digest(str(msg.get("token", "")), self.token):
                        return
                    authed = True
                    continue
                text = (msg.get("message") or {}).get("content") if msg.get("type") == "user" else None
                if isinstance(text, str) and text:
                    # A connection taken before the socket closed is refused, not put in: the
                    # daemon keeps the message for the session's next socket.
                    self._reply(conn, not self.closed and self._inject(text))
        finally:
            try:
                conn.close()
            except Exception:
                pass

    def _inject(self, text):
        with _lock:
            key = _keys.get(self.sid)
        ok = False
        if not key:
            log.warning("{{product}}: no key for Hermes session %s yet, so no turn for it; its socket closes", self.sid)
        else:
            try:
                ok = self.ctx.inject_message(text, role="user", session_key=key)
                if not ok:
                    log.warning("{{product}}: Hermes refused a turn in session %s; its socket closes", self.sid)
            except Exception as e:
                log.warning("{{product}}: couldn't put a message into Hermes session %s; its socket closes: %s", self.sid, e)
        if not ok:
            # Hermes has no turn for the session (it closed without saying so): stop answering, so
            # the daemon ends the session and keeps what comes next for it.
            _close_socket(self.sid, self)
        return ok

    def _reply(self, conn, ok):
        """Tells the daemon whether the session took the message. A {{command}} from before #1258 has
        hung up by now, as it waits for no answer."""
        line = b'{"type":"accepted"}\n' if ok else b'{"type":"refused"}\n'
        try:
            if os.name == "nt":
                conn.send_bytes(line)
            else:
                conn.sendall(line)
        except Exception:
            pass

    def _drain(self):
        """Connects to each of the pipe's instances that waits for a connection, until none is left.
        CreateFile fails at once on a busy pipe, where multiprocessing's Client would wait."""
        import _winapi
        for _ in range(16):
            try:
                h = _winapi.CreateFile(self.addr, _winapi.GENERIC_READ | _winapi.GENERIC_WRITE, 0,
                                       _winapi.NULL, _winapi.OPEN_EXISTING, 0, _winapi.NULL)
            except OSError:
                return
            _winapi.CloseHandle(h)

    def _drain_later(self):
        import time
        for _ in range(5):
            time.sleep(0.3)
            self._drain()

    def close(self):
        self.closed = True
        if os.name == "nt":
            try:
                self._ln.close()
            except Exception:
                pass
            # Its accept may be waiting on a pipe instance the close didn't reach, or about to make one:
            # connecting takes each until none is left, so nothing answers for the session after this.
            # Once now, and a few times after, for an instance its accept makes as the close happens.
            self._drain()
            threading.Thread(target=self._drain_later, name="{{server}}-socket-close", daemon=True).start()
        else:
            for f in (lambda: os.unlink(self.addr), self._ln.close, lambda: os.rmdir(self._dir)):
                try:
                    f()
                except Exception:
                    pass


def _socket_of(sid):
    """The address and token of the session's socket, or (None, None)."""
    with _lock:
        s = _sockets.get(sid)
    return (s.addr, s.token) if s else (None, None)


def _open_socket(ctx, sid):
    """The session's socket, opened if it has none: the socket, and whether it is new."""
    with _lock:
        s = _sockets.get(sid)
    if s:
        return s, False
    try:
        s = _Socket(ctx, sid)
    except Exception as e:
        log.warning("{{product}}: couldn't open a socket for Hermes session %s: %s", sid, e)
        return None, False
    with _lock:
        if sid in _sockets:  # another thread opened one meanwhile
            s.close()
            return _sockets[sid], False
        _sockets[sid] = s
    return s, True


def _close_socket(sid, only=None):
    """Closes the session's socket, or, given only, that socket if it is still the session's."""
    with _lock:
        s = _sockets.get(sid)
        if s is None or (only is not None and s is not only):
            s = None
        else:
            del _sockets[sid]
    if s:
        s.close()
    elif only is not None:
        only.close()


def _can_inject(ctx):
    """Says Hermes lets this plugin start turns (plugins.entries.{{server}}.allow_gateway_injection).
    Without it the plugin opens no socket, so the daemon keeps the session's messages for notices."""
    try:
        check = getattr(ctx, "_gateway_injection_allowed", None)
        return bool(check()) if callable(check) else True
    except Exception:
        return True


def _env(ctx, sid, cwd):
    env = dict(os.environ)
    env["{{sessionEnv}}"] = sid
    # Only once it has the session's key: a message the socket takes is out of the daemon's inbox,
    # and without the key the plugin couldn't put it in.
    with _lock:
        keyed = sid in _keys
    if keyed and _joined(cwd) and _can_inject(ctx):
        s, _ = _open_socket(ctx, sid)
        if s:
            env["{{socketEnv}}"], env["{{tokenEnv}}"] = s.addr, s.token
            env["{{repliesEnv}}"] = "1"  # it answers each message, so the daemon waits to hear
            if not s.announced:  # so `{{command}} hook` tells the daemon of it, as it does only now and then
                env["{{newSocketEnv}}"] = "1"
    return env


def _announced(sid, env):
    """Notes that a hook ran with the session's socket new, as env gave it."""
    if env.get("{{newSocketEnv}}"):
        with _lock:
            s = _sockets.get(sid)
            if s and s.addr == env.get("{{socketEnv}}"):
                s.announced = True


def _error(text):
    return json.dumps({"error": text})


def _handler(ctx, tool):
    def handle(args, session_id=None, **_):
        sid = _session(session_id)
        if not sid:
            return _error("{{product}} can't tell which Hermes session this is, so it can't act for it.")
        cwd = ctx.session_cwd()
        if not cwd:
            return _error("{{product}} can't tell which folder this session works in, so it can't tell which repo it is in.")
        if not os.path.isdir(cwd):
            return _error("this session's folder, %s, isn't there any more." % cwd)
        if sid == (session_id or sid):
            _note_key(sid)
        try:
            p = _run(["tool", "--agent", "hermes", tool], json.dumps(args or {}), cwd, _env(ctx, sid, cwd))
        except subprocess.TimeoutExpired:
            return _error("{{product}} took over two minutes to answer.")
        except OSError as e:
            return _error("couldn't run {{command}} (%s): %s" % (MUR, e))
        out = p.stdout.strip()
        if p.returncode == 0:
            return out
        return _error(out or p.stderr.strip() or "{{command}} exited with %d" % p.returncode)
    return handle


def _hook(ctx, session_id, event, **fields):
    """Runs `{{command}} hook` for the session's event, as Claude Code's hook would see it, and gives its
    answer, parsed, or None. Nothing for a subagent, outside a joined clone, or on any trouble: a
    hook never holds the session up for long or fails it."""
    sid = session_id or ""
    if not sid:
        return None
    with _lock:
        if sid in _parents:
            return None
    cwd = ctx.session_cwd()
    if not cwd or not os.path.isdir(cwd) or not _joined(cwd):
        return None
    _note_key(sid)
    payload = dict(fields, session_id=sid, hook_event_name=event, cwd=cwd)
    try:
        env = _env(ctx, sid, cwd)
        p = _run(["hook", "--agent", "hermes"], json.dumps(payload), cwd, env, timeout=25)
        if p.returncode == 0:  # 3: it couldn't register the session, so the socket may not be known
            _announced(sid, env)
        out = p.stdout.strip()
        return json.loads(out) if out else None
    except Exception as e:
        log.warning("{{product}}: its hook (%s) failed: %s", event, e)
        return None


def _context(answer):
    ctx = ((answer or {}).get("hookSpecificOutput") or {}).get("additionalContext")
    return ctx if isinstance(ctx, str) and ctx else None


def _register_hooks(ctx):
    def turn_start(session_id=None, user_message=None, **_):
        text = _context(_hook(ctx, session_id, "UserPromptSubmit", prompt=str(user_message or "")[:4096]))
        return {"context": text} if text else None

    def tool_start(tool_name=None, session_id=None, tool_call_id=None, **_):
        # A terminal call is bracketed, so the daemon credits the session with the commits it made.
        if tool_name == "terminal" and tool_call_id:
            _hook(ctx, session_id, "PreToolUse", tool_name=tool_name, tool_use_id=tool_call_id)
        return None

    def tool_result(tool_name=None, result=None, session_id=None, tool_call_id=None, **_):
        fields = {"tool_name": tool_name}
        if tool_name == "terminal" and tool_call_id:
            fields["tool_use_id"] = tool_call_id
        text = _context(_hook(ctx, session_id, "PostToolUse", **fields))
        if not text or not isinstance(result, str):
            return None
        return result + "\n\n" + text

    def turn_end(session_id=None, completed=True, interrupted=False, failed=False, **_):
        # Tells the daemon the session is idle. What it hasn't read comes back through its socket as a
        # turn, so nothing the daemon has marked read waits on an inject_message Hermes may refuse.
        _hook(ctx, session_id, "Stop", interrupted=bool(interrupted or failed))

    def closed(session_id=None, old_session_id=None, deleted_ids=None, **_):
        for sid in [session_id, old_session_id, *(deleted_ids or [])]:
            if sid:
                _close_socket(str(sid))
                with _lock:
                    _keys.pop(str(sid), None)

    ctx.register_hook("pre_llm_call", turn_start)
    ctx.register_hook("pre_tool_call", tool_start)
    ctx.register_hook("transform_tool_result", tool_result)
    ctx.register_hook("on_session_end", turn_end)
    ctx.register_hook("on_session_finalize", closed)
    ctx.register_hook("on_session_delete", closed)


def _child_start(parent_session_id=None, child_session_id=None, **_):
    if parent_session_id and child_session_id and parent_session_id != child_session_id:
        with _lock:
            _parents[str(child_session_id)] = str(parent_session_id)


def _child_stop(child_session_id=None, **_):
    if child_session_id:
        with _lock:
            _parents.pop(str(child_session_id), None)


def register(ctx):
    try:
        p = _run(["tool", "--list"], timeout=8)
        tools = json.loads(p.stdout) if p.returncode == 0 else []
        if p.returncode != 0:
            log.warning("{{product}}: `{{command}} tool --list` failed: %s", p.stderr.strip())
    except Exception as e:
        log.warning("{{product}}: couldn't list its tools with %s: %s", MUR, e)
        tools = []
    for t in tools:
        name = "{{server}}_" + t["name"]
        schema = {"name": name, "description": t["description"], "parameters": t["inputSchema"]}
        ctx.register_tool(name=name, toolset="{{server}}", schema=schema, handler=_handler(ctx, t["name"]))
    _register_hooks(ctx)
    ctx.register_hook("subagent_start", _child_start)
    ctx.register_hook("subagent_stop", _child_stop)
