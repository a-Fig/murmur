package hook

import (
	"slices"
	"testing"
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
		name  string
		pids  []int
		procs map[int]procInfo
		want  []int
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
			want: []int{400},
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
			want: []int{900},
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
			want: []int{900},
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
			want: []int{950, 850},
		},
		{
			name: "no kiro-cli in chain leaves ancestors unchanged",
			pids: []int{500, 400},
			procs: map[int]procInfo{
				500: {exe: "/bin/sh", ok: true},
				400: {exe: "/usr/bin/node", ok: true},
			},
			want: []int{500, 400},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := kiroHookAncestors(tc.pids, func(pid int) (string, string, bool) {
				p := tc.procs[pid]
				return p.exe, p.comm, p.ok
			})
			if !slices.Equal(got, tc.want) {
				t.Errorf("kiroHookAncestors(%v) = %v, want %v", tc.pids, got, tc.want)
			}
		})
	}
}
