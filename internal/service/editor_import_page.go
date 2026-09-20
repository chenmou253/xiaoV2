package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"xiaov2/internal/model"
)

type ImportPageJSONInput struct {
	OCRPath     string
	ContentPath string
}

type ImportPageJSONResult struct {
	DraftID    string
	BookID     string
	Page       int
	OCRPath    string
	ContentPath string
}

func decodeImportedContent(raw []byte) (map[string]any, error) {
	var content map[string]any
	if err := json.Unmarshal(raw, &content); err != nil {
		return nil, fmt.Errorf("decode content JSON: %w", err)
	}
	segments, ok := content["segments"].([]any)
	if !ok {
		return nil, errors.New("content JSON must contain a segments array")
	}
	ids := make(map[string]struct{})
	for index, rawSegment := range segments {
		segment, ok := rawSegment.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("segment %d is not an object", index)
		}
		id, _ := segment["id"].(string)
		text, _ := segment["text"].(string)
		if strings.TrimSpace(id) == "" || strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("segment %d must contain non-empty id and text", index)
		}
		if _, duplicate := ids[id]; duplicate {
			return nil, fmt.Errorf("duplicate segment id %q", id)
		}
		ids[id] = struct{}{}
		if !validAnchor(segment["anchor"]) {
			return nil, fmt.Errorf("segment %q has invalid anchor", id)
		}
		words, ok := segment["words"].([]any)
		if !ok {
			return nil, fmt.Errorf("segment %q must contain a words array", id)
		}
		for wordIndex, rawWord := range words {
			word, ok := rawWord.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("segment %q word %d is not an object", id, wordIndex)
			}
			wordID, _ := word["id"].(string)
			wordText, _ := word["text"].(string)
			if strings.TrimSpace(wordID) == "" || strings.TrimSpace(wordText) == "" {
				return nil, fmt.Errorf("segment %q word %d must contain non-empty id and text", id, wordIndex)
			}
			if _, duplicate := ids[wordID]; duplicate {
				return nil, fmt.Errorf("duplicate word id %q", wordID)
			}
			ids[wordID] = struct{}{}
			if !unitArray(word["box"], 4, true) {
				return nil, fmt.Errorf("word %q has invalid box", wordText)
			}
			for _, optional := range []string{"meaning", "phonetic"} {
				if value, exists := word[optional]; exists {
					if _, ok := value.(string); !ok {
						return nil, fmt.Errorf("word %q field %s must be a string", wordText, optional)
					}
				}
			}
		}
		if value, exists := segment["translation"]; exists {
			if _, ok := value.(string); !ok {
				return nil, fmt.Errorf("segment %q translation must be a string", id)
			}
		}
	}
	return content, nil
}

func decodeImportedOCR(raw []byte) (map[string]any, error) {
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("decode OCR JSON: %w", err)
	}
	rows, ok := data["rows"].([]any)
	if !ok {
		return nil, errors.New("OCR JSON must contain a rows array")
	}
	wordCount := 0
	for rowIndex, rawRow := range rows {
		row, ok := rawRow.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("OCR row %d is not an object", rowIndex)
		}
		if value, exists := row["box"]; exists && !unitArray(value, 4, true) {
			return nil, fmt.Errorf("OCR row %d has invalid box", rowIndex)
		}
		words, ok := row["words"].([]any)
		if !ok {
			return nil, fmt.Errorf("OCR row %d must contain a words array", rowIndex)
		}
		for wordIndex, rawWord := range words {
			word, ok := rawWord.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("OCR row %d word %d is not an object", rowIndex, wordIndex)
			}
			text, _ := word["text"].(string)
			if strings.TrimSpace(text) == "" {
				return nil, fmt.Errorf("OCR row %d word %d has empty text", rowIndex, wordIndex)
			}
			if !unitArray(word["box"], 4, true) {
				return nil, fmt.Errorf("OCR word %q has invalid box", text)
			}
			wordCount++
		}
	}
	if len(rows) > 0 && wordCount == 0 {
		return nil, errors.New("OCR JSON contains rows but no word boxes")
	}
	return data, nil
}

func atomicWriteJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0640); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(raw); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}

func restoreFile(path string, previous []byte, existed bool) error {
	if !existed {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return err
	}
	return os.WriteFile(path, previous, 0640)
}

func readOptionalFile(path string) ([]byte, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return raw, err == nil, err
}

