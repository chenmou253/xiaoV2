package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"xiaov2/internal/config"
)

func TestClearDraftPageAudioArtifactsKeepsOCRAndOtherPages(t *testing.T) {
	root := t.TempDir()
	service := &EditorService{cfg: config.Config{EditorRoot: root}}
	bookRoot := filepath.Join(root, "draft-id", "work", "grade-4-up")
	for _, relative := range []string{
		"ocr/page-002.json",
		"metadata/pages/page-002.json",
		"tts/qa/page-002.jsonl",
		"tts/page-002/us.wav",
		"tts/page-003/us.wav",
		"tts/audio_failed/page-002/failed.wav",
		"audio/page-002/us.wav",
		"audio/page-003/us.wav",
	} {
		path := filepath.Join(bookRoot, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("audio or page metadata"), 0640); err != nil {
			t.Fatal(err)
		}
	}
	for _, relative := range []string{"tts/manifest.json", "audio/manifest.json"} {
		path := filepath.Join(bookRoot, filepath.FromSlash(relative))
		manifest := map[string]any{
			"schema_version": 2,
			"items": []map[string]any{
				{"page": 2, "file": "page-002/us.wav"},
				{"page": 3, "file": "page-003/us.wav"},
			},
			"failures": []map[string]any{
				{"page": 2, "item_id": "failed-2"},
				{"page": 3, "item_id": "failed-3"},
			},
		}
		raw, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0640); err != nil {
			t.Fatal(err)
		}
	}

	if err := service.clearDraftPageAudioArtifacts("draft-id", "grade-4-up", 2); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{
		"ocr/page-002.json",
		"metadata/pages/page-002.json",
		"tts/page-003/us.wav",
		"audio/page-003/us.wav",
	} {
		if _, err := os.Stat(filepath.Join(bookRoot, filepath.FromSlash(relative))); err != nil {
			t.Errorf("expected %s to remain: %v", relative, err)
		}
	}
	for _, relative := range []string{
		"tts/qa/page-002.jsonl",
		"tts/page-002/us.wav",
		"tts/audio_failed/page-002/failed.wav",
		"audio/page-002/us.wav",
	} {
		if _, err := os.Stat(filepath.Join(bookRoot, filepath.FromSlash(relative))); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed, stat error=%v", relative, err)
		}
	}
	for _, relative := range []string{"tts/manifest.json", "audio/manifest.json"} {
		raw, err := os.ReadFile(filepath.Join(bookRoot, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		var manifest struct {
			Items    []map[string]any `json:"items"`
			Failures []map[string]any `json:"failures"`
		}
		if err := json.Unmarshal(raw, &manifest); err != nil {
			t.Fatal(err)
		}
		if len(manifest.Items) != 1 || manifest.Items[0]["page"] != float64(3) {
			t.Errorf("%s retained wrong manifest items: %#v", relative, manifest.Items)
		}
		if len(manifest.Failures) != 1 || manifest.Failures[0]["page"] != float64(3) {
			t.Errorf("%s retained wrong failures: %#v", relative, manifest.Failures)
		}
	}
}

func TestRemovePageFromAudioManifestKeepsSharedWordFileWhileReferenced(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "word-cache", "ab", "shared.wav")
	if err := os.MkdirAll(filepath.Dir(shared), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shared, []byte("shared word audio"), 0640); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "manifest.json")
	manifest := map[string]any{
		"schema_version": 2,
		"items": []map[string]any{
			{"page": 1, "item_id": "p1-word", "file": "word-cache/ab/shared.wav"},
			{"page": 2, "item_id": "p2-word", "file": "word-cache/ab/shared.wav"},
		},
		"failures": []map[string]any{},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, raw, 0640); err != nil {
		t.Fatal(err)
	}

	if err := removePageFromAudioManifest(manifestPath, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(shared); err != nil {
		t.Fatalf("shared word audio was removed while page 2 still referenced it: %v", err)
	}
	if err := removePageFromAudioManifest(manifestPath, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(shared); err != nil {
		t.Fatalf("page cleanup must never delete book-wide shared word audio: %v", err)
	}
}

func TestRemovePageFromAudioManifestDoesNotDeleteSharedWordWhenManifestReferenceIsIncomplete(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "word-cache", "cd", "shared.wav")
	if err := os.MkdirAll(filepath.Dir(shared), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shared, []byte("shared word audio"), 0640); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "manifest.json")
	manifest := map[string]any{
		"schema_version": 2,
		"items": []map[string]any{
			// Simulate an older/incomplete manifest that only lists the page
			// currently being regenerated even though another DB page still
			// references the same word-cache WAV.
			{"page": 5, "item_id": "p5-word", "file": "word-cache/cd/shared.wav"},
		},
		"failures": []map[string]any{},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, raw, 0640); err != nil {
		t.Fatal(err)
	}
	if err := removePageFromAudioManifest(manifestPath, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(shared); err != nil {
		t.Fatalf("shared word cache was deleted by page cleanup: %v", err)
	}
}
