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
	// TeamRef holds the team keys (bus.TeamRecord), one shared ref pushed with a lease, as the
	// registry is.
	TeamRef = RefRoot + "team"
	// FilesPrefix + a random name holds the files one message carries (#570): one commit, which a
	// device fetches only when the message is for it. The name is inside the message's event.
	FilesPrefix = RefRoot + "files/"

	// StateDir is the daemon's directory inside the git common dir.
	StateDir = Name
	// ConfigRemote names the remote the bus lives on (default "origin").
	ConfigRemote = Name + ".remote"

	// Command is the binary's name on PATH, as written into teammates' config by init.
	Command = "mur"
	// MCPServer is the MCP server's name; Claude Code shows its tools as mcp__<name>__<tool>.
	MCPServer = Name
	// AppID names the desktop app to the system (#616): its bundle identifier on macOS, its
	// AppUserModelID on Windows, its .desktop file and icon on Linux. It is reverse DNS of the
	// site's domain (ReportURL's).
	AppID = "dev.afig." + Name

	// JoinDoc and UseDoc are the pages the marker in a repo's AGENTS.md and CLAUDE.md sends an
	// agent to, as raw text: how to join (docs/agents/join.md) and how to use the tools
	// (docs/agents/use.md). SetupDoc (docs/agents/setup.md) is how an agent sets Murmur up in a
	// repo that doesn't use it yet, for a user who asked; the README points to it. They read
	// without login, as the repo is public.
	JoinDoc  = "https://raw.githubusercontent.com/a-Fig/murmur/main/docs/agents/join.md"
	UseDoc   = "https://raw.githubusercontent.com/a-Fig/murmur/main/docs/agents/use.md"
	SetupDoc = "https://raw.githubusercontent.com/a-Fig/murmur/main/docs/agents/setup.md"

	// Site is Murmur's website. Each agent Murmur works in has a page there, AgentPage, for people,
	// and the same address plus .md for an agent (site/agents/, docs/agents/harnesses/, #934).
	// setup.md and join.md send an agent to its page, and that sends it to StepsDoc
	// (docs/agents/steps.md), the steps every agent shares.
	Site     = "https://murmur.afig.dev"
	StepsDoc = Site + "/steps"

	// InstallSh and InstallPs1 are the one-line installs docs/agents/steps.md tells an agent to run. The docs
	// spell them out, so a test holds them to these.
	InstallSh  = "https://raw.githubusercontent.com/a-Fig/murmur/main/install.sh"
	InstallPs1 = "https://raw.githubusercontent.com/a-Fig/murmur/main/install.ps1"

	// ReportURL is where a public repo's address is sent, once, so the repos using Murmur can be
	// counted: the only thing Murmur sends us (internal/publicrepo). ReportedRef, on the bus, says
	// the repo has been reported, so its other clones don't send it again.
	ReportURL   = "https://murmur.afig.dev/v1/repo"
	ReportedRef = RefRoot + "reported"

	// SessionsDirEnv, if set, is where MCP servers publish their records and find moves, in place
	// of the user's config directory (internal/here). Tests set it.
	SessionsDirEnv = "MURMUR_SESSIONS_DIR"

	// PageListenerEnv names, to a daemon started by one restarting onto an update, the descriptor
	// (a handle on Windows) of the page's listening socket it inherited (#255).
	PageListenerEnv = "MURMUR_PAGE_LISTENER"
)

// AgentPage is the address of the page about Murmur in one agent, by its name there, such as
// "claude-code".
func AgentPage(name string) string { return Site + "/agents/" + name }
