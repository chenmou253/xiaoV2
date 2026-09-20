package resource

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAudioStatusRequiresConfiguredVoiceAndEveryOccurrence(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "page-001"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "page-001", "a.wav"), []byte("wav"), 0640); err != nil {
		t.Fatal(err)
	}
	manifest := `{"schema_version":2,"items":[{"page":1,"item_id":"s1","accent":"en-US","voice":"Aiden","file":"page-001/a.wav"}],"failures":[]}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0640); err != nil {
		t.Fatal(err)
	}
	expected := []AudioExpectedItem{{Page: 1, ItemID: "s1"}, {Page: 1, ItemID: "s2"}}
	status := AudioStatusInDir(dir, "en-US", "Aiden", expected, false)
	if status.Ready != 1 || status.Total != 2 || status.Status != "generating" {
		t.Fatalf("unexpected status: %#v", status)
	}
	if AudioItemReadyInDir(dir, 1, "s1", "en-US", "Ryan", false) {
		t.Fatal("voice mismatch was treated as ready")
	}
	if !AudioItemReadyInDir(dir, 1, "s1", "en-US", "aIdEn", false) {
		t.Fatal("voice matching should be case-insensitive")
	}
	if !AudioItemReadyInDir(dir, 1, "s1", "en-US", "Ryan", true) {
		t.Fatal("legacy book did not accept its existing voice")
	}
}

func TestHasSpeakableTextRejectsStandaloneSymbols(t *testing.T) {
	for _, text := range []string{"?", ",", "—", "...", "***"} {
		if HasSpeakableText(text) {
			t.Errorf("standalone symbol %q was treated as speakable", text)
		}
	}
	for _, text := range []string{"word?", "don't", "1", "3/7"} {
		if !HasSpeakableText(text) {
			t.Errorf("spoken token %q was treated as a symbol", text)
		}
	}
}

func TestAudioStatusIgnoresFailuresForItemsNotExpected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "s1.wav"), []byte("wav"), 0640); err != nil {
		t.Fatal(err)
	}
	manifest := `{"schema_version":2,"items":[{"page":1,"item_id":"s1","accent":"en-US","voice":"aiden","file":"s1.wav"}],"failures":[{"page":1,"item_id":"symbol","accent":"en-US","voice":"aiden"}]}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0640); err != nil {
		t.Fatal(err)
	}
	status := AudioStatusInDir(dir, "en-US", "aiden", []AudioExpectedItem{{Page: 1, ItemID: "s1"}}, false)
	if status.Status != "ready" || status.Failed != 0 || status.Ready != 1 {
		t.Fatalf("failure outside expected items affected status: %#v", status)
	}
}

func TestAudioExpectedReadyInDirChecksManifestOnceSemantics(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "one.wav"), []byte("wav"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "two.wav"), []byte("wav"), 0640); err != nil {
		t.Fatal(err)
	}
	manifest := `{"schema_version":2,"items":[{"page":1,"item_id":"one","accent":"en-US","voice":"aiden","file":"one.wav"},{"page":1,"item_id":"two","accent":"en-US","voice":"aiden","file":"two.wav"}],"failures":[]}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0640); err != nil {
		t.Fatal(err)
	}
	expected := []AudioExpectedItem{{Page: 1, ItemID: "one"}, {Page: 1, ItemID: "two"}}
	if !AudioExpectedReadyInDir(dir, "en-US", "aiden", expected, false) {
		t.Fatal("complete expected set was reported missing")
	}
	expected = append(expected, AudioExpectedItem{Page: 1, ItemID: "three"})
	if AudioExpectedReadyInDir(dir, "en-US", "aiden", expected, false) {
		t.Fatal("missing expected item was reported ready")
	}
}
