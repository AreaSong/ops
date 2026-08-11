package main

import (
	"os"
	"testing"
)

func TestEnforceProductionStateRootMode(t *testing.T) {
	stateRoot := t.TempDir()
	if err := os.Chmod(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := enforceProductionStateRootMode(stateRoot); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o710 {
		t.Fatalf("state root mode = %04o, want 0710", got)
	}
}
