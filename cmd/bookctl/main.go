package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"xiaov2/internal/config"
	"xiaov2/internal/database"
	"xiaov2/internal/model"
	"xiaov2/internal/resource"
	"xiaov2/internal/service"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type manifest struct {
	SchemaVersion int `json:"schema_version"`
	Book          struct {
		BookID      string `json:"book_id"`
		Title       string `json:"title"`
		Subtitle    string `json:"subtitle"`
		Description string `json:"description"`
		Publisher   string `json:"publisher"`
		Grade       string `json:"grade"`
		Semester    string `json:"semester"`
		Cover       string `json:"cover"`
		Sort        int    `json:"sort"`
	} `json:"book"`
	Pages []struct {
		Position    int    `json:"page"`
		PrintedPage *int   `json:"printed_page"`
		Title       string `json:"title"`
		Unit        string `json:"unit"`
		Image       string `json:"image"`
		Content     string `json:"content"`
		Interactive bool   `json:"interactive"`
	} `json:"pages"`
}

func main() {
	publish := flag.Bool("publish", false, "make the imported book visible on the shelf")
	flag.Parse()
	if flag.NArg() == 1 && flag.Arg(0) == "migrate-audio-state" {
		cfg, err := config.Load()
		if err != nil {
			log.Fatal(err)
		}
		db, err := database.Open(cfg)
		if err != nil {
			log.Fatal(err)
		}
		sqlDB, _ := db.DB()
		defer sqlDB.Close()
		if err = database.Migrate(db); err != nil {
			log.Fatal(err)
		}
		resources, err := resource.New(cfg.ResourceRoot)
		if err != nil {
			log.Fatal(err)
		}
		count, err := service.NewEditorService(db, cfg, resources).BackfillAudioState(context.Background())
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("backfilled audio state for %d drafts\n", count)
		return
	}
	if flag.NArg() != 2 || flag.Arg(0) != "import" {
		log.Fatal("usage: go run ./cmd/bookctl [--publish] import <book_id> | go run ./cmd/bookctl migrate-audio-state")
	}
	bookID := flag.Arg(1)
	if !resource.ValidBookID(bookID) {
		log.Fatal("invalid book_id")
	}
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	resources, err := resource.New(cfg.ResourceRoot)
	if err != nil {
		log.Fatal(err)
	}
	manifestPath, err := resources.Resolve(bookID, "metadata/book.json")
	if err != nil {
		log.Fatal(err)
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		log.Fatal(err)
	}
	var data manifest
	if err = json.Unmarshal(raw, &data); err != nil {
		log.Fatal(err)
	}
	if data.SchemaVersion != 1 || data.Book.BookID != bookID || data.Book.Title == "" {
		log.Fatal("manifest schema, book_id, or title is invalid")
	}
	for _, page := range data.Pages {
		if page.Position < 1 || page.Image == "" || page.Content == "" {
			log.Fatalf("page %d has invalid metadata", page.Position)
		}
		for _, relative := range []string{page.Image, page.Content} {
			path, resolveErr := resources.Resolve(bookID, relative)
			if resolveErr != nil {
				log.Fatal(resolveErr)
			}
			if info, statErr := os.Stat(path); statErr != nil || !info.Mode().IsRegular() {
				log.Fatalf("resource is missing: %s", filepath.ToSlash(relative))
			}
		}
	}
	db, err := database.Open(cfg)
	if err != nil {
		log.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err = database.Migrate(db); err != nil {
		log.Fatal(err)
	}
	status := "draft"
	if *publish {
		status = "published"
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		book := model.Book{BookID: bookID, Title: data.Book.Title, Subtitle: data.Book.Subtitle, Description: data.Book.Description, Publisher: data.Book.Publisher, Grade: data.Book.Grade, Semester: data.Book.Semester, Cover: data.Book.Cover, Status: status, PageCount: len(data.Pages), Sort: data.Book.Sort}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "book_id"}}, DoUpdates: clause.AssignmentColumns([]string{"title", "subtitle", "description", "publisher", "grade", "semester", "cover", "status", "page_count", "sort", "updated_at"})}).Create(&book).Error; err != nil {
			return err
		}
		if err := tx.Where("book_id = ?", bookID).Delete(&model.BookPage{}).Error; err != nil {
			return err
		}
		for _, input := range data.Pages {
			page := model.BookPage{BookID: bookID, Position: input.Position, PrintedPage: input.PrintedPage, Title: input.Title, Unit: input.Unit, ImagePath: input.Image, ContentPath: input.Content, Interactive: input.Interactive}
			if err := tx.Create(&page).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			log.Fatal("manifest contains duplicate pages")
		}
		log.Fatal(err)
	}
	fmt.Printf("imported %s (%d pages, status=%s)\n", bookID, len(data.Pages), status)
}
