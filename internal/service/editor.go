package service

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"xiaov2/internal/ai"
	"xiaov2/internal/config"
	"xiaov2/internal/model"
	"xiaov2/internal/resource"
	"xiaov2/internal/tts"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type EditorService struct {
	db        *gorm.DB
	cfg       config.Config
	resources *resource.Manager
	ocrMu     sync.Mutex
	ocrCmd    *exec.Cmd
	ocrIn     io.WriteCloser
	ocrOut    *bufio.Scanner
	audioMu   sync.Mutex
	// audioProcessMu protects the long-lived daemon handles.  It is separate
	// from audioMu so an administrator can terminate a stuck daemon while the
	// worker goroutine is blocked waiting for its output.
	audioProcessMu   sync.Mutex
	audioCmd         *exec.Cmd
	audioIn          io.WriteCloser
	audioOut         *bufio.Scanner
	audioDaemonModel string

	translationMu          sync.Mutex
	translationProcessMu   sync.Mutex
	translationCmd         *exec.Cmd
	translationIn          io.WriteCloser
	translationOut         *bufio.Scanner
	translationDaemonModel string

	// audioRestartMu closes the small gap between claiming an audio job and
	// registering it as the in-process job.  Without it, a restart request
	// could clear files while the old worker was just about to start writing.
	audioRestartMu sync.Mutex
	audioJobMu     sync.Mutex
	audioJobID     uint64
	audioJobDone   chan struct{}
}

func NewEditorService(db *gorm.DB, cfg config.Config, r *resource.Manager) *EditorService {
	return &EditorService{db: db, cfg: cfg, resources: r}
}

const (
	daemonInactivityTimeout   = 15 * time.Minute
	daemonGracefulStopTimeout = 10 * time.Second
)

type daemonScanResult struct {
	line []byte
	err  error
}

type localTranslationIssuesError struct {
	Count int
}

func (e localTranslationIssuesError) Error() string {
	return fmt.Sprintf("%d 条本地翻译结果需要人工审核", e.Count)
}


func scanDaemonLine(ctx context.Context, scanner *bufio.Scanner, stop func(), name string) ([]byte, error) {
	result := make(chan daemonScanResult, 1)
	go func() {
		if scanner.Scan() {
			result <- daemonScanResult{line: append([]byte(nil), scanner.Bytes()...)}
			return
		}
		if err := scanner.Err(); err != nil {
			result <- daemonScanResult{err: err}
			return
		}
		result <- daemonScanResult{err: io.EOF}
	}()

	timer := time.NewTimer(daemonInactivityTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		stop()
		return nil, ctx.Err()
	case <-timer.C:
		stop()
		return nil, fmt.Errorf("%s daemon produced no output for %s", name, daemonInactivityTimeout)
	case scanned := <-result:
		return scanned.line, scanned.err
	}
}

type DraftSummary struct {
	model.TextbookDraft
	PageCount int64 `json:"page_count"`
}
type DraftDetail struct {
	Draft model.TextbookDraft          `json:"draft"`
	Pages []model.TextbookDraftPage    `json:"pages"`
	Jobs  []model.TextbookJob          `json:"jobs"`
	Audio []resource.AudioAccentStatus `json:"audio"`
}

type AudioSettings struct {
	AmericanEnabled bool   `json:"american_enabled"`
	BritishEnabled  bool   `json:"british_enabled"`
	AmericanVoiceID string `json:"american_voice_id"`
	BritishVoiceID  string `json:"british_voice_id"`
}

func DefaultAudioSettings() AudioSettings {
	return AudioSettings{AmericanEnabled: true, BritishEnabled: true, AmericanVoiceID: tts.DefaultAmericanVoice, BritishVoiceID: tts.DefaultBritishVoice}
}

func normalizeAudioSettings(value AudioSettings) AudioSettings {
	value.AmericanVoiceID = tts.DefaultAmericanVoice
	value.BritishVoiceID = tts.DefaultBritishVoice
	return value
}

func draftAudioConfig(d model.TextbookDraft) tts.Config {
	return tts.Config{AmericanEnabled: d.AmericanEnabled, BritishEnabled: d.BritishEnabled, AmericanVoiceID: tts.DefaultAmericanVoice, BritishVoiceID: tts.DefaultBritishVoice}
}

func (s *EditorService) currentModelSettings(ctx context.Context) (ai.Settings, error) {
	settings := ai.DefaultSettings()
	var rows []model.SiteSetting
	if e := s.db.WithContext(ctx).Where("dict_code IN ? AND status=1", []string{"ai.ocr_model", "ai.translation_model", "ai.tts_model", "ai.tts_voice"}).Order("id").Find(&rows).Error; e != nil {
		return settings, e
	}
	for _, row := range rows {
		switch row.DictCode {
		case "ai.ocr_model":
			settings.OCRModel = row.ItemValue
		case "ai.translation_model":
			settings.TranslationModel = row.ItemValue
		case "ai.tts_model":
			settings.TTSModel = row.ItemValue
		case "ai.tts_voice":
			settings.TTSVoice = row.ItemValue
		}
	}
	settings = ai.NormalizeSettings(settings)
	if item, ok := ai.Find(settings.OCRModel); !ok || !item.Enabled {
		settings.OCRModel = ai.LocalOCRModel
	}
	if item, ok := ai.Find(settings.TranslationModel); !ok || !item.Enabled {
		settings.TranslationModel = ai.CloudTranslationModel
	}
	if item, ok := ai.Find(settings.TTSModel); !ok || !item.Enabled || !ai.ValidVoice(settings.TTSModel, settings.TTSVoice) {
		settings.TTSModel, settings.TTSVoice = ai.LocalTTSModel, "aiden"
	}
	return settings, nil
}

func draftModelSettings(d model.TextbookDraft) ai.Settings {
	return ai.NormalizeSettings(ai.Settings{OCRModel: d.OCRModel, TranslationModel: d.TranslationModel, TTSModel: d.TTSModel, TTSVoice: d.TTSVoice})
}

func pageModelSettings(d model.TextbookDraft, p model.TextbookDraftPage) ai.Settings {
	settings := draftModelSettings(d)
	if p.OCRModel != "" {
		settings.OCRModel = p.OCRModel
	}
	if p.TranslationModel != "" {
		settings.TranslationModel = p.TranslationModel
	}
	if p.TTSModel != "" {
		settings.TTSModel = p.TTSModel
	}
	if p.TTSVoice != "" {
		settings.TTSVoice = p.TTSVoice
	}
	return ai.NormalizeSettings(settings)
}

func pageIsLocked(p model.TextbookDraftPage) bool { return p.Checked && p.AudioChecked }

// Translation provenance is locked as soon as the text stage is confirmed.
// Before that, a page follows the current draft translation default; the page
// snapshot is written only after a translation job actually succeeds.
func pageTranslationSettings(d model.TextbookDraft, p model.TextbookDraftPage) ai.Settings {
	if p.Checked {
		return pageModelSettings(d, p)
	}
	return draftModelSettings(d)
}

// Unreviewed pages follow the draft default. Fully confirmed pages stay pinned
// to their historical model snapshot so a later draft-level switch is safe.
func pageAudioSettings(d model.TextbookDraft, p model.TextbookDraftPage) ai.Settings {
	if pageIsLocked(p) {
		return pageModelSettings(d, p)
	}
	return draftModelSettings(d)
}

func jobModel(d model.TextbookDraft, kind string) ai.Model {
	settings := draftModelSettings(d)
	id := settings.OCRModel
	if kind == "translate" {
		id = settings.TranslationModel
	} else if strings.HasPrefix(kind, "audio") {
		id = settings.TTSModel
	}
	item, ok := ai.Find(id)
	if !ok {
		item, _ = ai.Find(ai.LocalOCRModel)
		if kind == "translate" {
			item, _ = ai.Find(ai.CloudTranslationModel)
		} else if strings.HasPrefix(kind, "audio") {
			item, _ = ai.Find(ai.LocalTTSModel)
		}
	}
	return item
}

func draftVoices(d model.TextbookDraft) map[string]string {
	return settingsVoices(d, draftModelSettings(d))
}

func settingsVoices(d model.TextbookDraft, settings ai.Settings) map[string]string {
	config := draftAudioConfig(d)
	if settings.TTSModel != ai.CloudTTSModel {
		return config.EnabledVoices()
	}
	voice := settings.TTSVoice
	result := make(map[string]string, 2)
	if config.AmericanEnabled {
		result[tts.AccentUS] = voice
	}
	if config.BritishEnabled {
		result[tts.AccentGB] = voice
	}
	return result
}

type DraftPageView struct {
	Content      map[string]any `json:"content"`
	Title        string         `json:"title"`
	Unit         string         `json:"unit"`
	Version      uint64         `json:"version"`
	Preview      bool           `json:"preview"`
	Checked      bool           `json:"checked"`
	AudioChecked bool           `json:"audio_checked"`
	AudioReady   bool           `json:"audio_ready"`
	Image        string         `json:"image"`
	Issues       []string       `json:"issues"`
}


func clonePageContent(content map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(content)
	if err != nil {
		return nil, err
	}
	var cloned map[string]any
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return nil, err
	}
	return cloned, nil
}

// stripTranslationFields keeps textbook_draft_pages.content limited to OCR
// structure, coordinates and audio metadata. Translation data is owned only by
// textbook_translation_items.
func stripTranslationFields(content map[string]any) {
	segments, _ := content["segments"].([]any)
	for _, rawSegment := range segments {
		segment, ok := rawSegment.(map[string]any)
		if !ok {
			continue
		}
		delete(segment, "translation")
		delete(segment, "context")
		delete(segment, "paragraph")
		delete(segment, "block_text")
		delete(segment, "block")
		words, _ := segment["words"].([]any)
		for _, rawWord := range words {
			word, ok := rawWord.(map[string]any)
			if !ok {
				continue
			}
			delete(word, "meaning")
			delete(word, "phonetic")
		}
	}
}

