package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"xiaov2/internal/ai"
	"xiaov2/internal/model"
	"xiaov2/internal/tts"
)

type AudioIssue struct {
	Page         int      `json:"page"`
	ItemID       string   `json:"item_id"`
	SegmentID    string   `json:"segment_id"`
	Kind         string   `json:"kind"`
	Text         string   `json:"text"`
	Context      string   `json:"context"`
	Accent       string   `json:"accent"`
	Reasons      []string `json:"reasons"`
	Attempt      int      `json:"attempt"`
	FinalScore   float64  `json:"final_score"`
	HasCandidate bool     `json:"has_candidate"`
	Status       string   `json:"status"`
	JobID        uint64   `json:"job_id,omitempty"`
	ModelID      string   `json:"model_id"`
	Provider     string   `json:"provider"`
	VoiceID      string   `json:"voice_id"`
}

type audioManifest struct {
	SchemaVersion int              `json:"schema_version"`
	Model         string           `json:"model,omitempty"`
	Items         []map[string]any `json:"items"`
	Failures      []map[string]any `json:"failures"`
}

type audioContentItem struct {
	SegmentID string
	Kind      string
	Text      string
	Context   string
	WordIndex *int
}

func audioIssueKey(page int, itemID, accent string) string {
	return fmt.Sprintf("%d\x00%s\x00%s", page, itemID, accent)
}

func numberValue(value any) float64 {
	switch number := value.(type) {
	case float64:
		return number
	case int:
		return float64(number)
	case json.Number:
		result, _ := number.Float64()
		return result
	default:
		return 0
	}
}

func stringSlice(value any) []string {
	values, _ := value.([]any)
	out := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok && text != "" {
			out = append(out, text)
		}
	}
	return out
}

func readAudioManifest(path string) (audioManifest, error) {
	var manifest audioManifest
	raw, err := os.ReadFile(path)
	if err != nil {
		return manifest, err
	}
	if err = json.Unmarshal(raw, &manifest); err != nil {
		return manifest, err
	}
	if manifest.SchemaVersion != 2 {
		return manifest, fmt.Errorf("unsupported audio manifest schema")
	}
	if manifest.Items == nil {
		manifest.Items = make([]map[string]any, 0)
	}
	if manifest.Failures == nil {
		manifest.Failures = make([]map[string]any, 0)
	}
	return manifest, nil
}

