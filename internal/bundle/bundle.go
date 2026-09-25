package bundle

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"xiaov2/internal/model"
	"xiaov2/internal/resource"

	"gorm.io/gorm"
)

// Bundle is the portable, published-only resource format. It deliberately
// excludes draft/job rows and local generation artefacts.
type Bundle struct {
	SchemaVersion int    `json:"schema_version"`
	Book          Book   `json:"book"`
	Pages         []Page `json:"pages"`
}

type Book struct {
	BookID          string `json:"book_id"`
	Title           string `json:"title"`
	Subtitle        string `json:"subtitle"`
	Description     string `json:"description"`
	Publisher       string `json:"publisher"`
	Grade           string `json:"grade"`
	Semester        string `json:"semester"`
	Cover           string `json:"cover"`
	Sort            int    `json:"sort"`
	AmericanEnabled bool   `json:"american_enabled"`
	BritishEnabled  bool   `json:"british_enabled"`
	AmericanVoiceID string `json:"american_voice_id"`
	BritishVoiceID  string `json:"british_voice_id"`
}

type Page struct {
	Position    int    `json:"page"`
	PrintedPage *int   `json:"printed_page"`
	Title       string `json:"title"`
	Unit        string `json:"unit"`
	Image       string `json:"image"`
	Content     string `json:"content"`
	Interactive bool   `json:"interactive"`
	Preview     bool   `json:"preview"`
}

func Export(db *gorm.DB, resources *resource.Manager, bookID, destination string) (err error) {
	if !resource.ValidBookID(bookID) {
		return errors.New("invalid book_id")
	}
	var book model.Book
	if err = db.Where("book_id = ? AND status = ?", bookID, "published").First(&book).Error; err != nil {
		return fmt.Errorf("find published book: %w", err)
	}
	var pages []model.BookPage
	if err = db.Where("book_id = ?", bookID).Order("position").Find(&pages).Error; err != nil {
		return err
	}
	if len(pages) == 0 {
		return errors.New("published book has no pages")
	}
	source, err := resources.BookRoot(bookID)
	if err != nil {
		return err
	}
	absoluteDestination, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if relative, relErr := filepath.Rel(source, absoluteDestination); relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("export directory must be outside the published book directory")
	}
	if err = os.Mkdir(destination, 0750); err != nil {
		return fmt.Errorf("create new export directory: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(destination)
		}
	}()
	data := Bundle{SchemaVersion: 2, Book: Book{
		BookID: book.BookID, Title: book.Title, Subtitle: book.Subtitle,
		Description: book.Description, Publisher: book.Publisher, Grade: book.Grade,
		Semester: book.Semester, Cover: book.Cover, Sort: book.Sort,
		AmericanEnabled: book.AmericanEnabled, BritishEnabled: book.BritishEnabled,
		AmericanVoiceID: book.AmericanVoiceID, BritishVoiceID: book.BritishVoiceID,
	}}
	assets := map[string]bool{}
	images := map[string]string{}
	if book.Cover != "" {
		images[resource.WebPPath(book.Cover)] = book.Cover
		data.Book.Cover = resource.WebPPath(book.Cover)
	}
	for _, page := range pages {
		imagePath := resource.WebPPath(page.ImagePath)
		data.Pages = append(data.Pages, Page{
			Position: page.Position, PrintedPage: page.PrintedPage, Title: page.Title,
			Unit: page.Unit, Image: imagePath, Content: page.ContentPath,
			Interactive: page.Interactive, Preview: page.Preview,
		})
		images[imagePath] = page.ImagePath
		assets[page.ContentPath] = true
	}
	for relative := range assets {
		if err = copyAsset(source, destination, relative); err != nil {
			return err
		}
	}
	for target, relative := range images {
		imageSource, resolveErr := resources.Resolve(bookID, relative)
		if resolveErr != nil {
			return resolveErr
		}
		imageTarget := filepath.Join(destination, filepath.FromSlash(target))
		if err = resource.ConvertImageToWebP(imageSource, imageTarget); err != nil {
			return fmt.Errorf("convert image %s: %w", relative, err)
		}
	}
	for _, name := range []string{"tts", "audio"} {
		if err = copyAudioFiles(source, destination, name); err != nil {
			return err
		}
	}
	if err = writeManifest(destination, data); err != nil {
		return err
	}
	_, err = Validate(destination)
	return err
}

