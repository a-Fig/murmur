// Package brand holds the product's name and every identifier built from it, so no other package
// spells the name out: a rename happens here, plus the module path. The product was called t4
// until 2026-11-01, when it became Murmur (docs/names.md).
package brand

// Name is the product's name in identifiers. It reaches the remote in ref names, so once a team
// has used the tool, changing it strands their bus history under the old refs.
const Name = "murmur"

// Product is the name as people read it, in prose: messages, help text, the web page's title.
const Product = "Murmur"

const (
	// RefRoot holds every bus ref, on the remote and in each clone's local copy.
	RefRoot = "refs/" + Name + "/"
	// MeshPrefix + deviceID is a device's event log: one commit per event, one writer.
	MeshPrefix = RefRoot + "mesh/"
	// PresencePrefix + deviceID is a device's current state, force-pushed on every change.
	PresencePrefix = RefRoot + "presence/"
	// NamesRef is the registry of session names, one shared ref that devices take names on by
	// compare-and-swap.
	NamesRef = RefRoot + "names"
	// ConfigRef holds the repo's settings for the bus, such as whether it is encrypted (#156).
	ConfigRef = RefRoot + "config"

	// StateDir is the daemon's directory inside the git common dir.
	StateDir = Name
	// ConfigRemote names the remote the bus lives on (default "origin").
	ConfigRemote = Name + ".remote"

	// Command is the binary's name on PATH, as written into teammates' config by init.
	Command = "mur"
	// MCPServer is the MCP server's name; Claude Code shows its tools as mcp__<name>__<tool>.
	MCPServer = Name

	// JoinDoc and UseDoc are the pages the marker in a repo's AGENTS.md and CLAUDE.md sends an
	// agent to, as raw text: how to join (docs/agents/join.md) and how to use the tools
	// (docs/agents/use.md). SetupDoc (docs/agents/setup.md) is how an agent sets Murmur up in a
	// repo that doesn't use it yet, for a user who asked; the README points to it. They read
	// without login, as the repo is public.
	JoinDoc  = "https://raw.githubusercontent.com/a-Fig/murmur/main/docs/agents/join.md"
	UseDoc   = "https://raw.githubusercontent.com/a-Fig/murmur/main/docs/agents/use.md"
	SetupDoc = "https://raw.githubusercontent.com/a-Fig/murmur/main/docs/agents/setup.md"

	// InstallSh and InstallPs1 are the one-line installs join.md tells an agent to run. The docs
	// spell them out, so a test holds them to these.
	InstallSh  = "https://raw.githubusercontent.com/a-Fig/murmur/main/install.sh"
	InstallPs1 = "https://raw.githubusercontent.com/a-Fig/murmur/main/install.ps1"

	// CheckInURL is where a daemon checks in once a day, which is how active users are counted, and
	// learns the latest release (internal/checkin). ConfigUsage set to false in git config, or
	// NoUsageEnv or DO_NOT_TRACK set in the environment, turns that off.
	CheckInURL  = "https://murmur.afig.dev/v1/checkin"
	ConfigUsage = Name + ".usage"
	NoUsageEnv  = "MURMUR_NO_USAGE"
)
