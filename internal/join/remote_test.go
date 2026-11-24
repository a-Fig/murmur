package join

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// #1213: a `murmur` entry in mcp.json that is a remote server (a url, or a type other than stdio)
// isn't one join can run mur in. Join leaves it as it is, says why, and writes no approval for the
// name, which would cover that server's tools; uninstall leaves it too.

var remoteEntries = map[string]string{
	"url":        `{"mcpServers": {"murmur": {"url": "https://example.com/mcp"}, "docs": {"command": "docs-mcp"}}}`,
	"http":       `{"mcpServers": {"murmur": {"type": "http", "url": "https://example.com/mcp", "headers": {"A": "b"}}}}`,
	"streamable": `{"mcpServers": {"murmur": {"type": "streamable-http", "command": "x"}}}`,
}

func TestCursorJoinLeavesARemoteEntryAlone(t *testing.T) {
	for name, body := range remoteEntries {
		t.Run(name, func(t *testing.T) {
			sb := newSandbox(t, false)
			h := withCursor(t, sb)
			path := filepath.Join(sb.CursorDir, "mcp.json")
			write(t, path, body)

			did, fresh, err := sb.registerCursorAt(h)
			if err == nil || !strings.Contains(err.Error(), "remote") || !strings.Contains(err.Error(), path) || fresh {
				t.Errorf("fresh=%v err=%v, want a collision that names the file and says remote", fresh, err)
			}
			if read(t, path) != body {
				t.Errorf("mcp.json was changed:\n%s", read(t, path))
			}
			if exists(sb.CursorConfig) || exists(ruleRecordPath(sb.CursorConfig)) {
				t.Errorf("an approval was written for the remote server: did=%q\n%s", did, read(t, sb.CursorConfig))
			}
			for _, d := range did {
				if strings.Contains(d, "allowed") || strings.Contains(d, "registered") {
					t.Errorf("join says %q", d)
				}
			}
		})
	}
}

// A server join can't register gets no approval either.
func TestCursorJoinWritesNoApprovalWhenTheServerFails(t *testing.T) {
	sb := newSandbox(t, false)
	h := withCursor(t, sb)
	write(t, filepath.Join(sb.CursorDir, "mcp.json"), "{")
	if _, _, err := sb.registerCursorAt(h); err == nil {
		t.Fatal("join of a broken mcp.json reported no problem")
	}
	if exists(sb.CursorConfig) || exists(ruleRecordPath(sb.CursorConfig)) {
		t.Errorf("an approval was written though the server wasn't:\n%s", read(t, sb.CursorConfig))
	}
}

func TestKiroJoinLeavesARemoteEntryAlone(t *testing.T) {
	for name, body := range remoteEntries {
		t.Run(name, func(t *testing.T) {
			sb := newSandbox(t, false)
			withAgents(sb)
			mustMakeDir(t, sb.KiroHome)
			h := sb.defaultHome(agents[agentIndex("kiro")])
			path := filepath.Join(sb.KiroHome, "settings", "mcp.json")
			perms := filepath.Join(sb.KiroHome, "settings", "permissions.json")
			write(t, path, body)

			did, fresh, err := sb.registerKiroAt(h)
			if err == nil || !strings.Contains(err.Error(), "remote") || !strings.Contains(err.Error(), path) || fresh {
				t.Errorf("fresh=%v err=%v, want a collision that names the file and says remote", fresh, err)
			}
			if read(t, path) != body {
				t.Errorf("mcp.json was changed:\n%s", read(t, path))
			}
			if exists(perms) || exists(ruleRecordPath(perms)) {
				t.Errorf("an approval was written for the remote server: did=%q\n%s", did, read(t, perms))
			}
		})
	}
}

// A local entry with its type spelled out is still Murmur's to set up.
func TestAStdioEntryIsNotACollision(t *testing.T) {
	sb := newSandbox(t, false)
	h := withCursor(t, sb)
	path := filepath.Join(sb.CursorDir, "mcp.json")
	write(t, path, `{"mcpServers": {"murmur": {"type": "stdio", "command": "/old/mur", "env": {"A": "b"}}}}`)
	if _, fresh, err := sb.registerCursorAt(h); err != nil || !fresh {
		t.Fatalf("fresh=%v err=%v", fresh, err)
	}
	if entry := serverEntry(t, path); entry["command"] != sb.Exe || envOf(entry)["A"] != "b" {
		t.Errorf("entry %v", entry)
	}
}

func TestUninstallLeavesARemoteEntryAlone(t *testing.T) {
	for name, body := range remoteEntries {
		t.Run(name, func(t *testing.T) {
			sb := newSandbox(t, false)
			withAgents(sb)
			mustMakeDir(t, sb.KiroHome)
			ch := withCursor(t, sb)
			kh := sb.defaultHome(agents[agentIndex("kiro")])
			cursorPath := filepath.Join(sb.CursorDir, "mcp.json")
			kiroPath := filepath.Join(sb.KiroHome, "settings", "mcp.json")
			write(t, cursorPath, body)
			write(t, kiroPath, body)

			_, cnotes, err := sb.unregisterCursorAt(context.Background(), []agentHome{ch})
			if err != nil || read(t, cursorPath) != body || !strings.Contains(strings.Join(cnotes, "\n"), cursorPath) {
				t.Errorf("cursor: err=%v notes=%q\n%s", err, cnotes, read(t, cursorPath))
			}
			_, knotes, err := sb.unregisterKiroAt(context.Background(), []agentHome{kh})
			if err != nil || read(t, kiroPath) != body || !strings.Contains(strings.Join(knotes, "\n"), kiroPath) {
				t.Errorf("kiro: err=%v notes=%q\n%s", err, knotes, read(t, kiroPath))
			}
			if rem := cursorRemoval(sb.Env, ch); strings.Contains(rem, "mcpServers") {
				t.Errorf("cursor removal advice names the remote entry: %q", rem)
			}
			if rem := kiroRemoval(sb.Env, kh); strings.Contains(rem, "mcpServers") {
				t.Errorf("kiro removal advice names the remote entry: %q", rem)
			}
		})
	}
}