func Validate(root string) (Bundle, error) {
	var data Bundle
	if err := validateAsset(root, "metadata/book.json"); err != nil {
		return data, err
	}
	raw, err := os.ReadFile(filepath.Join(root, "metadata", "book.json"))
	if err != nil {
		return data, err
	}
	if err = json.Unmarshal(raw, &data); err != nil {
		return data, fmt.Errorf("decode bundle manifest: %w", err)
	}
	if data.SchemaVersion != 2 || !resource.ValidBookID(data.Book.BookID) || strings.TrimSpace(data.Book.Title) == "" || len(data.Pages) == 0 {
		return data, errors.New("invalid bundle schema, book_id, title or pages")
	}
	if data.Book.Cover != "" {
		if err = validateAsset(root, data.Book.Cover); err != nil {
			return data, fmt.Errorf("cover: %w", err)
		}
	}
	positions := map[int]bool{}
	for _, page := range data.Pages {
		if page.Position < 1 || positions[page.Position] {
			return data, fmt.Errorf("duplicate or invalid page %d", page.Position)
		}
		positions[page.Position] = true
		for _, relative := range []string{page.Image, page.Content} {
			if err = validateAsset(root, relative); err != nil {
				return data, fmt.Errorf("page %d: %w", page.Position, err)
			}
		}
		content, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(page.Content)))
		if readErr != nil || !json.Valid(content) {
			return data, fmt.Errorf("page %d content is not valid JSON", page.Position)
		}
		var document struct {
			Segments []json.RawMessage `json:"segments"`
		}
		if json.Unmarshal(content, &document) != nil || document.Segments == nil {
			return data, fmt.Errorf("page %d content must contain a segments array", page.Position)
		}
	}
	for page := 1; page <= len(data.Pages); page++ {
		if !positions[page] {
			return data, fmt.Errorf("missing page %d", page)
		}
	}
	for _, name := range []string{"tts", "audio"} {
		if err = validateAudio(root, name, positions); err != nil {
			return data, err
		}
	}
	return data, nil
}

func Import(db *gorm.DB, resources *resource.Manager, source string) (string, string, error) {
	data, err := Validate(source)
	if err != nil {
		return "", "", err
	}
	root := resources.Root()
	stage, err := os.MkdirTemp(root, ".import-stage-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(stage)
	assets := map[string]bool{}
	images := map[string]string{}
	if data.Book.Cover != "" {
		original := data.Book.Cover
		data.Book.Cover = resource.WebPPath(original)
		images[data.Book.Cover] = original
	}
	for index := range data.Pages {
		original := data.Pages[index].Image
		data.Pages[index].Image = resource.WebPPath(original)
		images[data.Pages[index].Image] = original
		assets[data.Pages[index].Content] = true
	}
	for relative := range assets {
		if err = copyAsset(source, stage, relative); err != nil {
			return "", "", err
		}
	}
	for target, relative := range images {
		if err = resource.ConvertImageToWebP(
			filepath.Join(source, filepath.FromSlash(relative)),
			filepath.Join(stage, filepath.FromSlash(target)),
		); err != nil {
			return "", "", fmt.Errorf("convert image %s: %w", relative, err)
		}
	}
	for _, name := range []string{"tts", "audio"} {
		if err = copyAudioFiles(source, stage, name); err != nil {
			return "", "", err
		}
	}
	if err = writeManifest(stage, data); err != nil {
		return "", "", err
	}
	if _, err = Validate(stage); err != nil {
		return "", "", err
	}
	target, err := resources.BookRoot(data.Book.BookID)
	if err != nil {
		return "", "", err
	}
	backup, err := os.MkdirTemp(root, ".import-backup-")
	if err != nil {
		return "", "", err
	}
	old := filepath.Join(backup, data.Book.BookID)
	previous := false
	if _, statErr := os.Stat(target); statErr == nil {
		if err = os.Rename(target, old); err != nil {
			_ = os.Remove(backup)
			return "", "", err
		}
		previous = true
	} else if !os.IsNotExist(statErr) {
		_ = os.Remove(backup)
		return "", "", statErr
	}
	if err = os.Rename(stage, target); err != nil {
		if previous {
			_ = os.Rename(old, target)
		}
		return "", "", err
	}
	err = db.Transaction(func(tx *gorm.DB) error { return upsert(tx, data) })
	if err != nil {
		// Retain the failed new version if restoring the old directory fails.
		failed := filepath.Join(backup, "failed-new")
		if moveErr := os.Rename(target, failed); moveErr != nil {
			return "", backup, fmt.Errorf("database: %w; could not move new files for rollback: %v", err, moveErr)
		}
		if previous {
			if restoreErr := os.Rename(old, target); restoreErr != nil {
				return "", backup, fmt.Errorf("database: %w; restore old files from %s: %v", err, old, restoreErr)
			}
		}
		return "", backup, fmt.Errorf("database: %w (failed files retained in %s)", err, failed)
	}
	if !previous {
		_ = os.Remove(backup)
		backup = ""
	}
	return data.Book.BookID, backup, nil
}

func upsert(tx *gorm.DB, data Bundle) error {
	var existing model.Book
	err := tx.Where("book_id = ?", data.Book.BookID).First(&existing).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	fields := map[string]any{
		"title": data.Book.Title, "subtitle": data.Book.Subtitle,
		"description": data.Book.Description, "publisher": data.Book.Publisher,
		"grade": data.Book.Grade, "semester": data.Book.Semester,
		"cover": data.Book.Cover, "sort": data.Book.Sort,
		"status": "published", "page_count": len(data.Pages),
		"american_enabled":     data.Book.AmericanEnabled,
		"british_enabled":      data.Book.BritishEnabled,
		"american_voice_id":    data.Book.AmericanVoiceID,
		"british_voice_id":     data.Book.BritishVoiceID,
		"audio_config_version": 1,
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		book := model.Book{BookID: data.Book.BookID}
		if err = tx.Create(&book).Error; err != nil {
			return err
		}
	} else {
		fields["revision"] = gorm.Expr("revision + 1")
	}
	if err = tx.Model(&model.Book{}).Where("book_id = ?", data.Book.BookID).Updates(fields).Error; err != nil {
		return err
	}
	if err = tx.Where("book_id = ?", data.Book.BookID).Delete(&model.BookPage{}).Error; err != nil {
		return err
	}
	for _, input := range data.Pages {
		page := model.BookPage{BookID: data.Book.BookID, Position: input.Position,
			PrintedPage: input.PrintedPage, Title: input.Title, Unit: input.Unit,
			ImagePath: input.Image, ContentPath: input.Content,
			Interactive: input.Interactive, Preview: input.Preview}
		if err = tx.Create(&page).Error; err != nil {
			return err
		}
	}
	return nil
}

func writeManifest(root string, data Bundle) error {
	path := filepath.Join(root, "metadata", "book.json")
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0640)
}

