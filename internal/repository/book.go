package repository

import (
	"context"
	"errors"

	"xiaov2/internal/model"

	"gorm.io/gorm"
)

type BookRepository interface {
	ListPublished(context.Context) ([]model.Book, error)
	FindPublished(context.Context, string) (model.Book, error)
	ListPages(context.Context, string) ([]model.BookPage, error)
	FindPage(context.Context, string, int) (model.BookPage, error)
}

type GORMBookRepository struct{ db *gorm.DB }

func NewBookRepository(db *gorm.DB) *GORMBookRepository { return &GORMBookRepository{db: db} }

func (r *GORMBookRepository) ListPublished(ctx context.Context) ([]model.Book, error) {
	var books []model.Book
	err := r.db.WithContext(ctx).Where("status = ?", "published").Order("sort ASC, created_at ASC, book_id ASC").Find(&books).Error
	return books, err
}

func (r *GORMBookRepository) FindPublished(ctx context.Context, bookID string) (model.Book, error) {
	var book model.Book
	err := r.db.WithContext(ctx).Where("book_id = ? AND status = ?", bookID, "published").First(&book).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Book{}, ErrNotFound
	}
	return book, err
}

func (r *GORMBookRepository) ListPages(ctx context.Context, bookID string) ([]model.BookPage, error) {
	var pages []model.BookPage
	err := r.db.WithContext(ctx).Where("book_id = ?", bookID).Order("position ASC").Find(&pages).Error
	return pages, err
}

func (r *GORMBookRepository) FindPage(ctx context.Context, bookID string, position int) (model.BookPage, error) {
	var page model.BookPage
	err := r.db.WithContext(ctx).Where("book_id = ? AND position = ?", bookID, position).First(&page).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.BookPage{}, ErrNotFound
	}
	return page, err
}

var ErrNotFound = errors.New("not found")
