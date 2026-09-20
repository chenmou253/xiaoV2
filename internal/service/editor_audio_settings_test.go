package service

import (
	"testing"

	"xiaov2/internal/ai"
	"xiaov2/internal/config"
	"xiaov2/internal/model"
	"xiaov2/internal/tts"
)

func TestNormalizeAudioSettingsLocksAccentVoices(t *testing.T) {
	got := normalizeAudioSettings(AudioSettings{
		AmericanEnabled: false,
		BritishEnabled:  true,
		AmericanVoiceID: "ryan",
		BritishVoiceID:  "aiden",
	})
	if got.AmericanVoiceID != "aiden" || got.BritishVoiceID != "ryan" {
		t.Fatalf("unexpected locked mapping: American=%q British=%q", got.AmericanVoiceID, got.BritishVoiceID)
	}
	if got.AmericanEnabled || !got.BritishEnabled {
		t.Fatalf("accent enable flags were not preserved: American=%t British=%t", got.AmericanEnabled, got.BritishEnabled)
	}
}

func TestCloudDraftUsesSelectedVoiceForEnabledAccents(t *testing.T) {
	got := draftVoices(model.TextbookDraft{
		AmericanEnabled: true,
		BritishEnabled:  true,
		TTSModel:        ai.CloudTTSModel,
		TTSVoice:        "Jennifer",
	})
	if got[tts.AccentUS] != "Jennifer" || got[tts.AccentGB] != "Jennifer" {
		t.Fatalf("cloud voice was not sourced from draft model settings: %#v", got)
	}
}

func TestJobModelSeparatesOCRAndTTSProviders(t *testing.T) {
	draft := model.TextbookDraft{OCRModel: ai.CloudOCRModel, TTSModel: ai.LocalTTSModel}
	if got := jobModel(draft, "ocr"); got.ID != ai.CloudOCRModel || got.Provider != "dashscope" {
		t.Fatalf("unexpected OCR provider: %#v", got)
	}
	if got := jobModel(draft, "audio"); got.ID != ai.LocalTTSModel || got.Provider != "local" {
		t.Fatalf("unexpected TTS provider: %#v", got)
	}
}

func TestDraftAudioConfigIgnoresLegacyVoiceOverrides(t *testing.T) {
	got := draftAudioConfig(model.TextbookDraft{
		AmericanEnabled: true,
		BritishEnabled:  true,
		AmericanVoiceID: "ryan",
		BritishVoiceID:  "aiden",
	}).EnabledVoices()
	if got[tts.AccentUS] != "aiden" || got[tts.AccentGB] != "ryan" {
		t.Fatalf("legacy speaker overrides escaped lock: %#v", got)
	}
}

func TestDraftAudioConfigHonorsAllAccentsDisabled(t *testing.T) {
	got := draftAudioConfig(model.TextbookDraft{}).EnabledVoices()
	if len(got) != 0 {
		t.Fatalf("disabled pronunciation switches unexpectedly enabled audio: %#v", got)
	}
}

func TestDraftAudioConfigOnlyUsesEnabledAccent(t *testing.T) {
	got := draftAudioConfig(model.TextbookDraft{BritishEnabled: true}).EnabledVoices()
	if len(got) != 1 || got[tts.AccentGB] != "ryan" {
		t.Fatalf("generation config ignored persisted accent switches: %#v", got)
	}
}

func TestMissingDraftAudioAllowsEmptyOCRPageWithoutManifest(t *testing.T) {
	service := &EditorService{cfg: config.Config{EditorRoot: t.TempDir()}}
	draft := model.TextbookDraft{
		ID:              "empty-draft",
		BookID:          "empty-book",
		AmericanEnabled: true,
		TTSModel:        ai.LocalTTSModel,
	}
	page := model.TextbookDraftPage{
		Position: 1,
		Content:  `{"page":1,"segments":[]}`,
	}

	if service.missingDraftAudio(draft, page) {
		t.Fatal("empty OCR page should not require an audio manifest")
	}
}

