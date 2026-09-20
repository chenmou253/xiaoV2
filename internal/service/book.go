package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"xiaov2/internal/model"
	"xiaov2/internal/repository"
	"xiaov2/internal/resource"
	"xiaov2/internal/tts"
)

var (
	ErrInvalidBookID = errors.New("invalid book id")
	ErrInvalidPage   = errors.New("invalid page")
	ErrNotFound      = errors.New("book not found")
)

type Book struct {
	BookID      string    `json:"book_id"`
	Title       string    `json:"title"`
	Subtitle    string    `json:"subtitle"`
	Description string    `json:"description"`
	Publisher   string    `json:"publisher"`
	Grade       string    `json:"grade"`
	Semester    string    `json:"semester"`
	Cover       string    `json:"cover"`
	Status      string    `json:"status"`
	PageCount   int       `json:"page_count"`
	Sort        int       `json:"sort"`
	Audio       BookAudio `json:"audio"`
}

type BookAudio struct {
	AvailableAccents []string `json:"available_accents"`
	DefaultAccent    string   `json:"default_accent"`
}

type PageSummary struct {
	BookID      string `json:"book_id"`
	Position    int    `json:"page"`
	PrintedPage *int   `json:"printed_page"`
	Title       string `json:"title"`
	Unit        string `json:"unit"`
	Image       string `json:"image"`
	Interactive bool   `json:"interactive"`
}

type PageContent struct {
	Meta     PageSummary     `json:"-"`
	Segments json.RawMessage `json:"segments"`
}

func (p PageContent) MarshalJSON() ([]byte, error) {
	extra := map[string]any{"segments": p.Segments}
	extra["book_id"] = p.Meta.BookID
	extra["page"] = p.Meta.Position
	extra["printed_page"] = p.Meta.PrintedPage
	extra["title"] = p.Meta.Title
	extra["unit"] = p.Meta.Unit
	extra["image"] = p.Meta.Image
	extra["interactive"] = p.Meta.Interactive
	return json.Marshal(extra)
}

type BookService struct {
	repository repository.BookRepository
	resources  *resource.Manager
}

func NewBookService(repo repository.BookRepository, resources *resource.Manager) *BookService {
	return &BookService{repository: repo, resources: resources}
}

func (s *BookService) List(ctx context.Context) ([]Book, error) {
	models, err := s.repository.ListPublished(ctx)
	if err != nil {
		return nil, err
	}
	books := make([]Book, 0, len(models))
	for _, item := range models {
		books = append(books, s.toBook(ctx, item))
	}
	return books, nil
}

func (s *BookService) Get(ctx context.Context, bookID string) (Book, error) {
	if !resource.ValidBookID(bookID) {
		return Book{}, ErrInvalidBookID
	}
	book, err := s.repository.FindPublished(ctx, bookID)
	if errors.Is(err, repository.ErrNotFound) {
		return Book{}, ErrNotFound
	}
	if err != nil {
		return Book{}, err
	}
	return s.toBook(ctx, book), nil
}

func (s *BookService) Pages(ctx context.Context, bookID string) ([]PageSummary, error) {
	if _, err := s.Get(ctx, bookID); err != nil {
		return nil, err
	}
	pages, err := s.repository.ListPages(ctx, bookID)
	if err != nil {
		return nil, err
	}
	result := make([]PageSummary, 0, len(pages))
	for _, page := range pages {
		result = append(result, s.toPage(page))
	}
	return result, nil
}

func (s *BookService) Page(ctx context.Context, bookID string, position int) (PageContent, error) {
	if !resource.ValidBookID(bookID) {
		return PageContent{}, ErrInvalidBookID
	}
	if position < 1 {
		return PageContent{}, ErrInvalidPage
	}
	if _, err := s.repository.FindPublished(ctx, bookID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return PageContent{}, ErrNotFound
		}
		return PageContent{}, err
	}
	page, err := s.repository.FindPage(ctx, bookID, position)
	if errors.Is(err, repository.ErrNotFound) {
		return PageContent{}, ErrNotFound
	}
	if err != nil {
		return PageContent{}, err
	}
	path, err := s.resources.Resolve(bookID, page.ContentPath)
	if err != nil {
		return PageContent{}, fmt.Errorf("resolve page metadata: %w", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return PageContent{}, fmt.Errorf("read page metadata: %w", err)
	}
	var document map[string]json.RawMessage
	if err = json.Unmarshal(raw, &document); err != nil {
		return PageContent{}, fmt.Errorf("decode page metadata: %w", err)
	}
	segments := document["segments"]
	if len(segments) == 0 {
		segments = json.RawMessage(`[]`)
	}
	return PageContent{Meta: s.toPage(page), Segments: segments}, nil
}

