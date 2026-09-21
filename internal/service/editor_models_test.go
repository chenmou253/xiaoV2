package service

import (
	"testing"

	"xiaov2/internal/ai"
	"xiaov2/internal/model"
)

func TestPageAudioSettingsKeepsLockedPageSnapshot(t *testing.T) {
	draft := model.TextbookDraft{OCRModel: ai.CloudOCRModel, TTSModel: ai.CloudTTSModel, TTSVoice: "Aiden"}
	locked := model.TextbookDraftPage{Checked: true, AudioChecked: true, OCRModel: ai.LocalOCRModel, TTSModel: ai.LocalTTSModel, TTSVoice: "aiden"}
	if got := pageAudioSettings(draft, locked); got.TTSModel != ai.LocalTTSModel || got.TTSVoice != "aiden" {
		t.Fatalf("locked page must retain its snapshot, got %#v", got)
	}
	locked.AudioChecked = false
	if got := pageAudioSettings(draft, locked); got.TTSModel != ai.CloudTTSModel || got.TTSVoice != "Aiden" {
		t.Fatalf("unlocked page must use draft default, got %#v", got)
	}
}


func TestPageModelSettingsKeepsTranslationSnapshot(t *testing.T) {
	draft := model.TextbookDraft{
		OCRModel: ai.LocalOCRModel,
		TranslationModel: ai.LocalTranslationModel,
		TTSModel: ai.LocalTTSModel,
		TTSVoice: "aiden",
	}
	page := model.TextbookDraftPage{TranslationModel: ai.CloudTranslationModel}
	got := pageModelSettings(draft, page)
	if got.TranslationModel != ai.CloudTranslationModel {
		t.Fatalf("page translation snapshot = %q", got.TranslationModel)
	}
}