func nullableTranslation(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func translationStatus(translation, meaning, phonetic string) string {
	if strings.TrimSpace(translation) != "" || strings.TrimSpace(meaning) != "" || strings.TrimSpace(phonetic) != "" {
		return "translated"
	}
	return "pending"
}

func translationItemsFromContent(draftID string, page int, sourceVersion uint64, content map[string]any, modelID, provider string) ([]model.TextbookTranslationItem, error) {
	segments, ok := content["segments"].([]any)
	if !ok {
		return nil, bad("页面缺少 segments")
	}
	items := make([]model.TextbookTranslationItem, 0)
	for segmentIndex, rawSegment := range segments {
		segment, ok := rawSegment.(map[string]any)
		if !ok {
			continue
		}
		segmentID, _ := segment["id"].(string)
		segmentID = strings.TrimSpace(segmentID)
		if segmentID == "" {
			segmentID = fmt.Sprintf("p%d-s%d", page, segmentIndex)
		}
		sourceText, _ := segment["text"].(string)
		translation, _ := segment["translation"].(string)
		items = append(items, model.TextbookTranslationItem{
			DraftID: draftID, Page: uint32(page), ItemID: segmentID, SegmentID: segmentID,
			ItemType: "sentence", WordIndex: 0, SourceText: sourceText,
			Translation: nullableTranslation(translation), Meaning: "", Phonetic: "",
			TranslationModel: modelID, Provider: provider,
			Status: translationStatus(translation, "", ""), SourcePageVersion: sourceVersion, Revision: 1,
		})
		words, _ := segment["words"].([]any)
		for wordIndex, rawWord := range words {
			word, ok := rawWord.(map[string]any)
			if !ok {
				continue
			}
			wordID, _ := word["id"].(string)
			wordID = strings.TrimSpace(wordID)
			if wordID == "" {
				wordID = fmt.Sprintf("%s-w%d", segmentID, wordIndex)
			}
			wordText, _ := word["text"].(string)
			meaning, _ := word["meaning"].(string)
			phonetic, _ := word["phonetic"].(string)
			items = append(items, model.TextbookTranslationItem{
				DraftID: draftID, Page: uint32(page), ItemID: wordID, SegmentID: segmentID,
				ItemType: "word", WordIndex: uint32(wordIndex), SourceText: wordText,
				Translation: nil, Meaning: strings.TrimSpace(meaning), Phonetic: strings.TrimSpace(phonetic),
				TranslationModel: modelID, Provider: provider,
				Status: translationStatus("", meaning, phonetic), SourcePageVersion: sourceVersion, Revision: 1,
			})
		}
	}
	return items, nil
}

func syncTranslationItems(tx *gorm.DB, draftID string, page int, sourceVersion uint64, content map[string]any, modelID, provider string) error {
	items, err := translationItemsFromContent(draftID, page, sourceVersion, content, modelID, provider)
	if err != nil {
		return err
	}
	if err := tx.Where("draft_id=? AND page=?", draftID, page).Delete(&model.TextbookTranslationItem{}).Error; err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	return tx.Create(&items).Error
}

func (s *EditorService) hydrateTranslationItems(ctx context.Context, draftID string, page int, content map[string]any) error {
	stripTranslationFields(content)
	var items []model.TextbookTranslationItem
	if err := s.db.WithContext(ctx).Where("draft_id=? AND page=?", draftID, page).Order("id").Find(&items).Error; err != nil {
		return err
	}
	byID := make(map[string]model.TextbookTranslationItem, len(items))
	for _, item := range items {
		byID[item.ItemID] = item
	}
	segments, _ := content["segments"].([]any)
	for _, rawSegment := range segments {
		segment, ok := rawSegment.(map[string]any)
		if !ok {
			continue
		}
		segmentID, _ := segment["id"].(string)
		if item, found := byID[segmentID]; found && item.ItemType == "sentence" && item.Translation != nil {
			segment["translation"] = *item.Translation
		} else {
			segment["translation"] = ""
		}
		words, _ := segment["words"].([]any)
		for _, rawWord := range words {
			word, ok := rawWord.(map[string]any)
			if !ok {
				continue
			}
			wordID, _ := word["id"].(string)
			if item, found := byID[wordID]; found && item.ItemType == "word" {
				word["meaning"] = item.Meaning
				word["phonetic"] = item.Phonetic
			} else {
				word["meaning"] = ""
				word["phonetic"] = ""
			}
		}
	}
	return nil
}

func (s *EditorService) hydratedPageContent(ctx context.Context, draftID string, page int, raw string) (map[string]any, error) {
	var content map[string]any
	if err := json.Unmarshal([]byte(raw), &content); err != nil {
		return nil, err
	}
	if err := s.hydrateTranslationItems(ctx, draftID, page, content); err != nil {
		return nil, err
	}
	return content, nil
}

// Keep draft uploads aligned with the canonical resource book_id protocol.
// In particular, underscore is valid (for example: grade_4_up).
var draftBookID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,79}$`)

func draftID() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return hex.EncodeToString(b), nil
}
func (s *EditorService) List(ctx context.Context) ([]DraftSummary, error) {
	var out []DraftSummary
	e := s.db.WithContext(ctx).Table("textbook_drafts d").Select("d.*,(SELECT COUNT(*) FROM textbook_draft_pages p WHERE p.draft_id=d.id) page_count").Order("d.created_at DESC").Limit(200).Scan(&out).Error
	return out, e
}

type AdminBook struct {
	model.Book
	AudioStatus []resource.AudioAccentStatus `json:"audio_status"`
}

func (s *EditorService) Books(ctx context.Context) ([]AdminBook, error) {
	var books []model.Book
	if e := s.db.WithContext(ctx).Order("sort,book_id").Find(&books).Error; e != nil {
		return nil, e
	}
	out := make([]AdminBook, 0, len(books))
	for _, book := range books {
		out = append(out, AdminBook{Book: book, AudioStatus: s.publishedAudioStatus(ctx, book)})
	}
	return out, nil
}
func (s *EditorService) SaveBook(ctx context.Context, id, title, grade, semester, publisher, status string, revision, actor uint64) error {
	if !resource.ValidBookID(id) || strings.TrimSpace(title) == "" || (status != "draft" && status != "published") {
		return bad("教材信息不完整")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.Book{}).Where("book_id=? AND revision=?", id, revision).Updates(map[string]any{"title": title, "grade": grade, "semester": semester, "publisher": publisher, "status": status, "revision": gorm.Expr("revision+1")})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return conflict("教材已被修改，请刷新重试")
		}
		return editorAudit(tx, actor, "book.save", id, map[string]string{"status": status})
	})
}
func (s *EditorService) PublishedPages(ctx context.Context, book string) ([]model.BookPage, error) {
	var out []model.BookPage
	e := s.db.WithContext(ctx).Where("book_id=?", book).Order("position").Find(&out).Error
	for i := range out {
		out[i].ImagePath = ""
		out[i].ContentPath = ""
	}
	return out, e
}
func (s *EditorService) PublishedPage(ctx context.Context, book string, pos int) (map[string]any, error) {
	var p model.BookPage
	if e := s.db.WithContext(ctx).Where("book_id=? AND position=?", book, pos).First(&p).Error; e != nil {
		return nil, e
	}
	path, e := s.resources.Resolve(book, p.ContentPath)
	if e != nil {
		return nil, e
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	var data map[string]any
	if e = json.Unmarshal(raw, &data); e != nil {
		return nil, e
	}
	data["book_id"], data["page"], data["title"], data["unit"], data["preview"], data["version"] = book, pos, p.Title, p.Unit, p.Preview, p.Version
	data["image"] = fmt.Sprintf("/api/v1/admin/books/%s/pages/%d/image", book, pos)
	return data, nil
}
func (s *EditorService) CreateUpload(ctx context.Context, book, title string, grade int, term, edition, source string, actor uint64, audio AudioSettings) (string, error) {
	if !draftBookID.MatchString(book) || strings.TrimSpace(title) == "" || grade < 1 || grade > 6 || (term != "上册" && term != "下册") || len(title) > 200 || len(edition) > 200 {
		return "", bad("请填写有效教材标识、名称、年级和学期")
	}
	audio = normalizeAudioSettings(audio)
	models, e := s.currentModelSettings(ctx)
	if e != nil {
		return "", e
	}
	id, e := draftID()
	if e != nil {
		return "", e
	}
	dir := filepath.Join(s.cfg.EditorRoot, id)
	if e = os.MkdirAll(dir, 0750); e != nil {
		return "", e
	}
	if e = copyFile(source, filepath.Join(dir, "source.pdf")); e != nil {
		return "", e
	}
	d := model.TextbookDraft{ID: id, BookID: book, Title: title, Grade: grade, Term: term, Edition: edition, AmericanEnabled: audio.AmericanEnabled, BritishEnabled: audio.BritishEnabled, AmericanVoiceID: audio.AmericanVoiceID, BritishVoiceID: audio.BritishVoiceID, AudioConfigVersion: 1, OCRModel: models.OCRModel, TranslationModel: models.TranslationModel, TTSModel: models.TTSModel, TTSVoice: models.TTSVoice, Status: "queued", CreatedBy: actor, UpdatedBy: actor}
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Create(&d).Error; e != nil {
			return e
		}
		if e := tx.Model(&d).Updates(map[string]any{"american_enabled": audio.AmericanEnabled, "british_enabled": audio.BritishEnabled, "american_voice_id": audio.AmericanVoiceID, "british_voice_id": audio.BritishVoiceID, "audio_config_version": 1}).Error; e != nil {
			return e
		}
		// An upload begins with OCR for page one only. Audio and later pages are
		// independently queued after their respective review gates.
		if e := tx.Create(&model.TextbookJob{DraftID: id, Kind: "ocr", Page: 1, Status: "queued"}).Error; e != nil {
			return e
		}
		return editorAudit(tx, actor, "draft.upload", id, map[string]any{"book_id": book})
	})
	if e != nil {
		os.RemoveAll(dir)
		return "", e
	}
	return id, nil
}
func (s *EditorService) CopyPublished(ctx context.Context, book string, actor uint64) (string, error) {
	id, e := draftID()
	if e != nil {
		return "", e
	}
	models, e := s.currentModelSettings(ctx)
	if e != nil {
		return "", e
	}
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var b model.Book
		if e := tx.Where("book_id=?", book).First(&b).Error; e != nil {
			return e
		}
		grade := 0
		fmt.Sscan(b.Grade, &grade)
		americanVoice, britishVoice := tts.DefaultAmericanVoice, tts.DefaultBritishVoice
		if b.AudioConfigVersion == 0 {
			b.AmericanEnabled, b.BritishEnabled = true, true
		}
		d := model.TextbookDraft{ID: id, BookID: book, Title: b.Title, Grade: grade, Term: b.Semester, Edition: b.Publisher, AmericanEnabled: b.AmericanEnabled, BritishEnabled: b.BritishEnabled, AmericanVoiceID: americanVoice, BritishVoiceID: britishVoice, AudioConfigVersion: 1, OCRModel: models.OCRModel, TranslationModel: models.TranslationModel, TTSModel: models.TTSModel, TTSVoice: models.TTSVoice, Status: "draft", CreatedBy: actor, UpdatedBy: actor}
		if e := tx.Create(&d).Error; e != nil {
			return e
		}
		if e := tx.Model(&d).Updates(map[string]any{"american_enabled": b.AmericanEnabled, "british_enabled": b.BritishEnabled, "american_voice_id": americanVoice, "british_voice_id": britishVoice, "audio_config_version": 1}).Error; e != nil {
			return e
		}
		var pages []model.BookPage
		if e := tx.Where("book_id=?", book).Order("position").Find(&pages).Error; e != nil {
			return e
		}
		d.SourcePageCount = len(pages)
		if e := tx.Model(&d).Update("source_page_count", d.SourcePageCount).Error; e != nil {
			return e
		}
		for _, p := range pages {
			path, e := s.resources.Resolve(book, p.ContentPath)
			if e != nil {
				return e
			}
			raw, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			var publishedContent map[string]any
			if e := json.Unmarshal(raw, &publishedContent); e != nil {
				return e
			}
			storageContent, e := clonePageContent(publishedContent)
			if e != nil {
				return e
			}
			stripTranslationFields(storageContent)
			storageRaw, e := json.Marshal(storageContent)
			if e != nil {
				return e
			}
			dp := model.TextbookDraftPage{DraftID: id, Position: p.Position, PrintedPage: p.PrintedPage, Title: p.Title, Unit: p.Unit, ImagePath: "published:" + p.ImagePath, Content: string(storageRaw), Preview: p.Preview, Checked: true, OCRModel: models.OCRModel, TranslationModel: models.TranslationModel, TTSModel: models.TTSModel, TTSVoice: models.TTSVoice}
			if e = tx.Create(&dp).Error; e != nil {
				return e
			}
			translationInfo, _ := ai.Find(models.TranslationModel)
			if e = syncTranslationItems(tx, id, p.Position, 1, publishedContent, models.TranslationModel, translationInfo.Provider); e != nil {
				return e
			}
		}
		workBook := filepath.Join(s.cfg.EditorRoot, id, "work", book)
		if e := os.MkdirAll(filepath.Join(workBook, "metadata", "pages"), 0750); e != nil {
			return e
		}
		for _, p := range pages {
			var copied model.TextbookDraftPage
			if e := tx.Where("draft_id=? AND position=?", id, p.Position).First(&copied).Error; e != nil {
				return e
			}
			if e := os.WriteFile(filepath.Join(workBook, "metadata", "pages", fmt.Sprintf("page-%03d.json", p.Position)), []byte(copied.Content), 0640); e != nil {
				return e
			}
		}
		if liveTTS, dirErr := s.resources.Dir(book, "tts"); dirErr == nil && fileExists(liveTTS) {
			if e := copyTree(liveTTS, filepath.Join(workBook, "tts")); e != nil {
				return e
			}
		}
		var copiedPages []model.TextbookDraftPage
		if e := tx.Where("draft_id=?", id).Find(&copiedPages).Error; e != nil {
			return e
		}
		for _, page := range copiedPages {
			audioChecked := !hasAudioItems(page.Content)
			if !audioChecked && len(draftVoices(d)) > 0 {
				audioChecked = !s.missingDraftAudio(d, page)
			}
			if e := tx.Model(&page).Update("audio_checked", audioChecked).Error; e != nil {
				return e
			}
		}
		return editorAudit(tx, actor, "draft.copy", id, map[string]any{"book_id": book, "revision": b.Revision})
	})
	if e == nil {
		e = s.ensureDraftAudioState(ctx, id)
	}
	return id, e
}
func (s *EditorService) Get(ctx context.Context, id string) (DraftDetail, error) {
	out := DraftDetail{
		Pages: make([]model.TextbookDraftPage, 0),
		Jobs:  make([]model.TextbookJob, 0),
	}
	if e := s.db.WithContext(ctx).First(&out.Draft, "id=?", id).Error; e != nil {
		return out, e
	}
	if e := s.db.WithContext(ctx).Select("id,draft_id,position,printed_page,title,unit,preview,checked,audio_checked,ocr_model,tts_model,tts_voice,version,updated_at").Where("draft_id=?", id).Order("position").Find(&out.Pages).Error; e != nil {
		return out, e
	}
	e := s.db.WithContext(ctx).Where("draft_id=?", id).Order("id DESC").Limit(100).Find(&out.Jobs).Error
	if e == nil {
		out.Audio, e = s.draftAudioStatusDB(ctx, out.Draft)
	}
	return out, e
}

// Delete removes an unpublished draft and all of its conversion jobs/pages.
// Running jobs are protected from deletion so a worker cannot continue writing
// into a draft after the database record has been removed.
func (s *EditorService) Delete(ctx context.Context, id string, actor uint64) error {
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var d model.TextbookDraft
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&d, "id=?", id).Error; e != nil {
			return e
		}
		if d.Status == "published" {
			return conflict("已发布教材不能删除，请先创建新的草稿")
		}
		var jobs []model.TextbookJob
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("draft_id=?", id).Find(&jobs).Error; e != nil {
			return e
		}
		for _, job := range jobs {
			if job.Status == "running" {
				return conflict("转换任务正在执行，请等待任务结束后再删除")
			}
		}
		if e := editorAudit(tx, actor, "draft.delete", id, map[string]string{"book_id": d.BookID, "status": d.Status}); e != nil {
			return e
		}
		if e := tx.Where("draft_id=?", id).Delete(&model.TextbookJob{}).Error; e != nil {
			return e
		}
		itemIDs := tx.Model(&model.TextbookAudioItem{}).Select("id").Where("draft_id=?", id)
		if e := tx.Where("audio_item_id IN (?)", itemIDs).Delete(&model.TextbookAudioAttempt{}).Error; e != nil {
			return e
		}
		if e := tx.Where("draft_id=?", id).Delete(&model.TextbookAudioItem{}).Error; e != nil {
			return e
		}
		if e := tx.Where("draft_id=?", id).Delete(&model.TextbookTranslationItem{}).Error; e != nil {
			return e
		}
		if e := tx.Where("draft_id=?", id).Delete(&model.TextbookDraftPage{}).Error; e != nil {
			return e
		}
		return tx.Delete(&d).Error
	})
	if e != nil {
		return e
	}
	return os.RemoveAll(filepath.Join(s.cfg.EditorRoot, id))
}

func (s *EditorService) Page(ctx context.Context, id string, pos int) (DraftPageView, error) {
	var d model.TextbookDraft
	if e := s.db.WithContext(ctx).First(&d, "id=?", id).Error; e != nil {
		return DraftPageView{}, e
	}
	var p model.TextbookDraftPage
	if e := s.db.WithContext(ctx).Where("draft_id=? AND position=?", id, pos).First(&p).Error; e != nil {
		return DraftPageView{}, e
	}
	content, e := s.hydratedPageContent(ctx, id, pos, p.Content)
	if e != nil {
		return DraftPageView{}, e
	}
	audioReady := !hasAudioItems(p.Content)
	if !audioReady {
		ready, err := s.pageAudioReadyDB(ctx, d, pos)
		if err != nil {
			return DraftPageView{}, err
		}
		audioReady = ready
	}
	return DraftPageView{Content: content, Title: p.Title, Unit: p.Unit, Version: p.Version, Preview: p.Preview, Checked: p.Checked, AudioChecked: p.AudioChecked, AudioReady: audioReady, Image: fmt.Sprintf("/api/v1/admin/drafts/%s/pages/%d/image", id, pos), Issues: publicationIssues(content)}, nil
}
func (s *EditorService) Image(ctx context.Context, id string, pos int) (string, error) {
	var p model.TextbookDraftPage
	if e := s.db.WithContext(ctx).Select("image_path").Where("draft_id=? AND position=?", id, pos).First(&p).Error; e != nil {
		return "", e
	}
	return s.resolveDraftImage(id, p.ImagePath)
}
func (s *EditorService) Audio(ctx context.Context, id string, pos int, item, accent string) (string, error) {
	if accent != "en-US" && accent != "en-GB" {
		return "", bad("口音无效")
	}
	var p model.TextbookDraftPage
	if e := s.db.WithContext(ctx).Where("draft_id=? AND position=?", id, pos).First(&p).Error; e != nil {
		return "", e
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
	if e := json.Unmarshal([]byte(p.Content), &content); e != nil {
		return "", e
	}
	found := false
	for _, seg := range content.Segments {
		if seg.ID == item && seg.AudioMode != "word_only" && seg.AudioMode != "none" {
			found = true
		}
		for _, w := range seg.Words {
			if w.ID == item && seg.AudioMode != "none" {
				found = true
			}
		}
	}
	if !found {
		return "", gorm.ErrRecordNotFound
	}
	if e := s.ensureDraftAudioState(ctx, id); e != nil {
		return "", e
	}
	var state model.TextbookAudioItem
	if e := s.db.WithContext(ctx).Where("draft_id=? AND page=? AND item_id=? AND accent=? AND active=1 AND status='ready'", id, pos, item, accent).First(&state).Error; e == nil && state.AudioPath != "" {
		path, pathErr := s.resolveAudioStatePath(id, state.AudioPath)
		if pathErr == nil {
			if info, statErr := os.Stat(path); statErr == nil && info.Mode().IsRegular() {
				return path, nil
			}
		}
	}
	var d model.TextbookDraft
	if e := s.db.WithContext(ctx).First(&d, "id=?", id).Error; e != nil {
		return "", e
	}
	for _, dir := range []string{"tts", "audio"} {
		root := filepath.Join(s.cfg.EditorRoot, id, "work", d.BookID, dir)
		if path, manifestErr := resource.AudioItemFileInDir(root, pos, item, accent); manifestErr == nil {
			return path, nil
		}
	}
	return "", os.ErrNotExist
}
func (s *EditorService) resolveDraftImage(id, value string) (string, error) {
	if strings.HasPrefix(value, "published:") {
		var d model.TextbookDraft
		if e := s.db.First(&d, "id=?", id).Error; e != nil {
			return "", e
		}
		return s.resources.Resolve(d.BookID, strings.TrimPrefix(value, "published:"))
	}
	root := filepath.Join(s.cfg.EditorRoot, id)
	target := filepath.Clean(filepath.Join(root, value))
	rel, e := filepath.Rel(root, target)
	if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", errors.New("invalid draft resource path")
	}
	return target, nil
}
func (s *EditorService) SaveMeta(ctx context.Context, id, title string, grade int, term, edition string, version, actor uint64, audio AudioSettings) error {
	if strings.TrimSpace(title) == "" || grade < 1 || grade > 6 || (term != "上册" && term != "下册") {
		return bad("教材信息不完整")
	}
	audio = normalizeAudioSettings(audio)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var draft model.TextbookDraft
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&draft, "id=?", id).Error; e != nil {
			return e
		}
		if draft.Version != version || (draft.Status != "draft" && draft.Status != "failed") {
			return conflict("草稿已更新或不处于编辑状态")
		}
		updates := map[string]any{"title": title, "grade": grade, "term": term, "edition": edition, "american_enabled": audio.AmericanEnabled, "british_enabled": audio.BritishEnabled, "american_voice_id": audio.AmericanVoiceID, "british_voice_id": audio.BritishVoiceID, "audio_config_version": 1, "version": gorm.Expr("version+1"), "updated_by": actor}
		if draft.Status == "failed" {
			updates["status"] = "draft"
		}
		if e := tx.Model(&draft).Updates(updates).Error; e != nil {
			return e
		}
		if draft.AmericanEnabled != audio.AmericanEnabled || draft.BritishEnabled != audio.BritishEnabled {
			if e := tx.Model(&model.TextbookDraftPage{}).Where("draft_id=?", id).Update("audio_checked", false).Error; e != nil {
				return e
			}
			draft.AmericanEnabled, draft.BritishEnabled = audio.AmericanEnabled, audio.BritishEnabled
			var pages []model.TextbookDraftPage
			if e := tx.Where("draft_id=?", id).Find(&pages).Error; e != nil {
				return e
			}
			for _, page := range pages {
				if e := s.syncPageAudioState(tx, draft, page, false); e != nil {
					return e
				}
			}
		}
		return editorAudit(tx, actor, "draft.audio.settings", id, audio)
	})
}
func canonicalPageContentBySegmentID(content map[string]any) ([]byte, bool) {
	segments, ok := content["segments"].([]any)
	if !ok {
		return nil, false
	}
	ordered := append([]any(nil), segments...)
	seen := make(map[string]struct{}, len(ordered))
	for _, rawSegment := range ordered {
		segment, segmentOK := rawSegment.(map[string]any)
		segmentID, idOK := segment["id"].(string)
		if !segmentOK || !idOK || strings.TrimSpace(segmentID) == "" {
			return nil, false
		}
		if _, duplicate := seen[segmentID]; duplicate {
			return nil, false
		}
		seen[segmentID] = struct{}{}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		left := ordered[i].(map[string]any)["id"].(string)
		right := ordered[j].(map[string]any)["id"].(string)
		return left < right
	})
	normalized := make(map[string]any, len(content))
	for key, value := range content {
		normalized[key] = value
	}
	normalized["segments"] = ordered
	raw, err := json.Marshal(normalized)
	return raw, err == nil
}

func samePageContentExceptSegmentOrder(stored, incoming map[string]any) bool {
	storedRaw, storedOK := canonicalPageContentBySegmentID(stored)
	incomingRaw, incomingOK := canonicalPageContentBySegmentID(incoming)
	return storedOK && incomingOK && bytes.Equal(storedRaw, incomingRaw)
}

func (s *EditorService) SavePage(ctx context.Context, id string, pos int, input DraftPageView, actor uint64) error {
	normalizeContentAnchors(input.Content)
	if _, ok := input.Content["segments"].([]any); !ok {
		return bad("页面缺少 segments")
	}
	if (input.Checked || input.AudioChecked) && len(publicationIssues(input.Content)) > 0 {
		return bad("页面仍有待完成内容")
	}
	storageContent, e := clonePageContent(input.Content)
	if e != nil {
		return bad("页面 JSON 无效")
	}
	stripTranslationFields(storageContent)
	raw, e := json.Marshal(storageContent)
	if e != nil {
		return bad("页面 JSON 无效")
	}
	if len(raw) > 4<<20 {
		return bad("页面内容过大")
	}
	if e := s.ensureDraftAudioState(ctx, id); e != nil {
		return e
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var d model.TextbookDraft
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&d, "id=?", id).Error; e != nil {
			return e
		}
		if d.Status != "draft" && d.Status != "failed" {
			return conflict("当前草稿正在处理，暂时不能修改")
		}
		var p model.TextbookDraftPage
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("draft_id=? AND position=?", id, pos).First(&p).Error; e != nil {
			return e
		}
		if p.Version != input.Version {
			return conflict("页面已被修改，请重新加载")
		}
		// Generated page JSON is indented while browser saves are compact.  A
		// string comparison therefore treated every first save as a content
		// edit and immediately cleared the review flags.  Compare canonical
		// JSON instead so formatting alone cannot invalidate an approval.
		changed := true
		generationChanged := true
		var stored map[string]any
		if e := json.Unmarshal([]byte(p.Content), &stored); e == nil {
			stripTranslationFields(stored)
			storedRaw, marshalErr := json.Marshal(stored)
			changed = marshalErr != nil || !bytes.Equal(storedRaw, raw)
			generationChanged = changed && !samePageContentExceptSegmentOrder(stored, storageContent)
		}
		// Text, IDs, coordinates, or word edits invalidate prior OCR/audio
		// approval. Reordering otherwise-identical segments only changes reading
		// order, so audio keyed by stable segment/word IDs remains valid.
		if generationChanged {
			input.Checked = false
			input.AudioChecked = false
		}
		// Cover images and separator pages can contain no OCR segments. They
		// have nothing to synthesize and may be approved without audio files.
		if input.AudioChecked && hasAudioItems(string(raw)) {
			ready, readyErr := s.pageAudioReadyDB(ctx, d, pos)
			if readyErr != nil {
				return readyErr
			}
			if !ready {
				return bad("本页已启用的发音音频尚未生成完整")
			}
		}
		if generationChanged {
			if e := s.clearDraftPageArtifacts(d.ID, d.BookID, pos); e != nil {
				return e
			}
			// A user edit unlocks the page for a fresh TTS pass under the current
			// draft default; OCR provenance remains the last recognized model.
			p.TTSModel, p.TTSVoice = draftModelSettings(d).TTSModel, draftModelSettings(d).TTSVoice
		}
		pageUpdates := map[string]any{"content": string(raw), "title": input.Title, "unit": input.Unit, "preview": input.Preview, "checked": input.Checked, "audio_checked": input.AudioChecked, "version": gorm.Expr("version+1")}
		if generationChanged {
			pageUpdates["tts_model"], pageUpdates["tts_voice"] = p.TTSModel, p.TTSVoice
		}
		if input.Checked && input.AudioChecked && p.OCRModel == "" {
			p.OCRModel = draftModelSettings(d).OCRModel
			pageUpdates["ocr_model"] = p.OCRModel
		}
		if e := tx.Model(&p).Updates(pageUpdates).Error; e != nil {
			return e
		}
		translationModel := p.TranslationModel
		if translationModel == "" {
			translationModel = pageTranslationSettings(d, p).TranslationModel
		}
		provider := ""
		if translationInfo, ok := ai.Find(translationModel); ok {
			provider = translationInfo.Provider
		}
		if e := syncTranslationItems(tx, id, pos, p.Version+1, input.Content, translationModel, provider); e != nil {
			return e
		}
		if generationChanged {
			p.Content, p.Title, p.Unit, p.Preview = string(raw), input.Title, input.Unit, input.Preview
			p.Checked, p.AudioChecked, p.Version = input.Checked, input.AudioChecked, p.Version+1
			if e := s.syncPageAudioState(tx, d, p, true); e != nil {
				return e
			}
		}
		updates := map[string]any{"version": gorm.Expr("version+1"), "updated_by": actor}
		if d.Status == "failed" {
			updates["status"] = "draft"
		}
		if e := tx.Model(&d).Updates(updates).Error; e != nil {
			return e
		}
		return editorAudit(tx, actor, "draft.page.save", id, map[string]int{"page": pos})
	})
}

// normalizeContentAnchors keeps sentence hotspots inside the page when a
// browser submits coordinates that were created before the width-aware clamp
// was added.  The page coordinate system is normalized to 0..1; a rectangle's
// left/top therefore cannot exceed 1-width/1-height.
func normalizeContentAnchors(content map[string]any) {
	segments, ok := content["segments"].([]any)
	if !ok {
		return
	}
	for _, raw := range segments {
		segment, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		anchor, ok := segment["anchor"].([]any)
		if !ok {
			continue
		}
		switch len(anchor) {
		case 2:
			if x, ok := anchor[0].(float64); ok {
				anchor[0] = clampUnit(x)
			}
			if y, ok := anchor[1].(float64); ok {
				anchor[1] = clampUnit(y)
			}
		case 4:
			x, xOK := anchor[0].(float64)
			y, yOK := anchor[1].(float64)
			width, widthOK := anchor[2].(float64)
			height, heightOK := anchor[3].(float64)
			if !xOK || !yOK || !widthOK || !heightOK {
				continue
			}
			width = clampPositiveUnit(width)
			height = clampPositiveUnit(height)
			anchor[0] = clampUnitMax(x, 1-width)
			anchor[1] = clampUnitMax(y, 1-height)
			anchor[2] = width
			anchor[3] = height
		}
	}
}

func clampUnit(value float64) float64 { return clampUnitMax(value, 1) }

func clampUnitMax(value, max float64) float64 {
	if value < 0 {
		return 0
	}
	if value > max {
		return max
	}
	return value
}

func clampPositiveUnit(value float64) float64 {
	if value < 0.001 {
		return 0.001
	}
	if value > 1 {
		return 1
	}
	return value
}

func (s *EditorService) Reorder(ctx context.Context, id string, positions []int, version, actor uint64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var d model.TextbookDraft
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&d, "id=?", id).Error; e != nil {
			return e
		}
		if d.Status != "draft" || d.Version != version {
			return conflict("草稿已更新或不处于编辑状态")
		}
		var pages []model.TextbookDraftPage
		if e := tx.Where("draft_id=?", id).Order("position").Find(&pages).Error; e != nil {
			return e
		}
		if len(pages) != len(positions) {
			return bad("页序必须包含所有页面")
		}
		byPos := map[int]model.TextbookDraftPage{}
		for _, p := range pages {
			byPos[p.Position] = p
		}
		for _, p := range positions {
			if _, ok := byPos[p]; !ok {
				return bad("页序包含重复或无效页码")
			}
			delete(byPos, p)
		}
		if e := tx.Model(&model.TextbookDraftPage{}).Where("draft_id=?", id).Update("position", gorm.Expr("position+1000000")).Error; e != nil {
			return e
		}
		if e := tx.Model(&model.TextbookTranslationItem{}).Where("draft_id=?", id).Update("page", gorm.Expr("page+1000000")).Error; e != nil {
			return e
		}
		for i, old := range positions {
			if e := tx.Model(&model.TextbookDraftPage{}).Where("draft_id=? AND position=?", id, old+1000000).Updates(map[string]any{"position": i + 1, "checked": false, "audio_checked": false, "version": gorm.Expr("version+1")}).Error; e != nil {
				return e
			}
			if e := tx.Model(&model.TextbookTranslationItem{}).Where("draft_id=? AND page=?", id, old+1000000).Update("page", i+1).Error; e != nil {
				return e
			}
		}
		tx.Model(&d).Updates(map[string]any{"version": gorm.Expr("version+1"), "updated_by": actor})
		return editorAudit(tx, actor, "draft.reorder", id, positions)
	})
}
func (s *EditorService) pageTextReviewIssues(ctx context.Context, draftID string, page int, raw string) []string {
	content, err := s.hydratedPageContent(ctx, draftID, page, raw)
	if err != nil {
		return []string{"页面 JSON 无效"}
	}
	return publicationIssues(content)
}

func (s *EditorService) pageAudioRegenerationIssues(ctx context.Context, draftID string, page int, raw string) []string {
	content, err := s.hydratedPageContent(ctx, draftID, page, raw)
	if err != nil {
		return []string{"页面 JSON 无效"}
	}
	issues := publicationIssues(content)
	if !hasAudioItems(raw) {
		issues = append(issues, "没有可朗读的 OCR 内容")
	}
	return issues
}

func (s *EditorService) Action(ctx context.Context, id, action, note string, version, actor uint64, page int) error {
	if action == "audio-restart-page" {
		return s.restartPageAudio(ctx, id, version, actor, page)
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var d model.TextbookDraft
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&d, "id=?", id).Error; e != nil {
			return e
		}
		if d.Version != version {
			return conflict("草稿已更新，请刷新")
		}
		allowed := map[string][]string{"submit": {"draft"}, "approve": {"in_review"}, "reject": {"in_review", "approved"}, "withdraw": {"in_review", "approved"}, "publish": {"approved"}, "retry": {"failed"}, "translate": {"draft"}, "audio": {"draft"}, "audio-replace-page": {"draft", "failed"}, "audio-missing": {"draft"}, "audio-regenerate-us": {"draft"}, "audio-regenerate-uk": {"draft"}, "audio-retry-failed": {"draft"}, "confirm-text": {"draft", "failed"}, "next-page": {"draft"}, "reocr": {"draft", "failed"}}
		ok := false
		for _, st := range allowed[action] {
			if d.Status == st {
				ok = true
			}
		}
		if !ok || (action == "reject" && strings.TrimSpace(note) == "") {
			return conflict("当前状态不允许此操作；退回时必须填写意见")
		}
		next := map[string]string{"submit": "in_review", "approve": "approved", "reject": "draft", "withdraw": "draft", "publish": "published", "retry": "queued", "translate": "queued", "audio": "queued", "audio-replace-page": "queued", "audio-missing": "queued", "audio-regenerate-us": "queued", "audio-regenerate-uk": "queued", "audio-retry-failed": "queued", "confirm-text": "draft", "next-page": "queued", "reocr": "queued"}[action]
		if action == "submit" || action == "approve" || action == "publish" {
			var pages []model.TextbookDraftPage
			if e := tx.Where("draft_id=?", id).Find(&pages).Error; e != nil {
				return e
			}
			if len(pages) == 0 {
				return bad("没有可发布的页面")
			}
			if d.SourcePageCount > 0 && len(pages) != d.SourcePageCount {
				return bad(fmt.Sprintf("PDF 共 %d 页，尚未生成完毕", d.SourcePageCount))
			}
			for _, p := range pages {
				if !p.Checked || !p.AudioChecked || len(s.pageTextReviewIssues(ctx, id, p.Position, p.Content)) > 0 || s.missingDraftAudio(d, p) {
					return bad(fmt.Sprintf("第%d页尚未完成正文或音频确认", p.Position))
				}
			}
		}
		if action == "publish" {
			if e := s.publish(tx, d); e != nil {
				return e
			}
		}
		if action == "retry" {
			var job model.TextbookJob
			if e := tx.Where("draft_id=?", id).Order("id DESC").First(&job).Error; e != nil {
				return e
			}
			if job.Kind == "audio" {
				issues, issueErr := s.AudioIssues(ctx, id, job.Page)
				if issueErr != nil {
					return issueErr
				}
				if len(issues) > 0 {
					return conflict("请在音频异常页面逐项重新生成，不要重新排队整页")
				}
			}
			if e := tx.Create(&model.TextbookJob{DraftID: id, Kind: job.Kind, Page: job.Page, Status: "queued", Total: job.Total}).Error; e != nil {
				return e
			}
		}
		if action == "confirm-text" {
			if page < 1 {
				return bad("请指定要确认文字的页面")
			}
			var current model.TextbookDraftPage
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("draft_id=? AND position=?", id, page).First(&current).Error; e != nil {
				return bad(fmt.Sprintf("第 %d 页尚未生成", page))
			}
			if issues := s.pageTextReviewIssues(ctx, id, current.Position, current.Content); len(issues) > 0 {
				return bad(fmt.Sprintf("第 %d 页正文仍有待处理内容：%s", page, strings.Join(issues, "；")))
			}
			updates := map[string]any{
				"checked": true,
				"version": gorm.Expr("version+1"),
			}
			// A page with no speakable items, or a draft with audio disabled,
			// has no independent audio work to review.
			if !hasAudioItems(current.Content) || len(draftAudioConfig(d).EnabledVoices()) == 0 {
				updates["audio_checked"] = true
			}
			if e := tx.Model(&current).Updates(updates).Error; e != nil {
				return e
			}
		}
		if action == "reocr" {
			if page < 1 || (d.SourcePageCount > 0 && page > d.SourcePageCount) {
				return bad("无效的 OCR 页面")
			}
			var target model.TextbookDraftPage
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("draft_id=? AND position=?", id, page).First(&target).Error; e != nil {
				return bad(fmt.Sprintf("第 %d 页尚未生成，无法重新 OCR", page))
			}
			var active int64
			if e := tx.Model(&model.TextbookJob{}).Where("draft_id=? AND status IN ?", id, []string{"queued", "running"}).Count(&active).Error; e != nil {
				return e
			}
			if active > 0 {
				return conflict("已有页面生成任务正在进行")
			}
			// The queued OCR worker must never see stale page metadata or audio. The
			// cleanup happens before the job is committed, so it cannot race the
			// worker; the source page image is intentionally retained.
			if e := s.clearDraftPageArtifacts(d.ID, d.BookID, page); e != nil {
				return e
			}
			emptyContent, _ := json.Marshal(map[string]any{
				"book_id":  d.BookID,
				"page":     page,
				"segments": []any{},
				"reviewed": false,
			})
			if e := tx.Model(&target).Updates(map[string]any{
				"content":       string(emptyContent),
				"checked":       false,
				"audio_checked": false,
				"version":       gorm.Expr("version+1"),
			}).Error; e != nil {
				return e
			}
			if e := tx.Where("draft_id=? AND page=?", id, page).Delete(&model.TextbookTranslationItem{}).Error; e != nil {
				return e
			}
			if e := tx.Create(&model.TextbookJob{DraftID: id, Kind: "ocr", Page: page, Status: "queued", Total: d.SourcePageCount}).Error; e != nil {
				return e
			}
		}
		if action == "audio" {
			if len(draftAudioConfig(d).EnabledVoices()) == 0 {
				return conflict("当前草稿已关闭所有发音，已跳过音频生成")
			}
			var pages []model.TextbookDraftPage
			if e := tx.Where("draft_id=?", id).Order("position").Find(&pages).Error; e != nil {
				return e
			}
			if len(pages) == 0 {
				return conflict("第 1 页的 OCR 尚未转换完成")
			}
			current := pages[len(pages)-1]
			if page > 0 {
				found := false
				for _, candidate := range pages {
					if candidate.Position == page {
						current = candidate
						found = true
						break
					}
				}
				if !found {
					return bad(fmt.Sprintf("第 %d 页尚未生成", page))
				}
			}
			if !current.Checked {
				return bad(fmt.Sprintf("请先确认第 %d 页正文，再生成音频", current.Position))
			}
			if issues := s.pageAudioRegenerationIssues(ctx, id, current.Position, current.Content); len(issues) > 0 {
				return bad(fmt.Sprintf("第 %d 页暂不能生成音频：%s", current.Position, strings.Join(issues, "；")))
			}
			if !s.missingDraftAudio(d, current) {
				return conflict(fmt.Sprintf("第 %d 页已启用的发音音频已生成，请直接试听确认", current.Position))
			}
			var active int64
			if e := tx.Model(&model.TextbookJob{}).Where("draft_id=? AND status IN ?", id, []string{"queued", "running"}).Count(&active).Error; e != nil {
				return e
			}
			if active > 0 {
				return conflict("已有页面生成任务正在进行")
			}
			if e := tx.Model(&current).Update("audio_checked", false).Error; e != nil {
				return e
			}
			if e := tx.Create(&model.TextbookJob{DraftID: id, Kind: "audio", Page: current.Position, Status: "queued"}).Error; e != nil {
				return e
			}
		}
		if action == "audio-replace-page" {
			if len(draftAudioConfig(d).EnabledVoices()) == 0 {
				return conflict("当前草稿已关闭所有发音，无法重新生成音频")
			}
			if page < 1 {
				return bad("请指定要重新生成音频的页面")
			}
			var current model.TextbookDraftPage
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("draft_id=? AND position=?", id, page).First(&current).Error; e != nil {
				return bad(fmt.Sprintf("第 %d 页尚未生成", page))
			}
			if issues := s.pageAudioRegenerationIssues(ctx, id, current.Position, current.Content); len(issues) > 0 {
				return bad(fmt.Sprintf("第 %d 页暂不能重新生成音频：%s", current.Position, strings.Join(issues, "；")))
			}
			var active int64
			if e := tx.Model(&model.TextbookJob{}).Where("draft_id=? AND status IN ?", id, []string{"queued", "running"}).Count(&active).Error; e != nil {
				return e
			}
			if active > 0 {
				return conflict("已有页面生成任务正在进行")
			}
			// Remove both passing and failed audio for this page before queuing,
			// so the replacement job cannot race stale artifacts or expose them
			// while the model is generating. OCR/translation and other pages stay.
			if e := s.clearDraftPageAudioArtifacts(d.ID, d.BookID, page); e != nil {
				return e
			}
			if e := tx.Model(&current).Updates(map[string]any{"checked": true, "audio_checked": false, "version": gorm.Expr("version+1")}).Error; e != nil {
				return e
			}
			current.Checked = true
			current.AudioChecked = false
			current.Version++
			if e := s.syncPageAudioState(tx, d, current, true); e != nil {
				return e
			}
			if e := tx.Create(&model.TextbookJob{DraftID: id, Kind: "audio-replace", Page: page, Status: "queued"}).Error; e != nil {
				return e
			}
		}
		if action == "audio-missing" || action == "audio-regenerate-us" || action == "audio-regenerate-uk" || action == "audio-retry-failed" {
			var active int64
			if e := tx.Model(&model.TextbookJob{}).Where("draft_id=? AND status IN ?", id, []string{"queued", "running"}).Count(&active).Error; e != nil {
				return e
			}
			if active > 0 {
				return conflict("已有音频任务正在执行，请勿重复提交")
			}
			var pageCount int64
			if e := tx.Model(&model.TextbookDraftPage{}).Where("draft_id=?", id).Count(&pageCount).Error; e != nil {
				return e
			}
			if pageCount == 0 {
				return conflict("当前草稿还没有可生成音频的页面")
			}
			if action == "audio-missing" {
				var pages []model.TextbookDraftPage
				if e := tx.Where("draft_id=?", id).Order("position").Find(&pages).Error; e != nil {
					return e
				}
				pending := 0
				for index := range pages {
					p := &pages[index]
					if !p.Checked || len(s.pageTextReviewIssues(ctx, id, p.Position, p.Content)) > 0 || !hasAudioItems(p.Content) {
						continue
					}
					if s.missingDraftAudio(d, *p) {
						pending++
						if e := tx.Model(p).Update("audio_checked", false).Error; e != nil {
							return e
						}
					}
				}
				if pending == 0 {
					return conflict("没有已确认且缺失音频的页面")
				}
			}
			kind, accent, voice := "audio-missing", "", ""
			config := draftAudioConfig(d)
			configuredVoices := draftVoices(d)
			switch action {
			case "audio-regenerate-us":
				if !config.AmericanEnabled {
					return bad("美式发音尚未开启")
				}
				kind, accent, voice = "audio-accent", tts.AccentUS, configuredVoices[tts.AccentUS]
			case "audio-regenerate-uk":
				if !config.BritishEnabled {
					return bad("英式发音尚未开启")
				}
				kind, accent, voice = "audio-accent", tts.AccentGB, configuredVoices[tts.AccentGB]
			case "audio-retry-failed":
				kind = "audio-failed"
			}
			if len(configuredVoices) == 0 {
				return bad("请至少开启一种发音后再生成音频")
			}
			if e := tx.Create(&model.TextbookJob{DraftID: id, Kind: kind, Page: 0, Accent: accent, VoiceID: voice, Status: "queued"}).Error; e != nil {
				return e
			}
		}
		if action == "translate" {
			var pages []model.TextbookDraftPage
			if e := tx.Where("draft_id=?", id).Order("position").Find(&pages).Error; e != nil {
				return e
			}
			if len(pages) == 0 {
				return conflict("第 1 页的 OCR 尚未转换完成")
			}
			current := pages[len(pages)-1]
			if !hasSegments(current.Content) {
				return conflict(fmt.Sprintf("第 %d 页没有可翻译的 OCR 内容", current.Position))
			}
			translationModelID := pageTranslationSettings(d, current).TranslationModel
			translationModel, ok := ai.Find(translationModelID)
			if !ok || translationModel.Type != "translation" || !translationModel.Enabled {
				return bad("当前页翻译模型无效")
			}
			if !translationModel.Available {
				return bad("当前页翻译模型不可用：" + translationModel.UnavailableReason)
			}
			var active int64
			if e := tx.Model(&model.TextbookJob{}).Where("draft_id=? AND status IN ?", id, []string{"queued", "running"}).Count(&active).Error; e != nil {
				return e
			}
			if active > 0 {
				return conflict("已有页面生成任务正在进行")
			}
			if e := tx.Create(&model.TextbookJob{DraftID: id, Kind: "translate", Page: current.Position, Status: "queued"}).Error; e != nil {
				return e
			}
		}
		if action == "next-page" {
			var pages []model.TextbookDraftPage
			if e := tx.Where("draft_id=?", id).Order("position").Find(&pages).Error; e != nil {
				return e
			}
			if d.SourcePageCount == 0 || len(pages) == 0 {
				return conflict("第 1 页尚未转换完成")
			}
			current := pages[len(pages)-1]
			if !current.Checked {
				return bad(fmt.Sprintf("请先确认第 %d 页正文", current.Position))
			}
			if issues := s.pageTextReviewIssues(ctx, id, current.Position, current.Content); len(issues) > 0 {
				return bad(fmt.Sprintf("第 %d 页正文仍有待处理内容：%s", current.Position, strings.Join(issues, "；")))
			}
			nextPage := current.Position + 1
			if nextPage > d.SourcePageCount {
				return bad("PDF 所有页面均已生成")
			}
			var active int64
			if e := tx.Model(&model.TextbookJob{}).Where("draft_id=? AND status IN ?", id, []string{"queued", "running"}).Count(&active).Error; e != nil {
				return e
			}
			if active > 0 {
				return conflict("已有页面生成任务正在进行")
			}
			if e := tx.Create(&model.TextbookJob{DraftID: id, Kind: "ocr", Page: nextPage, Status: "queued", Total: d.SourcePageCount}).Error; e != nil {
				return e
			}
		}
		if e := tx.Model(&d).Updates(map[string]any{"status": next, "review_note": note, "version": gorm.Expr("version+1"), "updated_by": actor}).Error; e != nil {
			return e
		}
		return editorAudit(tx, actor, "draft."+action, id, map[string]string{"book_id": d.BookID, "note": note})
	})
}
func (s *EditorService) missingDraftAudio(d model.TextbookDraft, page model.TextbookDraftPage) bool {
	items, err := expectedAudioItems(page.Content)
	if err != nil {
		return true
	}
	// A cover/separator page or a page whose segments explicitly disable audio
	// has no expected items and therefore needs no audio manifest.
	if len(items) == 0 {
		return false
	}
	expected := make([]resource.AudioExpectedItem, 0, len(items))
	for _, item := range items {
		if item.ItemID == "" {
			return true
		}
		expected = append(expected, resource.AudioExpectedItem{Page: page.Position, ItemID: item.ItemID})
	}
	voices := settingsVoices(d, pageAudioSettings(d, page))
	for accent, voice := range voices {
		found := false
		for _, dir := range []string{"tts", "audio"} {
			root := filepath.Join(s.cfg.EditorRoot, d.ID, "work", d.BookID, dir)
			if resource.AudioExpectedReadyInDir(root, accent, voice, expected, false) {
				found = true
				break
			}
		}
		if !found {
			return true
		}
	}
	return false
}

func (s *EditorService) draftAudioStatus(d model.TextbookDraft, pages []model.TextbookDraftPage) []resource.AudioAccentStatus {
	expected := make([]resource.AudioExpectedItem, 0)
	for _, page := range pages {
		items, err := expectedAudioItems(page.Content)
		if err != nil {
			continue
		}
		for _, item := range items {
			if item.ItemID != "" {
				expected = append(expected, resource.AudioExpectedItem{Page: page.Position, ItemID: item.ItemID})
			}
		}
	}
	config := draftAudioConfig(d)
	voices := draftVoices(d)
	result := make([]resource.AudioAccentStatus, 0, 2)
	root := filepath.Join(s.cfg.EditorRoot, d.ID, "work", d.BookID, "tts")
	for _, setting := range []struct {
		accent, voice string
		enabled       bool
	}{
		{tts.AccentUS, voices[tts.AccentUS], config.AmericanEnabled},
		{tts.AccentGB, voices[tts.AccentGB], config.BritishEnabled},
	} {
		status := resource.AudioStatusInDir(root, setting.accent, setting.voice, expected, false)
		if !setting.enabled {
			status.Status = "disabled"
		}
		result = append(result, status)
	}
	return result
}

func (s *EditorService) publishedAudioStatus(ctx context.Context, book model.Book) []resource.AudioAccentStatus {
	var pages []model.BookPage
	if s.db.WithContext(ctx).Where("book_id=?", book.BookID).Order("position").Find(&pages).Error != nil {
		return []resource.AudioAccentStatus{}
	}
	expected := make([]resource.AudioExpectedItem, 0)
	for _, page := range pages {
		path, e := s.resources.Resolve(book.BookID, page.ContentPath)
		if e != nil {
			continue
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			continue
		}
		var content struct {
			Segments []struct {
				ID    string `json:"id"`
				Words []struct {
					ID   string `json:"id"`
					Text string `json:"text"`
				} `json:"words"`
			} `json:"segments"`
		}
		if json.Unmarshal(raw, &content) != nil {
			continue
		}
		for _, segment := range content.Segments {
			if segment.ID != "" {
				expected = append(expected, resource.AudioExpectedItem{Page: page.Position, ItemID: segment.ID})
			}
			for _, word := range segment.Words {
				if word.ID != "" && resource.HasSpeakableText(word.Text) {
					expected = append(expected, resource.AudioExpectedItem{Page: page.Position, ItemID: word.ID})
				}
			}
		}
	}
	american, british := book.AmericanVoiceID, book.BritishVoiceID
	if american == "" {
		american = tts.DefaultAmericanVoice
	}
	if british == "" {
		british = tts.DefaultBritishVoice
	}
	legacy := book.AudioConfigVersion == 0
	if legacy {
		book.AmericanEnabled, book.BritishEnabled = true, true
	}
	dir, _ := s.resources.Dir(book.BookID, "tts")
	result := []resource.AudioAccentStatus{
		resource.AudioStatusInDir(dir, tts.AccentUS, american, expected, legacy),
		resource.AudioStatusInDir(dir, tts.AccentGB, british, expected, legacy),
	}
	if !book.AmericanEnabled {
		result[0].Status = "disabled"
	}
	if !book.BritishEnabled {
		result[1].Status = "disabled"
	}
	return result
}
func (s *EditorService) publish(tx *gorm.DB, d model.TextbookDraft) error {
	var pages []model.TextbookDraftPage
	if e := tx.Where("draft_id=?", d.ID).Order("position").Find(&pages).Error; e != nil {
		return e
	}
	bookRoot, e := s.resources.BookRoot(d.BookID)
	if e != nil {
		return e
	}
	for _, kind := range []string{"source", "pages", "audio", "tts", "ocr", "text", "metadata", "cache"} {
		if e = os.MkdirAll(filepath.Join(bookRoot, kind), 0750); e != nil {
			return e
		}
	}
	if e = os.MkdirAll(filepath.Join(bookRoot, "metadata", "pages"), 0750); e != nil {
		return e
	}
	workRoot := filepath.Join(s.cfg.EditorRoot, d.ID, "work", d.BookID)
	for _, kind := range []string{"ocr", "text"} {
		source := filepath.Join(workRoot, kind)
		if fileExists(source) {
			if e = copyTree(source, filepath.Join(bookRoot, kind)); e != nil {
				return e
			}
		}
	}
	if source := filepath.Join(s.cfg.EditorRoot, d.ID, "source.pdf"); fileExists(source) {
		if e = copyFile(source, filepath.Join(bookRoot, "source", "original.pdf")); e != nil {
			return e
		}
	}
	newPages := make([]model.BookPage, 0, len(pages))
	for _, p := range pages {
		image, e := s.resolveDraftImage(d.ID, p.ImagePath)
		if e != nil {
			return e
		}
		imageRel := fmt.Sprintf("pages/page-%03d.png", p.Position)
		if e = copyFile(image, filepath.Join(bookRoot, filepath.FromSlash(imageRel))); e != nil {
			return e
		}
		contentRel := fmt.Sprintf("metadata/pages/page-%03d.json", p.Position)
		publishedContent, contentErr := s.hydratedPageContent(tx.Statement.Context, d.ID, p.Position, p.Content)
		if contentErr != nil {
			return contentErr
		}
		publishedRaw, contentErr := json.MarshalIndent(publishedContent, "", "  ")
		if contentErr != nil {
			return contentErr
		}
		if e = os.WriteFile(filepath.Join(bookRoot, filepath.FromSlash(contentRel)), append(publishedRaw, '\n'), 0640); e != nil {
			return e
		}
		newPages = append(newPages, model.BookPage{BookID: d.BookID, Position: p.Position, PrintedPage: p.PrintedPage, Title: p.Title, Unit: p.Unit, ImagePath: imageRel, ContentPath: contentRel, Interactive: hasSegments(p.Content), Preview: p.Preview})
	}
	manifest := map[string]any{"schema_version": 1, "book": map[string]any{"book_id": d.BookID, "title": d.Title, "grade": d.Grade, "semester": d.Term, "publisher": d.Edition, "cover": newPages[0].ImagePath}, "pages": newPages}
	manifestJSON, _ := json.MarshalIndent(manifest, "", "  ")
	if e = os.WriteFile(filepath.Join(bookRoot, "metadata", "book.json"), append(manifestJSON, '\n'), 0640); e != nil {
		return e
	}
	workTTS := filepath.Join(s.cfg.EditorRoot, d.ID, "work", d.BookID, "tts")
	if fileExists(workTTS) {
		if e = publishTTSTree(workTTS, filepath.Join(bookRoot, "tts")); e != nil {
			return e
		}
	}
	voices := draftVoices(d)
	americanVoice, britishVoice := voices[tts.AccentUS], voices[tts.AccentGB]
	if americanVoice == "" {
		americanVoice = tts.DefaultAmericanVoice
	}
	if britishVoice == "" {
		britishVoice = tts.DefaultBritishVoice
	}
	book := model.Book{BookID: d.BookID, Title: d.Title, Publisher: d.Edition, Grade: fmt.Sprint(d.Grade), Semester: d.Term, Cover: newPages[0].ImagePath, Status: "published", PageCount: len(newPages), AmericanEnabled: d.AmericanEnabled, BritishEnabled: d.BritishEnabled, AmericanVoiceID: americanVoice, BritishVoiceID: britishVoice, AudioConfigVersion: 1}
	var existing model.Book
	e = tx.Where("book_id=?", d.BookID).First(&existing).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		if e = tx.Create(&book).Error; e != nil {
			return e
		}
		if e = tx.Model(&book).Updates(map[string]any{"american_enabled": book.AmericanEnabled, "british_enabled": book.BritishEnabled, "american_voice_id": book.AmericanVoiceID, "british_voice_id": book.BritishVoiceID, "audio_config_version": 1}).Error; e != nil {
			return e
		}
	} else if e == nil {
		if e = tx.Model(&existing).Updates(map[string]any{"title": book.Title, "publisher": book.Publisher, "grade": book.Grade, "semester": book.Semester, "cover": book.Cover, "status": "published", "page_count": book.PageCount, "american_enabled": book.AmericanEnabled, "british_enabled": book.BritishEnabled, "american_voice_id": book.AmericanVoiceID, "british_voice_id": book.BritishVoiceID, "audio_config_version": 1, "revision": gorm.Expr("revision+1")}).Error; e != nil {
			return e
		}
	} else {
		return e
	}
	if e = tx.Where("book_id=?", d.BookID).Delete(&model.BookPage{}).Error; e != nil {
		return e
	}
	return tx.Create(&newPages).Error
}

func (s *EditorService) RunWorker(ctx context.Context) error {
	// A force-killed process cannot finalize its running job. Recover those
	// jobs before claiming new work; otherwise the worker only looks at queued
	// jobs and the UI remains stuck on "处理中" forever.
	if err := s.recoverInterruptedJobs(ctx); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		worked, e := s.runOne(ctx)
		if e != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
			continue
		}
		if !worked {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
}

func (s *EditorService) recoverInterruptedJobs(ctx context.Context) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var jobs []model.TextbookJob
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("status=?", "running").Order("id").Find(&jobs).Error; err != nil {
			return err
		}
		for _, job := range jobs {
			result := tx.Model(&model.TextbookJob{}).
				Where("id=? AND status=?", job.ID, "running").
				Updates(map[string]any{
					"status":       "queued",
					"progress":     0,
					"error":        "服务中断，任务已自动重新排队",
					"request_id":   "",
					"available_at": nil,
					"started_at":   nil,
					"finished_at":  nil,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				continue
			}

			if job.Kind == "audio-item" {
				if err := tx.Model(&model.TextbookAudioItem{}).
					Where("active_job_id=?", job.ID).
					Updates(map[string]any{"status": "queued", "revision": gorm.Expr("revision+1")}).Error; err != nil {
					return err
				}
			}
			draftStatus := "converting"
			if job.Kind == "translate" {
				draftStatus = "translating"
			} else if strings.HasPrefix(job.Kind, "audio") {
				draftStatus = "audio"
			}
			if err := tx.Model(&model.TextbookDraft{}).Where("id=?", job.DraftID).
				Updates(map[string]any{"status": draftStatus, "version": gorm.Expr("version+1")}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *EditorService) runOne(ctx context.Context) (bool, error) {
	var job model.TextbookJob
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Select a candidate without locking it first, then lock the draft row
		// before claiming the job. Delete uses the same draft-first lock order;
		// this prevents a worker that has only observed a queued job from
		// continuing to write after the draft has been removed.
		var candidate struct {
			ID      uint64
			DraftID string
		}
		result := tx.Model(&model.TextbookJob{}).Select("id, draft_id").Where("status='queued' AND (available_at IS NULL OR available_at<=?)", time.Now()).Order("priority DESC,id").Limit(1).Scan(&candidate)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		var draft model.TextbookDraft
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&draft, "id=?", candidate.DraftID).Error; e != nil {
			return e
		}
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status='queued'", candidate.ID).First(&job).Error; e != nil {
			return gorm.ErrRecordNotFound
		}
		selectedModel := jobModel(draft, job.Kind)
		// A manual-review retry may have an item-specific TTS model.  It is
		// deliberately stored on the queued job instead of changing the page or
		// draft defaults, so do not replace it with the page snapshot here.
		if job.Kind == "audio-item" && job.ModelID != "" {
			item, ok := ai.Find(job.ModelID)
			if !ok || item.Type != "tts" || !item.Enabled || !item.Available {
				return bad("单项重试所选的 TTS 模型当前不可用")
			}
			selectedModel = item
		} else if job.Kind == "translate" && job.Page > 0 {
			var draftPage model.TextbookDraftPage
			if err := tx.Where("draft_id=? AND position=?", draft.ID, job.Page).First(&draftPage).Error; err != nil {
				return err
			}
			modelID := pageTranslationSettings(draft, draftPage).TranslationModel
			if item, ok := ai.Find(modelID); ok {
				selectedModel = item
			}
		} else if strings.HasPrefix(job.Kind, "audio") && job.Page > 0 {
			var draftPage model.TextbookDraftPage
			if err := tx.Where("draft_id=? AND position=?", draft.ID, job.Page).First(&draftPage).Error; err != nil {
				return err
			}
			if item, ok := ai.Find(pageAudioSettings(draft, draftPage).TTSModel); ok {
				selectedModel = item
			}
		}
		job.ModelID, job.Provider = selectedModel.ID, selectedModel.Provider
		status := "converting"
		if strings.HasPrefix(job.Kind, "audio") {
			status = "audio"
		} else if job.Kind == "translate" {
			status = "translating"
		}
		now := time.Now()
		if e := tx.Model(&job).Updates(map[string]any{"status": "running", "attempts": gorm.Expr("attempts+1"), "error": "", "model_id": job.ModelID, "provider": job.Provider, "request_id": "", "started_at": now}).Error; e != nil {
			return e
		}
		if job.Kind == "audio-item" {
			if e := tx.Model(&model.TextbookAudioItem{}).Where("active_job_id=?", job.ID).Updates(map[string]any{"status": "generating", "revision": gorm.Expr("revision+1")}).Error; e != nil {
				return e
			}
		}
		return tx.Model(&model.TextbookDraft{}).Where("id=?", job.DraftID).Updates(map[string]any{"status": status, "version": gorm.Expr("version+1")}).Error
	})
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if strings.HasPrefix(job.Kind, "audio") {
		// Coordinate with restartPageAudio before the old task begins touching
		// page files.  A job cancelled in the claim/register gap exits quietly.
		s.audioRestartMu.Lock()
		var currentStatus string
		statusErr := s.db.WithContext(ctx).Model(&model.TextbookJob{}).
			Select("status").Where("id=?", job.ID).Scan(&currentStatus).Error
		if statusErr != nil || currentStatus != "running" {
			s.audioRestartMu.Unlock()
			if statusErr != nil {
				return true, statusErr
			}
			return true, nil
		}
		s.beginAudioJob(job.ID)
		s.audioRestartMu.Unlock()
		defer s.finishAudioJob(job.ID)
	}
	runErr := s.convert(ctx, &job)
	if strings.HasPrefix(job.Kind, "audio") {
		if stateErr := s.refreshAudioStateFromManifest(ctx, job); stateErr != nil && runErr == nil {
			runErr = stateErr
		}
	}
	status, ds := "completed", "draft"
	msg := ""
	if runErr != nil {
		status, ds, msg = "failed", "failed", runErr.Error()
		var localIssues localTranslationIssuesError
		if errors.As(runErr, &localIssues) {
			status, ds = "issues", "draft"
		}
		// Audio QA failures are recoverable per item. Keep the draft editable
		// and expose the unresolved entries in the dedicated review screen.
		if strings.HasPrefix(job.Kind, "audio") {
			status, ds = "issues", "draft"
		}
		if ai.IsCloud(job.ModelID) {
			status = "manual_review_required"
			if strings.HasPrefix(job.Kind, "audio") {
				ds = "draft"
			}
		}
		if len(msg) > 4000 {
			msg = msg[len(msg)-4000:]
		}
	} else {
		job.Progress = job.Total
	}
	finalizeErr := s.db.WithContext(context.Background()).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.TextbookJob{}).Where("id=? AND status='running'", job.ID).
			Updates(map[string]any{"status": status, "error": msg, "progress": job.Progress, "request_id": job.RequestID, "finished_at": time.Now()})
		if result.Error != nil {
			return result.Error
		}
		// A restart operation changes the old job to cancelled before killing
		// its process.  In that case the old worker must not overwrite the new
		// queued job's draft status when it eventually unwinds.
		if result.RowsAffected == 0 {
			return nil
		}
		if job.Kind == "audio-item" {
			updates := map[string]any{"active_job_id": nil, "revision": gorm.Expr("revision+1")}
			if runErr != nil {
				itemStatus := "qa_failed"
				if ai.IsCloud(job.ModelID) {
					itemStatus = "manual_review_required"
				}
				updates["status"] = itemStatus
			}
			if e := tx.Model(&model.TextbookAudioItem{}).Where("active_job_id=?", job.ID).
				Updates(updates).Error; e != nil {
				return e
			}
		}
		return tx.Model(&model.TextbookDraft{}).Where("id=?", job.DraftID).Updates(map[string]any{"status": ds, "version": gorm.Expr("version+1")}).Error
	})
	if finalizeErr != nil {
		finalizeErr = fmt.Errorf("finalize job %d: %w", job.ID, finalizeErr)
		if runErr != nil {
			return true, errors.Join(runErr, finalizeErr)
		}
		return true, finalizeErr
	}
	return true, runErr
}
func (s *EditorService) convert(ctx context.Context, job *model.TextbookJob) error {
	var d model.TextbookDraft
	if e := s.db.WithContext(ctx).First(&d, "id=?", job.DraftID).Error; e != nil {
		return e
	}
	work := filepath.Join(s.cfg.EditorRoot, d.ID, "work")
	script := "prepare_book.py"
	targetPage := job.Page
	if targetPage < 1 {
		// Legacy all-document jobs are deliberately made safe: after this
		// rollout they begin at page one instead of converting the full PDF.
		targetPage = 1
	}
	if job.Kind == "translate" {
		return s.translatePage(ctx, job, d, work, targetPage)
	}
	if strings.HasPrefix(job.Kind, "audio") {
		if job.Kind == "audio-missing" || job.Kind == "audio-accent" || job.Kind == "audio-failed" {
			return s.runBookAudioJob(ctx, job, d, work)
		}
		// Audio must use the OCR the reviewer just confirmed, not the original
		// pre-review manifest written by prepare_book.py. Keep the generated
		// metadata file in sync immediately before invoking the TTS script.
		var reviewed model.TextbookDraftPage
		if e := s.db.WithContext(ctx).Where("draft_id=? AND position=?", d.ID, targetPage).First(&reviewed).Error; e != nil {
			return e
		}
		metadata := filepath.Join(work, d.BookID, "metadata", "pages")
		if e := os.MkdirAll(metadata, 0750); e != nil {
			return e
		}
		contentPath := filepath.Join(metadata, fmt.Sprintf("page-%03d.json", targetPage))
		if e := os.WriteFile(contentPath, []byte(reviewed.Content), 0640); e != nil {
			return e
		}
		return s.runAudioDaemon(ctx, job, d, work, targetPage)
	}
	args := []string{filepath.Join("scripts", script), "--input", filepath.Join(s.cfg.EditorRoot, d.ID, "source.pdf"), "--book-id", d.BookID, "--title", d.Title, "--grade", fmt.Sprint(d.Grade), "--semester", d.Term, "--publisher", d.Edition, "--page", fmt.Sprint(targetPage), "--ocr-model", draftModelSettings(d).OCRModel}
	if job.Kind == "ocr" || job.Kind == "page" {
		if e := s.runOCRDaemon(ctx, job, d, work, targetPage); e != nil {
			return e
		}
		if job.Kind == "ocr" {
			return s.importConverted(ctx, d, work)
		}
		// Legacy page jobs continue with their historical audio step below,
		// but their OCR now also benefits from the resident model.
		if e := s.importConverted(ctx, d, work); e != nil {
			return e
		}
		return s.generatePageAudio(ctx, d, work, targetPage)
	}
	cmd := exec.CommandContext(ctx, s.cfg.Python, args...)
	cmd.Dir = filepath.Dir(filepath.Dir(s.cfg.ResourceRoot))
	cmd.Env = append(os.Environ(), "RESOURCE_ROOT="+work)
	pipe, e := cmd.StdoutPipe()
	if e != nil {
		return e
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if e = cmd.Start(); e != nil {
		return e
	}
	scanner := bufio.NewScanner(pipe)
	for scanner.Scan() {
		var event struct {
			Page     int `json:"page"`
			Progress int `json:"progress"`
			Total    int `json:"total"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) == nil {
			job.Progress, job.Total = event.Progress, event.Total
			s.db.Model(job).Updates(map[string]any{"progress": job.Progress, "total": job.Total})
		}
	}
	if e = cmd.Wait(); e != nil {
		return fmt.Errorf("%s: %w: %s", job.Kind, e, strings.TrimSpace(stderr.String()))
	}
	if job.Kind == "page" || job.Kind == "pdf" {
		if e := s.importConverted(ctx, d, work); e != nil {
			return e
		}
		return s.generatePageAudio(ctx, d, work, targetPage)
	}
	return nil
}

