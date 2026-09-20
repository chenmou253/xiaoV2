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
