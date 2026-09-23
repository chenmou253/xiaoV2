package service

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"xiaov2/internal/model"
)

type TranslationIssue struct {
	ID               uint64  `json:"id"`
	ItemID           string  `json:"item_id"`
	SegmentID        string  `json:"segment_id"`
	ItemType         string  `json:"item_type"`
	WordIndex        uint32  `json:"word_index"`
	SourceText       string  `json:"source_text"`
	Translation      *string `json:"translation,omitempty"`
	Meaning          string  `json:"meaning"`
	Phonetic         string  `json:"phonetic"`
	FailureReason    string  `json:"failure_reason"`
	Status           string  `json:"status"`
	TranslationModel string  `json:"translation_model"`
	Provider         string  `json:"provider"`
	Revision         uint64  `json:"revision"`
}

func (s *EditorService) TranslationIssues(ctx context.Context, draftID string, page int) ([]TranslationIssue, error) {
	var rows []model.TextbookTranslationItem
	if err := s.db.WithContext(ctx).
		Where("draft_id=? AND page=? AND failure_reason IS NOT NULL AND TRIM(failure_reason)<>''", draftID, page).
		Order("segment_id ASC, CASE item_type WHEN 'sentence' THEN 0 ELSE 1 END, word_index ASC, id ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]TranslationIssue, 0, len(rows))
	for _, row := range rows {
		reason := ""
		if row.FailureReason != nil {
			reason = strings.TrimSpace(*row.FailureReason)
		}
		out = append(out, TranslationIssue{
			ID: row.ID, ItemID: row.ItemID, SegmentID: row.SegmentID,
			ItemType: row.ItemType, WordIndex: row.WordIndex, SourceText: row.SourceText,
			Translation: row.Translation, Meaning: row.Meaning, Phonetic: row.Phonetic,
			FailureReason: reason, Status: row.Status, TranslationModel: row.TranslationModel,
			Provider: row.Provider, Revision: row.Revision,
		})
	}
	return out, nil
}

func (s *EditorService) ResolveTranslationIssue(
	ctx context.Context,
	draftID string,
	page int,
	itemID string,
	revision uint64,
	translation string,
	meaning string,
	phonetic string,
	actor uint64,
) error {
	translation = strings.TrimSpace(translation)
	meaning = strings.TrimSpace(meaning)
	phonetic = strings.TrimSpace(phonetic)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var draft model.TextbookDraft
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&draft, "id=?", draftID).Error; err != nil {
			return err
		}
		if draft.Status != "draft" && draft.Status != "failed" && draft.Status != "audio" {
			return conflict("当前草稿正在处理，暂时不能修改翻译异常")
		}
		var item model.TextbookTranslationItem
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("draft_id=? AND page=? AND item_id=?", draftID, page, itemID).
			First(&item).Error; err != nil {
			return err
		}
		if item.Revision != revision {
			return conflict("该翻译项已被修改，请刷新后重试")
		}
		if item.FailureReason == nil || strings.TrimSpace(*item.FailureReason) == "" {
			return conflict("该翻译异常已经处理")
		}

		updates := map[string]any{
			"status":         "translated",
			"failure_reason": nil,
			"reviewed_by":    actor,
			"reviewed_at":    time.Now(),
			"revision":       gorm.Expr("revision+1"),
		}
		switch item.ItemType {
		case "sentence":
			if translation == "" {
				return bad("整句翻译不能为空")
			}
			updates["translation"] = translation
		case "word":
			if meaning == "" || phonetic == "" {
				return bad("单词词义和音标不能为空")
			}
			if utf8.RuneCountInString(meaning) > 100 {
				return bad("单词词义过长")
			}
			if utf8.RuneCountInString(phonetic) > 191 {
				return bad("单词音标过长")
			}
			updates["meaning"] = meaning
			updates["phonetic"] = phonetic
		default:
			return bad("未知翻译项类型")
		}
		result := tx.Model(&model.TextbookTranslationItem{}).
			Where("id=? AND revision=?", item.ID, revision).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return conflict("该翻译项已被修改，请刷新后重试")
		}
		if err := tx.Model(&model.TextbookDraft{}).Where("id=?", draftID).
			Update("version", gorm.Expr("version+1")).Error; err != nil {
			return err
		}
		return editorAudit(tx, actor, "draft.translation.review", draftID, map[string]any{
			"page": page, "item_id": itemID, "item_type": item.ItemType,
		})
	})
}
