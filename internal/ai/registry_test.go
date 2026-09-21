package ai

import "testing"

func TestCloudModelsUnavailableWithoutKey(t *testing.T) {
	t.Setenv("DASHSCOPE_API_KEY", "")
	for _, id := range []string{CloudOCRModel, CloudTTSModel} {
		model, ok := Find(id)
		if !ok || model.Available || model.UnavailableReason == "" {
			t.Fatalf("expected %s to remain registered but unavailable: %#v", id, model)
		}
	}
}

func TestCloudModelsAvailableWithKeyAndVoiceComesFromRegistry(t *testing.T) {
	t.Setenv("DASHSCOPE_API_KEY", "test-only")
	model, ok := Find(CloudTTSModel)
	if !ok || !model.Available || model.RetryPolicy != "none" {
		t.Fatalf("unexpected cloud TTS metadata: %#v", model)
	}
	if !ValidVoice(CloudTTSModel, "Aiden") || ValidVoice(CloudTTSModel, "aiden") {
		t.Fatal("cloud voice validation must preserve the provider's case-sensitive IDs")
	}
}


func TestLocalTTSVariantsRegisteredWithSameVoices(t *testing.T) {
	for _, id := range []string{LocalTTSModel, LocalTTS17BModel} {
		model, ok := Find(id)
		if !ok {
			t.Fatalf("local TTS model %s is not registered", id)
		}
		if model.Provider != "local" || model.Cloud || !model.Available || model.DefaultVoice != "aiden" {
			t.Fatalf("unexpected local TTS metadata for %s: %#v", id, model)
		}
		if !IsLocalTTS(id) {
			t.Fatalf("expected %s to be recognized as local TTS", id)
		}
		if !ValidVoice(id, "aiden") || !ValidVoice(id, "ryan") {
			t.Fatalf("expected %s to support aiden and ryan", id)
		}
	}
}


func TestTranslationModelsRegistered(t *testing.T) {
	t.Setenv("TRANSLATION_API_KEY", "test-only")
	cloud, ok := Find(CloudTranslationModel)
	if !ok || cloud.Type != "translation" || !cloud.Cloud || !cloud.Available {
		t.Fatalf("unexpected cloud translation metadata: %#v", cloud)
	}
	local, ok := Find(LocalTranslationModel)
	if !ok || local.Type != "translation" || local.Cloud || local.Provider != "local-mlx" {
		t.Fatalf("unexpected local translation metadata: %#v", local)
	}
	if !IsLocalTranslation(LocalTranslationModel) {
		t.Fatal("local translation model should be recognized as local MLX")
	}
	settings := NormalizeSettings(Settings{})
	if settings.TranslationModel != CloudTranslationModel {
		t.Fatalf("default translation model = %q", settings.TranslationModel)
	}
}