func (s *BookService) CoverFile(ctx context.Context, bookID string) (string, error) {
	if !resource.ValidBookID(bookID) {
		return "", ErrInvalidBookID
	}
	book, err := s.repository.FindPublished(ctx, bookID)
	if errors.Is(err, repository.ErrNotFound) || (err == nil && book.Cover == "") {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return s.resources.Resolve(bookID, book.Cover)
}

func (s *BookService) PageImageFile(ctx context.Context, bookID string, position int) (string, error) {
	if !resource.ValidBookID(bookID) {
		return "", ErrInvalidBookID
	}
	if position < 1 {
		return "", ErrInvalidPage
	}
	// Page assets are publicly readable, but only after the book itself has
	// been published. This check used to be supplied by the removed billing
	// middleware and must remain local to the asset endpoint.
	if _, err := s.repository.FindPublished(ctx, bookID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return "", ErrNotFound
		}
		return "", err
	}
	page, err := s.repository.FindPage(ctx, bookID, position)
	if errors.Is(err, repository.ErrNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return s.resources.Resolve(bookID, page.ImagePath)
}

func (s *BookService) AudioFile(ctx context.Context, bookID string, position int, itemID, accent string) (string, error) {
	book, err := s.Get(ctx, bookID)
	if err != nil {
		return "", err
	}
	available := false
	for _, value := range book.Audio.AvailableAccents {
		available = available || value == accent
	}
	if !available {
		return "", ErrNotFound
	}
	page, err := s.Page(ctx, bookID, position)
	if err != nil {
		return "", err
	}
	var content struct {
		Segments []struct {
			ID        string `json:"id"`
			Text      string `json:"text"`
			AudioMode string `json:"audio_mode"`
			Words     []struct {
				ID   string `json:"id"`
				Text string `json:"text"`
			} `json:"words"`
		} `json:"segments"`
	}
	wrapped, _ := json.Marshal(map[string]json.RawMessage{"segments": page.Segments})
	if err = json.Unmarshal(wrapped, &content); err != nil {
		return "", err
	}
	for _, segment := range content.Segments {
		if segment.ID == itemID && segment.AudioMode != "word_only" && segment.AudioMode != "none" {
			return s.resources.AudioItemFile(bookID, position, segment.ID, accent)
		}
		for _, word := range segment.Words {
			if word.ID == itemID {
				if segment.AudioMode == "none" || !resource.HasSpeakableText(word.Text) {
					return "", ErrNotFound
				}
				return s.resources.AudioItemFile(bookID, position, word.ID, accent)
			}
		}
	}
	return "", ErrNotFound
}

func (s *BookService) toBook(ctx context.Context, item model.Book) Book {
	cover := ""
	if item.Cover != "" {
		cover = "/api/v1/books/" + url.PathEscape(item.BookID) + "/cover"
	}
	available := s.availableAccents(ctx, item)
	defaultAccent := ""
	if len(available) > 0 {
		defaultAccent = available[0]
	}
	return Book{BookID: item.BookID, Title: item.Title, Subtitle: item.Subtitle, Description: item.Description, Publisher: item.Publisher, Grade: item.Grade, Semester: item.Semester, Cover: cover, Status: item.Status, PageCount: item.PageCount, Sort: item.Sort, Audio: BookAudio{AvailableAccents: available, DefaultAccent: defaultAccent}}
}

func normalizeBookAudio(item model.Book) model.Book {
	if item.AmericanVoiceID == "" {
		item.AmericanVoiceID = tts.DefaultAmericanVoice
	}
	if item.BritishVoiceID == "" {
		item.BritishVoiceID = tts.DefaultBritishVoice
	}
	// Existing rows created before these columns are migration version zero.
	// Historically both accents were always generated, so keep both enabled.
	if item.AudioConfigVersion == 0 {
		item.AmericanEnabled, item.BritishEnabled = true, true
	}
	return item
}

func (s *BookService) availableAccents(ctx context.Context, item model.Book) []string {
	item = normalizeBookAudio(item)
	pages, err := s.repository.ListPages(ctx, item.BookID)
	if err != nil {
		return []string{}
	}
	type expectedItem struct {
		page int
		id   string
	}
	expected := make([]expectedItem, 0)
	for _, page := range pages {
		path, resolveErr := s.resources.Resolve(item.BookID, page.ContentPath)
		if resolveErr != nil {
			return []string{}
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return []string{}
		}
		var content struct {
			Segments []struct {
				ID        string `json:"id"`
				AudioMode string `json:"audio_mode"`
				Words     []struct {
					ID   string `json:"id"`
					Text string `json:"text"`
				} `json:"words"`
			} `json:"segments"`
		}
		if json.Unmarshal(raw, &content) != nil {
			return []string{}
		}
		for _, segment := range content.Segments {
			if segment.AudioMode == "none" {
				continue
			}
			if segment.AudioMode != "word_only" && segment.ID != "" {
				expected = append(expected, expectedItem{page.Position, segment.ID})
			}
			for _, word := range segment.Words {
				if word.ID != "" && resource.HasSpeakableText(word.Text) {
					expected = append(expected, expectedItem{page.Position, word.ID})
				}
			}
		}
	}
	if len(expected) == 0 {
		return []string{}
	}
	legacy := item.AudioConfigVersion == 0
	configs := []struct {
		accent, voice string
		enabled       bool
	}{
		{tts.AccentUS, item.AmericanVoiceID, item.AmericanEnabled},
		{tts.AccentGB, item.BritishVoiceID, item.BritishEnabled},
	}
	result := make([]string, 0, 2)
	for _, config := range configs {
		if !config.enabled {
			continue
		}
		complete := true
		for _, wanted := range expected {
			found := false
			for _, kind := range []string{"tts", "audio"} {
				dir, dirErr := s.resources.Dir(item.BookID, kind)
				if dirErr == nil && resource.AudioItemReadyInDir(filepath.Clean(dir), wanted.page, wanted.id, config.accent, config.voice, legacy) {
					found = true
					break
				}
			}
			if !found {
				complete = false
				break
			}
		}
		if complete {
			result = append(result, config.accent)
		}
	}
	return result
}

func (s *BookService) toPage(page model.BookPage) PageSummary {
	return PageSummary{BookID: page.BookID, Position: page.Position, PrintedPage: page.PrintedPage, Title: page.Title, Unit: page.Unit, Interactive: page.Interactive, Image: fmt.Sprintf("/api/v1/books/%s/pages/%d/image", url.PathEscape(page.BookID), page.Position)}
}
