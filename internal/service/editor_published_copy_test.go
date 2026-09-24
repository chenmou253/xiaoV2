package service

import (
	"os"
	"path/filepath"
	"testing"

	"xiaov2/internal/model"
	"xiaov2/internal/resource"
)

func TestPublishedAudioInheritanceNeedsPublishedOriginAndConfirmation(t *testing.T) {
	draft := model.TextbookDraft{SourceKind: "published"}
	page := model.TextbookDraftPage{AudioChecked: true, InheritedAudio: true}
	if !inheritedPublishedAudio(draft, page) {
		t.Fatal("unchanged published page should retain its audio review")
	}
	draft.SourceKind = "upload"
	if inheritedPublishedAudio(draft, page) {
		t.Fatal("an uploaded draft must use the fresh audio review rules")
	}
	draft.SourceKind = "published"
	page.AudioChecked = false
	if inheritedPublishedAudio(draft, page) {
		t.Fatal("an edited or deconfirmed page must not inherit audio approval")
	}
}

func TestLegacyCopyOnlyInheritsUnchangedPublishedContent(t *testing.T) {
	manager, err := resource.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path, err := manager.Resolve("book-one", "metadata/pages/page-001.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	base := `{"book_id":"book-one","page":1,"segments":[{"id":"s1","text":"Hello","translation":"你好","words":[]}]}`
	if err := os.WriteFile(path, []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	service := &EditorService{resources: manager}
	published := model.BookPage{ContentPath: "metadata/pages/page-001.json"}
	page := model.TextbookDraftPage{Content: `{"book_id":"book-one","page":1,"segments":[{"id":"s1","text":"Hello","words":[]}]}`}
	if !samePublishedPageContent(service, "book-one", page, published) {
		t.Fatal("translation fields removed during copying must not invalidate inheritance")
	}
	page.Content = `{"book_id":"book-one","page":1,"segments":[{"id":"s1","text":"Goodbye","words":[]}]}`
	if samePublishedPageContent(service, "book-one", page, published) {
		t.Fatal("changed reading text must require page review")
	}
}

func TestLegacyCopyKeepsOtherPagesWhenOnePageWasEdited(t *testing.T) {
	book := model.Book{AudioConfigVersion: 0}
	audits := []model.AuditLog{{Action: "draft.page.save", Detail: `{"page":10}`}, {Action: "draft.audio.settings", Detail: `{"american_enabled":true,"british_enabled":true}`}}
	global, touched := legacyCopyChanges(audits, book)
	if global || !touched[10] || touched[9] {
		t.Fatalf("page-local edit affected other pages: global=%t touched=%v", global, touched)
	}
	audits = append(audits, model.AuditLog{Action: "draft.audio.settings", Detail: `{"american_enabled":true,"british_enabled":false}`})
	global, _ = legacyCopyChanges(audits, book)
	if !global {
		t.Fatal("a book-wide accent change must invalidate inherited audio")
	}
	global, touched = legacyCopyChanges([]model.AuditLog{{Action: "draft.reocr", Detail: `{"page":3}`}, {Action: "draft.audio-restart-page", Detail: `{"page":4}`}}, book)
	if global || !touched[3] || !touched[4] || touched[5] {
		t.Fatalf("page-local regeneration affected other pages: global=%t touched=%v", global, touched)
	}
}

func TestPublishedCopyOnlyEnablesConfiguredPlayableAccents(t *testing.T) {
	book := model.Book{AudioConfigVersion: 1, AmericanEnabled: true, BritishEnabled: true}
	us, gb := publishedCopyAccents(book, []string{"en-US"})
	if !us || gb {
		t.Fatalf("published copy should not inherit a missing British recording: US=%t GB=%t", us, gb)
	}
	book.BritishEnabled = false
	us, gb = publishedCopyAccents(book, []string{"en-US", "en-GB"})
	if !us || gb {
		t.Fatalf("published copy should respect disabled British setting: US=%t GB=%t", us, gb)
	}
	book.AudioConfigVersion = 0
	book.AmericanEnabled = false
	us, gb = publishedCopyAccents(book, []string{"en-US"})
	if !us || gb {
		t.Fatalf("legacy published copy should inherit only existing recordings: US=%t GB=%t", us, gb)
	}
}