// ImportPageJSON replaces one already-generated draft page with manually
// reviewed OCR/content JSON. It intentionally refuses to run while any draft
// job is queued or running, resets prior audio state, writes the canonical
// work-tree files, and updates textbook_draft_pages.content so the admin editor
// sees the imported page immediately.
func (s *EditorService) ImportPageJSON(ctx context.Context, draftID string, page int, input ImportPageJSONInput) (ImportPageJSONResult, error) {
	if strings.TrimSpace(draftID) == "" || page < 1 {
		return ImportPageJSONResult{}, bad("draft-id 和 page 必须有效")
	}
	ocrRaw, err := os.ReadFile(input.OCRPath)
	if err != nil {
		return ImportPageJSONResult{}, fmt.Errorf("read OCR JSON: %w", err)
	}
	contentRaw, err := os.ReadFile(input.ContentPath)
	if err != nil {
		return ImportPageJSONResult{}, fmt.Errorf("read content JSON: %w", err)
	}
	ocr, err := decodeImportedOCR(ocrRaw)
	if err != nil {
		return ImportPageJSONResult{}, err
	}
	content, err := decodeImportedContent(contentRaw)
	if err != nil {
		return ImportPageJSONResult{}, err
	}
	normalizeContentAnchors(content)

	var draft model.TextbookDraft
	if err := s.db.WithContext(ctx).First(&draft, "id=?", draftID).Error; err != nil {
		return ImportPageJSONResult{}, err
	}
	if supplied, ok := content["book_id"].(string); ok && supplied != "" && supplied != draft.BookID {
		return ImportPageJSONResult{}, bad("content JSON 的 book_id 与草稿不一致")
	}
	if supplied, ok := content["page"].(float64); ok && int(supplied) != page {
		return ImportPageJSONResult{}, bad("content JSON 的 page 与 --page 不一致")
	}
	content["book_id"] = draft.BookID
	content["page"] = page
	content["reviewed"] = false
	delete(content, "_note")
	source, _ := content["source"].(map[string]any)
	if source == nil {
		source = make(map[string]any)
	}
	source["ocr"] = fmt.Sprintf("ocr/page-%03d.json", page)
	content["source"] = source
	if _, exists := ocr["method"]; !exists {
		ocr["method"] = "manual-import"
	}
	if _, exists := ocr["provider"]; !exists {
		ocr["provider"] = "manual"
	}
	if _, exists := ocr["model"]; !exists {
		ocr["model"] = "manual-review"
	}

	dbRaw, err := json.Marshal(content)
	if err != nil {
		return ImportPageJSONResult{}, err
	}
	if len(dbRaw) > 4<<20 {
		return ImportPageJSONResult{}, bad("页面内容超过 4 MiB")
	}

	workBook := filepath.Join(s.cfg.EditorRoot, draft.ID, "work", draft.BookID)
	ocrTarget := filepath.Join(workBook, "ocr", fmt.Sprintf("page-%03d.json", page))
	contentTarget := filepath.Join(workBook, "metadata", "pages", fmt.Sprintf("page-%03d.json", page))
	oldOCR, oldOCRExists, err := readOptionalFile(ocrTarget)
	if err != nil {
		return ImportPageJSONResult{}, err
	}
	oldContent, oldContentExists, err := readOptionalFile(contentTarget)
	if err != nil {
		return ImportPageJSONResult{}, err
	}
	if err := atomicWriteJSON(ocrTarget, ocr); err != nil {
		return ImportPageJSONResult{}, fmt.Errorf("write OCR JSON: %w", err)
	}
	if err := atomicWriteJSON(contentTarget, content); err != nil {
		_ = restoreFile(ocrTarget, oldOCR, oldOCRExists)
		return ImportPageJSONResult{}, fmt.Errorf("write content JSON: %w", err)
	}

	txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var lockedDraft model.TextbookDraft
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lockedDraft, "id=?", draft.ID).Error; err != nil {
			return err
		}
		if lockedDraft.Status != "draft" && lockedDraft.Status != "failed" {
			return conflict("当前草稿不处于可编辑状态")
		}
		var active int64
		if err := tx.Model(&model.TextbookJob{}).Where("draft_id=? AND status IN ?", draft.ID, []string{"queued", "running"}).Count(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			return conflict("当前草稿仍有 OCR/TTS 任务排队或运行，请任务结束后再导入")
		}
		var current model.TextbookDraftPage
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("draft_id=? AND position=?", draft.ID, page).First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return bad(fmt.Sprintf("第 %d 页尚未生成，不能人工导入", page))
			}
			return err
		}
		if err := s.clearDraftPageArtifacts(lockedDraft.ID, lockedDraft.BookID, page); err != nil {
			return err
		}
		settings := draftModelSettings(lockedDraft)
		if err := tx.Model(&current).Updates(map[string]any{
			"content":       string(dbRaw),
			"checked":       false,
			"audio_checked": false,
			"tts_model":     settings.TTSModel,
			"tts_voice":     settings.TTSVoice,
			"version":       gorm.Expr("version+1"),
		}).Error; err != nil {
			return err
		}
		current.Content = string(dbRaw)
		current.Checked = false
		current.AudioChecked = false
		current.TTSModel = settings.TTSModel
		current.TTSVoice = settings.TTSVoice
		current.Version++
		if err := s.syncPageAudioState(tx, lockedDraft, current, true); err != nil {
			return err
		}
		actor := lockedDraft.UpdatedBy
		if actor == 0 {
			actor = lockedDraft.CreatedBy
		}
		draftUpdates := map[string]any{"version": gorm.Expr("version+1"), "updated_by": actor}
		if lockedDraft.Status == "failed" {
			draftUpdates["status"] = "draft"
		}
		if err := tx.Model(&lockedDraft).Updates(draftUpdates).Error; err != nil {
			return err
		}
		return editorAudit(tx, actor, "draft.page.import-json", draft.ID, map[string]any{
			"page": page,
			"ocr_file": filepath.Base(input.OCRPath),
			"content_file": filepath.Base(input.ContentPath),
		})
	})
	if txErr != nil {
		restoreErr := errors.Join(
			restoreFile(ocrTarget, oldOCR, oldOCRExists),
			restoreFile(contentTarget, oldContent, oldContentExists),
		)
		if restoreErr != nil {
			return ImportPageJSONResult{}, errors.Join(txErr, fmt.Errorf("restore work files: %w", restoreErr))
		}
		return ImportPageJSONResult{}, txErr
	}
	return ImportPageJSONResult{
		DraftID: draft.ID,
		BookID: draft.BookID,
		Page: page,
		OCRPath: ocrTarget,
		ContentPath: contentTarget,
	}, nil
}
