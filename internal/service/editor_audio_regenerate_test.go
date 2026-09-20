package service

import "testing"

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