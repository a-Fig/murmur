package join

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/a-Fig/murmur/internal/brand"
)

// Codex keeps its MCP servers in config.toml, a file the user also edits by hand and Codex rewrites
// whole when asked to add a server (`codex mcp add` reorders keys and drops empty ones, seen on
// 0.164.0, 2026-10-29). So join edits it as text: it adds one table at the end, or changes the
// lines of its own table in place, and every other byte stays.

var codexArgs = []string{"mcp", "--agent", "codex"}

// tomlServer is what join writes in an agent's [mcp_servers.murmur] table: Codex's, and Grok's,
// which has the same shape (docs.x.ai/build/features/mcp-servers, read 2026-11-01).
type tomlServer struct {
	file    string   // the file's name, for errors
	args    []string // after the command
	envVars []string // the names env_vars must hold; nil writes no env_vars
	approve bool     // default_tools_approval_mode = "approve"
}

var codexServer = tomlServer{file: "config.toml", args: codexArgs, envVars: []string{t3CLIEnv, codexHomeEnv}, approve: true}

// t3CLIEnv is the variable that says a session runs inside T3 Code, and where its command line is.
// Codex starts an MCP server with a bare environment and passes on only the variables the table
// names in env_vars, so without this one the server can't tell it is in T3 and the daemon has no way
// to wake the thread with a message (docs/design/handoff.md, "Codex under T3").
const t3CLIEnv = "T3CODE_CLI_PATH"

// codexHomeEnv is the Codex home of a session whose home isn't ~/.codex. The server passes it to
// the daemon, which reads a Codex thread's name from that home outside T3; without it the daemon
// reads its own home, which may be another (docs/design/presence.md, the table of titles). Codex
// leaves out a name its own environment doesn't have (create_env_for_mcp_server in
// codex-rs/rmcp-client, read 2026-11-09), so a session in ~/.codex is started as before.
const codexHomeEnv = "CODEX_HOME"

// editCodexConfig returns config.toml's text with Murmur's server in it. changed is false, and out
// is text, when the table already says what join would write; a user's own env_vars is kept, with
// the names join forwards added to it if it lacks them. It refuses (an error) a file whose Murmur
// entry is written in a form it doesn't edit: two definitions of one table make the file invalid,
// and Codex then fails to load any of its config.
func editCodexConfig(text, exe string) (out string, changed bool, err error) {
	return editTOMLServer(text, exe, codexServer)
}

