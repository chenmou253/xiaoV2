package service

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"xiaov2/internal/model"
)

// upgradeLegacyPublishedDraft recognizes copies made before the origin and
// inherited-audio fields existed. Only unchanged page content is inherited;
// an explicit audio, OCR, or page-order action keeps the old review state.
func (s *EditorService) upgradeLegacyPublishedDraft(ctx context.Context, id string) error {
	var draft model.TextbookDraft
	if err := s.db.WithContext(ctx).First(&draft, "id=?", id).Error; err != nil {
		return err
	}
	if draft.SourceKind == "published" {
		return nil
	}
	var copies []model.AuditLog
	if err := s.db.WithContext(ctx).Where("target=? AND action=?", id, "draft.copy").Order("id DESC").Limit(1).Find(&copies).Error; err != nil {
		return err
	}
	if len(copies) == 0 {
		return nil
	}
	var pages []model.TextbookDraftPage
	if err := s.db.WithContext(ctx).Where("draft_id=?", id).Find(&pages).Error; err != nil {
		return err
	}
	if len(pages) == 0 {
		return nil
	}
	for _, page := range pages {
		if !strings.HasPrefix(page.ImagePath, "published:") {
			return nil
		}
	}
	var audits []model.AuditLog
	if err := s.db.WithContext(ctx).
		Where("target=? AND (action LIKE ? OR action IN ?)", id, "draft.audio%", []string{"draft.reorder", "draft.reocr", "draft.page.save", "draft.page.import-json"}).
		Find(&audits).Error; err != nil {
		return err
	}
	var book model.Book
	if err := s.db.WithContext(ctx).Where("book_id=?", draft.BookID).First(&book).Error; err != nil {
		return err
	}
	var copyDetail struct {
		Revision uint64 `json:"revision"`
	}
	if json.Unmarshal([]byte(copies[0].Detail), &copyDetail) != nil {
		return nil
	}
	samePublishedRevision := copyDetail.Revision != 0 && copyDetail.Revision == book.Revision
	var published []model.BookPage
	if err := s.db.WithContext(ctx).Where("book_id=?", draft.BookID).Find(&published).Error; err != nil {
		return err
	}
	byPosition := make(map[int]model.BookPage, len(published))
	for _, page := range published {
		byPosition[page.Position] = page
	}
	globalChange, touchedPages := legacyCopyChanges(audits, book)
	eligible := make([]uint64, 0, len(pages))
	if !globalChange && samePublishedRevision {
		for _, page := range pages {
			base, ok := byPosition[page.Position]
			if !ok || touchedPages[page.Position] || !page.Checked || !samePublishedPageContent(s, draft.BookID, page, base) {
				continue
			}
			eligible = append(eligible, page.ID)
		}
	}
	wantUS, wantGB := publishedCopyAccents(book, s.resources.AudioAccents(book.BookID))
	normalizeAccents := !globalChange && samePublishedRevision && (draft.AmericanEnabled != wantUS || draft.BritishEnabled != wantGB)
	if normalizeAccents {
		if err := s.ensureDraftAudioState(ctx, id); err != nil {
			return err
		}
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked model.TextbookDraft
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "id=?", id).Error; err != nil {
			return err
		}
		if locked.SourceKind == "published" {
			return nil
		}
		updates := map[string]any{"source_kind": "published"}
		if normalizeAccents {
			locked.AmericanEnabled, locked.BritishEnabled = wantUS, wantGB
			updates["american_enabled"] = locked.AmericanEnabled
			updates["british_enabled"] = locked.BritishEnabled
			updates["audio_config_version"] = 1
		}
		if err := tx.Model(&locked).Updates(updates).Error; err != nil {
			return err
		}
		if len(eligible) > 0 {
			if err := tx.Model(&model.TextbookDraftPage{}).Where("id IN ?", eligible).
				Updates(map[string]any{"audio_checked": true, "inherited_audio": true}).Error; err != nil {
				return err
			}
		}
		if normalizeAccents {
			for _, page := range pages {
				if err := s.syncPageAudioState(tx, locked, page, false); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func publishedCopyAccents(book model.Book, playable []string) (bool, bool) {
	configuredUS, configuredGB := book.AmericanEnabled, book.BritishEnabled
	if book.AudioConfigVersion == 0 {
		configuredUS, configuredGB = true, true
	}
	available := make(map[string]bool, len(playable))
	for _, accent := range playable {
		available[accent] = true
	}
	return configuredUS && available["en-US"], configuredGB && available["en-GB"]
}

func legacyCopyChanges(audits []model.AuditLog, book model.Book) (bool, map[int]bool) {
	touched := map[int]bool{}
	initialUS, initialGB := book.AmericanEnabled, book.BritishEnabled
	if book.AudioConfigVersion == 0 {
		initialUS, initialGB = true, true
	}
	for _, audit := range audits {
		if audit.Action == "draft.audio.settings" {
			var settings AudioSettings
			if json.Unmarshal([]byte(audit.Detail), &settings) != nil || settings.AmericanEnabled != initialUS || settings.BritishEnabled != initialGB {
				return true, touched
			}
			continue
		}
		if audit.Action == "draft.reorder" || audit.Action == "draft.audio-missing" || audit.Action == "draft.audio-regenerate-us" || audit.Action == "draft.audio-regenerate-uk" || audit.Action == "draft.audio-retry-failed" || audit.Action == "draft.audio" {
			return true, touched
		}
		var detail struct {
			Page int `json:"page"`
		}
		if json.Unmarshal([]byte(audit.Detail), &detail) != nil || detail.Page < 1 {
			return true, touched
		}
		touched[detail.Page] = true
	}
	return false, touched
}

func samePublishedPageContent(s *EditorService, bookID string, draft model.TextbookDraftPage, published model.BookPage) bool {
	path, err := s.resources.Resolve(bookID, published.ContentPath)
	if err != nil {
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var base, edited map[string]any
	if json.Unmarshal(raw, &base) != nil || json.Unmarshal([]byte(draft.Content), &edited) != nil {
		return false
	}
	stripTranslationFields(base)
	stripTranslationFields(edited)
	baseRaw, baseErr := json.Marshal(base)
	editedRaw, editedErr := json.Marshal(edited)
	return baseErr == nil && editedErr == nil && bytes.Equal(baseRaw, editedRaw)
}
