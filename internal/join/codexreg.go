package join

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/a-Fig/murmur/internal/brand"
)

// registerCodex registers in the Codex home of the process's own environment.
func (e Env) registerCodex() (did []string, fresh bool, err error) {
	return e.registerCodexAt(e.defaultHome(agents[1]))
}

// registerCodexAt puts Murmur's MCP server in the config.toml of Codex home h, for this user in
// every project. fresh reports whether the file changed. Codex isn't touched on a machine that has
// neither its command nor the home directory.
func (e Env) registerCodexAt(h agentHome) (did []string, fresh bool, err error) {
	if h.Dir == "" {
		return nil, false, nil
	}
	if _, lerr := e.look("codex"); lerr != nil && h.Bin == "" && !isDir(h.Dir) {
		return nil, false, nil
	}
	path := filepath.Join(h.Dir, "config.toml")
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, false, err
	}
	out, changed, err := editCodexConfig(string(b), e.Exe)
	if err == nil && changed {
		err = checkTOMLEdit(string(b), out)
	}
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", path, err)
	}
	if changed {
		if err := writeAtomic(path, []byte(out), 0o600); err != nil {
			return nil, false, fmt.Errorf("%s: %w", path, err)
		}
		did = append(did, h.Label+": registered the "+brand.MCPServer+" MCP server for you, in every project, and allowed its tools ("+path+")")
	}
	// The hook, after the server: its trust goes in the same config.toml, written again (#225).
	hookDid, err := e.installCodexHook(h)
	if err != nil {
		return did, changed, err
	}
	return append(did, hookDid...), changed, nil
}