// editTOMLServer is editCodexConfig for the table spec says.
func editTOMLServer(text, exe string, spec tomlServer) (out string, changed bool, err error) {
	nl := "\n"
	if strings.Contains(text, "\r\n") {
		nl = "\r\n"
	}
	lines := strings.Split(text, "\n") // a CRLF file keeps its \r on each line, which isn't touched
	start, end := -1, len(lines)
	open := ""         // the multi-line string the line starts in
	var table []string // the table the line is in; none before the first header
	for i, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		inString := open != ""
		if open = tomlLineEnd(line, open); inString {
			continue
		}
		if path, ok := tableHeader(line); ok {
			table = path
			switch {
			case start >= 0 && end == len(lines):
				end = i
			case start < 0 && len(path) == 2 && path[0] == "mcp_servers" && path[1] == brand.MCPServer && !strings.HasPrefix(strings.TrimSpace(line), "[["):
				start = i
			}
			continue
		}
		// A key that defines the server, or all the servers, without a [mcp_servers.murmur] header of its
		// own: `mcp_servers.x.command = ...` or `mcp_servers = {...}` before the first header, and
		// `murmur = {...}` or `murmur.command = ...` under [mcp_servers]. Join's own table would define it twice.
		if key, _, ok := keyValue(line); ok {
			if (len(table) == 0 && key[0] == "mcp_servers") || (len(table) == 1 && table[0] == "mcp_servers" && key[0] == brand.MCPServer) {
				return "", false, errors.New(spec.file + " sets the mcp_servers table, or the " + brand.MCPServer + " server in it, in a form join doesn't edit (a dotted key or an inline table)")
			}
		}
	}

	want := map[string]string{
		"command":  "command = " + tomlString(exe),
		"args":     "args = [" + joinQuoted(spec.args) + "]",
		"env_vars": "env_vars = [" + joinQuoted(spec.envVars) + "]",
	}
	approve := `default_tools_approval_mode = "approve"`
	keys := []string{"command", "args"}
	if spec.envVars != nil {
		keys = append(keys, "env_vars")
	}

	if start < 0 {
		var b strings.Builder
		b.WriteString(text)
		if text != "" && !strings.HasSuffix(text, "\n") {
			b.WriteString(nl)
		}
		if strings.TrimSpace(text) != "" {
			b.WriteString(nl)
		}
		lines := []string{"[mcp_servers." + brand.MCPServer + "]"}
		for _, k := range keys {
			lines = append(lines, want[k])
		}
		if spec.approve {
			lines = append(lines, approve)
		}
		for _, l := range lines {
			b.WriteString(l + nl)
		}
		return b.String(), true, nil
	}

	block := append([]string(nil), lines[start+1:end]...)
	found := map[string]bool{}
	for i, raw := range block {
		line := strings.TrimRight(raw, "\r")
		path, val, ok := keyValue(line)
		if !ok || len(path) != 1 {
			continue
		}
		key := path[0]
		cr := raw[len(line):]
		switch key {
		case "command", "args":
			found[key] = true
			if !singleLine(val) {
				return "", false, fmt.Errorf("%s has %s of the %s server on several lines, which join doesn't edit", spec.file, key, brand.MCPServer)
			}
			if !sameValue(key, val, exe, spec.args) {
				block[i] = want[key] + cr
			}
		case "env_vars":
			if spec.envVars == nil {
				continue // the user's own
			}
			found[key] = true
			items, after, ok := stringListRest(val)
			if !singleLine(val) || !ok || (after != "" && after[0] != '#') {
				return "", false, fmt.Errorf("%s has env_vars of the %s server in a form join doesn't edit (several lines, or not a list of names): add %s to it by hand", spec.file, brand.MCPServer, joinQuoted(spec.envVars))
			}
			var missing []string
			for _, v := range spec.envVars {
				if !slices.Contains(items, v) {
					missing = append(missing, v)
				}
			}
			if len(missing) > 0 {
				if after != "" {
					after = " " + after // the user's comment stays
				}
				block[i] = "env_vars = [" + joinQuoted(append(items, missing...)) + "]" + after + cr
			}
		case "default_tools_approval_mode":
			found[key] = true // the user's own setting, kept
		}
	}
	// Keys the table lacks go after its last line with a key or value on it, where uninstall reads the
	// table as ending. A comment below that line is the user's, about the table that follows: keys added
	// below it would take it into Murmur's table, and uninstall would drop it with them (#755). A line
	// of a multi-line string is the value's, whatever it starts with.
	at, open := 0, ""
	for i, raw := range block {
		line := strings.TrimRight(raw, "\r")
		inString := open != ""
		open = tomlLineEnd(line, open)
		if t := strings.TrimSpace(line); t != "" && (inString || !strings.HasPrefix(t, "#")) {
			at = i + 1
		}
	}
	var add []string
	for _, key := range keys {
		if !found[key] {
			add = append(add, want[key])
		}
	}
	if spec.approve && !found["default_tools_approval_mode"] {
		add = append(add, approve)
	}
	for i := range add {
		if nl == "\r\n" {
			add[i] += "\r"
		}
	}
	// Added after the file's last line, which has no newline, the keys take the CRLF that line lacks,
	// and the last of them goes without one, as that line did: a \r of their own at the very end
	// would be a lone CR, which TOML forbids, and Codex would then refuse the file (#783).
	if nl == "\r\n" && len(add) > 0 && at == len(block) && end == len(lines) {
		last := &lines[start]
		if at > 0 {
			last = &block[at-1]
		}
		if !strings.HasSuffix(*last, "\r") {
			*last += "\r"
		}
		add[len(add)-1] = strings.TrimSuffix(add[len(add)-1], "\r")
	}
	block = append(block[:at], append(add, block[at:]...)...)

	rebuilt := append(append(append([]string(nil), lines[:start+1]...), block...), lines[end:]...)
	out = strings.Join(rebuilt, "\n")
	return out, out != text, nil
}