func (s *EditorService) runBookAudioJob(ctx context.Context, job *model.TextbookJob, d model.TextbookDraft, work string) error {
	var pages []model.TextbookDraftPage
	if e := s.db.WithContext(ctx).Where("draft_id=?", d.ID).Order("position").Find(&pages).Error; e != nil {
		return e
	}
	job.Total, job.Progress = len(pages), 0
	s.db.Model(job).Updates(map[string]any{"progress": 0, "total": len(pages)})
	errorsFound := make([]string, 0)
	for index, page := range pages {
		if job.Kind == "audio-missing" && (!page.Checked || len(s.pageTextReviewIssues(ctx, d.ID, page.Position, page.Content)) > 0) {
			job.Progress = index + 1
			s.db.Model(job).Updates(map[string]any{"progress": job.Progress, "total": job.Total})
			continue
		}
		if !hasAudioItems(page.Content) {
			job.Progress = index + 1
			continue
		}
		metadata := filepath.Join(work, d.BookID, "metadata", "pages")
		if e := os.MkdirAll(metadata, 0750); e != nil {
			return e
		}
		audioContent, contentErr := s.hydratedPageContent(ctx, d.ID, page.Position, page.Content)
		if contentErr != nil {
			return contentErr
		}
		audioRaw, contentErr := json.Marshal(audioContent)
		if contentErr != nil {
			return contentErr
		}
		if e := os.WriteFile(filepath.Join(metadata, fmt.Sprintf("page-%03d.json", page.Position)), audioRaw, 0640); e != nil {
			return e
		}
		mode := "missing"
		if job.Kind == "audio-accent" {
			mode = "replace-accent"
		}
		if job.Kind == "audio-failed" {
			mode = "retry-failed"
		}
		if e := s.runAudioDaemonMode(ctx, job, d, work, page.Position, mode); e != nil {
			errorsFound = append(errorsFound, fmt.Sprintf("book=%s page=%d accent=%s voice=%s: %v", d.BookID, page.Position, job.Accent, job.VoiceID, e))
		}
		job.Progress = index + 1
		s.db.Model(job).Updates(map[string]any{"progress": job.Progress, "total": job.Total})
	}
	if len(errorsFound) > 0 {
		return errors.New(strings.Join(errorsFound, "; "))
	}
	return nil
}

