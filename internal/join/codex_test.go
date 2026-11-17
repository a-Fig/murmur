package join

import (
	"strings"
	"testing"
)

const tomlTable = "[mcp_servers.murmur]\ncommand = \"/opt/mur\"\nargs = [\"mcp\", \"--agent\", \"codex\"]\nenv_vars = [\"T3CODE_CLI_PATH\", \"CODEX_HOME\"]\ndefault_tools_approval_mode = \"approve\"\n"

func edit(t *testing.T, text string) (string, bool) {
	t.Helper()
	out, changed, err := editCodexConfig(text, "/opt/mur")
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	return out, changed
}

// No config yet: the file is just the server's table.
func TestCodexEditCreatesTheTable(t *testing.T) {
	out, changed := edit(t, "")
	if !changed || out != tomlTable {
		t.Errorf("changed=%v:\n%s", changed, out)
	}
}

// Everything the user wrote stays exactly as it was, comments and odd spacing included, and the
// table goes at the end.
func TestCodexEditKeepsTheRestOfTheFile(t *testing.T) {
	before := "# my codex\nmodel   =  \"x\"\n\n[mcp_servers.other]\ncommand = \"o\"   # keep\nargs=['a']\n\n[profiles.p]\nmodel = \"y\"\n"
	out, changed := edit(t, before)
	if want := before + "\n" + tomlTable; !changed || out != want {
		t.Errorf("changed=%v:\n%s\nwant:\n%s", changed, out, want)
	}
	// No newline at the end of the file.
	out, _ = edit(t, strings.TrimSuffix(before, "\n"))
	if want := before + "\n" + tomlTable; out != want {
		t.Errorf("without a final newline:\n%s\nwant:\n%s", out, want)
	}
}

// Joining again, with the same binary, changes nothing; with the binary somewhere else, it
// changes the lines that name it and keeps the user's own settings in the table.
func TestCodexEditIsIdempotentAndUpdatesInPlace(t *testing.T) {
	out, _ := edit(t, "model = \"x\"\n")
	if again, changed := edit(t, out); changed || again != out {
		t.Errorf("a second edit changed the file (changed=%v):\n%s", changed, again)
	}

	stale := "[mcp_servers.murmur]\n# mine\ncommand = '/old/mur'\nstartup_timeout_sec = 30\nargs = [\"mcp\"]\ndefault_tools_approval_mode = \"prompt\"\n\n[mcp_servers.murmur.env]\nA = \"1\"\n\n[other]\nx = 1\n"
	want := "[mcp_servers.murmur]\n# mine\ncommand = \"/opt/mur\"\nstartup_timeout_sec = 30\nargs = [\"mcp\", \"--agent\", \"codex\"]\ndefault_tools_approval_mode = \"prompt\"\nenv_vars = [\"T3CODE_CLI_PATH\", \"CODEX_HOME\"]\n\n[mcp_servers.murmur.env]\nA = \"1\"\n\n[other]\nx = 1\n"
	if got, changed := edit(t, stale); !changed || got != want {
		t.Errorf("changed=%v:\n%s\nwant:\n%s", changed, got, want)
	}

	// A table with only some of the keys gets the others, before the next table.
	partial := "[mcp_servers.murmur]\ncommand = \"/opt/mur\"\n\n[mcp_servers.murmur.env]\nA = \"1\"\n"
	want = "[mcp_servers.murmur]\ncommand = \"/opt/mur\"\nargs = [\"mcp\", \"--agent\", \"codex\"]\nenv_vars = [\"T3CODE_CLI_PATH\", \"CODEX_HOME\"]\ndefault_tools_approval_mode = \"approve\"\n\n[mcp_servers.murmur.env]\nA = \"1\"\n"
	if got, _ := edit(t, partial); got != want {
		t.Errorf("partial table:\n%s\nwant:\n%s", got, want)
	}

	// The keys go above a comment the user wrote for the next table, which stays with that table, and
	// uninstall then leaves it (#755). A comment between Murmur's keys stays where it is.
	commented := "[mcp_servers.murmur]\n# mine\ncommand = \"/opt/mur\"\nargs = [\"mcp\"]\n\n# GitHub's\n[mcp_servers.github]\ncommand = \"gh\"\n"
	want = "[mcp_servers.murmur]\n# mine\ncommand = \"/opt/mur\"\nargs = [\"mcp\", \"--agent\", \"codex\"]\nenv_vars = [\"T3CODE_CLI_PATH\", \"CODEX_HOME\"]\ndefault_tools_approval_mode = \"approve\"\n\n# GitHub's\n[mcp_servers.github]\ncommand = \"gh\"\n"
	got, _ := edit(t, commented)
	if got != want {
		t.Errorf("a comment for the next table:\n%s\nwant:\n%s", got, want)
	}
	server := []string{"mcp_servers", "murmur"}
	drop := func(p []string) ([]string, bool) { return nil, hasPrefix(p, server) }
	if out, _, err := editTOMLTables(got, "config.toml", [][]string{server}, drop); err != nil || out != "# GitHub's\n[mcp_servers.github]\ncommand = \"gh\"\n" {
		t.Errorf("uninstall after it: %v\n%s", err, out)
	}
}

