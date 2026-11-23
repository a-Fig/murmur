package join

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// T3 Code can run several accounts of one agent side by side, each a "provider instance" with a
// home of its own: Tyler has a work and a personal Claude Code in T3, the personal one under its own
// CLAUDE_CONFIG_DIR (2026-11-01). A session in a home join didn't register in has no Murmur tools,
// so join registers in every home T3's instances use, as well as in the agents' defaults.
//
// T3 keeps the instances in its settings.json (userdata/settings.json in its data directory), under
// providerInstances: an object of instance ID to {driver, displayName, enabled, environment,
// config}, seen on T3 0.1.x, 2026-11-01. Its own code (apps/server, bundled in the app) decides
// which are on and which home each runs in, and the rules below are copied from it:
//
//   - On: off if the instance's `enabled` or its config's `enabled` is false; else whichever of the
//     two is set; else by driver, where cursor, grok, kiro, muse, pi, opencode and antigravity default to
//     off and the rest (claudeAgent, codex) to on. A project can override that, which join doesn't
//     read: it registers per user.
//   - Home: config.homePath for claudeAgent (it is a CLAUDE_CONFIG_DIR) and codex (a CODEX_HOME),
//     with a leading ~ for the user's home; else the same variable in the instance's environment
//     list; else the agent's default. A codex instance's shadowHomePath is a home that holds only its
//     own auth.json and links the rest, config.toml included, to the shared one, so registering in
//     the shared home covers it.
//   - Binary: config.binaryPath, which is how an agent that isn't on PATH is found (Muse, here).
//
// The file also holds API keys (an instance's environment, a server password), so join decodes
// only the fields above, takes the value of an environment entry only by name and only if T3
// doesn't mark it sensitive, and never prints or logs the file.

// t3Instance is one of T3's provider instances, as much of it as join reads.
type t3Instance struct {
	ID, Driver, Name string
	On               bool
	HomePath         string            // config.homePath, as written
	BinaryPath       string            // config.binaryPath, as written
	Env              map[string]string // the instance's environment entries that aren't sensitive
}

// offByDefault are the drivers T3 leaves off unless told otherwise (DEFAULT_DISABLED_PROVIDER_DRIVERS).
var offByDefault = map[string]bool{"cursor": true, "grok": true, "kiro": true, "muse": true, "pi": true, "opencode": true, "antigravity": true}

// readT3Instances reads the provider instances from T3's settings.json at path, sorted by ID. A
// missing file is no instances; an instance whose fields aren't what T3 writes is passed over.
func readT3Instances(path string) ([]t3Instance, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var file struct {
		ProviderInstances map[string]json.RawMessage `json:"providerInstances"`
	}
	if err := json.Unmarshal(b, &file); err != nil {
		return nil, errors.New("T3's settings.json isn't the JSON it should be") // never the decoder's message, which can quote the file
	}
	var out []t3Instance
	for id, raw := range file.ProviderInstances {
		var in struct {
			Driver      string `json:"driver"`
			DisplayName string `json:"displayName"`
			Enabled     *bool  `json:"enabled"`
			Environment []struct {
				Name      string `json:"name"`
				Value     string `json:"value"`
				Sensitive bool   `json:"sensitive"`
			} `json:"environment"`
		}
		if json.Unmarshal(raw, &in) != nil || in.Driver == "" {
			continue
		}
		// config is decoded on its own, field by field, so that a field join doesn't read (a
		// driver's own settings, of any type) can't make the instance unreadable.
		var cfg map[string]json.RawMessage
		var wrapper struct {
			Config json.RawMessage `json:"config"`
		}
		_ = json.Unmarshal(raw, &wrapper)
		_ = json.Unmarshal(wrapper.Config, &cfg)
		var cfgEnabled *bool
		var homePath, binaryPath string
		if v, ok := cfg["enabled"]; ok {
			var e bool
			if json.Unmarshal(v, &e) == nil {
				cfgEnabled = &e
			}
		}
		if v, ok := cfg["homePath"]; ok {
			_ = json.Unmarshal(v, &homePath)
		}
		if v, ok := cfg["binaryPath"]; ok {
			_ = json.Unmarshal(v, &binaryPath)
		}
		on := !offByDefault[in.Driver]
		switch {
		case in.Enabled != nil && !*in.Enabled, cfgEnabled != nil && !*cfgEnabled:
			on = false
		case in.Enabled != nil:
			on = *in.Enabled
		case cfgEnabled != nil:
			on = *cfgEnabled
		}
		env := map[string]string{}
		for _, v := range in.Environment {
			if v.Name != "" && !v.Sensitive {
				env[v.Name] = v.Value
			}
		}
		name := in.DisplayName
		if name == "" {
			name = id
		}
		out = append(out, t3Instance{ID: id, Driver: in.Driver, Name: name, On: on,
			HomePath: strings.TrimSpace(homePath), BinaryPath: strings.TrimSpace(binaryPath), Env: env})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// expandHome is T3's expandHomePath: a leading ~ is the user's home; a path that isn't absolute
// after that is left out, since T3 would resolve it against its own working directory.
func expandHome(p, home string) string {
	switch {
	case p == "":
		return ""
	case p == "~":
		p = home
	case strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`):
		if home == "" {
			return ""
		}
		p = filepath.Join(home, p[2:])
	}
	if !filepath.IsAbs(p) {
		return ""
	}
	return filepath.Clean(p)
}

// homeOf is the home an instance of a driver with homePath runs in, from its config or the variable
// in its environment; "" means the agent's default.
func (in t3Instance) homeOf(variable, home string) string {
	if h := expandHome(in.HomePath, home); h != "" {
		return h
	}
	return expandHome(in.Env[variable], home)
}

// binary is the instance's binaryPath, if it names a file that is there.
func (in t3Instance) binary() string {
	p := in.BinaryPath
	if p == "" || !filepath.IsAbs(p) {
		return ""
	}
	if fi, err := os.Stat(p); err != nil || fi.IsDir() {
		return ""
	}
	return p
}