func (s *EditorService) generatePageAudio(ctx context.Context, d model.TextbookDraft, work string, page int) error {
	// Legacy combined jobs use the same configured resident engine path.
	job := model.TextbookJob{DraftID: d.ID, Kind: "audio", Page: page}
	return s.runAudioDaemon(ctx, &job, d, work, page)
}

// runAudioDaemon keeps Qwen3-TTS, Whisper and the alignment checker resident so
// subsequent pages do not reload several hundred megabytes of model state.
func (s *EditorService) runAudioDaemon(ctx context.Context, job *model.TextbookJob, d model.TextbookDraft, work string, page int) error {
	mode := "replace-page"
	if job.Kind == "audio-item" {
		// Explicit editor regeneration must synthesize a fresh candidate even
		// when a ready word cache entry already exists.
		mode = "replace-item"
	} else if job.Kind == "audio" {
		// Keep existing enabled-accent audio and fill only gaps, e.g. when an
		// editor enables a new accent on a published-book draft.
		mode = "missing"
	}
	return s.runAudioDaemonMode(ctx, job, d, work, page, mode)
}

func (s *EditorService) runAudioDaemonMode(ctx context.Context, job *model.TextbookJob, d model.TextbookDraft, work string, page int, mode string) error {
	// Audio jobs take over the local MLX memory budget. Cloud TTS is cheap, but
	// stopping an idle local translation daemon here also keeps the rule simple
	// and deterministic when a draft changes providers.
	s.translationMu.Lock()
	s.stopTranslationDaemon()
	s.translationMu.Unlock()
	s.audioMu.Lock()
	defer s.audioMu.Unlock()

	var draftPage model.TextbookDraftPage
	if e := s.db.WithContext(ctx).Where("draft_id=? AND position=?", d.ID, page).First(&draftPage).Error; e != nil {
		return e
	}
	settings := pageAudioSettings(d, draftPage)
	voices := settingsVoices(d, settings)
	modelID := settings.TTSModel
	if job.Kind == "audio-item" && job.ModelID != "" {
		if job.VoiceID == "" {
			return bad("单项重试缺少音色")
		}
		modelID = job.ModelID
		voices = map[string]string{job.Accent: job.VoiceID}
	}
	if job.Kind == "audio-accent" && job.Accent != "" {
		voice, enabled := voices[job.Accent]
		if !enabled || voice == "" {
			return bad("该口音已经关闭")
		}
		voices = map[string]string{job.Accent: voice}
	}

	// A daemon process is the memory boundary for local MLX models. Never ask
	// one Python process to load a second Qwen model: if the requested model is
	// different, terminate the idle daemon first and start a fresh one. The
	// outer audioMu serializes audio jobs, so no active generation is killed.
	s.audioProcessMu.Lock()
	if s.audioCmd != nil && s.audioCmd.ProcessState == nil && s.audioDaemonModel != "" && s.audioDaemonModel != modelID {
		s.stopAudioDaemonLocked()
	}
	if s.audioCmd == nil || s.audioCmd.ProcessState != nil {
		cmd := exec.Command(s.cfg.Python, filepath.Join("scripts", "generate_audio.py"), "--daemon")
		cmd.Dir = filepath.Dir(filepath.Dir(s.cfg.ResourceRoot))
		in, e := cmd.StdinPipe()
		if e != nil {
			s.audioProcessMu.Unlock()
			return e
		}
		out, e := cmd.StdoutPipe()
		if e != nil {
			s.audioProcessMu.Unlock()
			return e
		}
		cmd.Stderr = os.Stderr
		if e = cmd.Start(); e != nil {
			s.audioProcessMu.Unlock()
			return e
		}
		s.audioCmd, s.audioIn, s.audioOut, s.audioDaemonModel = cmd, in, bufio.NewScanner(out), modelID
	}
	audioIn, audioOut := s.audioIn, s.audioOut
	s.audioProcessMu.Unlock()

	request := map[string]any{"resource_root": work,
		"book_id": d.BookID, "page": page, "mode": mode, "voices": voices,
		"model_id": modelID}
	if job.Kind == "audio-item" {
		request["item_id"], request["accent"] = job.ItemID, job.Accent
		request["retry_variant"] = job.ID
	}
	raw, _ := json.Marshal(request)
	if _, e := fmt.Fprintf(audioIn, "%s\n", raw); e != nil {
		s.stopAudioDaemon()
		return e
	}
	for {
		line, e := scanDaemonLine(ctx, audioOut, s.stopAudioDaemon, "audio")
		if e != nil {
			if errors.Is(e, io.EOF) {
				e = errors.New("audio worker exited unexpectedly")
			}
			return e
		}
		var event struct {
			Event     string          `json:"event"`
			Progress  int             `json:"progress"`
			Total     int             `json:"total"`
			QA        json.RawMessage `json:"qa"`
			Error     string          `json:"error"`
			RequestID string          `json:"request_id"`
			Done      bool            `json:"done"`
		}
		if json.Unmarshal(line, &event) != nil {
			continue
		}
		if len(event.QA) > 0 {
			var result struct {
				Passed    bool   `json:"passed"`
				RequestID string `json:"request_id"`
			}
			_ = json.Unmarshal(event.QA, &result)
			if result.RequestID != "" {
				job.RequestID = result.RequestID
			}
			tag := "AUDIO QA"
			if !result.Passed {
				tag = "AUDIO QA FAIL"
			}
			fmt.Fprintf(os.Stderr, "[%s] %s\n", tag, event.QA)
		}
		if event.RequestID != "" {
			job.RequestID = event.RequestID
		}
		if event.Total > 0 && event.Event == "progress" {
			job.Progress, job.Total = event.Progress, event.Total
			s.db.Model(job).Updates(map[string]any{"progress": job.Progress, "total": job.Total})
		}
		if event.Done {
			if event.Error != "" {
				return errors.New(event.Error)
			}
			return nil
		}
	}
}