func TestCodexEditKeepsCRLF(t *testing.T) {
	out, _ := edit(t, "model = \"x\"\r\n")
	if strings.Count(out, "\n") != strings.Count(out, "\r\n") || !strings.Contains(out, "[mcp_servers.murmur]\r\n") {
		t.Errorf("mixed line endings: %q", out)
	}
	again, changed := edit(t, out)
	if changed || again != out {
		t.Errorf("a second edit changed the CRLF file: %q", again)
	}
	crStale := strings.ReplaceAll("[mcp_servers.murmur]\ncommand = \"/old\"\nargs = [\"mcp\"]\n", "\n", "\r\n")
	got, _ := edit(t, crStale)
	if strings.Count(got, "\n") != strings.Count(got, "\r\n") || !strings.Contains(got, "command = \"/opt/mur\"\r\n") {
		t.Errorf("stale CRLF table: %q", got)
	}

	// Murmur's table last, with no newline at the file's end: the keys added after its last line give
	// that line its CRLF, and the file still ends without one, never in a lone CR (#783). The same when
	// the header is the last line.
	tail := "env_vars = [\"T3CODE_CLI_PATH\", \"CODEX_HOME\"]\r\ndefault_tools_approval_mode = \"approve\""
	for in, want := range map[string]string{
		"x = 1\r\n\r\n[mcp_servers.murmur]\r\ncommand = \"/opt/mur\"\r\nargs = [\"mcp\", \"--agent\", \"codex\"]": "x = 1\r\n\r\n[mcp_servers.murmur]\r\ncommand = \"/opt/mur\"\r\nargs = [\"mcp\", \"--agent\", \"codex\"]\r\n" + tail,
		"x = 1\r\n\r\n[mcp_servers.murmur]": "x = 1\r\n\r\n[mcp_servers.murmur]\r\ncommand = \"/opt/mur\"\r\nargs = [\"mcp\", \"--agent\", \"codex\"]\r\n" + tail,
	} {
		got, _ := edit(t, in)
		if got != want {
			t.Errorf("CRLF, no final newline: %q\nwant %q", got, want)
		}
		if again, changed := edit(t, got); changed || again != got {
			t.Errorf("a second edit changed it: %q", again)
		}
		// Uninstall gives the file back without Murmur's table, ending as it did, without a lone CR.
		server := []string{"mcp_servers", "murmur"}
		drop := func(p []string) ([]string, bool) { return nil, hasPrefix(p, server) }
		if out, _, err := editTOMLTables(got, "config.toml", [][]string{server}, drop); err != nil || out != "x = 1" {
			t.Errorf("uninstall after it: %q %v", out, err)
		}
	}
}

