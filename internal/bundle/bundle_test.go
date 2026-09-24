package bundle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(path, value string) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(value), 0640); err != nil {
			t.Fatal(err)
		}
	}
	data := Bundle{SchemaVersion: 2, Book: Book{
		BookID: "grade-4-up", Title: "四年级上册", Cover: "pages/page-001.png",
		AmericanEnabled: true, AmericanVoiceID: "aiden",
	}, Pages: []Page{{Position: 1, Image: "pages/page-001.png", Content: "metadata/pages/page-001.json", Interactive: true}}}
	raw, _ := json.Marshal(data)
	write("metadata/book.json", string(raw))
	write("pages/page-001.png", "image")
	write("metadata/pages/page-001.json", `{"segments":[]}`)
	write("tts/manifest.json", `{"schema_version":2,"items":[{"page":1,"item_id":"s1","accent":"en-US","file":"page-001/s1.wav"}]}`)
	write("tts/page-001/s1.wav", "audio")
	return root
}

func TestValidateBundle(t *testing.T) {
	root := fixture(t)
	data, err := Validate(root)
	if err != nil {
		t.Fatal(err)
	}
	if data.Book.BookID != "grade-4-up" || len(data.Pages) != 1 {
		t.Fatalf("unexpected bundle: %+v", data)
	}
}

func TestValidateRejectsMissingAudio(t *testing.T) {
	root := fixture(t)
	if err := os.Remove(filepath.Join(root, "tts/page-001/s1.wav")); err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(root); err == nil || !strings.Contains(err.Error(), "audio") {
		t.Fatalf("expected missing audio error, got %v", err)
	}
}

func TestValidateRejectsTraversal(t *testing.T) {
	root := fixture(t)
	path := filepath.Join(root, "metadata/book.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var data Bundle
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	data.Pages[0].Image = "../outside.png"
	raw, _ = json.Marshal(data)
	if err = os.WriteFile(path, raw, 0640); err != nil {
		t.Fatal(err)
	}
	if _, err = Validate(root); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("expected traversal error, got %v", err)
	}
}

func TestValidateRejectsInvalidPageJSON(t *testing.T) {
	root := fixture(t)
	if err := os.WriteFile(filepath.Join(root, "metadata/pages/page-001.json"), []byte("not json"), 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(root); err == nil || !strings.Contains(err.Error(), "JSON") {
		t.Fatalf("expected JSON error, got %v", err)
	}
}

func TestValidateRejectsMissingSegments(t *testing.T) {
	root := fixture(t)
	if err := os.WriteFile(filepath.Join(root, "metadata/pages/page-001.json"), []byte(`{"page":1}`), 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(root); err == nil || !strings.Contains(err.Error(), "segments") {
		t.Fatalf("expected missing segments error, got %v", err)
	}
}

func TestValidateRejectsAudioTraversal(t *testing.T) {
	root := fixture(t)
	path := filepath.Join(root, "tts/manifest.json")
	raw := `{"schema_version":2,"items":[{"page":1,"item_id":"s1","accent":"en-US","file":"../../outside.wav"}]}`
	if err := os.WriteFile(path, []byte(raw), 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(root); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("expected audio traversal error, got %v", err)
	}
}

func TestCopyAudioFilesExcludesUnreferencedFiles(t *testing.T) {
	root := fixture(t)
	if err := os.WriteFile(filepath.Join(root, "tts/failed.wav"), []byte("failed"), 0640); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if err := copyAudioFiles(root, destination, "tts"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, "tts/page-001/s1.wav")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, "tts/failed.wav")); !os.IsNotExist(err) {
		t.Fatalf("unreferenced file should not be exported: %v", err)
	}
}
