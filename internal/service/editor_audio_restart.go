package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"xiaov2/internal/model"
)

// restartPageAudio is the recovery path for an audio task that is queued or
// running but no longer making progress.  It deliberately keeps OCR,
// translations and every other page, while removing all passing/failed audio
// artifacts for the requested page before enqueuing a clean replacement.
func (s *EditorService) restartPageAudio(ctx context.Context, id string, version, actor uint64, page int) error {
	if page < 1 {
		return bad("请指定要重新生成音频的页面")
	}

	s.audioRestartMu.Lock()
	defer s.audioRestartMu.Unlock()

	var cancelledIDs []uint64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var d model.TextbookDraft
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&d, "id=?", id).Error; e != nil {
			return e
		}
		if d.Version != version {
			return conflict("草稿已更新，请刷新后重试")
		}
		if len(draftVoices(d)) == 0 {
			return conflict("当前草稿已关闭所有发音，无法重新生成音频")
		}

		var current model.TextbookDraftPage
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("draft_id=? AND position=?", id, page).First(&current).Error; e != nil {
			return bad(fmt.Sprintf("第 %d 页尚未生成", page))
		}
		if issues := s.pageAudioRegenerationIssues(ctx, id, current.Position, current.Content); len(issues) > 0 {
			return bad(fmt.Sprintf("第 %d 页暂不能重新生成音频：%s", page, strings.Join(issues, "；")))
		}

		var active []model.TextbookJob
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("draft_id=? AND status IN ?", id, []string{"queued", "running"}).
			Find(&active).Error; e != nil {
			return e
		}
		for _, job := range active {
			if !strings.HasPrefix(job.Kind, "audio") || job.Page != page {
				return conflict("当前还有其他页面任务正在执行，不能终止为本页音频任务")
			}
			cancelledIDs = append(cancelledIDs, job.ID)
		}
		if len(cancelledIDs) == 0 {
			return conflict("当前页没有可终止的音频任务")
		}

		if e := tx.Model(&model.TextbookJob{}).Where("id IN ?", cancelledIDs).Updates(map[string]any{
			"status": "cancelled",
			"error":  "已由管理员终止，准备清除并重新生成本页音频",
		}).Error; e != nil {
			return e
		}
		if e := tx.Model(&d).Updates(map[string]any{
			"status":     "audio",
			"version":    gorm.Expr("version+1"),
			"updated_by": actor,
		}).Error; e != nil {
			return e
		}
		return editorAudit(tx, actor, "draft.audio-task-cancel", id, map[string]any{
			"page": page, "job_ids": cancelledIDs,
		})
	})
	if err != nil {
		return err
	}

	// If the job belongs to this process, killing the daemon immediately
	// unblocks its scanner.  If the service was restarted, no local job is
	// registered and the persisted orphan is already safely cancelled above.
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err = s.interruptAudioJobs(waitCtx, cancelledIDs); err != nil {
		_ = s.db.WithContext(context.Background()).Model(&model.TextbookDraft{}).
			Where("id=?", id).Updates(map[string]any{
			"status": "failed", "version": gorm.Expr("version+1"), "updated_by": actor,
		}).Error
		return err
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var d model.TextbookDraft
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&d, "id=?", id).Error; e != nil {
			return e
		}
		var current model.TextbookDraftPage
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("draft_id=? AND position=?", id, page).First(&current).Error; e != nil {
			return e
		}
		if e := s.clearDraftPageAudioArtifacts(d.ID, d.BookID, page); e != nil {
			return e
		}
		if e := tx.Model(&current).Updates(map[string]any{
			"checked":       true,
			"audio_checked": false,
			"version":       gorm.Expr("version+1"),
		}).Error; e != nil {
			return e
		}
		if e := tx.Create(&model.TextbookJob{
			DraftID: id, Kind: "audio-replace", Page: page, Status: "queued",
		}).Error; e != nil {
			return e
		}
		if e := tx.Model(&d).Updates(map[string]any{
			"status":     "queued",
			"version":    gorm.Expr("version+1"),
			"updated_by": actor,
		}).Error; e != nil {
			return e
		}
		return editorAudit(tx, actor, "draft.audio-restart-page", id, map[string]any{
			"page": page, "cancelled_job_ids": cancelledIDs,
		})
	})
}

func (s *EditorService) beginAudioJob(id uint64) {
	s.audioJobMu.Lock()
	defer s.audioJobMu.Unlock()
	s.audioJobID = id
	s.audioJobDone = make(chan struct{})
}

func (s *EditorService) finishAudioJob(id uint64) {
	s.audioJobMu.Lock()
	defer s.audioJobMu.Unlock()
	if s.audioJobID != id {
		return
	}
	if s.audioJobDone != nil {
		close(s.audioJobDone)
	}
	s.audioJobID = 0
	s.audioJobDone = nil
}

func (s *EditorService) interruptAudioJobs(ctx context.Context, ids []uint64) error {
	target := make(map[uint64]struct{}, len(ids))
	for _, id := range ids {
		target[id] = struct{}{}
	}
	s.audioJobMu.Lock()
	_, active := target[s.audioJobID]
	done := s.audioJobDone
	s.audioJobMu.Unlock()
	if !active || done == nil {
		return nil
	}

	s.stopAudioDaemon()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("终止旧音频任务超时，请重启服务后再次操作: %w", ctx.Err())
	}
}
