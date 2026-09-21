package service

import "testing"

func TestReleaseAudioDaemonForModelSwitchClearsIdleModel(t *testing.T) {
	s := &EditorService{audioDaemonModel: "local-qwen3-tts"}
	if err := s.ReleaseAudioDaemonForModelSwitch("local-qwen3-tts", "local-qwen3-tts-1.7b"); err != nil {
		t.Fatalf("ReleaseAudioDaemonForModelSwitch: %v", err)
	}
	if s.audioDaemonModel != "" {
		t.Fatalf("audioDaemonModel = %q, want empty", s.audioDaemonModel)
	}
}

func TestReleaseAudioDaemonForModelSwitchRejectsActiveJob(t *testing.T) {
	s := &EditorService{audioDaemonModel: "local-qwen3-tts", audioJobID: 7}
	if err := s.ReleaseAudioDaemonForModelSwitch("local-qwen3-tts", "local-qwen3-tts-1.7b"); err == nil {
		t.Fatal("expected active audio job to block model switch")
	}
	if s.audioDaemonModel != "local-qwen3-tts" {
		t.Fatalf("active model was unexpectedly cleared: %q", s.audioDaemonModel)
	}
}

func TestReleaseAudioDaemonForModelSwitchKeepsSameModel(t *testing.T) {
	s := &EditorService{audioDaemonModel: "local-qwen3-tts"}
	if err := s.ReleaseAudioDaemonForModelSwitch("local-qwen3-tts", "local-qwen3-tts"); err != nil {
		t.Fatalf("same model switch: %v", err)
	}
	if s.audioDaemonModel != "local-qwen3-tts" {
		t.Fatalf("same-model selection should keep resident daemon model, got %q", s.audioDaemonModel)
	}
}


func TestReleaseTranslationDaemonForModelSwitchClearsIdleModel(t *testing.T) {
	s := &EditorService{translationDaemonModel: "local-qwen3-4b-instruct-2507"}
	if err := s.ReleaseTranslationDaemonForModelSwitch("local-qwen3-4b-instruct-2507", "qwen3.7-flash"); err != nil {
		t.Fatalf("ReleaseTranslationDaemonForModelSwitch: %v", err)
	}
	if s.translationDaemonModel != "" {
		t.Fatalf("translationDaemonModel = %q, want empty", s.translationDaemonModel)
	}
}

func TestReleaseTranslationDaemonForModelSwitchRejectsActiveTranslation(t *testing.T) {
	s := &EditorService{translationDaemonModel: "local-qwen3-4b-instruct-2507"}
	s.translationMu.Lock()
	defer s.translationMu.Unlock()
	if err := s.ReleaseTranslationDaemonForModelSwitch("local-qwen3-4b-instruct-2507", "qwen3.7-flash"); err == nil {
		t.Fatal("expected active translation to block model switch")
	}
}
