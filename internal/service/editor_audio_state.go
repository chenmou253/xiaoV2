package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"xiaov2/internal/ai"
	"xiaov2/internal/model"
	"xiaov2/internal/resource"
	"xiaov2/internal/tts"
)

type expectedAudioItem struct {
	ItemID    string
	SegmentID string
	ItemType  string
	WordIndex *int
	Text      string
	Context   string
}

type DraftRuntimeStatus struct {
	DraftID         string              `json:"draft_id"`
	BookID          string              `json:"book_id"`
	Title           string              `json:"title"`
	Revision        uint64              `json:"revision"`
	DraftStatus     string              `json:"draft_status"`
	Jobs            []model.TextbookJob `json:"jobs"`
	PageIssueCounts map[int]int64       `json:"page_issue_counts"`
}

type AudioReviewQueueItem struct {
	DraftID       string    `json:"draft_id"`
	BookID        string    `json:"book_id"`
	Title         string    `json:"title"`
	Page          int       `json:"page"`
	IssueCount    int64     `json:"issue_count"`
	OldestIssueAt time.Time `json:"oldest_issue_at"`
}

func (s *EditorService) BackfillAudioState(ctx context.Context) (int, error) {
	var drafts []model.TextbookDraft
	if err := s.db.WithContext(ctx).Select("id").Order("created_at,id").Find(&drafts).Error; err != nil {
		return 0, err
	}
	completed := 0
	for _, draft := range drafts {
		if err := s.ensureDraftAudioState(ctx, draft.ID); err != nil {
			return completed, fmt.Errorf("backfill audio state for draft %s: %w", draft.ID, err)
		}
		completed++
	}
	return completed, nil
}

func (s *EditorService) DraftStatus(ctx context.Context, draftID string) (DraftRuntimeStatus, error) {
	var draft model.TextbookDraft
	if err := s.db.WithContext(ctx).Select("id,book_id,title,status,version").First(&draft, "id=?", draftID).Error; err != nil {
		return DraftRuntimeStatus{}, err
	}
	var jobs []model.TextbookJob
	if err := s.db.WithContext(ctx).Where("draft_id=?", draftID).Order("id DESC").Limit(40).Find(&jobs).Error; err != nil {
		return DraftRuntimeStatus{}, err
	}
	type issueCount struct {
		Page  int
		Count int64
	}
	var counts []issueCount
	if err := s.db.WithContext(ctx).Model(&model.TextbookAudioItem{}).Select("page,COUNT(*) AS count").
		Where("draft_id=? AND active=1 AND status IN ?", draftID, []string{"qa_failed", "manual_review_required"}).Group("page").Scan(&counts).Error; err != nil {
		return DraftRuntimeStatus{}, err
	}
	out := DraftRuntimeStatus{DraftID: draft.ID, BookID: draft.BookID, Title: draft.Title, Revision: draft.Version, DraftStatus: draft.Status, Jobs: jobs, PageIssueCounts: make(map[int]int64, len(counts))}
	for _, item := range counts {
		out.PageIssueCounts[item.Page] = item.Count
	}
	return out, nil
}

func (s *EditorService) AudioReviewQueue(ctx context.Context) ([]AudioReviewQueueItem, error) {
	var stateCount int64
	if err := s.db.WithContext(ctx).Model(&model.TextbookAudioItem{}).Count(&stateCount).Error; err != nil {
		return nil, err
	}
	if stateCount == 0 {
		if _, err := s.BackfillAudioState(ctx); err != nil {
			return nil, err
		}
	}
	var rows []AudioReviewQueueItem
	err := s.db.WithContext(ctx).Table("textbook_audio_items ai").
		Select("ai.draft_id,d.book_id,d.title,ai.page,COUNT(*) AS issue_count,MIN(ai.updated_at) AS oldest_issue_at").
		Joins("JOIN textbook_drafts d ON d.id=ai.draft_id").
		Where("ai.active=1 AND ai.status IN ?", []string{"qa_failed", "manual_review_required"}).
		Group("ai.draft_id,d.book_id,d.title,ai.page").Order("MIN(ai.updated_at),ai.draft_id,ai.page").Scan(&rows).Error
	return rows, err
}

