package join

import (
	"fmt"

	"github.com/BurntSushi/toml"
	"github.com/a-Fig/murmur/internal/brand"
)

// checkTOMLEdit is the last check before join writes its edit of a TOML file (Codex's or Grok's
// config.toml): an edit that would leave a file that parsed as one that doesn't is refused, and the
// user's file stays as it was. Join edits these files as text, by line, so a slip there (a lone \r
// at the end, #946) would otherwise reach the file, and Codex then refuses all of its config.
//
// A file that didn't parse before the edit isn't judged: it may be broken already, which join's
// edit doesn't make worse, or use TOML this parser doesn't read (it reads TOML 1.0, and Codex's
// parser may take newer syntax), and refusing it would stop join for a file Codex reads.
func checkTOMLEdit(before, after string) error {
	if _, err := toml.Decode(after, new(map[string]any)); err == nil {
		return nil
	} else if _, berr := toml.Decode(before, new(map[string]any)); berr == nil {
		return fmt.Errorf("%s's edit would have made it invalid TOML (%v), so it left the file as it was; this is a bug in %s, please report it", brand.Product, err, brand.Product)
	}
	return nil
}
