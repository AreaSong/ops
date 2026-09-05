//go:build linux

package runner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManagedLinuxOpenat2ReadReplaceAndSymlinkRejection(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "service.conf")
	before, after := "enabled=false\n", "enabled=true\n"
	if err := os.WriteFile(target, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	file, info, err := openManagedNode(root, "service.conf")
	if err != nil {
		t.Fatal(err)
	}
	content, err := readManagedTextFile(file, int64(len(before)))
	_ = file.Close()
	if err != nil || content != before {
		t.Fatalf("content=%q err=%v", content, err)
	}
	changed, err := replaceManagedFile(
		root, "service.conf", after, digestText(before), digestText(after), info,
	)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	result, err := os.ReadFile(target)
	if err != nil || string(result) != after {
		t.Fatalf("result=%q err=%v", result, err)
	}

	outside := filepath.Join(t.TempDir(), "outside.conf")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.conf")); err != nil {
		t.Fatal(err)
	}
	if file, _, err := openManagedNode(root, "link.conf"); err == nil {
		_ = file.Close()
		t.Fatal("openat2 followed a managed-file symlink")
	}
}