// A table header inside a multi-line string is text, not a table.
func TestCodexEditIgnoresHeadersInStrings(t *testing.T) {
	before := "notes = \"\"\"\n[mcp_servers.murmur]\ncommand = \"fake\"\n\"\"\"\n"
	out, _ := edit(t, before)
	if want := before + "\n" + tomlTable; out != want {
		t.Errorf("got:\n%s", out)
	}
}

// Forms join doesn't edit are refused, rather than guessed at: a second definition of the table
// would make the file invalid, and Codex then loads none of its config.
func TestCodexEditRefusesFormsItDoesNotEdit(t *testing.T) {
	for name, text := range map[string]string{
		"dotted key":     "mcp_servers.murmur.command = \"x\"\n",
		"inline table":   "mcp_servers = { murmur = { command = \"x\" } }\n",
		"multiline args": "[mcp_servers.murmur]\ncommand = \"/opt/mur\"\nargs = [\n  \"mcp\",\n]\n",
	} {
		if out, _, err := editCodexConfig(text, "/opt/mur"); err == nil {
			t.Errorf("%s: edited it:\n%s", name, out)
		}
	}
	// The same key names inside another table are that table's own.
	if _, _, err := editCodexConfig("[profiles.p]\nmcp_servers = 1\n", "/opt/mur"); err != nil {
		t.Errorf("refused a key that belongs to another table: %v", err)
	}
}

// A header may quote its keys and carry a comment; neither changes where a table starts or ends
// (Codex review, 2026-10-29: the comment kept the header from parsing, and the table before it
// swallowed the other server's lines).
func TestCodexEditReadsQuotedHeadersWithComments(t *testing.T) {
	before := tomlTable + "\n[mcp_servers.\"other\"] # personal\ncommand = \"o\"\nargs = [\"x\"]\n"
	if out, changed := edit(t, before); changed {
		t.Errorf("changed a table that was right:\n%s", out)
	}
	// The same spelling for Murmur's own table is that table, edited in place, not a second one.
	own := "[ mcp_servers . \"murmur\" ] # mine\ncommand = '/old/mur' # keep\n\n[other]\nx = 1\n"
	got, changed := edit(t, own)
	want := "[ mcp_servers . \"murmur\" ] # mine\ncommand = \"/opt/mur\"\nargs = [\"mcp\", \"--agent\", \"codex\"]\nenv_vars = [\"T3CODE_CLI_PATH\", \"CODEX_HOME\"]\ndefault_tools_approval_mode = \"approve\"\n\n[other]\nx = 1\n"
	if !changed || got != want {
		t.Errorf("changed=%v:\n%s\nwant:\n%s", changed, got, want)
	}
	// A comment after a value that is already right isn't a difference.
	commented := "[mcp_servers.murmur]\ncommand = \"/opt/mur\" # mine\nargs = [\"mcp\", \"--agent\", \"codex\"] # mine\nenv_vars = [\"T3CODE_CLI_PATH\", \"CODEX_HOME\"]\ndefault_tools_approval_mode = \"approve\"\n"
	if out, changed := edit(t, commented); changed {
		t.Errorf("rewrote a value that was right:\n%s", out)
	}
}

// Murmur's server can also be defined inside [mcp_servers] itself; a table of join's own would then
// be a second definition, and the file would no longer parse.
func TestCodexEditRefusesMurmurInsideTheParentTable(t *testing.T) {
	for name, text := range map[string]string{
		"inline table": "[mcp_servers]\nmurmur = { command = \"mur\", args = [\"mcp\"] }\n",
		"dotted key":   "[mcp_servers]\nmurmur.command = \"mur\"\n",
		"quoted key":   "[mcp_servers]\n\"murmur\".command = 'x'\n",
	} {
		if out, _, err := editCodexConfig(text, "/opt/mur"); err == nil {
			t.Errorf("%s: edited it:\n%s", name, out)
		}
	}
	// Other servers in the parent table are theirs, and Murmur's table goes after them.
	before := "[mcp_servers]\nother = { command = \"o\" }\n"
	if out, changed := edit(t, before); !changed || out != before+"\n"+tomlTable {
		t.Errorf("changed=%v:\n%s", changed, out)
	}
}

