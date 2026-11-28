package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
)

// OwnsMesh reads ownership from a stopped daemon's files, for leave and uninstall (#1287): the
// saved owner must be this clone's device with this clone's signing key.
func TestOwnsMeshReadsTheSavedOwner(t *testing.T) {
	gitDir := t.TempDir()
	dir := filepath.Join(gitDir, brand.StateDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if OwnsMesh(gitDir) {
		t.Fatal("a clone with no state owns a mesh")
	}
	device, sign, _, err := loadIdentity(dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	save := func(o *meshOwner) {
		t.Helper()
		b, err := json.Marshal(state{TeamOwner: o})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "state.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	save(nil)
	if OwnsMesh(gitDir) {
		t.Error("a mesh with no owner is owned")
	}
	save(&meshOwner{Device: "device-0123456789abcdef", Key: bus.SignPublic(sign)})
	if OwnsMesh(gitDir) {
		t.Error("another device's ID counts as this clone's")
	}
	save(&meshOwner{Device: device, Key: bus.SignPublic(bus.NewSignKey())})
	if OwnsMesh(gitDir) {
		t.Error("another signing key counts as this clone's")
	}
	save(&meshOwner{Device: device, Key: bus.SignPublic(sign)})
	if !OwnsMesh(gitDir) {
		t.Error("the owning clone doesn't own its mesh")
	}
}
