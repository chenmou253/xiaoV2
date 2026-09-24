package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyFilePreservesSourceWhenPathsAreSame(t *testing.T) {
	path := filepath.Join(t.TempDir(), "page-001.png")
	want := []byte("published page image")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(path, path); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("copying a published page onto itself changed its image: %q", got)
	}
}
