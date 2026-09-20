package service

import (
	"strings"
	"testing"
)

func TestPublicationIssuesFlagsTranslationSpellingErrorHint(t *testing.T) {
	content := map[string]any{
		"segments": []any{
			map[string]any{
				"id": "p1-s0",
				"text": "hospita",
				"translation": "医院",
				"anchor": []any{0.1, 0.1, 0.2, 0.05},
				"words": []any{
					map[string]any{
						"id": "p1-s0-w0",
						"text": "hospita",
						"meaning": "医院（拼写错误，应为hospital）",
						"phonetic": "ˈhɑspɪtəl",
						"box": []any{0.1, 0.1, 0.1, 0.03},
					},
				},
			},
		},
	}
	issues := publicationIssues(content)
	found := false
	for _, issue := range issues {
		if strings.Contains(issue, "hospita：翻译模型提示拼写错误") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected spelling warning, got %#v", issues)
	}
}

func TestPublicationIssuesFlagsAlternateSpellingHint(t *testing.T) {
	content := map[string]any{
		"segments": []any{
			map[string]any{
				"id": "p1-s0",
				"text": "hospita",
				"translation": "医院",
				"anchor": []any{0.1, 0.1, 0.2, 0.05},
				"words": []any{
					map[string]any{
						"id": "p1-s0-w0",
						"text": "hospita",
						"meaning": "医院（拼写有误，应为hospital）",
						"phonetic": "ˈhɑspɪtəl",
						"box": []any{0.1, 0.1, 0.1, 0.03},
					},
				},
			},
		},
	}
	issues := publicationIssues(content)
	if len(issues) == 0 {
		t.Fatal("expected spelling warning")
	}
}

func TestPublicationIssuesAllowsNormalMeaning(t *testing.T) {
	content := map[string]any{
		"segments": []any{
			map[string]any{
				"id": "p1-s0",
				"text": "hospital",
				"translation": "医院",
				"anchor": []any{0.1, 0.1, 0.2, 0.05},
				"words": []any{
					map[string]any{
						"id": "p1-s0-w0",
						"text": "hospital",
						"meaning": "医院",
						"phonetic": "ˈhɑspɪtəl",
						"box": []any{0.1, 0.1, 0.1, 0.03},
					},
				},
			},
		},
	}
	issues := publicationIssues(content)
	for _, issue := range issues {
		if strings.Contains(issue, "拼写错误") {
			t.Fatalf("normal meaning should not be flagged: %#v", issues)
		}
	}
}
