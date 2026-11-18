package connection

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalFS_writeReadRemove(t *testing.T) {
	ctx := t.Context()
	dir := filepath.Join(t.TempDir(), "project")
	path := filepath.Join(dir, "nested", "compose.yaml")
	fsys := localFS{}

	if _, err := fsys.ReadFile(ctx, path); !IsNotExist(err) {
		t.Fatalf("ReadFile on missing file: want not-exist error, got %v", err)
	}

	if err := fsys.WriteFile(ctx, path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fsys.WriteFile(ctx, path, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := fsys.ReadFile(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second" {
		t.Errorf("content = %q, want %q", got, "second")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}

	if err := fsys.RemoveAll(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if err := fsys.RemoveAll(ctx, dir); err != nil {
		t.Errorf("RemoveAll on missing dir: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("dir still exists: %v", err)
	}
}
