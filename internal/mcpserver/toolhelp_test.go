package mcpserver

import (
	"slices"
	"strings"
	"testing"
)

// `mur help` lists the tools from toolList, so a tool registered without a line there would be
// missing from it (#1103).
func TestToolListNamesEveryRegisteredTool(t *testing.T) {
	tools, err := registered()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tool := range tools {
		got = append(got, tool.Name)
		if strings.TrimSpace(tool.Description) == "" {
			t.Errorf("tool %s has no description", tool.Name)
		}
	}
	var want []string
	for _, info := range Tools() {
		want = append(want, info.Name)
		if strings.TrimSpace(info.Summary) == "" || strings.Contains(info.Summary, "\n") {
			t.Errorf("tool %s needs a one-line summary for mur help, got %q", info.Name, info.Summary)
		}
	}
	if !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))) {
		t.Errorf("the server registers %q but mur help lists %q: add the missing tool to toolList", got, want)
	}
	if d, err := ToolDescription("lookup"); err != nil || !strings.Contains(d, "Look up") {
		t.Errorf("ToolDescription(lookup) = %q, %v", d, err)
	}
	if _, err := ToolDescription("nope"); err == nil {
		t.Error("a description for no tool")
	}
}