func writeAudioManifest(path string, manifest audioManifest) error {
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err = os.WriteFile(temporary, append(raw, '\n'), 0640); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func manifestEntryKey(entry map[string]any) string {
	return audioIssueKey(int(numberValue(entry["page"])), fmt.Sprint(entry["item_id"]), fmt.Sprint(entry["accent"]))
}

func (s *EditorService) audioRoot(d model.TextbookDraft) string {
	return filepath.Join(s.cfg.EditorRoot, d.ID, "work", d.BookID, "tts")
}

func (s *EditorService) AudioIssues(ctx context.Context, id string, page int) ([]AudioIssue, error) {
	if err := s.ensureDraftAudioState(ctx, id); err != nil {
		return nil, err
	}
	var rows []model.TextbookAudioItem
	if err := s.db.WithContext(ctx).Where("draft_id=? AND page=? AND active=1 AND status IN ?", id, page, []string{"qa_failed", "manual_review_required", "queued", "generating"}).Order("item_id,accent").Find(&rows).Error; err != nil {
		return nil, err
	}
	var jobs []model.TextbookJob
	if err := s.db.WithContext(ctx).Where("draft_id=? AND page=? AND kind=? AND status IN ?", id, page, "audio-item", []string{"queued", "running"}).Find(&jobs).Error; err != nil {
		return nil, err
	}
	active := make(map[uint64]model.TextbookJob, len(jobs))
	for _, job := range jobs {
		active[job.ID] = job
	}
	issues := make([]AudioIssue, 0, len(rows))
	for _, row := range rows {
		hasCandidate := false
		if row.CandidatePath != "" {
			if path, err := s.resolveAudioStatePath(id, row.CandidatePath); err == nil {
				if info, statErr := os.Stat(path); statErr == nil && info.Mode().IsRegular() {
					hasCandidate = true
				}
			}
		}
		issue := AudioIssue{
			Page: page, ItemID: row.ItemID, SegmentID: row.SegmentID,
			Kind: row.ItemType, Text: row.Text, Context: row.Context, Accent: row.Accent,
			Reasons: parseFailureReasons(row.FailureReasons), Attempt: row.AttemptCount,
			HasCandidate: hasCandidate, Status: row.Status,
			ModelID: row.ModelID, Provider: row.Provider, VoiceID: row.VoiceID,
		}
		if row.QAScore != nil {
			issue.FinalScore = *row.QAScore
		}
		if row.ActiveJobID != nil {
			job, ok := active[*row.ActiveJobID]
			if ok {
				issue.Status, issue.JobID = job.Status, job.ID
			}
		}
		issues = append(issues, issue)
	}
	sortAudioIssues(issues)
	return issues, nil
}

func retryAudioIssueSettings(draft model.TextbookDraft, pageSettings ai.Settings, item model.TextbookAudioItem, accent, requestedModelID, requestedVoiceID string) (ai.Model, string, error) {
	if _, enabled := draftVoices(draft)[accent]; !enabled {
		return ai.Model{}, "", conflict("该口音已经关闭")
	}
	modelID := strings.TrimSpace(requestedModelID)
	if modelID == "" {
		modelID = item.ModelID
	}
	if modelID == "" {
		modelID = pageSettings.TTSModel
	}
	selected, ok := ai.Find(modelID)
	if !ok || selected.Type != "tts" || !selected.Enabled {
		return ai.Model{}, "", bad("TTS 模型无效")
	}
	if !selected.Available {
		return ai.Model{}, "", bad("TTS 模型当前不可用：" + selected.UnavailableReason)
	}
	voiceID := strings.TrimSpace(requestedVoiceID)
	if voiceID == "" && modelID == item.ModelID {
		voiceID = item.VoiceID
	}
	if voiceID == "" {
		if selected.ID == ai.LocalTTSModel {
			if accent == tts.AccentGB {
				voiceID = tts.DefaultBritishVoice
			} else {
				voiceID = tts.DefaultAmericanVoice
			}
		} else {
			voiceID = selected.DefaultVoice
		}
	}
	if !ai.ValidVoice(selected.ID, voiceID) {
		return ai.Model{}, "", bad("该模型不支持所选音色")
	}
	if selected.ID == ai.LocalTTSModel {
		expected := tts.DefaultAmericanVoice
		if accent == tts.AccentGB {
			expected = tts.DefaultBritishVoice
		}
		if voiceID != expected {
			return ai.Model{}, "", bad("本地 TTS 的美式和英式音色需要分别使用 aiden 和 ryan")
		}
	}
	return selected, voiceID, nil
}

func (s *EditorService) RetryAudioIssue(ctx context.Context, id string, page int, itemID, accent, requestedModelID, requestedVoiceID string, version, actor uint64) error {
	if itemID == "" || (accent != "en-US" && accent != "en-GB") {
		return bad("音频条目或口音无效")
	}
	if err := s.ensureDraftAudioState(ctx, id); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var draft model.TextbookDraft
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&draft, "id=?", id).Error; err != nil {
			return err
		}
		if draft.Version != version {
			return conflict("草稿已更新，请刷新")
		}
		if draft.Status != "draft" && draft.Status != "failed" {
			return conflict("当前状态不能重新生成音频")
		}
		var audioItem model.TextbookAudioItem
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("draft_id=? AND page=? AND item_id=? AND accent=? AND active=1", id, page, itemID, accent).First(&audioItem).Error; err != nil {
			return err
		}
		if audioItem.Status != "qa_failed" && audioItem.Status != "manual_review_required" {
			return conflict("该音频已经通过或没有待处理记录")
		}
		if audioItem.ActiveJobID != nil {
			return conflict("这条音频正在生成，请等待完成")
		}
		var duplicate int64
		if err := tx.Model(&model.TextbookJob{}).Where("draft_id=? AND page=? AND kind=? AND item_id=? AND accent=? AND status IN ?", id, page, "audio-item", itemID, accent, []string{"queued", "running"}).Count(&duplicate).Error; err != nil {
			return err
		}
		if duplicate > 0 {
			return conflict("这条音频正在生成，请等待完成")
		}
		var draftPage model.TextbookDraftPage
		if err := tx.Where("draft_id=? AND position=?", id, page).First(&draftPage).Error; err != nil {
			return err
		}
		selected, voiceID, err := retryAudioIssueSettings(draft, pageAudioSettings(draft, draftPage), audioItem, accent, requestedModelID, requestedVoiceID)
		if err != nil {
			return err
		}
		job := model.TextbookJob{DraftID: id, Kind: "audio-item", Page: page, ItemID: itemID, Accent: accent, ModelID: selected.ID, Provider: selected.Provider, VoiceID: voiceID, Status: "queued", Total: 1, Priority: 100}
		if err := tx.Create(&job).Error; err != nil {
			return err
		}
		if err := tx.Model(&audioItem).Updates(map[string]any{"status": "queued", "active_job_id": job.ID, "model_id": selected.ID, "provider": selected.Provider, "voice_id": voiceID, "revision": gorm.Expr("revision+1")}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.TextbookDraftPage{}).Where("draft_id=? AND position=?", id, page).Update("audio_checked", false).Error; err != nil {
			return err
		}
		if err := tx.Model(&draft).Updates(map[string]any{"status": "draft", "version": gorm.Expr("version+1"), "updated_by": actor}).Error; err != nil {
			return err
		}
		return editorAudit(tx, actor, "draft.audio.retry-item", id, map[string]any{"page": page, "item_id": itemID, "accent": accent, "model_id": selected.ID, "voice_id": voiceID, "job_id": job.ID})
	})
}

