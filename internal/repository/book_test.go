package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestGORMBookRepositoryFindPublished(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	query := "SELECT * FROM `books` WHERE book_id = ? AND status = ? ORDER BY `books`.`id` LIMIT ?"
	mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs("book-one", "published", 1).WillReturnRows(
		sqlmock.NewRows([]string{"id", "book_id", "title", "status", "created_at", "updated_at"}).AddRow(1, "book-one", "One", "published", now, now),
	)
	book, err := NewBookRepository(db).FindPublished(context.Background(), "book-one")
	if err != nil || book.BookID != "book-one" {
		t.Fatalf("unexpected book: %#v, %v", book, err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
