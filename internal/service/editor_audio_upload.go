package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"xiaov2/internal/model"
)

type manualWordAudioInfo struct {
	NormalizedWord string  `json:"normalized_word"`
	WordCacheKey   string  `json:"word_cache_key"`
	SampleRate     int     `json:"sample_rate"`
	DurationMS     uint    `json:"duration_ms"`
	FileSize       uint64  `json:"file_size"`
	FileSHA256     string  `json:"file_sha256"`
	Peak           float64 `json:"peak"`
	RMS            float64 `json:"rms"`
	ClippingRatio  float64 `json:"clipping_ratio"`
}

var sharedWordTrim = regexp.MustCompile(`^[^A-Za-z0-9_]+|[^A-Za-z0-9_]+$`)

func normalizeSharedWord(text string) string {
	value := strings.TrimSpace(strings.NewReplacer("‘", "'", "’", "'").Replace(text))
	value = strings.Join(strings.Fields(value), " ")
	value = sharedWordTrim.ReplaceAllString(value, "")
	value = strings.ToLower(value)
	if value == "" {
		value = strings.ToLower(strings.TrimSpace(text))
	}
	return value
}

func (s *EditorService) normalizeManualWordAudio(ctx context.Context, source, text string) (string, manualWordAudioInfo, error) {
	root := filepath.Dir(filepath.Dir(s.cfg.ResourceRoot))
	tmp, err := os.CreateTemp("", "xiaov2-manual-word-*.wav")
	if err != nil {
		return "", manualWordAudioInfo{}, err
	}
	output := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(output)
	cmd := exec.CommandContext(ctx, s.cfg.Python, filepath.Join("scripts", "import_manual_word_audio.py"),
		"--input", source, "--output", output, "--text", text)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		_ = os.Remove(output)
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", manualWordAudioInfo{}, bad("上传音频检查失败：" + detail)
	}
	var info manualWordAudioInfo
	if err := json.Unmarshal(bytes.TrimSpace(raw), &info); err != nil {
		_ = os.Remove(output)
		return "", manualWordAudioInfo{}, fmt.Errorf("parse manual word audio result: %w", err)
	}
	if len(info.WordCacheKey) != 64 || info.SampleRate != 24000 || info.FileSize == 0 || info.DurationMS == 0 {
		_ = os.Remove(output)
		return "", manualWordAudioInfo{}, bad("上传音频规范化结果无效")
	}
	return output, info, nil
}

func updateManualWordManifest(manifest *audioManifest, rows []model.TextbookAudioItem, normalized, relative, generation string, actor uint64, info manualWordAudioInfo) {
	now := time.Now().UTC().Format(time.RFC3339)
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row.ItemType != "word" || normalizeSharedWord(row.Text) != normalized {
			continue
		}
		key := audioIssueKey(row.Page, row.ItemID, row.Accent)
		seen[key] = true
		found := false
		for _, entry := range manifest.Items {
			if manifestEntryKey(entry) != key {
				continue
			}
			entry["kind"] = "word"
			entry["text"] = row.Text
			entry["context"] = row.Context
			entry["accent"] = row.Accent
			entry["voice"] = row.VoiceID
			entry["tts_model"] = row.ModelID
			entry["status"] = "ready"
			entry["file"] = relative
			entry["word_cache_key"] = info.WordCacheKey
			entry["generation_id"] = generation
			entry["generation_version"] = generation
			entry["generated_at"] = now
			entry["qa"] = map[string]any{
				"passed": true, "manual_upload": true, "basic_validation_only": true,
				"reviewed_by": actor, "reviewed_at": now,
				"sample_rate": info.SampleRate, "duration_ms": info.DurationMS,
				"peak": info.Peak, "rms": info.RMS, "clipping_ratio": info.ClippingRatio,
			}
			found = true
			break
		}
		if !found {
			wordIndex := any(nil)
			if row.WordIndex != nil {
				wordIndex = *row.WordIndex
			}
			manifest.Items = append(manifest.Items, map[string]any{
				"page": row.Page, "item_id": row.ItemID, "segment_id": row.SegmentID,
				"word_index": wordIndex, "kind": "word", "text": row.Text,
				"context": row.Context, "accent": row.Accent, "voice": row.VoiceID,
				"tts_model": row.ModelID, "status": "ready", "file": relative,
				"word_cache_key": info.WordCacheKey, "generation_id": generation,
				"generation_version": generation, "generated_at": now,
				"qa": map[string]any{
					"passed": true, "manual_upload": true, "basic_validation_only": true,
					"reviewed_by": actor, "reviewed_at": now,
					"sample_rate": info.SampleRate, "duration_ms": info.DurationMS,
					"peak": info.Peak, "rms": info.RMS, "clipping_ratio": info.ClippingRatio,
				},
			})
		}
	}
	filteredFailures := make([]map[string]any, 0, len(manifest.Failures))
	for _, failure := range manifest.Failures {
		if !seen[manifestEntryKey(failure)] {
			filteredFailures = append(filteredFailures, failure)
		}
	}
	manifest.Failures = filteredFailures
}

