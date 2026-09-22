package service

import (
	"encoding/json"
	"testing"
)


func testPageContentIssues(t *testing.T, raw string, requireAudio bool) []string {
	t.Helper()
	var content map[string]any
	if err := json.Unmarshal([]byte(raw), &content); err != nil {
		t.Fatal(err)
	}
	issues := publicationIssues(content)
	if requireAudio && !hasAudioItems(raw) {
		issues = append(issues, "没有可朗读的 OCR 内容")
	}
	return issues
}

func TestLookupAudioContentShapeIncludesSentenceAndWordIDs(t *testing.T) {
	items, err := expectedAudioItems(`{"segments":[{"id":"s1","text":"Make a model.","audio_mode":"sentence_and_words","words":[{"id":"w1","text":"Make"},{"id":"w2","text":"model"}]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("expected sentence plus two words, got %#v", items)
	}
	if items[0].ItemID != "s1" || items[0].ItemType != "sentence" {
		t.Fatalf("sentence item is not addressable for regeneration: %#v", items[0])
	}
	if items[1].ItemID != "w1" || items[1].ItemType != "word" {
		t.Fatalf("word item is not addressable for regeneration: %#v", items[1])
	}
}

func TestWordOnlySegmentDoesNotExposeSentenceRegeneration(t *testing.T) {
	items, err := expectedAudioItems(`{"segments":[{"id":"s1","text":"hospital park","audio_mode":"word_only","words":[{"id":"w1","text":"hospital"},{"id":"w2","text":"park"}]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("word-only segment should expose only words, got %#v", items)
	}
	for _, item := range items {
		if item.ItemType != "word" {
			t.Fatalf("word-only segment unexpectedly exposed a sentence item: %#v", item)
		}
	}
}

func TestPageAudioRegenerationIssuesIgnoreReviewFlagsAndValidateContentOnly(t *testing.T) {
	raw := `{"segments":[{"id":"s1","text":"There is a hospital.","translation":"这里有一家医院。","anchor":[0.1,0.1,0.5,0.05],"words":[{"id":"w1","text":"hospital","meaning":"医院","phonetic":"ˈhɑspɪtəl","box":[0.2,0.1,0.1,0.03]}]}]}`
	if issues := testPageContentIssues(t, raw, true); len(issues) != 0 {
		t.Fatalf("complete OCR content should allow page audio regeneration, got %#v", issues)
	}
}

func TestPageAudioRegenerationIssuesRejectRealContentProblems(t *testing.T) {
	raw := `{"segments":[{"id":"s1","text":"hospita","translation":"医院","anchor":[0.1,0.1,0.5,0.05],"words":[{"id":"w1","text":"hospita","meaning":"医院（拼写错误，应为hospital）","phonetic":"ˈhɑspɪtəl","box":[0.2,0.1,0.1,0.03]}]}]}`
	if issues := testPageContentIssues(t, raw, true); len(issues) == 0 {
		t.Fatal("real publication issue should still block page audio regeneration")
	}
}


func TestPageTextReviewIssuesIgnoreAudioState(t *testing.T) {
	raw := `{"segments":[{"id":"s1","text":"There is a hospital.","translation":"这里有一家医院。","anchor":[0.1,0.1,0.5,0.05],"words":[{"id":"w1","text":"hospital","meaning":"医院","phonetic":"ˈhɑspɪtəl","box":[0.2,0.1,0.1,0.03]}]}]}`
	if issues := testPageContentIssues(t, raw, false); len(issues) != 0 {
		t.Fatalf("complete text content should be independently confirmable before audio, got %#v", issues)
	}
}

func TestPageTextReviewIssuesStillRejectRealTextProblems(t *testing.T) {
	raw := `{"segments":[{"id":"s1","text":"hospita","translation":"医院","anchor":[0.1,0.1,0.5,0.05],"words":[{"id":"w1","text":"hospita","meaning":"医院（拼写错误，应为hospital）","phonetic":"ˈhɑspɪtəl","box":[0.2,0.1,0.1,0.03]}]}]}`
	if issues := testPageContentIssues(t, raw, false); len(issues) == 0 {
		t.Fatal("real text/publication issue must block text confirmation")
	}
}
