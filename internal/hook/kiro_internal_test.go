package hook

import (
	"os"
	"slices"
	"testing"

	"github.com/a-Fig/murmur/internal/proc"
)

// Kiro runs hooks under kiro-cli -> node (acp-server.js) -> shell -> mur hook kiro. When a nested
// kiro-cli is started from an outer Kiro session's shell, kiroHookAncestors must scope the
// ancestor list to the inner Kiro client process so the inner hook never walks into the outer
// session's node process (#910).
func TestKiroHookAncestors(t *testing.T) {
	type procInfo struct {
		exe  string
		comm string
		ok   bool
	}
	for _, tc := range []struct {
		name        string
		pids        []int
		procs       map[int]procInfo
		want        []int
		wantBounded bool
	}{
		{
			name: "normal kiro-cli -> node",
			pids: []int{500, 400, 300, 200},
			procs: map[int]procInfo{
				500: {exe: `C:\Windows\System32\cmd.exe`, ok: true},
				400: {exe: `C:\Users\u\AppData\Local\Kiro-Cli\node.exe`, ok: true},
				300: {exe: `C:\Users\u\AppData\Local\Kiro-Cli\kiro-cli.exe`, ok: true},
				200: {exe: `C:\Users\u\t3.exe`, ok: true},
			},
			want:        []int{400},
			wantBounded: true,
		},
		{
			name: "nested kiro stops at inner node",
			pids: []int{950, 900, 850, 800, 700, 650},
			procs: map[int]procInfo{
				950: {exe: "/bin/sh", ok: true},
				900: {exe: "/opt/kiro/node", ok: true},
				850: {exe: "/opt/kiro/kiro-cli", ok: true},
				800: {exe: "/bin/bash", ok: true},
				700: {exe: "/opt/kiro/node", ok: true},
				650: {exe: "/opt/kiro/kiro-cli", ok: true},
			},
			want:        []int{900},
			wantBounded: true,
		},
		{
			name: "linux comm fallback and deleted exe",
			pids: []int{950, 900, 850, 700},
			procs: map[int]procInfo{
				950: {exe: "/bin/sh", ok: true},
				900: {exe: "/opt/kiro/node (deleted)", ok: true},
				850: {comm: "kiro-cli", ok: false},
				700: {exe: "/opt/kiro/node", ok: true},
			},
			want:        []int{900},
			wantBounded: true,
		},
		{
			name: "direct kiro-cli without node below stops before outer kiro",
			pids: []int{950, 850, 800, 700, 650},
			procs: map[int]procInfo{
				950: {exe: "/bin/sh", ok: true},
				850: {exe: "/opt/kiro/kiro-cli", ok: true},
				800: {exe: "/bin/bash", ok: true},
				700: {exe: "/opt/kiro/node", ok: true},
				650: {exe: "/opt/kiro/kiro-cli", ok: true},
			},
			want:        []int{950, 850},
			wantBounded: true,
		},
		{
			name: "no kiro-cli in chain leaves ancestors unchanged and unbounded",
			pids: []int{500, 400},
			procs: map[int]procInfo{
				500: {exe: "/bin/sh", ok: true},
				400: {exe: "/usr/bin/node", ok: true},
			},
			want:        []int{500, 400},
			wantBounded: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, bounded := kiroHookAncestors(tc.pids, func(pid int) (string, string, bool) {
				p := tc.procs[pid]
				return p.exe, p.comm, p.ok
			})
			if !slices.Equal(got, tc.want) || bounded != tc.wantBounded {
				t.Errorf("kiroHookAncestors(%v) = (%v, %v), want (%v, %v)", tc.pids, got, bounded, tc.want, tc.wantBounded)
			}
		})
	}
}

// An environment-derived KIRO_SESSION_ID is only accepted when identify verifies a Kiro process
// boundary, preventing an unbounded nested process from reusing an inherited outer KIRO_SESSION_ID.
func TestKiroIdentifyRequiresBoundaryForEnvSession(t *testing.T) {
	orig := kiroExeAt
	t.Cleanup(func() { kiroExeAt = orig })

	// Unbounded ancestry: env-derived session ID is rejected; stdin session ID is accepted.
	kiroExeAt = func(int) (string, string, bool) { return "/usr/bin/other", "", true }
	if _, ok := identify(Kiro, input{SessionID: "from-env"}, true); ok {
		t.Error("accepted env-derived KIRO_SESSION_ID without verified Kiro process boundary")
	}
	if req, ok := identify(Kiro, input{SessionID: "from-stdin"}, false); !ok || req.ClaudeSession != "from-stdin" {
		t.Errorf("stdin session_id = (%+v, %v), want accepted", req, ok)
	}

	// Verified Kiro boundary (parent is kiro-cli): env-derived session ID is accepted and scoped.
	anc := proc.Ancestors(os.Getppid(), maxAncestors)
	if len(anc) == 0 {
		t.Skip("no ancestor PIDs available")
	}
	kiroExeAt = func(pid int) (string, string, bool) {
		if pid == anc[0] {
			return "/opt/kiro/kiro-cli", "", true
		}
		return "/usr/bin/other", "", true
	}
	req, ok := identify(Kiro, input{SessionID: "from-env"}, true)
	if !ok || req.ClaudeSession != "from-env" || !slices.Equal(req.Ancestors, anc[:1]) {
		t.Errorf("bounded env session = (%+v, %v), want ancestors %v", req, ok, anc[:1])
	}
}