func stopResidentDaemonProcess(cmd *exec.Cmd, in io.WriteCloser) {
	if in != nil {
		// Both translation_daemon.py and generate_audio.py --daemon iterate
		// over stdin. Closing it lets Python leave the loop normally and run
		// MLX/multiprocessing cleanup instead of being SIGKILLed immediately.
		_ = in.Close()
	}
	if cmd == nil || cmd.Process == nil || cmd.ProcessState != nil {
		return
	}
	done := make(chan struct{}, 1)
	go func() {
		_ = cmd.Wait()
		done <- struct{}{}
	}()
	select {
	case <-done:
		return
	case <-time.After(daemonGracefulStopTimeout):
		_ = cmd.Process.Kill()
	}
	// Always reap a force-killed child too. Do not leave a zombie or stale
	// multiprocessing resource tracker behind for the next MLX model load.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

func (s *EditorService) stopAudioDaemonLocked() {
	stopResidentDaemonProcess(s.audioCmd, s.audioIn)
	s.audioCmd, s.audioIn, s.audioOut = nil, nil, nil
	s.audioDaemonModel = ""
}

func (s *EditorService) stopAudioDaemon() {
	s.audioProcessMu.Lock()
	defer s.audioProcessMu.Unlock()
	s.stopAudioDaemonLocked()
}

// ReleaseAudioDaemonForModelSwitch drops an idle resident TTS process when the
// selected model changes. The next audio job lazily starts the newly selected
// model, so choosing a model by itself does not consume MLX memory.
func (s *EditorService) ReleaseAudioDaemonForModelSwitch(oldModel, newModel string) error {
	if strings.TrimSpace(oldModel) == strings.TrimSpace(newModel) {
		return nil
	}
	s.audioJobMu.Lock()
	active := s.audioJobID != 0
	s.audioJobMu.Unlock()
	if active {
		return conflict("当前有音频生成任务正在运行，请完成后再切换 TTS 模型")
	}
	s.stopAudioDaemon()
	return nil
}

func (s *EditorService) translatePage(ctx context.Context, job *model.TextbookJob, d model.TextbookDraft, work string, page int) error {
	var current model.TextbookDraftPage
	if e := s.db.WithContext(ctx).Where("draft_id=? AND position=?", d.ID, page).First(&current).Error; e != nil {
		return e
	}
	modelID := strings.TrimSpace(job.ModelID)
	if modelID == "" {
		modelID = pageTranslationSettings(d, current).TranslationModel
	}

	if ai.IsLocalTranslation(modelID) {
		return s.translateLocalItems(ctx, job, d, current, modelID)
	}

	if e := os.MkdirAll(work, 0750); e != nil {
		return e
	}
	input := filepath.Join(work, fmt.Sprintf("translate-page-%03d-input.json", page))
	output := filepath.Join(work, fmt.Sprintf("translate-page-%03d-output.json", page))
	defer os.Remove(input)
	defer os.Remove(output)
	translationInput, e := s.hydratedPageContent(ctx, d.ID, page, current.Content)
	if e != nil {
		return e
	}
	translationRaw, e := json.Marshal(translationInput)
	if e != nil {
		return e
	}
	if e := os.WriteFile(input, translationRaw, 0640); e != nil {
		return e
	}

	// Cloud translation keeps the existing whole-page structured flow.
	s.stopTranslationDaemon()
	cmd := exec.CommandContext(
		ctx,
		s.cfg.Python,
		filepath.Join("scripts", "translate_page.py"),
		"--input", input,
		"--output", output,
		"--model-id", modelID,
	)
	cmd.Dir = filepath.Dir(filepath.Dir(s.cfg.ResourceRoot))
	cmd.Env = append(os.Environ(), "RESOURCE_ROOT="+work)
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		return e
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	job.Progress, job.Total = 0, 2
	s.db.Model(job).Updates(map[string]any{"progress": job.Progress, "total": job.Total})
	if e := cmd.Start(); e != nil {
		return e
	}
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		var event struct {
			Event    string `json:"event"`
			Stage    string `json:"stage"`
			Progress int    `json:"progress"`
			Total    int    `json:"total"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Event != "progress" || event.Total <= 0 {
			continue
		}
		job.Progress, job.Total = event.Progress, event.Total
		s.db.Model(job).Updates(map[string]any{"progress": job.Progress, "total": job.Total})
	}
	if e := scanner.Err(); e != nil {
		_ = cmd.Process.Kill()
		return e
	}
	if e := cmd.Wait(); e != nil {
		return fmt.Errorf("translate: %w: %s", e, strings.TrimSpace(stderr.String()))
	}

	translated, e := os.ReadFile(output)
	if e != nil {
		return e
	}
	var translatedContent map[string]any
	if e := json.Unmarshal(translated, &translatedContent); e != nil {
		return fmt.Errorf("decode translated page: %w", e)
	}
	translationInfo, _ := ai.Find(modelID)
	job.Progress, job.Total = 2, 2
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := syncTranslationItems(tx, d.ID, page, current.Version, translatedContent, modelID, translationInfo.Provider); e != nil {
			return e
		}
		if e := tx.Model(&model.TextbookDraftPage{}).Where("draft_id=? AND position=?", d.ID, page).Updates(map[string]any{
			"translation_model": modelID,
			"checked":           false,
			"audio_checked":     false,
			"version":           gorm.Expr("version+1"),
		}).Error; e != nil {
			return e
		}
		return tx.Model(&model.TextbookDraft{}).Where("id=?", d.ID).Updates(map[string]any{"version": gorm.Expr("version+1")}).Error
	})
}

type localTranslationResponse struct {
	Done        bool   `json:"done"`
	Task        string `json:"task"`
	Translation string `json:"translation"`
	Meaning     string `json:"meaning"`
	Phonetic    string `json:"phonetic"`
	Error       string `json:"error"`
	ErrorType   string `json:"error_type"`
	Recoverable bool   `json:"recoverable"`
}

type localTranslationItemError struct {
	Message string
}

func (e localTranslationItemError) Error() string { return e.Message }

func (s *EditorService) translateLocalItems(ctx context.Context, job *model.TextbookJob, d model.TextbookDraft, current model.TextbookDraftPage, modelID string) error {
	var items []model.TextbookTranslationItem
	if e := s.db.WithContext(ctx).
		Where("draft_id=? AND page=? AND item_type IN ? AND status IN ?",
			d.ID,
			current.Position,
			[]string{"sentence", "word"},
			[]string{"pending", "review_warning"},
		).
		Order("segment_id ASC, CASE item_type WHEN 'sentence' THEN 0 ELSE 1 END, word_index ASC, id ASC").
		Find(&items).Error; e != nil {
		return e
	}

	job.Progress, job.Total = 0, len(items)
	if e := s.db.Model(job).Updates(map[string]any{"progress": 0, "total": len(items)}).Error; e != nil {
		return e
	}
	if len(items) == 0 {
		return nil
	}

	provider := ""
	if info, ok := ai.Find(modelID); ok {
		provider = info.Provider
	}

	issues := 0
	for index, item := range items {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		request := map[string]any{
			"task":     item.ItemType,
			"text":     item.SourceText,
			"model_id": modelID,
		}

		response, err := s.runLocalTranslationItem(ctx, request, modelID)
		if err != nil {
			var itemErr localTranslationItemError
			if !errors.As(err, &itemErr) {
				return err
			}
			if markErr := s.markLocalTranslationIssue(ctx, item.ID, modelID, provider, itemErr.Error()); markErr != nil {
				return errors.Join(err, markErr)
			}
			issues++
			job.Progress = index + 1
			s.db.Model(job).Updates(map[string]any{"progress": job.Progress, "total": job.Total})
			continue
		}

		updates := map[string]any{
			"translation_model": modelID,
			"provider":          provider,
			"status":            "translated",
			"failure_reason":    nil,
			"revision":          gorm.Expr("revision+1"),
		}
		if item.ItemType == "sentence" {
			updates["translation"] = response.Translation
		} else {
			updates["meaning"] = response.Meaning
			updates["phonetic"] = response.Phonetic
		}
		if e := s.db.WithContext(ctx).Model(&model.TextbookTranslationItem{}).Where("id=?", item.ID).Updates(updates).Error; e != nil {
			return e
		}
		job.Progress = index + 1
		s.db.Model(job).Updates(map[string]any{"progress": job.Progress, "total": job.Total})
	}

	if e := s.db.WithContext(ctx).Model(&model.TextbookDraftPage{}).
		Where("draft_id=? AND position=?", d.ID, current.Position).
		Updates(map[string]any{
			"translation_model": modelID,
			"checked":           false,
			"audio_checked":     false,
			"version":           gorm.Expr("version+1"),
		}).Error; e != nil {
		return e
	}
	if e := s.db.WithContext(ctx).Model(&model.TextbookDraft{}).Where("id=?", d.ID).
		Update("version", gorm.Expr("version+1")).Error; e != nil {
		return e
	}
	if issues > 0 {
		return localTranslationIssuesError{Count: issues}
	}
	return nil
}

func (s *EditorService) markLocalTranslationIssue(ctx context.Context, id uint64, modelID, provider, reason string) error {
	reason = strings.TrimSpace(reason)
	if len(reason) > 2000 {
		reason = reason[:2000]
	}
	return s.db.WithContext(ctx).Model(&model.TextbookTranslationItem{}).Where("id=?", id).Updates(map[string]any{
		"translation_model": modelID,
		"provider":          provider,
		"status":            "review_warning",
		"failure_reason":    reason,
		"revision":          gorm.Expr("revision+1"),
	}).Error
}

func (s *EditorService) runLocalTranslationItem(ctx context.Context, request map[string]any, modelID string) (localTranslationResponse, error) {
	s.translationMu.Lock()
	defer s.translationMu.Unlock()

	s.stopAudioDaemon()

	s.translationProcessMu.Lock()
	if s.translationCmd != nil && s.translationCmd.ProcessState == nil &&
		s.translationDaemonModel != "" && s.translationDaemonModel != modelID {
		s.stopTranslationDaemonLocked()
	}
	if s.translationCmd == nil || s.translationCmd.ProcessState != nil {
		cmd := exec.Command(s.cfg.Python, filepath.Join("scripts", "translation_daemon.py"))
		cmd.Dir = filepath.Dir(filepath.Dir(s.cfg.ResourceRoot))
		cmd.Env = append(os.Environ())
		in, e := cmd.StdinPipe()
		if e != nil {
			s.translationProcessMu.Unlock()
			return localTranslationResponse{}, e
		}
		out, e := cmd.StdoutPipe()
		if e != nil {
			s.translationProcessMu.Unlock()
			return localTranslationResponse{}, e
		}
		cmd.Stderr = os.Stderr
		if e := cmd.Start(); e != nil {
			s.translationProcessMu.Unlock()
			return localTranslationResponse{}, e
		}
		s.translationCmd, s.translationIn, s.translationOut = cmd, in, bufio.NewScanner(out)
		s.translationDaemonModel = modelID
	}
	in, out := s.translationIn, s.translationOut
	s.translationProcessMu.Unlock()

	raw, e := json.Marshal(request)
	if e != nil {
		return localTranslationResponse{}, e
	}
	if _, e = fmt.Fprintf(in, "%s\n", raw); e != nil {
		s.stopTranslationDaemon()
		return localTranslationResponse{}, e
	}

	for {
		line, e := scanDaemonLine(ctx, out, s.stopTranslationDaemon, "translation")
		if e != nil {
			if errors.Is(e, io.EOF) {
				e = errors.New("translation worker exited unexpectedly")
			}
			return localTranslationResponse{}, e
		}
		var response localTranslationResponse
		if json.Unmarshal(line, &response) != nil {
			continue
		}
		if !response.Done {
			continue
		}
		if response.Error != "" {
			message := strings.TrimSpace(response.ErrorType + ": " + response.Error)
			if response.Recoverable {
				return response, localTranslationItemError{Message: message}
			}
			return response, errors.New(message)
		}
		if response.Task == "sentence" {
			if strings.TrimSpace(response.Translation) == "" {
				return response, errors.New("local sentence translation is empty")
			}
			return response, nil
		}
		if response.Task == "word" {
			if strings.TrimSpace(response.Meaning) == "" || strings.TrimSpace(response.Phonetic) == "" {
				return response, errors.New("local word result is incomplete")
			}
			return response, nil
		}
		return response, errors.New("local translation daemon returned an unknown task")
	}
}

func (s *EditorService) stopTranslationDaemonLocked() {
	stopResidentDaemonProcess(s.translationCmd, s.translationIn)
	s.translationCmd, s.translationIn, s.translationOut = nil, nil, nil
	s.translationDaemonModel = ""
}

func (s *EditorService) stopTranslationDaemon() {
	s.translationProcessMu.Lock()
	defer s.translationProcessMu.Unlock()
	s.stopTranslationDaemonLocked()
}

func (s *EditorService) ReleaseTranslationDaemonForModelSwitch(oldModel, newModel string) error {
	if strings.TrimSpace(oldModel) == strings.TrimSpace(newModel) {
		return nil
	}
	if !s.translationMu.TryLock() {
		return conflict("当前有本地翻译任务正在运行，请完成后再切换翻译模型")
	}
	defer s.translationMu.Unlock()
	s.stopTranslationDaemon()
	return nil
}

// runOCRDaemon sends one page request to a process that keeps PaddleOCR's
// detection and recognition models resident. The mutex matches the single
// database worker and also prevents interleaved stdin/stdout protocol frames.
func (s *EditorService) runOCRDaemon(ctx context.Context, job *model.TextbookJob, d model.TextbookDraft, work string, page int) error {
	s.ocrMu.Lock()
	defer s.ocrMu.Unlock()
	if s.ocrCmd == nil || s.ocrCmd.ProcessState != nil {
		cmd := exec.Command(s.cfg.Python, filepath.Join("scripts", "ocr_daemon.py"))
		cmd.Dir = filepath.Dir(filepath.Dir(s.cfg.ResourceRoot))
		cmd.Env = append(os.Environ(), "RESOURCE_ROOT="+work)
		in, e := cmd.StdinPipe()
		if e != nil {
			return e
		}
		out, e := cmd.StdoutPipe()
		if e != nil {
			return e
		}
		cmd.Stderr = os.Stderr
		if e = cmd.Start(); e != nil {
			return e
		}
		s.ocrCmd, s.ocrIn, s.ocrOut = cmd, in, bufio.NewScanner(out)
	}
	request := map[string]any{"input": filepath.Join(s.cfg.EditorRoot, d.ID, "source.pdf"), "book_id": d.BookID,
		"title": d.Title, "grade": d.Grade, "semester": d.Term, "publisher": d.Edition,
		"page": page, "resource_root": work, "model_id": draftModelSettings(d).OCRModel}
	raw, _ := json.Marshal(request)
	if _, e := fmt.Fprintf(s.ocrIn, "%s\n", raw); e != nil {
		s.stopOCRDaemon()
		return e
	}
	for {
		line, e := scanDaemonLine(ctx, s.ocrOut, s.stopOCRDaemon, "OCR")
		if e != nil {
			if errors.Is(e, io.EOF) {
				e = errors.New("OCR worker exited unexpectedly")
			}
			return e
		}
		var event struct {
			Page      int    `json:"page"`
			Progress  int    `json:"progress"`
			Total     int    `json:"total"`
			Stage     string `json:"stage"`
			Error     string `json:"error"`
			RequestID string `json:"request_id"`
			Done      bool   `json:"done"`
		}
		if json.Unmarshal(line, &event) != nil {
			continue
		}
		if event.Error != "" {
			s.stopOCRDaemon()
			return errors.New(event.Error)
		}
		if event.RequestID != "" {
			job.RequestID = event.RequestID
		}
		if event.Total > 0 {
			job.Progress, job.Total = event.Progress, event.Total
			s.db.Model(job).Updates(map[string]any{"progress": job.Progress, "total": job.Total})
		}
		if event.Done {
			return nil
		}
	}
}

func (s *EditorService) stopOCRDaemon() {
	if s.ocrIn != nil {
		_ = s.ocrIn.Close()
	}
	if s.ocrCmd != nil && s.ocrCmd.Process != nil {
		_ = s.ocrCmd.Process.Kill()
	}
	s.ocrCmd, s.ocrIn, s.ocrOut = nil, nil, nil
}
func (s *EditorService) importConverted(ctx context.Context, d model.TextbookDraft, work string) error {
	raw, e := os.ReadFile(filepath.Join(work, d.BookID, "metadata", "book.json"))
	if e != nil {
		return e
	}
	var manifest struct {
		SourcePageCount int `json:"source_page_count"`
		Pages           []struct {
			Position    int    `json:"page"`
			PrintedPage *int   `json:"printed_page"`
			Title       string `json:"title"`
			Unit        string `json:"unit"`
			Image       string `json:"image"`
			Content     string `json:"content"`
		} `json:"pages"`
	}
	if e = json.Unmarshal(raw, &manifest); e != nil {
		return e
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if manifest.SourcePageCount < 1 {
			return errors.New("converted PDF is missing its page count")
		}
		if e := tx.Model(&model.TextbookDraft{}).Where("id=?", d.ID).Update("source_page_count", manifest.SourcePageCount).Error; e != nil {
			return e
		}
		for _, p := range manifest.Pages {
			content, e := os.ReadFile(filepath.Join(work, d.BookID, filepath.FromSlash(p.Content)))
			if e != nil {
				return e
			}
			var displayContent map[string]any
			if e := json.Unmarshal(content, &displayContent); e != nil {
				return e
			}
			storageContent, e := clonePageContent(displayContent)
			if e != nil {
				return e
			}
			stripTranslationFields(storageContent)
			storageRaw, e := json.Marshal(storageContent)
			if e != nil {
				return e
			}
			page := model.TextbookDraftPage{DraftID: d.ID, Position: p.Position, PrintedPage: p.PrintedPage, Title: p.Title, Unit: p.Unit, ImagePath: filepath.ToSlash(filepath.Join("work", d.BookID, p.Image)), Content: string(storageRaw), Preview: p.Position == 1, OCRModel: draftModelSettings(d).OCRModel, TranslationModel: draftModelSettings(d).TranslationModel, TTSModel: draftModelSettings(d).TTSModel, TTSVoice: draftModelSettings(d).TTSVoice}
			var existing model.TextbookDraftPage
			e = tx.Where("draft_id=? AND position=?", d.ID, p.Position).First(&existing).Error
			if errors.Is(e, gorm.ErrRecordNotFound) {
				if e = tx.Create(&page).Error; e != nil {
					return e
				}
				translationInfo, _ := ai.Find(page.TranslationModel)
				if e = syncTranslationItems(tx, d.ID, p.Position, 1, displayContent, page.TranslationModel, translationInfo.Provider); e != nil {
					return e
				}
				if e = s.syncPageAudioState(tx, d, page, true); e != nil {
					return e
				}
				continue
			}
			if e != nil {
				return e
			}
			if e = tx.Model(&existing).Updates(map[string]any{"printed_page": page.PrintedPage, "title": page.Title, "unit": page.Unit, "image_path": page.ImagePath, "content": page.Content, "preview": page.Preview, "ocr_model": page.OCRModel, "translation_model": page.TranslationModel, "tts_model": page.TTSModel, "tts_voice": page.TTSVoice, "checked": false, "audio_checked": false, "version": gorm.Expr("version+1")}).Error; e != nil {
				return e
			}
			page.ID, page.Version = existing.ID, existing.Version+1
			translationInfo, _ := ai.Find(page.TranslationModel)
			if e = syncTranslationItems(tx, d.ID, p.Position, page.Version, displayContent, page.TranslationModel, translationInfo.Provider); e != nil {
				return e
			}
			if e = s.syncPageAudioState(tx, d, page, true); e != nil {
				return e
			}
		}
		return nil
	})
}

func translationSpellingHint(value string) bool {
	return strings.Contains(value, "拼写错误") || strings.Contains(value, "拼写有误")
}

func issuePreview(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 28 {
		return string(runes[:28]) + "…"
	}
	return value
}

func segmentIssueLocation(index int, id, text string) string {
	location := fmt.Sprintf("第%d个片段", index+1)
	if id != "" {
		location += fmt.Sprintf("（%s）", id)
	}
	if preview := issuePreview(text); preview != "" {
		location += fmt.Sprintf("「%s」", preview)
	}
	return location
}

func wordIssueLocation(segmentLocation string, index int, id, text string) string {
	location := fmt.Sprintf("%s · 第%d个单词", segmentLocation, index+1)
	if id != "" {
		location += fmt.Sprintf("（%s）", id)
	}
	if preview := issuePreview(text); preview != "" {
		location += fmt.Sprintf("「%s」", preview)
	}
	return location
}

func publicationIssues(content map[string]any) []string {
	segments, ok := content["segments"].([]any)
	if !ok {
		return []string{"缺少 segments"}
	}
	issues := []string{}
	ids := map[string]bool{}
	for segmentIndex, raw := range segments {
		s, ok := raw.(map[string]any)
		if !ok {
			issues = append(issues, fmt.Sprintf("第%d个片段：片段格式无效", segmentIndex+1))
			continue
		}
		id, _ := s["id"].(string)
		text, _ := s["text"].(string)
		translation, _ := s["translation"].(string)
		words, ok := s["words"].([]any)
		segmentLocation := segmentIssueLocation(segmentIndex, id, text)
		if rawMode, exists := s["audio_mode"]; exists {
			mode, modeOK := rawMode.(string)
			if !modeOK || (mode != "sentence_and_words" && mode != "word_only" && mode != "none") {
				issues = append(issues, segmentLocation+"：片段音频模式无效")
			}
		}
		if id == "" || ids[id] {
			issues = append(issues, segmentLocation+"：片段 ID 为空或重复")
		}
		ids[id] = true
		if strings.TrimSpace(text) == "" || strings.TrimSpace(translation) == "" {
			issues = append(issues, segmentLocation+"：片段英文或翻译为空")
		}
		if !validAnchor(s["anchor"]) {
			issues = append(issues, segmentLocation+"：整句按钮位置无效")
		}
		if !ok || len(words) == 0 {
			issues = append(issues, segmentLocation+"：片段词项为空")
		}
		for wordIndex, rw := range words {
			w, _ := rw.(map[string]any)
			wid, _ := w["id"].(string)
			wt, _ := w["text"].(string)
			meaning, _ := w["meaning"].(string)
			wordLocation := wordIssueLocation(segmentLocation, wordIndex, wid, wt)
			if wid == "" || ids[wid] || wt == "" || meaning == "" {
				issues = append(issues, wordLocation+"：单词 ID、英文或词义无效")
			}
			if translationSpellingHint(meaning) {
				issues = append(issues, wordLocation+"：翻译模型提示拼写错误，请人工核对")
			}
			if regexp.MustCompile(`[A-Za-z]`).MatchString(wt) {
				phonetic, _ := w["phonetic"].(string)
				if strings.TrimSpace(phonetic) == "" {
					issues = append(issues, wordLocation+"：缺少音标")
				}
				if !unitArray(w["box"], 4, true) {
					issues = append(issues, wordLocation+"：单词框无效")
				}
			}
			ids[wid] = true
		}
	}
	sort.Strings(issues)
	return issues
}
func unitArray(value any, length int, positiveSize bool) bool {
	values, ok := value.([]any)
	if !ok || len(values) != length {
		return false
	}
	numbers := make([]float64, length)
	for i, v := range values {
		n, ok := v.(float64)
		if !ok || n < 0 || n > 1 {
			return false
		}
		numbers[i] = n
	}
	if positiveSize && (numbers[2] <= 0 || numbers[3] <= 0 || numbers[0]+numbers[2] > 1.000001 || numbers[1]+numbers[3] > 1.000001) {
		return false
	}
	return true
}
func validAnchor(value any) bool {
	return unitArray(value, 2, false) || unitArray(value, 4, true)
}
func hasSegments(raw string) bool {
	var c map[string]any
	if json.Unmarshal([]byte(raw), &c) != nil {
		return false
	}
	a, _ := c["segments"].([]any)
	return len(a) > 0
}
func hasAudioItems(raw string) bool {
	items, err := expectedAudioItems(raw)
	return err == nil && len(items) > 0
}
func editorAudit(tx *gorm.DB, actor uint64, action, target string, detail any) error {
	raw, _ := json.Marshal(detail)
	return tx.Create(&model.AuditLog{ActorID: actor, Action: action, Target: target, Detail: string(raw)}).Error
}
func copyFile(src, dst string) error {
	in, e := os.Open(src)
	if e != nil {
		return e
	}
	defer in.Close()
	if e = os.MkdirAll(filepath.Dir(dst), 0750); e != nil {
		return e
	}
	out, e := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0640)
	if e != nil {
		return e
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		rel, e := filepath.Rel(src, path)
		if e != nil {
			return e
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0750)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return copyFile(path, target)
	})
}

// publishTTSTree never removes the active files. Versioned WAV files are
// copied first and manifest.json is replaced last, atomically. A request that
// already resolved the old manifest can therefore still open its old file,
// while new requests see the complete new mapping.
func publishTTSTree(src, dst string) error {
	if e := filepath.Walk(src, func(path string, info os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		rel, e := filepath.Rel(src, path)
		if e != nil {
			return e
		}
		if rel == "manifest.json" {
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0750)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return copyFile(path, target)
	}); e != nil {
		return e
	}
	raw, e := os.ReadFile(filepath.Join(src, "manifest.json"))
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	if e = os.MkdirAll(dst, 0750); e != nil {
		return e
	}
	temporary := filepath.Join(dst, ".manifest.json.tmp")
	if e = os.WriteFile(temporary, raw, 0640); e != nil {
		return e
	}
	return os.Rename(temporary, filepath.Join(dst, "manifest.json"))
}
func fileExists(path string) bool {
	info, e := os.Stat(path)
	return e == nil && (info.Mode().IsRegular() || info.IsDir())
}
