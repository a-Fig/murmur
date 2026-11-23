package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mur docs prints each page of docs/agents/ as it is in the source, and lists them all (#831). An
// agent with no internet reads them this way, so a page must be the same text the links point to.
func TestDocsPrintsTheAgentPages(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "docs", "agents", "*.md"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no pages in docs/agents: %v", err)
	}
	var list bytes.Buffer
	if err := runDocs(nil, &list); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".md")
		want, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := runDocs([]string{name}, &out); err != nil || !bytes.Equal(out.Bytes(), want) {
			t.Errorf("mur docs %s: %v, %d bytes, want the file's %d", name, err, out.Len(), len(want))
		}
		if !strings.Contains(list.String(), name) {
			t.Errorf("mur docs doesn't list %s: %q", name, list.String())
		}
	}
	for _, name := range []string{"join", "use"} {
		if !strings.Contains(list.String(), name) {
			t.Errorf("mur docs doesn't list %s, which the repo's section links to", name)
		}
	}
	if err := runDocs([]string{"nope"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "join") {
		t.Errorf("mur docs nope: %v", err)
	}
	var help bytes.Buffer
	if err := runDocs([]string{"--help"}, &help); err != nil || help.String() != list.String() {
		t.Errorf("mur docs --help: %v, %q", err, help.String())
	}
	if err := runDocs([]string{"join", "use"}, &bytes.Buffer{}); err == nil {
		t.Error("mur docs with two pages worked")
	}
}
