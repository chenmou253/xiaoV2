package service

import (
	"encoding/json"
	"testing"
)

func TestDecodeImportedContentAcceptsWordBoxes(t *testing.T) {
	raw := []byte(`{
		"book_id":"grade4",
		"page":32,
		"segments":[{
			"id":"p32-s0",
			"text":"I see a doctor there.",
			"translation":"我在那里看医生。",
			"anchor":[0.1,0.2,0.5,0.1],
			"words":[
				{"id":"p32-s0-w0","text":"I","meaning":"我","phonetic":"aɪ","box":[0.1,0.2,0.02,0.03]},
				{"id":"p32-s0-w1","text":"see","meaning":"看","phonetic":"siː","box":[0.13,0.2,0.08,0.03]}
			]
		}]
	}`)
	content, err := decodeImportedContent(raw)
	if err != nil {
		t.Fatalf("decodeImportedContent: %v", err)
	}
	segments, ok := content["segments"].([]any)
	if !ok || len(segments) != 1 {
		t.Fatalf("segments = %#v", content["segments"])
	}
}

func TestDecodeImportedContentRejectsDuplicateIDs(t *testing.T) {
	raw := []byte(`{
		"segments":[{
			"id":"same",
			"text":"hello",
			"anchor":[0.1,0.2,0.2,0.1],
			"words":[{"id":"same","text":"hello","box":[0.1,0.2,0.2,0.1]}]
		}]
	}`)
	if _, err := decodeImportedContent(raw); err == nil {
		t.Fatal("expected duplicate ID error")
	}
}

func TestDecodeImportedContentRejectsInvalidWordBox(t *testing.T) {
	raw := []byte(`{
		"segments":[{
			"id":"p1-s0",
			"text":"hello",
			"anchor":[0.1,0.2,0.2,0.1],
			"words":[{"id":"p1-s0-w0","text":"hello","box":[0.9,0.2,0.2,0.1]}]
		}]
	}`)
	if _, err := decodeImportedContent(raw); err == nil {
		t.Fatal("expected invalid word box error")
	}
}

func TestDecodeImportedOCRRequiresWordBoxes(t *testing.T) {
	raw := []byte(`{"rows":[{"text":"hello","box":[0.1,0.2,0.2,0.1],"words":[]}]}`)
	if _, err := decodeImportedOCR(raw); err == nil {
		t.Fatal("expected missing OCR word boxes error")
	}
}

func TestDecodeImportedOCRAcceptsProjectShape(t *testing.T) {
	input := map[string]any{
		"method": "manual-ground-truth",
		"rows": []any{
			map[string]any{
				"text": "go shopping",
				"box": []any{0.1, 0.2, 0.3, 0.1},
				"words": []any{
					map[string]any{"text":"go","box":[]any{0.1,0.2,0.08,0.04}},
					map[string]any{"text":"shopping","box":[]any{0.2,0.2,0.2,0.04}},
				},
			},
		},
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeImportedOCR(raw); err != nil {
		t.Fatalf("decodeImportedOCR: %v", err)
	}
}