// UploadWordAudio replaces the canonical shared word audio for the whole draft.
// It deliberately bypasses ASR/Whisper QA because phonics targets such as "ph"
// may correctly contain only a single sound. The Python normalizer still checks
// decodeability, duration, silence and severe clipping before this method can
// publish the file.
func (s *EditorService) UploadWordAudio(ctx context.Context, id string, page int, itemID, visibleText, source string, version, actor uint64) error {
	if itemID == "" || strings.TrimSpace(visibleText) == "" {
		return bad("单词音频参数无效")
	}
	if err := s.ensureDraftAudioState(ctx, id); err != nil {
		return err
	}

	var draft model.TextbookDraft
	if err := s.db.WithContext(ctx).First(&draft, "id=?", id).Error; err != nil {
		return err
	}
	if draft.Version != version {
		return conflict("草稿已更新，请刷新")
	}
	if draft.Status != "draft" && draft.Status != "failed" {
		return conflict("当前状态不能上传单词音频")
	}
	contentItem, err := s.lookupAudioContentItem(s.db.WithContext(ctx), id, page, itemID)
	if err != nil {
		return err
	}
	if contentItem.Kind != "word" {
		return bad("人工上传目前只支持单词/phonics 音频")
	}
	if strings.TrimSpace(contentItem.Text) != strings.TrimSpace(visibleText) {
		return conflict("当前单词有未保存修改，请先保存本页修改后再上传音频")
	}

	normalizedFile, info, err := s.normalizeManualWordAudio(ctx, source, contentItem.Text)
	if err != nil {
		return err
	}
	defer os.Remove(normalizedFile)
	if normalizeSharedWord(contentItem.Text) != info.NormalizedWord {
		return bad("上传音频对应的单词规范化失败")
	}

	s.audioMu.Lock()
	defer s.audioMu.Unlock()

	var active int64
	if err := s.db.WithContext(ctx).Model(&model.TextbookJob{}).
		Where("draft_id=? AND status IN ?", id, []string{"queued", "running"}).Count(&active).Error; err != nil {
		return err
	}
	if active > 0 {
		return conflict("当前有生成任务正在执行，请完成后再上传单词音频")
	}

	var rows []model.TextbookAudioItem
	if err := s.db.WithContext(ctx).Where("draft_id=? AND active=1 AND item_type='word'", id).Find(&rows).Error; err != nil {
		return err
	}
	matched := make([]model.TextbookAudioItem, 0)
	pages := map[int]bool{}
	for _, row := range rows {
		if normalizeSharedWord(row.Text) == info.NormalizedWord && row.Status != "disabled" {
			matched = append(matched, row)
			pages[row.Page] = true
		}
	}
	if len(matched) == 0 {
		return notFound("没有找到该单词的音频条目")
	}

	root := s.audioRoot(draft)
	relative := filepath.ToSlash(filepath.Join("word-cache", info.WordCacheKey[:2], info.WordCacheKey+".wav"))
	formal := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(formal), 0750); err != nil {
		return err
	}
	manifestPath := filepath.Join(root, "manifest.json")
	manifest := audioManifest{SchemaVersion: 2, Items: []map[string]any{}, Failures: []map[string]any{}}
	oldManifest, manifestErr := os.ReadFile(manifestPath)
	manifestExisted := manifestErr == nil
	if manifestErr == nil {
		if err := json.Unmarshal(oldManifest, &manifest); err != nil || manifest.SchemaVersion != 2 {
			return bad("现有音频 manifest 无效，不能安全覆盖共享单词音频")
		}
	} else if !errors.Is(manifestErr, os.ErrNotExist) {
		return manifestErr
	}

	generation := fmt.Sprintf("manual-%d", time.Now().UTC().UnixNano())
	updateManualWordManifest(&manifest, matched, info.NormalizedWord, relative, generation, actor, info)

	backup := formal + ".manual-upload.bak"
	_ = os.Remove(backup)
	hadOldAudio := false
	if _, statErr := os.Stat(formal); statErr == nil {
		if err := os.Rename(formal, backup); err != nil {
			return err
		}
		hadOldAudio = true
	}
	restore := func() {
		_ = os.Remove(formal)
		if hadOldAudio {
			_ = os.Rename(backup, formal)
		}
		if manifestExisted {
			_ = os.WriteFile(manifestPath, oldManifest, 0640)
		} else {
			_ = os.Remove(manifestPath)
		}
	}
	staging := formal + ".manual-upload.tmp"
	_ = os.Remove(staging)
	if err := copyFile(normalizedFile, staging); err != nil {
		if hadOldAudio {
			_ = os.Rename(backup, formal)
		}
		return err
	}
	if err := os.Rename(staging, formal); err != nil {
		_ = os.Remove(staging)
		if hadOldAudio {
			_ = os.Rename(backup, formal)
		}
		return err
	}
	if err := writeAudioManifest(manifestPath, manifest); err != nil {
		restore()
		return err
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked model.TextbookDraft
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "id=?", id).Error; err != nil {
			return err
		}
		if locked.Version != version {
			return conflict("草稿已更新，请刷新")
		}
		var running int64
		if err := tx.Model(&model.TextbookJob{}).Where("draft_id=? AND status IN ?", id, []string{"queued", "running"}).Count(&running).Error; err != nil {
			return err
		}
		if running > 0 {
			return conflict("当前有生成任务正在执行，请完成后再上传单词音频")
		}
		now := time.Now().UTC()
		for _, row := range matched {
			if err := tx.Model(&model.TextbookAudioItem{}).Where("id=? AND active=1", row.ID).Updates(map[string]any{
				"status": "ready", "audio_path": relative, "candidate_path": "",
				"failure_reasons": "[]", "qa_score": nil, "active_job_id": nil,
				"request_id": "manual-upload", "generation_version": generation,
				"file_sha256": info.FileSHA256, "file_size": info.FileSize,
				"duration_ms": info.DurationMS, "reviewed_by": actor, "reviewed_at": now,
				"revision": gorm.Expr("revision+1"),
			}).Error; err != nil {
				return err
			}
		}
		for affectedPage := range pages {
			if err := tx.Model(&model.TextbookDraftPage{}).
				Where("draft_id=? AND position=?", id, affectedPage).
				Updates(map[string]any{"audio_checked": false, "version": gorm.Expr("version+1")}).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&locked).Updates(map[string]any{
			"status": "draft", "version": gorm.Expr("version+1"), "updated_by": actor,
		}).Error; err != nil {
			return err
		}
		return editorAudit(tx, actor, "draft.audio.upload-word", id, map[string]any{
			"page": page, "item_id": itemID, "text": contentItem.Text,
			"word_cache_key": info.WordCacheKey, "file": relative,
			"affected_pages": len(pages), "affected_items": len(matched),
			"duration_ms": info.DurationMS, "file_size": info.FileSize,
		})
	})
	if err != nil {
		restore()
		return err
	}
	_ = os.Remove(backup)
	return nil
}
