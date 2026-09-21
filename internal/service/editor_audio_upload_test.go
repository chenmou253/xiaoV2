package service

import (
	"testing"

	"xiaov2/internal/model"
)

func TestNormalizeSharedWordMatchesBookWideCacheSemantics(t *testing.T) {
	for input, want := range map[string]string{
		"Hospital": "hospital",
		" hospital. ": "hospital",
		"PH": "ph",
		"“phone”": "phone",
	} {
		if got := normalizeSharedWord(input); got != want {
			t.Fatalf("normalizeSharedWord(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestUpdateManualWordManifestUpdatesAllMatchingWordItems(t *testing.T) {
	manifest := audioManifest{
		SchemaVersion: 2,
		Items: []map[string]any{
			{"page": 1, "item_id": "w1", "kind": "word", "text": "ph", "accent": "en-US", "file": "old.wav"},
			{"page": 2, "item_id": "w2", "kind": "word", "text": "ph", "accent": "en-US", "file": "old.wav"},
			{"page": 2, "item_id": "s2", "kind": "sentence", "text": "Philip has a phone.", "accent": "en-US", "file": "sentence.wav"},
		},
		Failures: []map[string]any{
			{"page": 1, "item_id": "w1", "accent": "en-US"},
			{"page": 3, "item_id": "other", "accent": "en-US"},
		},
	}
	rows := []model.TextbookAudioItem{
		{Page: 1, ItemID: "w1", SegmentID: "s1", ItemType: "word", Text: "ph", Accent: "en-US", VoiceID: "aiden", ModelID: "local-qwen3-tts"},
		{Page: 2, ItemID: "w2", SegmentID: "s2", ItemType: "word", Text: "PH", Accent: "en-US", VoiceID: "aiden", ModelID: "local-qwen3-tts"},
	}
	info := manualWordAudioInfo{WordCacheKey: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SampleRate: 24000, DurationMS: 400}
	updateManualWordManifest(&manifest, rows, "ph", "word-cache/aa/shared.wav", "manual-1", 9, info)

	updated := 0
	for _, item := range manifest.Items {
		if item["kind"] == "word" && normalizeSharedWord(item["text"].(string)) == "ph" {
			if item["file"] != "word-cache/aa/shared.wav" {
				t.Fatalf("matching word retained old audio path: %#v", item)
			}
			qa, _ := item["qa"].(map[string]any)
			if qa["manual_upload"] != true || qa["passed"] != true {
				t.Fatalf("manual upload provenance missing: %#v", item)
			}
			updated++
		}
		if item["kind"] == "sentence" && item["file"] != "sentence.wav" {
			t.Fatalf("sentence audio should remain untouched: %#v", item)
		}
	}
	if updated != 2 {
		t.Fatalf("updated %d matching words, want 2", updated)
	}
	if len(manifest.Failures) != 1 || manifest.Failures[0]["item_id"] != "other" {
		t.Fatalf("matching word failures were not cleared safely: %#v", manifest.Failures)
	}
}