// tomlLineEnd reads one line of TOML that starts inside the multi-line string open (three double
// quotes or three single quotes, "" for none) and returns the one still open at its end, "" for none.
// A line that starts inside one is the string's text, not a header or a key. Comments and one-line
// strings are skipped as wholes, so a `"""` in either opens nothing: counting the quotes on a line
// took a comment like `# wrap prompts in """` for an opening, missed [mcp_servers.murmur] below it,
// and the next join appended a second one, which makes Codex refuse the whole file.
func tomlLineEnd(line, open string) string {
	i := 0
	if open != "" {
		if i = closeMultiline(line, 0, open); i < 0 {
			return open
		}
	}
	for i < len(line) {
		switch c := line[i]; c {
		case '#':
			return ""
		case '"', '\'':
			q := string(c)
			if strings.HasPrefix(line[i:], q+q+q) {
				if i = closeMultiline(line, i+3, q+q+q); i < 0 {
					return q + q + q
				}
				continue
			}
			j := i + 1
			for j < len(line) && line[j] != c {
				if c == '"' && line[j] == '\\' {
					j++ // an escape, in a basic string only
				}
				j++
			}
			i = j + 1
		default:
			i++
		}
	}
	return ""
}

// closeMultiline finds the end of the multi-line string delim (three double or three
// single quotes) in line from i, and returns the index just past it, or -1 when the string goes on
// past the line. Up to two more quotes right before the closing three belong to the string. A
// backslash escapes the next character in a basic string, not in a literal one.
func closeMultiline(line string, i int, delim string) int {
	q := delim[0]
	for i < len(line) {
		switch {
		case q == '"' && line[i] == '\\':
			i += 2
		case line[i] == q:
			n := 0
			for i+n < len(line) && line[i+n] == q {
				n++
			}
			if n >= 3 {
				return i + min(n, 5)
			}
			i += n
		default:
			i++
		}
	}
	return -1
}

// tableHeader parses a line that is a table header, [a.b] or [[a.b]], into its key path. Only a
// comment may follow the header.
func tableHeader(line string) ([]string, bool) {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "[") {
		return nil, false
	}
	closing := "]"
	if strings.HasPrefix(t, "[[") {
		closing = "]]" // array of tables
	}
	path, rest, ok := keyPath(strings.TrimLeft(t, "[")) // past the opening brackets of either form
	if !ok || !strings.HasPrefix(rest, closing) {
		return nil, false
	}
	rest = strings.TrimSpace(strings.TrimPrefix(rest, closing))
	return path, rest == "" || rest[0] == '#'
}

// keyValue splits a `key = value` line into its key path (a.b.c is three parts) and its value.
func keyValue(line string) (path []string, val string, ok bool) {
	path, rest, ok := keyPath(strings.TrimSpace(line))
	if !ok || !strings.HasPrefix(rest, "=") {
		return nil, "", false
	}
	return path, strings.TrimSpace(rest[1:]), true
}

// keyPath reads a dotted key (bare or quoted parts, spaces allowed around the dots) from the start of
// t and returns it with what follows, left-trimmed.
func keyPath(t string) (path []string, rest string, ok bool) {
	for {
		t = strings.TrimLeft(t, " \t")
		var part string
		switch {
		case t == "":
			return nil, "", false
		case t[0] == '"' || t[0] == '\'':
			end := closingQuote(t)
			if end < 0 {
				return nil, "", false
			}
			if part, ok = tomlUnquote(t[:end+1]); !ok {
				return nil, "", false
			}
			t = t[end+1:]
		default:
			end := 0
			for end < len(t) && (t[end] == '-' || t[end] == '_' || t[end] >= '0' && t[end] <= '9' || t[end] >= 'a' && t[end] <= 'z' || t[end] >= 'A' && t[end] <= 'Z') {
				end++
			}
			if end == 0 {
				return nil, "", false
			}
			part, t = t[:end], t[end:]
		}
		path = append(path, part)
		t = strings.TrimLeft(t, " \t")
		if !strings.HasPrefix(t, ".") {
			return path, t, true
		}
		t = t[1:]
	}
}

