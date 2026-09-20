package service

import (
	"encoding/json"
	"testing"
)

func pageContentForOrderTest(t *testing.T, raw string) map[string]any {
	t.Helper()
	var content map[string]any
	if err := json.Unmarshal([]byte(raw), &content); err != nil {
		t.Fatal(err)
	}
	return content
}

func TestSamePageContentExceptSegmentOrder(t *testing.T) {
	stored := pageContentForOrderTest(t, `{"segments":[{"id":"s1","text":"first","words":[{"id":"w1","text":"first"}]},{"id":"s2","text":"second","words":[{"id":"w2","text":"second"}]}],"title":"page"}`)
	reordered := pageContentForOrderTest(t, `{"title":"page","segments":[{"id":"s2","text":"second","words":[{"id":"w2","text":"second"}]},{"id":"s1","text":"first","words":[{"id":"w1","text":"first"}]}]}`)
	if !samePageContentExceptSegmentOrder(stored, reordered) {
		t.Fatal("segment-only reordering should preserve generated audio")
	}
}

func TestSamePageContentExceptSegmentOrderRejectsMaterialEdits(t *testing.T) {
	stored := pageContentForOrderTest(t, `{"segments":[{"id":"s1","text":"first","words":[{"id":"w1","text":"first"}]},{"id":"s2","text":"second","words":[{"id":"w2","text":"second"}]}]}`)
	cases := map[string]string{
		"segment text": `{"segments":[{"id":"s2","text":"changed","words":[{"id":"w2","text":"second"}]},{"id":"s1","text":"first","words":[{"id":"w1","text":"first"}]}]}`,
		"word order":   `{"segments":[{"id":"s2","text":"second","words":[{"id":"w2b","text":"other"},{"id":"w2","text":"second"}]},{"id":"s1","text":"first","words":[{"id":"w1","text":"first"}]}]}`,
		"duplicate id": `{"segments":[{"id":"s1","text":"first","words":[]},{"id":"s1","text":"second","words":[]}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if samePageContentExceptSegmentOrder(stored, pageContentForOrderTest(t, raw)) {
				t.Fatal("material edit must invalidate generated audio")
			}
		})
	}
}
