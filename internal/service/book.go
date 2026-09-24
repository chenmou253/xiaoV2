package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"

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
	PageCount   int       `json:"page_count"`
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
		books = append(books, s.toBook(item))
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
	return s.toBook(book), nil
}

func (s *BookService) Pages(ctx context.Context, bookID string) ([]PageSummary, error) {
	if !resource.ValidBookID(bookID) {
		return nil, ErrInvalidBookID
	}
	if _, err := s.repository.FindPublished(ctx, bookID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrNotFound
		}
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
	return s.pageContent(ctx, bookID, position)
}

func (s *BookService) pageContent(ctx context.Context, bookID string, position int) (PageContent, error) {
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
	if !resource.ValidBookID(bookID) {
		return "", ErrInvalidBookID
	}
	if position < 1 {
		return "", ErrInvalidPage
	}
	book, err := s.repository.FindPublished(ctx, bookID)
	if errors.Is(err, repository.ErrNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if !bookAccentEnabled(book, accent) {
		return "", ErrNotFound
	}
	// Runtime audio lookup trusts the published database row plus the audio
	// manifest. Do not read the whole page JSON just to validate one item.
	if _, err = s.repository.FindPage(ctx, bookID, position); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return "", ErrNotFound
		}
		return "", err
	}
	path, err := s.resources.AudioItemFile(bookID, position, itemID, accent)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNotFound
	}
	return path, err
}

func (s *BookService) toBook(item model.Book) Book {
	cover := ""
	if item.Cover != "" {
		cover = "/api/v1/books/" + url.PathEscape(item.BookID) + "/cover"
	}
	available := bookAvailableAccents(item)
	// A configured accent is only usable when published audio exists for it.
	playable := make(map[string]bool, 2)
	for _, accent := range s.resources.AudioAccents(item.BookID) {
		playable[accent] = true
	}
	filtered := available[:0]
	for _, accent := range available {
		if playable[accent] {
			filtered = append(filtered, accent)
		}
	}
	available = filtered
	defaultAccent := ""
	if len(available) > 0 {
		defaultAccent = available[0]
	}
	return Book{
		BookID: item.BookID, Title: item.Title, Subtitle: item.Subtitle,
		Description: item.Description, Publisher: item.Publisher, Grade: item.Grade,
		Semester: item.Semester, Cover: cover, PageCount: item.PageCount,
		Audio: BookAudio{AvailableAccents: available, DefaultAccent: defaultAccent},
	}
}

func normalizeBookAudio(item model.Book) model.Book {
	if item.AmericanVoiceID == "" {
		item.AmericanVoiceID = tts.DefaultAmericanVoice
	}
	if item.BritishVoiceID == "" {
		item.BritishVoiceID = tts.DefaultBritishVoice
	}
	// Existing rows created before these columns are migration version zero.
	// Historically both accents were always enabled.
	if item.AudioConfigVersion == 0 {
		item.AmericanEnabled, item.BritishEnabled = true, true
	}
	return item
}

func bookAvailableAccents(item model.Book) []string {
	item = normalizeBookAudio(item)
	result := make([]string, 0, 2)
	if item.AmericanEnabled {
		result = append(result, tts.AccentUS)
	}
	if item.BritishEnabled {
		result = append(result, tts.AccentGB)
	}
	return result
}

func bookAccentEnabled(item model.Book, accent string) bool {
	for _, value := range bookAvailableAccents(item) {
		if value == accent {
			return true
		}
	}
	return false
}

func (s *BookService) toPage(page model.BookPage) PageSummary {
	return PageSummary{BookID: page.BookID, Position: page.Position, PrintedPage: page.PrintedPage, Title: page.Title, Unit: page.Unit, Interactive: page.Interactive, Image: fmt.Sprintf("/api/v1/books/%s/pages/%d/image", url.PathEscape(page.BookID), page.Position)}
}