func validateAsset(root, relative string) error {
	if relative == "" || filepath.IsAbs(relative) || strings.Contains(relative, "\\") {
		return fmt.Errorf("invalid resource path %q", relative)
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("resource path escapes bundle: %q", relative)
	}
	path := filepath.Join(root, clean)
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resource missing %q: %w", relative, err)
	}
	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	within, err := filepath.Rel(base, resolved)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return fmt.Errorf("resource escapes bundle: %q", relative)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("resource missing or empty %q", relative)
	}
	return nil
}

func copyAsset(source, target, relative string) error {
	if err := validateAsset(source, relative); err != nil {
		return err
	}
	return copyFile(filepath.Join(source, filepath.FromSlash(relative)), filepath.Join(target, filepath.FromSlash(relative)))
}

func copyFile(source, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0750); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func copyAudioFiles(source, target, name string) error {
	manifestPath := filepath.Join(source, name, "manifest.json")
	_, err := os.Lstat(manifestPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = validateAsset(source, name+"/manifest.json"); err != nil {
		return err
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var manifest struct {
		SchemaVersion int `json:"schema_version"`
		Items         []struct {
			File string `json:"file"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &manifest) != nil || manifest.SchemaVersion != 2 {
		return fmt.Errorf("invalid %s audio manifest", name)
	}
	if err = copyAsset(source, target, name+"/manifest.json"); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, item := range manifest.Items {
		if seen[item.File] {
			continue
		}
		seen[item.File] = true
		if err = validateAsset(filepath.Join(source, name), item.File); err != nil {
			return fmt.Errorf("%s audio resource: %w", name, err)
		}
		if err = copyAsset(source, target, name+"/"+item.File); err != nil {
			return err
		}
	}
	return nil
}

func validateAudio(root, name string, pages map[int]bool) error {
	path := filepath.Join(root, name, "manifest.json")
	_, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = validateAsset(root, name+"/manifest.json"); err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var manifest struct {
		SchemaVersion int `json:"schema_version"`
		Items         []struct {
			Page   int    `json:"page"`
			ItemID string `json:"item_id"`
			Accent string `json:"accent"`
			File   string `json:"file"`
		} `json:"items"`
	}
	if err = json.Unmarshal(raw, &manifest); err != nil || manifest.SchemaVersion != 2 {
		return fmt.Errorf("invalid %s audio manifest", name)
	}
	for _, item := range manifest.Items {
		if !pages[item.Page] || item.ItemID == "" || (item.Accent != "en-US" && item.Accent != "en-GB") {
			return fmt.Errorf("invalid %s audio item", name)
		}
		if err = validateAsset(filepath.Join(root, name), item.File); err != nil {
			return fmt.Errorf("%s audio item %s: %w", name, item.ItemID, err)
		}
	}
	return nil
}