func TestMissingDraftAudioAllowsExplicitlySilentSegments(t *testing.T) {
	service := &EditorService{cfg: config.Config{EditorRoot: t.TempDir()}}
	draft := model.TextbookDraft{ID: "silent-draft", BookID: "silent-book", AmericanEnabled: true, TTSModel: ai.LocalTTSModel}
	page := model.TextbookDraftPage{Position: 1, Content: `{"page":1,"segments":[{"id":"s1","text":"Name","audio_mode":"none","words":[{"id":"w1","text":"Name"}]}]}`}

	if service.missingDraftAudio(draft, page) {
		t.Fatal("audio-disabled segments should not require an audio manifest")
	}
}

func TestRetryAudioIssueSettingsUsesOnlyTheItemOverride(t *testing.T) {
	draft := model.TextbookDraft{
		AmericanEnabled: true,
		BritishEnabled:  true,
		TTSModel:        ai.CloudTTSModel,
		TTSVoice:        "Aiden",
	}
	selected, voice, err := retryAudioIssueSettings(
		draft,
		ai.Settings{TTSModel: ai.CloudTTSModel, TTSVoice: "Aiden"},
		model.TextbookAudioItem{ModelID: ai.LocalTTSModel, VoiceID: tts.DefaultAmericanVoice},
		tts.AccentUS,
		ai.LocalTTSModel,
		tts.DefaultAmericanVoice,
	)
	if err != nil {
		t.Fatal(err)
	}
	if selected.ID != ai.LocalTTSModel || voice != tts.DefaultAmericanVoice {
		t.Fatalf("item retry override was not retained: model=%q voice=%q", selected.ID, voice)
	}
	if draft.TTSModel != ai.CloudTTSModel || draft.TTSVoice != "Aiden" {
		t.Fatalf("item retry unexpectedly changed draft defaults: %#v", draft)
	}
}

func TestRetryAudioIssueSettingsRejectsWrongLocalAccentVoice(t *testing.T) {
	_, _, err := retryAudioIssueSettings(
		model.TextbookDraft{AmericanEnabled: true, BritishEnabled: true},
		ai.Settings{TTSModel: ai.LocalTTSModel},
		model.TextbookAudioItem{},
		tts.AccentGB,
		ai.LocalTTSModel,
		tts.DefaultAmericanVoice,
	)
	if err == nil {
		t.Fatal("expected British retry with the American local voice to be rejected")
	}
}

func TestExpectedAudioItemsBuildsSentenceAndSpeakableWords(t *testing.T) {
	items, err := expectedAudioItems(`{"segments":[{"id":"s1","text":"Write it!","words":[{"id":"w1","text":"Write"},{"id":"symbol","text":"!"},{"id":"w2","text":"it"}]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("expected one sentence and two words, got %#v", items)
	}
	if items[0].ItemID != "s1" || items[0].ItemType != "sentence" || items[0].WordIndex != nil {
		t.Fatalf("unexpected sentence state: %#v", items[0])
	}
	if items[1].ItemID != "w1" || items[1].WordIndex == nil || *items[1].WordIndex != 0 {
		t.Fatalf("unexpected first word state: %#v", items[1])
	}
	if items[2].ItemID != "w2" || items[2].WordIndex == nil || *items[2].WordIndex != 2 {
		t.Fatalf("unexpected second word state: %#v", items[2])
	}
}

func TestExpectedAudioItemsHonorsSegmentAudioMode(t *testing.T) {
	items, err := expectedAudioItems(`{"segments":[{"id":"word-only","text":"Activity","audio_mode":"word_only","words":[{"id":"activity","text":"Activity"}]},{"id":"silent","text":"Name","audio_mode":"none","words":[{"id":"name","text":"Name"}]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ItemID != "activity" || items[0].ItemType != "word" {
		t.Fatalf("unexpected audio plan: %#v", items)
	}
}