func (s *EditorService) FailedAudio(ctx context.Context, id string, page int, itemID, accent string) (string, error) {
	if err := s.ensureDraftAudioState(ctx, id); err != nil {
		return "", err
	}
	var item model.TextbookAudioItem
	if err := s.db.WithContext(ctx).Where("draft_id=? AND page=? AND item_id=? AND accent=? AND active=1", id, page, itemID, accent).First(&item).Error; err != nil {
		return "", err
	}
	path, err := s.resolveAudioStatePath(id, item.CandidatePath)
	if err != nil {
		return "", err
	}
	if info, statErr := os.Stat(path); statErr == nil && info.Mode().IsRegular() {
		return path, nil
	}
	return "", os.ErrNotExist
}

func (s *EditorService) ApproveAudioIssue(ctx context.Context, id string, page int, itemID, accent string, version, actor uint64) error {
	if itemID == "" || (accent != "en-US" && accent != "en-GB") {
		return bad("音频条目或口音无效")
	}
	if err := s.ensureDraftAudioState(ctx, id); err != nil {
		return err
	}
	s.audioMu.Lock()
	defer s.audioMu.Unlock()
	var draft model.TextbookDraft
	if err := s.db.WithContext(ctx).First(&draft, "id=?", id).Error; err != nil {
		return err
	}
	if draft.Version != version {
		return conflict("草稿已更新，请刷新")
	}
	if draft.Status != "draft" && draft.Status != "failed" {
		return conflict("当前状态不能审核音频")
	}
	var active int64
	if err := s.db.WithContext(ctx).Model(&model.TextbookJob{}).Where("draft_id=? AND page=? AND kind=? AND item_id=? AND accent=? AND status IN ?", id, page, "audio-item", itemID, accent, []string{"queued", "running"}).Count(&active).Error; err != nil {
		return err
	}
	if active > 0 {
		return conflict("这条音频正在生成，请等待完成")
	}
	var audioItem model.TextbookAudioItem
	if err := s.db.WithContext(ctx).Where("draft_id=? AND page=? AND item_id=? AND accent=? AND active=1", id, page, itemID, accent).First(&audioItem).Error; err != nil {
		return err
	}
	if audioItem.Status != "qa_failed" && audioItem.Status != "manual_review_required" {
		return conflict("该音频已经通过或没有待处理记录")
	}
	contentItem, err := s.lookupAudioContentItem(s.db.WithContext(ctx), id, page, itemID)
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(s.audioRoot(draft), "manifest.json")
	manifest, err := readAudioManifest(manifestPath)
	if err != nil {
		return err
	}
	key := audioIssueKey(page, itemID, accent)
	var failure map[string]any
	for index := len(manifest.Failures) - 1; index >= 0; index-- {
		if manifestEntryKey(manifest.Failures[index]) == key {
			failure = manifest.Failures[index]
			break
		}
	}
	if failure == nil {
		failure = map[string]any{
			"page": page, "item_id": itemID, "segment_id": audioItem.SegmentID,
			"type": audioItem.ItemType, "text": audioItem.Text, "context": audioItem.Context,
			"accent": accent, "voice": audioItem.VoiceID, "tts_model": audioItem.ModelID,
			"generation_id": audioItem.GenerationVersion, "failed_file": audioItem.CandidatePath,
			"attempt": audioItem.AttemptCount, "final_score": audioItem.QAScore,
			"reasons": parseFailureReasons(audioItem.FailureReasons),
		}
	}
	candidate, err := s.resolveAudioStatePath(id, audioItem.CandidatePath)
	if err == nil {
		if info, statErr := os.Stat(candidate); statErr != nil || !info.Mode().IsRegular() {
			err = os.ErrNotExist
		}
	}
	if err != nil {
		candidate, err = s.failedAudioCandidate(draft, page, itemID, accent, failure)
	}
	if err != nil || candidate == "" {
		return conflict("没有可供人工审核的失败音频")
	}
	digest := sha256.Sum256([]byte(itemID))
	name := regexp.MustCompile(`[^A-Za-z0-9_-]+`).ReplaceAllString(itemID, "-")
	name = strings.Trim(name, "-")
	if name == "" {
		name = "item"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	name += "-" + hex.EncodeToString(digest[:4])
	accentName := "us"
	if accent == "en-GB" {
		accentName = "uk"
	}
	relative := filepath.Join(fmt.Sprintf("page-%03d", page), "manual", name, fmt.Sprintf("%s-manual-%d.wav", accentName, time.Now().UnixNano()))
	formal := filepath.Join(s.audioRoot(draft), relative)
	if err = copyFile(candidate, formal); err != nil {
		return err
	}
	filteredItems := make([]map[string]any, 0, len(manifest.Items)+1)
	for _, item := range manifest.Items {
		if manifestEntryKey(item) != key {
			filteredItems = append(filteredItems, item)
		}
	}
	wordIndex := any(nil)
	if contentItem.WordIndex != nil {
		wordIndex = *contentItem.WordIndex
	}
	filteredItems = append(filteredItems, map[string]any{
		"page": page, "item_id": itemID, "segment_id": contentItem.SegmentID,
		"word_index": wordIndex, "kind": contentItem.Kind, "text": contentItem.Text,
		"context": contentItem.Context, "accent": accent, "voice": failure["voice"],
		"tts_model": failure["tts_model"], "generation_id": failure["generation_id"],
		"file": filepath.ToSlash(relative),
		"qa":   map[string]any{"passed": true, "manual_override": true, "reviewed_by": actor, "reviewed_at": time.Now().UTC().Format(time.RFC3339), "original_failure": failure},
	})
	manifest.Items = filteredItems
	filteredFailures := make([]map[string]any, 0, len(manifest.Failures))
	for _, item := range manifest.Failures {
		if manifestEntryKey(item) != key {
			filteredFailures = append(filteredFailures, item)
		}
	}
	manifest.Failures = filteredFailures
	if err = writeAudioManifest(manifestPath, manifest); err != nil {
		_ = os.Remove(formal)
		return err
	}
	failedPageRoot := filepath.Join(s.audioRoot(draft), "audio_failed", fmt.Sprintf("page-%03d", page))
	if relativeCandidate, relErr := filepath.Rel(failedPageRoot, candidate); relErr == nil && relativeCandidate != ".." && !strings.HasPrefix(relativeCandidate, ".."+string(filepath.Separator)) {
		_ = os.RemoveAll(filepath.Dir(candidate))
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		result := tx.Model(&model.TextbookAudioItem{}).Where("id=? AND revision=? AND active=1 AND status IN ?", audioItem.ID, audioItem.Revision, []string{"qa_failed", "manual_review_required"}).Updates(map[string]any{
			"status": "ready", "audio_path": filepath.ToSlash(relative), "candidate_path": "",
			"failure_reasons": "[]", "active_job_id": nil, "reviewed_by": actor,
			"reviewed_at": now, "revision": gorm.Expr("revision+1"),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return conflict("该音频状态已经变化，请刷新后重试")
		}
		if err := tx.Model(&model.TextbookDraftPage{}).Where("draft_id=? AND position=?", id, page).Update("audio_checked", false).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.TextbookDraft{}).Where("id=?", id).Updates(map[string]any{"status": "draft", "version": gorm.Expr("version+1"), "updated_by": actor}).Error; err != nil {
			return err
		}
		return editorAudit(tx, actor, "draft.audio.approve-item", id, map[string]any{"page": page, "item_id": itemID, "accent": accent})
	})
}

