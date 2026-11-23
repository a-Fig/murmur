package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/a-Fig/murmur/docs/agents"
	"github.com/a-Fig/murmur/internal/brand"
)

// runDocs is `mur docs [PAGE]`: a page of docs/agents/, as this version has it, for an agent that
// can read the repo's section but can't open the pages it links to, as in a sandbox with no internet
// (#831). The section itself stays as it is (Tyler, 2026-11-16).
func runDocs(args []string, out io.Writer) error {
	names := agents.Names()
	switch len(args) {
	case 0:
		fmt.Fprintf(out, "Pages: %s. Print one with %s docs PAGE.\n", strings.Join(names, ", "), brand.Command)
		return nil
	case 1:
		if page, ok := agents.Page(args[0]); ok {
			fmt.Fprint(out, page)
			return nil
		}
		return fmt.Errorf("no page %q: the pages are %s", args[0], strings.Join(names, ", "))
	}
	return fmt.Errorf("usage: %s docs [PAGE]", brand.Command)
}