// closingQuote is the index of the quote that ends the string t starts with, or -1.
func closingQuote(t string) int {
	if t[0] == '\'' {
		if end := strings.IndexByte(t[1:], '\''); end >= 0 {
			return end + 1
		}
		return -1
	}
	for j := 1; j < len(t); j++ {
		switch t[j] {
		case '\\':
			j++
		case '"':
			return j
		}
	}
	return -1
}

// singleLine reports whether a value is complete on its line.
func singleLine(val string) bool {
	switch {
	case strings.HasPrefix(val, `"""`), strings.HasPrefix(val, `'''`):
		return false
	case strings.HasPrefix(val, "["):
		return strings.Count(val, "[") == strings.Count(val, "]")
	}
	return true
}

// sameValue reports whether a table's command or args value already says what join writes. A
// value it can't read counts as different, and is replaced.
func sameValue(key, val, exe string, args []string) bool {
	items, ok := stringList(val)
	if key == "command" {
		s, ok := tomlUnquote(stripComment(val))
		return ok && s == exe
	}
	if !ok || len(items) != len(args) {
		return false
	}
	for i := range items {
		if items[i] != args[i] {
			return false
		}
	}
	return true
}

func stripComment(val string) string {
	if len(val) > 0 && (val[0] == '"' || val[0] == '\'') {
		if end := closingQuote(val); end >= 0 {
			return val[:end+1]
		}
		return val
	}
	if i := strings.Index(val, "#"); i >= 0 {
		return strings.TrimSpace(val[:i])
	}
	return val
}

// stringList reads ["a", 'b'] on one line.
func stringList(val string) ([]string, bool) {
	items, _, ok := stringListRest(val)
	return items, ok
}

// stringListRest is stringList with what follows the closing bracket, trimmed: a comment, or nothing.
func stringListRest(val string) (items []string, after string, ok bool) {
	val = strings.TrimSpace(val)
	if !strings.HasPrefix(val, "[") {
		return nil, "", false
	}
	rest := strings.TrimSpace(val[1:])
	var out []string
	for {
		if strings.HasPrefix(rest, "]") {
			return out, strings.TrimSpace(rest[1:]), true
		}
		if rest == "" || (rest[0] != '"' && rest[0] != '\'') {
			return nil, "", false
		}
		var s string
		var n int
		if rest[0] == '\'' {
			end := strings.IndexByte(rest[1:], '\'')
			if end < 0 {
				return nil, "", false
			}
			s, n = rest[1:1+end], end+2
		} else {
			end := -1
			for j := 1; j < len(rest); j++ {
				if rest[j] == '\\' {
					j++
				} else if rest[j] == '"' {
					end = j
					break
				}
			}
			if end < 0 {
				return nil, "", false
			}
			var ok bool
			if s, ok = tomlUnquote(rest[:end+1]); !ok {
				return nil, "", false
			}
			n = end + 1
		}
		out = append(out, s)
		rest = strings.TrimSpace(rest[n:])
		rest = strings.TrimSpace(strings.TrimPrefix(rest, ","))
	}
}

// tomlString writes s as a TOML basic string.
func tomlString(s string) string { return strconv.Quote(s) }

// tomlUnquote reads a single-line TOML string, basic or literal.
func tomlUnquote(v string) (string, bool) {
	if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
		return v[1 : len(v)-1], true
	}
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		s, err := strconv.Unquote(v)
		return s, err == nil
	}
	return "", false
}

func joinQuoted(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = tomlString(s)
	}
	return strings.Join(q, ", ")
}