func expectedAudioItems(raw string) ([]expectedAudioItem, error) {
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
	if err := json.Unmarshal([]byte(raw), &content); err != nil {
		return nil, err
	}
	out := make([]expectedAudioItem, 0)
	for _, segment := range content.Segments {
		if segment.AudioMode == "none" {
			continue
		}
		if segment.AudioMode != "word_only" && segment.ID != "" && strings.TrimSpace(segment.Text) != "" {
			out = append(out, expectedAudioItem{ItemID: segment.ID, SegmentID: segment.ID, ItemType: "sentence", Text: segment.Text, Context: segment.Text})
		}
		for index, word := range segment.Words {
			if word.ID == "" || !resource.HasSpeakableText(word.Text) {
				continue
			}
			position := index
			out = append(out, expectedAudioItem{ItemID: word.ID, SegmentID: segment.ID, ItemType: "word", WordIndex: &position, Text: word.Text, Context: segment.Text})
		}
	}
	return out, nil
}

func providerForModel(modelID string) string {
	if current, ok := ai.Find(modelID); ok {
		return current.Provider
	}
	return ""
}

func audioJSON(value any, fallback string) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return fallback
	}
	return string(raw)
}

// ensureDraftAudioState performs an idempotent, lazy import for historical
// drafts. New writes keep the tables current; old manifests are read only once
// when no database state exists yet.
func (s *EditorService) ensureDraftAudioState(ctx context.Context, draftID string) error {
	var count int64
	if err := s.db.WithContext(ctx).Model(&model.TextbookAudioItem{}).Where("draft_id=?", draftID).Count(&count).Error; err != nil || count > 0 {
		return err
	}
	var draft model.TextbookDraft
	if err := s.db.WithContext(ctx).First(&draft, "id=?", draftID).Error; err != nil {
		return err
	}
	var pages []model.TextbookDraftPage
	if err := s.db.WithContext(ctx).Where("draft_id=?", draftID).Order("position").Find(&pages).Error; err != nil {
		return err
	}
	manifest, manifestErr := readAudioManifest(filepath.Join(s.audioRoot(draft), "manifest.json"))
	if manifestErr != nil && !os.IsNotExist(manifestErr) {
		return manifestErr
	}
	ready := make(map[string]map[string]any)
	failed := make(map[string]map[string]any)
	if manifestErr == nil {
		for _, entry := range manifest.Items {
			ready[manifestEntryKey(entry)] = entry
		}
		for _, entry := range manifest.Failures {
			failed[manifestEntryKey(entry)] = entry
		}
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, page := range pages {
			if err := s.importPageAudioState(tx, draft, page, ready, failed); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *EditorService) importPageAudioState(tx *gorm.DB, draft model.TextbookDraft, page model.TextbookDraftPage, ready, failed map[string]map[string]any) error {
	expected, err := expectedAudioItems(page.Content)
	if err != nil {
		return err
	}
	settings := pageAudioSettings(draft, page)
	provider := providerForModel(settings.TTSModel)
	configured := settingsVoices(draft, settings)
	for _, item := range expected {
		for _, accent := range []string{tts.AccentUS, tts.AccentGB} {
			voice, enabled := configured[accent]
			if voice == "" {
				voice = settings.TTSVoice
				if settings.TTSModel != ai.CloudTTSModel {
					if accent == tts.AccentUS {
						voice = tts.DefaultAmericanVoice
					} else {
						voice = tts.DefaultBritishVoice
					}
				}
			}
			row := model.TextbookAudioItem{
				DraftID: draft.ID, Page: page.Position, ItemID: item.ItemID,
				SegmentID: item.SegmentID, ItemType: item.ItemType, WordIndex: item.WordIndex,
				Text: item.Text, Context: item.Context, Accent: accent,
				ModelID: settings.TTSModel, Provider: provider, VoiceID: voice,
				Status: "pending", Active: true, SourcePageVersion: page.Version,
				FailureReasons: "[]",
			}
			if !enabled {
				row.Status = "disabled"
			}
			key := audioIssueKey(page.Position, item.ItemID, accent)
			if entry, ok := ready[key]; ok && enabled {
				entryVoice := fmt.Sprint(entry["voice"])
				if (entryVoice == "" || strings.EqualFold(entryVoice, voice)) && audioStateFileExists(s.audioRoot(draft), fmt.Sprint(entry["file"])) {
					row.Status = "ready"
					row.AudioPath = fmt.Sprint(entry["file"])
					row.GenerationVersion = fmt.Sprint(entry["generation_version"])
					if row.GenerationVersion == "" {
						row.GenerationVersion = fmt.Sprint(entry["generation_id"])
					}
					row.RequestID = fmt.Sprint(entry["request_id"])
				}
			} else if entry, ok := failed[key]; ok && enabled {
				row.Status = "qa_failed"
				score := numberValue(entry["final_score"])
				row.QAScore = &score
				row.AttemptCount = int(numberValue(entry["attempt"]))
				row.FailureReasons = audioJSON(stringSlice(entry["reasons"]), "[]")
				row.CandidatePath = fmt.Sprint(entry["failed_file"])
				row.GenerationVersion = fmt.Sprint(entry["generation_version"])
				if row.GenerationVersion == "" {
					row.GenerationVersion = fmt.Sprint(entry["generation_id"])
				}
				row.RequestID = fmt.Sprint(entry["request_id"])
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *EditorService) syncPageAudioState(tx *gorm.DB, draft model.TextbookDraft, page model.TextbookDraftPage, reset bool) error {
	expected, err := expectedAudioItems(page.Content)
	if err != nil {
		return err
	}
	var existing []model.TextbookAudioItem
	if err := tx.Where("draft_id=? AND page=?", draft.ID, page.Position).Find(&existing).Error; err != nil {
		return err
	}
	byKey := make(map[string]model.TextbookAudioItem, len(existing))
	for _, row := range existing {
		byKey[row.ItemID+"\x00"+row.Accent] = row
	}
	if err := tx.Model(&model.TextbookAudioItem{}).Where("draft_id=? AND page=?", draft.ID, page.Position).
		Updates(map[string]any{"active": false, "status": "obsolete", "active_job_id": nil, "revision": gorm.Expr("revision+1")}).Error; err != nil {
		return err
	}
	settings := pageAudioSettings(draft, page)
	provider := providerForModel(settings.TTSModel)
	voices := settingsVoices(draft, settings)
	for _, item := range expected {
		for _, accent := range []string{tts.AccentUS, tts.AccentGB} {
			voice, enabled := voices[accent]
			if voice == "" {
				voice = settings.TTSVoice
				if settings.TTSModel != ai.CloudTTSModel {
					if accent == tts.AccentUS {
						voice = tts.DefaultAmericanVoice
					} else {
						voice = tts.DefaultBritishVoice
					}
				}
			}
			key := item.ItemID + "\x00" + accent
			old, found := byKey[key]
			status := "pending"
			preserve := found && !reset && old.Text == item.Text && old.Context == item.Context && old.ModelID == settings.TTSModel && strings.EqualFold(old.VoiceID, voice)
			if !enabled {
				status = "disabled"
			} else if preserve && old.Status != "disabled" && old.Status != "obsolete" {
				status = old.Status
			}
			values := map[string]any{
				"segment_id": item.SegmentID, "item_type": item.ItemType, "word_index": item.WordIndex,
				"text": item.Text, "context": item.Context, "model_id": settings.TTSModel,
				"provider": provider, "voice_id": voice, "status": status, "active": true,
				"source_page_version": page.Version, "revision": gorm.Expr("revision+1"),
			}
			if !preserve || !enabled {
				values["audio_path"], values["candidate_path"], values["failure_reasons"] = "", "", "[]"
				values["qa_score"], values["active_job_id"], values["request_id"] = nil, nil, ""
				values["attempt_count"] = 0
			}
			if found {
				if err := tx.Model(&model.TextbookAudioItem{}).Where("id=?", old.ID).Updates(values).Error; err != nil {
					return err
				}
			} else {
				row := model.TextbookAudioItem{DraftID: draft.ID, Page: page.Position, ItemID: item.ItemID, Accent: accent, FailureReasons: "[]"}
				createValues := make(map[string]any, len(values))
				for name, value := range values {
					createValues[name] = value
				}
				delete(createValues, "revision")
				row.Revision = 1
				if err := tx.Assign(createValues).FirstOrCreate(&row, "draft_id=? AND page=? AND item_id=? AND accent=?", draft.ID, page.Position, item.ItemID, accent).Error; err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *EditorService) draftAudioStatusDB(ctx context.Context, draft model.TextbookDraft) ([]resource.AudioAccentStatus, error) {
	if err := s.ensureDraftAudioState(ctx, draft.ID); err != nil {
		return nil, err
	}
	var rows []model.TextbookAudioItem
	if err := s.db.WithContext(ctx).Select("accent,status").Where("draft_id=? AND active=1", draft.ID).Find(&rows).Error; err != nil {
		return nil, err
	}
	voices, config := draftVoices(draft), draftAudioConfig(draft)
	out := make([]resource.AudioAccentStatus, 0, 2)
	for _, setting := range []struct {
		accent  string
		enabled bool
	}{{tts.AccentUS, config.AmericanEnabled}, {tts.AccentGB, config.BritishEnabled}} {
		status := resource.AudioAccentStatus{Accent: setting.accent, VoiceID: voices[setting.accent], Status: "not_generated"}
		for _, row := range rows {
			if row.Accent != setting.accent || row.Status == "obsolete" {
				continue
			}
			status.Total++
			if row.Status == "ready" {
				status.Ready++
			}
			if row.Status == "qa_failed" || row.Status == "manual_review_required" {
				status.Failed++
			}
		}
		switch {
		case !setting.enabled:
			status.Status = "disabled"
		case status.Total > 0 && status.Ready == status.Total && status.Failed == 0:
			status.Status = "ready"
		case status.Failed > 0:
			status.Status = "partial_failed"
		case status.Ready > 0:
			status.Status = "generating"
		}
		out = append(out, status)
	}
	return out, nil
}

func (s *EditorService) pageAudioReadyDB(ctx context.Context, draft model.TextbookDraft, page int) (bool, error) {
	if err := s.ensureDraftAudioState(ctx, draft.ID); err != nil {
		return false, err
	}
	var count int64
	err := s.db.WithContext(ctx).Model(&model.TextbookAudioItem{}).
		Where("draft_id=? AND page=? AND active=1 AND status NOT IN ?", draft.ID, page, []string{"ready", "disabled", "obsolete"}).Count(&count).Error
	return count == 0, err
}

func (s *EditorService) refreshAudioStateFromManifest(ctx context.Context, job model.TextbookJob) error {
	var draft model.TextbookDraft
	if err := s.db.WithContext(ctx).First(&draft, "id=?", job.DraftID).Error; err != nil {
		return err
	}
	manifest, err := readAudioManifest(filepath.Join(s.audioRoot(draft), "manifest.json"))
	if err != nil {
		return err
	}
	ready := make(map[string]map[string]any)
	failed := make(map[string]map[string]any)
	for _, entry := range manifest.Items {
		if job.Page == 0 || int(numberValue(entry["page"])) == job.Page {
			ready[manifestEntryKey(entry)] = entry
		}
	}
	for _, entry := range manifest.Failures {
		if job.Page == 0 || int(numberValue(entry["page"])) == job.Page {
			failed[manifestEntryKey(entry)] = entry
		}
	}
	query := s.db.WithContext(ctx).Where("draft_id=? AND active=1", job.DraftID)
	if job.Page > 0 {
		query = query.Where("page=?", job.Page)
	}
	if job.ItemID != "" {
		query = query.Where("item_id=? AND accent=?", job.ItemID, job.Accent)
	}
	var rows []model.TextbookAudioItem
	if err := query.Find(&rows).Error; err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, row := range rows {
			key := audioIssueKey(row.Page, row.ItemID, row.Accent)
			updates := map[string]any{"active_job_id": nil, "revision": gorm.Expr("revision+1")}
			var attempt *model.TextbookAudioAttempt
			if entry, ok := ready[key]; ok && (fmt.Sprint(entry["voice"]) == "" || strings.EqualFold(fmt.Sprint(entry["voice"]), row.VoiceID)) {
				generation := fmt.Sprint(entry["generation_version"])
				if generation == "" {
					generation = fmt.Sprint(entry["generation_id"])
				}
				updates["status"], updates["audio_path"], updates["candidate_path"] = "ready", fmt.Sprint(entry["file"]), ""
				updates["failure_reasons"], updates["qa_score"], updates["generation_version"] = "[]", nil, generation
				qa, _ := entry["qa"].(map[string]any)
				attempt = audioAttemptFromState(row, job, generation, "ready", qa)
			} else if entry, ok := failed[key]; ok {
				generation := fmt.Sprint(entry["generation_version"])
				if generation == "" {
					generation = fmt.Sprint(entry["generation_id"])
				}
				status := "qa_failed"
				if ai.IsCloud(row.ModelID) {
					status = "manual_review_required"
				}
				score := numberValue(entry["final_score"])
				updates["status"], updates["candidate_path"], updates["audio_path"] = status, fmt.Sprint(entry["failed_file"]), ""
				updates["failure_reasons"], updates["qa_score"] = audioJSON(stringSlice(entry["reasons"]), "[]"), score
				updates["attempt_count"], updates["generation_version"], updates["request_id"] = int(numberValue(entry["attempt"])), generation, fmt.Sprint(entry["request_id"])
				attempt = audioAttemptFromState(row, job, generation, status, entry)
			} else if row.ActiveJobID != nil && *row.ActiveJobID == job.ID {
				status := "qa_failed"
				if ai.IsCloud(row.ModelID) {
					status = "manual_review_required"
				}
				updates["status"] = status
			}
			if err := tx.Model(&model.TextbookAudioItem{}).Where("id=?", row.ID).Updates(updates).Error; err != nil {
				return err
			}
			if attempt != nil && attempt.GenerationVersion != "" {
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(attempt).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func audioAttemptFromState(row model.TextbookAudioItem, job model.TextbookJob, generation, result string, qa map[string]any) *model.TextbookAudioAttempt {
	jobID := job.ID
	score := numberValue(qa["final_score"])
	attempt := &model.TextbookAudioAttempt{
		AudioItemID: row.ID, JobID: &jobID, GenerationVersion: generation,
		AttemptNo: int(numberValue(qa["attempt"])), ModelID: row.ModelID,
		Provider: row.Provider, VoiceID: row.VoiceID, RequestID: fmt.Sprint(qa["request_id"]),
		Result: result, FailureReasons: audioJSON(stringSlice(qa["reasons"]), "[]"), QADetail: audioJSON(qa, "{}"),
		CandidatePath: fmt.Sprint(qa["failed_file"]),
	}
	if attempt.AttemptNo < 1 {
		attempt.AttemptNo = 1
	}
	if score > 0 {
		attempt.QAScore = &score
	}
	if generationData, ok := qa["generation"].(map[string]any); ok {
		attempt.GenerationMS = uint(numberValue(generationData["generation_seconds"]) * 1000)
		attempt.DurationMS = uint(numberValue(generationData["audio_duration"]) * 1000)
	}
	return attempt
}

func parseFailureReasons(raw string) []string {
	var reasons []string
	_ = json.Unmarshal([]byte(raw), &reasons)
	return reasons
}

func (s *EditorService) resolveAudioStatePath(draftID, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) {
		return "", os.ErrNotExist
	}
	var draft model.TextbookDraft
	if err := s.db.Select("id,book_id").First(&draft, "id=?", draftID).Error; err != nil {
		return "", err
	}
	root := s.audioRoot(draft)
	target := filepath.Clean(filepath.Join(root, filepath.FromSlash(relative)))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", os.ErrNotExist
	}
	return target, nil
}

func audioStateFileExists(root, relative string) bool {
	if relative == "" || filepath.IsAbs(relative) {
		return false
	}
	target := filepath.Clean(filepath.Join(root, filepath.FromSlash(relative)))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	info, err := os.Stat(target)
	return err == nil && !info.IsDir()
}

func sortAudioIssues(items []AudioIssue) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].ItemID == items[j].ItemID {
			return items[i].Accent < items[j].Accent
		}
		return items[i].ItemID < items[j].ItemID
	})
}
