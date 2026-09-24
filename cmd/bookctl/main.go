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
	"strings"

	"xiaov2/internal/bundle"
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

func runImportPage(args []string) {
	fs := flag.NewFlagSet("import-page", flag.ExitOnError)
	draftID := fs.String("draft-id", "", "draft ID")
	page := fs.Int("page", 0, "source PDF page number")
	ocrPath := fs.String("ocr", "", "path to raw OCR JSON")
	contentPath := fs.String("content", "", "path to reviewed page JSON")
	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}
	if strings.TrimSpace(*draftID) == "" || *page < 1 || strings.TrimSpace(*ocrPath) == "" || strings.TrimSpace(*contentPath) == "" {
		log.Fatal("usage: go run ./cmd/bookctl import-page --draft-id <id> --page <n> --ocr <ocr.json> --content <page.json>")
	}
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
	result, err := service.NewEditorService(db, cfg, resources).ImportPageJSON(
		context.Background(),
		*draftID,
		*page,
		service.ImportPageJSONInput{OCRPath: *ocrPath, ContentPath: *contentPath},
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("imported draft=%s book=%s page=%d\nocr=%s\ncontent=%s\n", result.DraftID, result.BookID, result.Page, result.OCRPath, result.ContentPath)
}

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "export-bundle" || os.Args[1] == "import-bundle") {
		runBundle(os.Args[1:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "import-page" {
		runImportPage(os.Args[2:])
		return
	}
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
		log.Fatal("usage: go run ./cmd/bookctl [--publish] import <book_id> | go run ./cmd/bookctl migrate-audio-state | go run ./cmd/bookctl import-page --draft-id <id> --page <n> --ocr <ocr.json> --content <page.json>")
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

func runBundle(args []string) {
	if (args[0] == "export-bundle" && len(args) != 3) || (args[0] == "import-bundle" && len(args) != 2) {
		log.Fatal("usage: bookctl export-bundle <book_id> <new_directory> | bookctl import-bundle <directory>")
	}
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
	if args[0] == "export-bundle" {
		if err = bundle.Export(db, resources, args[1], args[2]); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("exported %s to %s\n", args[1], args[2])
		return
	}
	bookID, backup, err := bundle.Import(db, resources, args[1])
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("imported %s (published)\n", bookID)
	if backup != "" {
		fmt.Printf("previous resource backup: %s\n", backup)
	}
}
