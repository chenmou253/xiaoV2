package resource

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBookIDValidation(t *testing.T) {
	for _, id := range []string{"grade4-semester1", "Book_2026", "a"} {
		if !ValidBookID(id) {
			t.Fatalf("expected %q to be valid", id)
		}
	}
	for _, id := range []string{"", "../book", "book/other", `book\other`, ".", "含中文"} {
		if ValidBookID(id) {
			t.Fatalf("expected %q to be invalid", id)
		}
	}
}

func TestResourcePathsAndTraversal(t *testing.T) {
	m, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, err := m.BookRoot("safe-book")
	if err != nil {
		t.Fatal(err)
	}
	pages, err := m.Dir("safe-book", "pages")
	if err != nil {
		t.Fatal(err)
	}
	if pages != filepath.Join(root, "pages") {
		t.Fatalf("unexpected pages path %q", pages)
	}
	for _, path := range []string{"../../outside", "/tmp/outside", "pages/../../../outside"} {
		if _, err = m.Resolve("safe-book", path); err == nil {
			t.Fatalf("expected traversal %q to fail", path)
		}
	}
}

func TestPythonAndGoPathProtocolsMatch(t *testing.T) {
	projectRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	resourceRoot := t.TempDir()
	m, err := New(resourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", "scripts/verify_paths.py", "codex-path-verification")
	cmd.Dir = projectRoot
	cmd.Env = append(os.Environ(), "RESOURCE_ROOT="+resourceRoot)
	raw, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var python map[string]string
	if err = json.Unmarshal(raw, &python); err != nil {
		t.Fatal(err)
	}
	goRoot, _ := m.BookRoot("codex-path-verification")
	if python["book_root"] != goRoot {
		t.Fatalf("book root differs: python=%q go=%q", python["book_root"], goRoot)
	}
	for _, kind := range []string{"source", "pages", "audio", "tts", "ocr", "text", "metadata", "cache"} {
		goPath, _ := m.Dir("codex-path-verification", kind)
		if python[kind] != goPath {
			t.Fatalf("%s differs: python=%q go=%q", kind, python[kind], goPath)
		}
	}
}

func TestAudioManifestResolvesByPageItemAndAccent(t *testing.T) {
	root := t.TempDir()
	m, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	tts, _ := m.Dir("book", "tts")
	first := filepath.Join(tts, "page-001", "words", "w1", "us-run.wav")
	second := filepath.Join(tts, "page-001", "words", "w2", "us-run.wav")
	for _, path := range []string{first, second} {
		if err = os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, []byte("wav"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	manifest := map[string]any{"schema_version": 2, "items": []map[string]any{
		{"page": 1, "item_id": "w1", "accent": "en-US", "file": "page-001/words/w1/us-run.wav"},
		{"page": 1, "item_id": "w2", "accent": "en-US", "file": "page-001/words/w2/us-run.wav"},
	}}
	raw, _ := json.Marshal(manifest)
	if err = os.WriteFile(filepath.Join(tts, "manifest.json"), raw, 0o640); err != nil {
		t.Fatal(err)
	}
	gotFirst, err := m.AudioItemFile("book", 1, "w1", "en-US")
	if err != nil || gotFirst != first {
		t.Fatalf("first item lookup=%q error=%v", gotFirst, err)
	}
	gotSecond, err := m.AudioItemFile("book", 1, "w2", "en-US")
	if err != nil || gotSecond != second || gotFirst == gotSecond {
		t.Fatalf("second item lookup=%q error=%v", gotSecond, err)
	}
}
