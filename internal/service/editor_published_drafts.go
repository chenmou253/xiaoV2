package service

import (
	"errors"

	"xiaov2/internal/model"

	"gorm.io/gorm"
)

// currentPublishedDraftID prefers the explicit publication revision. Records
// created before that field existed fall back to their publication timestamp.
func currentPublishedDraftID(db *gorm.DB, bookID string, revision uint64) (string, error) {
	var current model.TextbookDraft
	err := db.Where("book_id=? AND status=? AND published_revision=?", bookID, "published", revision).
		Order("updated_at DESC").Order("id DESC").First(&current).Error
	if err == nil {
		return current.ID, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}
	publishedIDs := db.Model(&model.TextbookDraft{}).Select("id").Where("book_id=? AND status=?", bookID, "published")
	var latestAudit model.AuditLog
	err = db.Where("action=? AND target IN (?)", "draft.publish", publishedIDs).
		Order("created_at DESC").Order("id DESC").First(&latestAudit).Error
	if err == nil {
		return latestAudit.Target, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}
	err = db.Where("book_id=? AND status=?", bookID, "published").
		Order("updated_at DESC").Order("created_at DESC").Order("id DESC").First(&current).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return current.ID, nil
}