func TestCodexEditQuotesWindowsPaths(t *testing.T) {
	out, _, err := editCodexConfig("", `C:\Users\a b\bin\mur.exe`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `command = "C:\\Users\\a b\\bin\\mur.exe"`) {
		t.Errorf("the path isn't written as a TOML string:\n%s", out)
	}
	if _, changed, _ := editCodexConfig(out, `C:\Users\a b\bin\mur.exe`); changed {
		t.Error("a second edit changed it")
	}
}

// Codex starts MCP servers with a bare environment and passes on only the variables a table names
// in env_vars, so join names T3CODE_CLI_PATH, how a session in T3 Code is found and woken, and
// CODEX_HOME, where the daemon reads a Codex thread's name. What the user already lists there
// stays, and each name is added to it only if it is missing: a table an older join wrote, with
// T3CODE_CLI_PATH only, gets CODEX_HOME on the next join.
func TestCodexEditForwardsItsVariables(t *testing.T) {
	head := "[mcp_servers.murmur]\ncommand = \"/opt/mur\"\nargs = [\"mcp\", \"--agent\", \"codex\"]\n"
	tail := "default_tools_approval_mode = \"approve\"\n"
	for name, tc := range map[string]struct{ in, want string }{
		"none":             {head + tail, head + tail + "env_vars = [\"T3CODE_CLI_PATH\", \"CODEX_HOME\"]\n"},
		"someone else's":   {head + "env_vars = [\"HTTP_PROXY\"]\n" + tail, head + "env_vars = [\"HTTP_PROXY\", \"T3CODE_CLI_PATH\", \"CODEX_HOME\"]\n" + tail},
		"empty":            {head + "env_vars = []\n" + tail, head + "env_vars = [\"T3CODE_CLI_PATH\", \"CODEX_HOME\"]\n" + tail},
		"with a comment":   {head + "env_vars = [\"A\"] # mine [x]\n" + tail, head + "env_vars = [\"A\", \"T3CODE_CLI_PATH\", \"CODEX_HOME\"] # mine [x]\n" + tail},
		"an older join's":  {head + "env_vars = [\"T3CODE_CLI_PATH\"]\n" + tail, head + "env_vars = [\"T3CODE_CLI_PATH\", \"CODEX_HOME\"]\n" + tail},
		"the home only":    {head + "env_vars = [\"A\", 'CODEX_HOME']\n" + tail, head + "env_vars = [\"A\", \"CODEX_HOME\", \"T3CODE_CLI_PATH\"]\n" + tail},
		"already there":    {head + "env_vars = [\"A\", \"T3CODE_CLI_PATH\", \"CODEX_HOME\"]\n" + tail, ""},
		"in another order": {head + "env_vars = ['CODEX_HOME', 'A', 'T3CODE_CLI_PATH']\n" + tail, ""},
	} {
		got, changed := edit(t, tc.in)
		if tc.want == "" {
			if changed || got != tc.in {
				t.Errorf("%s: changed the user's own list (changed=%v):\n%s", name, changed, got)
			}
			continue
		}
		if !changed || got != tc.want {
			t.Errorf("%s: changed=%v:\n%s\nwant:\n%s", name, changed, got, tc.want)
		}
		if again, changed := edit(t, got); changed || again != got {
			t.Errorf("%s: a second edit changed it:\n%s", name, again)
		}
	}
	// A list over several lines, or of anything but names, is the user's to change.
	for name, text := range map[string]string{
		"several lines": head + "env_vars = [\n  \"A\",\n]\n" + tail,
		"inline tables": head + "env_vars = [{ name = \"A\" }]\n" + tail,
	} {
		if out, _, err := editCodexConfig(text, "/opt/mur"); err == nil {
			t.Errorf("%s: edited it:\n%s", name, out)
		} else if !strings.Contains(err.Error(), `"T3CODE_CLI_PATH", "CODEX_HOME"`) {
			t.Errorf("%s: the error doesn't say what to add by hand: %v", name, err)
		}
	}
}
