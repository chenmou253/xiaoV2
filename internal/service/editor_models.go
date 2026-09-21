package service

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"xiaov2/internal/ai"
	"xiaov2/internal/model"
)

func validDraftModelSettings(value ai.Settings) (ai.Settings, error) {
	value = ai.NormalizeSettings(value)
	ocr, ok := ai.Find(value.OCRModel)
	if !ok || ocr.Type != "ocr" || !ocr.Enabled {
		return value, bad("OCR 模型无效")
	}
	if !ocr.Available {
		return value, bad("OCR 模型当前不可用：" + ocr.UnavailableReason)
	}
	translationModel, ok := ai.Find(value.TranslationModel)
	if !ok || translationModel.Type != "translation" || !translationModel.Enabled {
		return value, bad("翻译模型无效")
	}
	if !translationModel.Available {
		return value, bad("翻译模型当前不可用：" + translationModel.UnavailableReason)
	}
	ttsModel, ok := ai.Find(value.TTSModel)
	if !ok || ttsModel.Type != "tts" || !ttsModel.Enabled {
		return value, bad("TTS 模型无效")
	}
	if !ttsModel.Available {
		return value, bad("TTS 模型当前不可用：" + ttsModel.UnavailableReason)
	}
	if !ai.ValidVoice(value.TTSModel, value.TTSVoice) {
		return value, bad("TTS 音色不属于所选模型")
	}
	return value, nil
}

// SwitchModels updates only the draft defaults. Pages with both OCR and audio
// confirmed are pinned to their old page snapshot and are never regenerated.
// Provisional audio is disposable, so a TTS/voice switch clears it and makes
// the page wait for regeneration under the new model.
func (s *EditorService) SwitchModels(ctx context.Context, id string, version, actor uint64, requested ai.Settings) error {
	settings, err := validDraftModelSettings(requested)
	if err != nil {
		return err
	}
	// Import historical manifest rows before changing the draft default, so old
	// locked audio has an immutable database record of its original model/voice.
	if err := s.ensureDraftAudioState(ctx, id); err != nil {
		return err
	}
	var current model.TextbookDraft
	if err := s.db.WithContext(ctx).First(&current, "id=?", id).Error; err != nil {
		return err
	}
	currentSettings := draftModelSettings(current)
	if currentSettings.TTSModel != settings.TTSModel {
		// Release an idle resident model immediately. The transaction below
		// still performs the authoritative queued/running job check.
		if err := s.ReleaseAudioDaemonForModelSwitch(currentSettings.TTSModel, settings.TTSModel); err != nil {
			return err
		}
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var draft model.TextbookDraft
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&draft, "id=?", id).Error; err != nil {
			return err
		}
		if draft.Version != version || (draft.Status != "draft" && draft.Status != "failed") {
			return conflict("草稿已更新、正在处理或不处于可编辑状态")
		}
		var active int64
		if err := tx.Model(&model.TextbookJob{}).Where("draft_id=? AND status IN ?", id, []string{"queued", "running"}).Count(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			return conflict("有任务正在执行，暂时不能切换模型")
		}
		old := draftModelSettings(draft)
		translationChanged := old.TranslationModel != settings.TranslationModel
		ttsChanged := old.TTSModel != settings.TTSModel || old.TTSVoice != settings.TTSVoice
		var pages []model.TextbookDraftPage
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("draft_id=?", id).Order("position").Find(&pages).Error; err != nil {
			return err
		}

		// First materialize legacy page snapshots using the old draft defaults.
		// This is what makes an old confirmed page immune to the switch.
		for index := range pages {
			page := &pages[index]
			snapshot := pageModelSettings(draft, *page)
			updates := map[string]any{}
			if page.OCRModel == "" {
				page.OCRModel, updates["ocr_model"] = snapshot.OCRModel, snapshot.OCRModel
			}
			if page.TranslationModel == "" {
				page.TranslationModel, updates["translation_model"] = snapshot.TranslationModel, snapshot.TranslationModel
			}
			if page.TTSModel == "" {
				page.TTSModel, updates["tts_model"] = snapshot.TTSModel, snapshot.TTSModel
			}
			if page.TTSVoice == "" {
				page.TTSVoice, updates["tts_voice"] = snapshot.TTSVoice, snapshot.TTSVoice
			}
			if len(updates) > 0 {
				if err := tx.Model(page).Updates(updates).Error; err != nil {
					return err
				}
			}
		}

		next := draft
		next.OCRModel, next.TranslationModel, next.TTSModel, next.TTSVoice = settings.OCRModel, settings.TranslationModel, settings.TTSModel, settings.TTSVoice
		if err := tx.Model(&draft).Updates(map[string]any{
			"ocr_model": settings.OCRModel, "translation_model": settings.TranslationModel, "tts_model": settings.TTSModel, "tts_voice": settings.TTSVoice,
			"version": gorm.Expr("version+1"), "updated_by": actor,
			"status": func() string {
				if draft.Status == "failed" {
					return "draft"
				}
				return draft.Status
			}(),
		}).Error; err != nil {
			return err
		}

		if translationChanged {
			for index := range pages {
				page := &pages[index]
				// Text-confirmed pages keep the model that produced the accepted
				// translation. Only text-unlocked pages follow the new default.
				if page.Checked {
					continue
				}
				page.TranslationModel = settings.TranslationModel
				if err := tx.Model(page).Updates(map[string]any{
					"translation_model": settings.TranslationModel,
					"version": gorm.Expr("version+1"),
				}).Error; err != nil {
					return err
				}
				page.Version++
			}
		}
		if ttsChanged {
			for index := range pages {
				page := &pages[index]
				if pageIsLocked(*page) {
					continue
				}
				if err := s.clearDraftPageAudioArtifacts(draft.ID, draft.BookID, page.Position); err != nil {
					return err
				}
				page.TTSModel, page.TTSVoice, page.AudioChecked = settings.TTSModel, settings.TTSVoice, false
				if err := tx.Model(page).Updates(map[string]any{
					"tts_model": settings.TTSModel, "tts_voice": settings.TTSVoice, "audio_checked": false,
					"version": gorm.Expr("version+1"),
				}).Error; err != nil {
					return err
				}
				page.Version++
				if err := s.syncPageAudioState(tx, next, *page, true); err != nil {
					return err
				}
			}
		}
		return editorAudit(tx, actor, "draft.models.switch", id, map[string]any{
			"before": old, "after": settings, "translation_unlocked_updated": translationChanged, "tts_pending_audio_reset": ttsChanged,
		})
	})
}
