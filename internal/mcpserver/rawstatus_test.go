package mcpserver

import (
	"io"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-Fig/murmur/internal/proto"
)

// A status is checked for secrets as written, as a subject is (#1048): only its cleaned line goes
// to the daemon, where a key a zero-width space joins to the next word, or one the line's cut
// shortens, no longer looks like one.
func TestAStatusIsCheckedAsWritten(t *testing.T) {
	cut := strings.Repeat("x ", 55) + "AKIA" + "IOSFODNN7EXAMPLE"
	for _, doing := range []string{akiaJoined, cut} {
		if _, err := statusLine(doing); err == nil || !strings.Contains(err.Error(), "an AWS access key") {
			t.Errorf("statusLine(%q): %v", doing, err)
		}
		if _, err := commandRequest("status", strings.Fields(doing), strings.NewReader(""), io.Discard); err == nil {
			t.Errorf("mur status %q: no error", doing)
		}
		f := &fakeDaemon{}
		res := callListTool(t, f, "status", map[string]any{"doing": doing})
		if text := res.Content[0].(*mcp.TextContent).Text; !res.IsError || !strings.Contains(text, "an AWS access key") {
			t.Errorf("the status tool with %q: error %v, %q", doing, res.IsError, text)
		}
		if got := f.requestsOf(proto.OpStatus); len(got) != 0 {
			t.Errorf("the daemon was asked to set %+v", got)
		}
	}
	for _, doing := range []string{"fixing #1048\u200b, the status gap", "", `reading C:\Users\tyler\murmur`, "the sk- prefix is for keys"} {
		if got, err := statusLine(doing); err != nil || got != oneLine(doing) {
			t.Errorf("statusLine(%q) = %q, %v", doing, got, err)
		}
	}
}
