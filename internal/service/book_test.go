package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"xiaov2/internal/model"
	"xiaov2/internal/repository"
	"xiaov2/internal/resource"
)

func writeBookAudioFixture(t *testing.T, resources *resource.Manager, book model.Book, accents map[string]string) fakeRepository {
	t.Helper()
	metadata, _ := resources.Dir(book.BookID, "metadata")
	if err := os.MkdirAll(filepath.Join(metadata, "pages"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(metadata, "pages", "page-001.json"), []byte(`{"segments":[{"id":"s1","text":"Hello","words":[{"id":"w1","text":"Hello"}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ttsDir, _ := resources.Dir(book.BookID, "tts")
	if err := os.MkdirAll(ttsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	items := make([]map[string]any, 0)
	for accent, voice := range accents {
		for _, id := range []string{"s1", "w1"} {
			name := accent + "-" + id + ".wav"
			if err := os.WriteFile(filepath.Join(ttsDir, name), []byte("wav"), 0o600); err != nil {
				t.Fatal(err)
			}
			items = append(items, map[string]any{"page": 1, "item_id": id, "accent": accent, "voice": voice, "file": name})
		}
	}
	raw, _ := json.Marshal(map[string]any{"schema_version": 2, "items": items, "failures": []any{}})
	if err := os.WriteFile(filepath.Join(ttsDir, "manifest.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return fakeRepository{books: []model.Book{book}, pages: []model.BookPage{{BookID: book.BookID, Position: 1, ContentPath: "metadata/pages/page-001.json"}}}
}

type fakeRepository struct {
	books []model.Book
	pages []model.BookPage
}

func (f fakeRepository) ListPublished(context.Context) ([]model.Book, error) { return f.books, nil }
func (f fakeRepository) FindPublished(_ context.Context, id string) (model.Book, error) {
	for _, book := range f.books {
		if book.BookID == id {
			return book, nil
		}
	}
	return model.Book{}, repository.ErrNotFound
}
func (f fakeRepository) ListPages(context.Context, string) ([]model.BookPage, error) {
	return f.pages, nil
}
func (f fakeRepository) FindPage(_ context.Context, id string, page int) (model.BookPage, error) {
	for _, item := range f.pages {
		if item.BookID == id && item.Position == page {
			return item, nil
		}
	}
	return model.BookPage{}, repository.ErrNotFound
}

func TestBookServiceListAndValidation(t *testing.T) {
	resources, _ := resource.New(t.TempDir())
	svc := NewBookService(fakeRepository{books: []model.Book{{BookID: "book-one", Title: "One", Status: "published", Cover: "pages/page-001.webp"}}}, resources)
	books, err := svc.List(context.Background())
	if err != nil || len(books) != 1 || books[0].BookID != "book-one" || books[0].Cover != "/api/v1/books/book-one/cover" {
		t.Fatalf("unexpected list: %#v, %v", books, err)
	}
	if _, err = svc.Get(context.Background(), "../bad"); !errors.Is(err, ErrInvalidBookID) {
		t.Fatalf("expected invalid id, got %v", err)
	}
}

func TestBookServiceReadsCanonicalPageMetadata(t *testing.T) {
	root := t.TempDir()
	resources, _ := resource.New(root)
	directory, _ := resources.Dir("book-one", "metadata")
	if err := os.MkdirAll(filepath.Join(directory, "pages"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "pages", "page-001.json"), []byte(`{"segments":[{"id":"s1","text":"Hello","words":[]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := fakeRepository{books: []model.Book{{BookID: "book-one", Status: "published"}}, pages: []model.BookPage{{BookID: "book-one", Position: 1, ImagePath: "pages/page-001.webp", ContentPath: "metadata/pages/page-001.json", Interactive: true}}}
	page, err := NewBookService(repo, resources).Page(context.Background(), "book-one", 1)
	if err != nil || string(page.Segments) == "" || page.Meta.Position != 1 {
		t.Fatalf("unexpected page: %#v, %v", page, err)
	}
}

func TestAvailableAccentsComeFromPublishedDatabaseState(t *testing.T) {
	resources, _ := resource.New(t.TempDir())
	book := model.Book{BookID: "audio-book", Status: "published", AmericanEnabled: true, BritishEnabled: true, AmericanVoiceID: "aiden", BritishVoiceID: "ryan", AudioConfigVersion: 1}
	repo := writeBookAudioFixture(t, resources, book, map[string]string{"en-US": "aiden", "en-GB": "ryan"})
	view, err := NewBookService(repo, resources).Get(context.Background(), book.BookID)
	if err != nil || len(view.Audio.AvailableAccents) != 2 {
		t.Fatalf("enabled accents were not exposed from database state: %#v %v", view.Audio, err)
	}

	book.BritishEnabled = false
	repo = fakeRepository{books: []model.Book{book}}
	view, err = NewBookService(repo, resources).Get(context.Background(), book.BookID)
	if err != nil || len(view.Audio.AvailableAccents) != 1 || view.Audio.AvailableAccents[0] != "en-US" {
		t.Fatalf("disabled British accent was exposed: %#v %v", view.Audio, err)
	}
}

func TestConfiguredAndLegacyAvailableAccents(t *testing.T) {
	resources, _ := resource.New(t.TempDir())
	book := model.Book{BookID: "both-book", Status: "published", AmericanEnabled: true, BritishEnabled: true, AmericanVoiceID: "aiden", BritishVoiceID: "ryan", AudioConfigVersion: 1}
	repo := writeBookAudioFixture(t, resources, book, map[string]string{"en-US": "aiden", "en-GB": "ryan"})
	view, err := NewBookService(repo, resources).Get(context.Background(), book.BookID)
	if err != nil || len(view.Audio.AvailableAccents) != 2 {
		t.Fatalf("expected both accents: %#v %v", view.Audio, err)
	}

	// The database can still advertise British after its audio files disappear.
	repo = writeBookAudioFixture(t, resources, book, map[string]string{"en-US": "aiden"})
	view, err = NewBookService(repo, resources).Get(context.Background(), book.BookID)
	if err != nil || len(view.Audio.AvailableAccents) != 1 || view.Audio.AvailableAccents[0] != "en-US" {
		t.Fatalf("missing British audio must not be offered: %#v %v", view.Audio, err)
	}

	book.AudioConfigVersion = 0
	book.AmericanEnabled, book.BritishEnabled = false, false
	repo = writeBookAudioFixture(t, resources, book, map[string]string{"en-US": "aiden"})
	view, err = NewBookService(repo, resources).Get(context.Background(), book.BookID)
	if err != nil || len(view.Audio.AvailableAccents) != 1 || view.Audio.AvailableAccents[0] != "en-US" {
		t.Fatalf("legacy accents should reflect generated audio: %#v %v", view.Audio, err)
	}
}

func TestAudioFileUsesPublishedPageAndManifestWithoutReadingPageJSON(t *testing.T) {
	resources, _ := resource.New(t.TempDir())
	book := model.Book{BookID: "audio-book", Status: "published", AmericanEnabled: true, AmericanVoiceID: "aiden", AudioConfigVersion: 1}
	repo := writeBookAudioFixture(t, resources, book, map[string]string{"en-US": "aiden"})
	metadata, _ := resources.Dir(book.BookID, "metadata")
	if err := os.Remove(filepath.Join(metadata, "pages", "page-001.json")); err != nil {
		t.Fatal(err)
	}
	svc := NewBookService(repo, resources)
	if _, err := svc.AudioFile(context.Background(), book.BookID, 1, "s1", "en-US"); err != nil {
		t.Fatalf("manifest-backed sentence audio should not depend on page JSON: %v", err)
	}
	if _, err := svc.AudioFile(context.Background(), book.BookID, 1, "w1", "en-US"); err != nil {
		t.Fatalf("manifest-backed word audio should not depend on page JSON: %v", err)
	}
	if _, err := svc.AudioFile(context.Background(), book.BookID, 2, "s1", "en-US"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing published page should be rejected: %v", err)
	}
}