func (s *EditorService) lookupAudioContentItem(db *gorm.DB, id string, page int, itemID string) (audioContentItem, error) {
	var draftPage model.TextbookDraftPage
	if err := db.Where("draft_id=? AND position=?", id, page).First(&draftPage).Error; err != nil {
		return audioContentItem{}, err
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
	if err := json.Unmarshal([]byte(draftPage.Content), &content); err != nil {
		return audioContentItem{}, err
	}
	for _, segment := range content.Segments {
		if segment.ID == itemID && segment.AudioMode != "word_only" && segment.AudioMode != "none" {
			return audioContentItem{SegmentID: segment.ID, Kind: "sentence", Text: segment.Text, Context: segment.Text}, nil
		}
		for index, word := range segment.Words {
			if word.ID == itemID && segment.AudioMode != "none" {
				wordIndex := index
				return audioContentItem{SegmentID: segment.ID, Kind: "word", Text: word.Text, Context: segment.Text, WordIndex: &wordIndex}, nil
			}
		}
	}
	return audioContentItem{}, notFound("音频条目不存在")
}

func (s *EditorService) failedAudioCandidate(draft model.TextbookDraft, page int, itemID, accent string, failure map[string]any) (string, error) {
	ttsRoot := s.audioRoot(draft)
	if relative, ok := failure["failed_file"].(string); ok && relative != "" {
		candidate := filepath.Clean(filepath.Join(ttsRoot, filepath.FromSlash(relative)))
		if rel, err := filepath.Rel(ttsRoot, candidate); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			if info, statErr := os.Stat(candidate); statErr == nil && info.Mode().IsRegular() {
				return candidate, nil
			}
		}
	}
	root := filepath.Join(ttsRoot, "audio_failed", fmt.Sprintf("page-%03d", page))
	var newest string
	var newestTime time.Time
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		var record map[string]any
		if json.Unmarshal(raw, &record) != nil || manifestEntryKey(record) != audioIssueKey(page, itemID, accent) {
			return nil
		}
		generation := fmt.Sprint(record["generation_id"])
		matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), generation+"-attempt-*.wav"))
		for _, match := range matches {
			if info, statErr := os.Stat(match); statErr == nil && info.ModTime().After(newestTime) {
				newest, newestTime = match, info.ModTime()
			}
		}
		return nil
	})
	if os.IsNotExist(err) {
		return "", nil
	}
	return newest, err
}
