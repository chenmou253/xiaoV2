package ai

import (
	"os"
	"runtime"
	"strings"
)

const (
	LocalOCRModel = "local-paddleocr"
	CloudOCRModel = "qwen3.5-ocr"
	LocalTranslationModel = "local-qwen3-4b-instruct-2507"
	CloudTranslationModel = "qwen3.7-flash"
	LocalTTSModel    = "local-qwen3-tts"
	LocalTTS17BModel = "local-qwen3-tts-1.7b"
	CloudTTSModel    = "qwen3-tts-flash"
)

type Model struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Type              string   `json:"type"`
	Provider          string   `json:"provider"`
	Enabled           bool     `json:"enabled"`
	Cloud             bool     `json:"cloud"`
	Available         bool     `json:"available"`
	UnavailableReason string   `json:"unavailable_reason,omitempty"`
	Capabilities      []string `json:"capabilities"`
	DefaultVoice      string   `json:"default_voice,omitempty"`
	RetryPolicy       string   `json:"retry_policy"`
}

type Voice struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

type Settings struct {
	OCRModel         string `json:"ocr_model"`
	TranslationModel string `json:"translation_model"`
	TTSModel         string `json:"tts_model"`
	TTSVoice         string `json:"tts_voice"`
}

func DefaultSettings() Settings {
	return Settings{OCRModel: LocalOCRModel, TranslationModel: CloudTranslationModel, TTSModel: LocalTTSModel, TTSVoice: "aiden"}
}

func Models() []Model {
	hasKey := strings.TrimSpace(os.Getenv("DASHSCOPE_API_KEY")) != ""
	cloudReason := ""
	if !hasKey {
		cloudReason = "未配置 DASHSCOPE_API_KEY"
	}
	hasTranslationKey := strings.TrimSpace(os.Getenv("TRANSLATION_API_KEY")) != "" || hasKey
	translationCloudReason := ""
	if !hasTranslationKey {
		translationCloudReason = "未配置 TRANSLATION_API_KEY 或 DASHSCOPE_API_KEY"
	}
	localTranslationAvailable := runtime.GOOS == "darwin" && runtime.GOARCH == "arm64"
	localTranslationReason := ""
	if !localTranslationAvailable {
		localTranslationReason = "本地 MLX 翻译仅支持 Apple Silicon macOS"
	}
	return []Model{
		{ID: LocalOCRModel, Name: "本地 PaddleOCR", Type: "ocr", Provider: "local", Enabled: true, Available: true, Capabilities: []string{"text", "coordinates", "confidence"}, RetryPolicy: "local-quality-gate"},
		{ID: CloudOCRModel, Name: "Qwen3.5 OCR", Type: "ocr", Provider: "dashscope", Enabled: true, Cloud: true, Available: hasKey, UnavailableReason: cloudReason, Capabilities: []string{"text", "coordinates", "document-ocr"}, RetryPolicy: "none"},
		{ID: LocalTranslationModel, Name: "本地 Qwen3 4B Instruct 2507 4bit", Type: "translation", Provider: "local-mlx", Enabled: true, Available: localTranslationAvailable, UnavailableReason: localTranslationReason, Capabilities: []string{"translation", "contextual-word-meaning", "spelling-review", "general-american-ipa", "structured-json"}, RetryPolicy: "none"},
		{ID: CloudTranslationModel, Name: "Qwen3.7 Flash", Type: "translation", Provider: "dashscope", Enabled: true, Cloud: true, Available: hasTranslationKey, UnavailableReason: translationCloudReason, Capabilities: []string{"translation", "contextual-word-meaning", "spelling-review", "general-american-ipa", "structured-json"}, RetryPolicy: "none"},
		{ID: LocalTTSModel, Name: "本地 Qwen3 TTS 0.6B 8bit", Type: "tts", Provider: "local", Enabled: true, Available: true, Capabilities: []string{"speech", "en-US", "en-GB", "local-qa"}, DefaultVoice: "aiden", RetryPolicy: "local-quality-gate"},
		{ID: LocalTTS17BModel, Name: "本地 Qwen3 TTS 1.7B 8bit", Type: "tts", Provider: "local", Enabled: true, Available: true, Capabilities: []string{"speech", "en-US", "en-GB", "local-qa"}, DefaultVoice: "aiden", RetryPolicy: "local-quality-gate"},
		{ID: CloudTTSModel, Name: "Qwen3 TTS Flash", Type: "tts", Provider: "dashscope", Enabled: true, Cloud: true, Available: hasKey, UnavailableReason: cloudReason, Capabilities: []string{"speech", "multilingual", "local-qa"}, DefaultVoice: "Aiden", RetryPolicy: "none"},
	}
}

func Find(id string) (Model, bool) {
	for _, item := range Models() {
		if item.ID == strings.TrimSpace(id) {
			return item, true
		}
	}
	return Model{}, false
}

func IsCloud(id string) bool {
	item, ok := Find(id)
	return ok && item.Cloud
}

func IsLocalTTS(id string) bool {
	item, ok := Find(id)
	return ok && item.Type == "tts" && item.Provider == "local" && !item.Cloud
}

func IsLocalTranslation(id string) bool {
	item, ok := Find(id)
	return ok && item.Type == "translation" && item.Provider == "local-mlx" && !item.Cloud
}

func Voices(modelID string) []Voice {
	switch modelID {
	case LocalTTSModel, LocalTTS17BModel:
		return []Voice{
			{ID: "aiden", Name: "aiden", DisplayName: "Aiden（本地美式）"},
			{ID: "ryan", Name: "ryan", DisplayName: "Ryan（本地英式）"},
		}
	case CloudTTSModel:
		names := []string{"Serena", "Ethan", "Chelsie", "Momo", "Vivian", "Moon", "Maia", "Kai", "Nofish", "Bella", "Jennifer", "Ryan", "Katerina", "Aiden", "Eldric Sage", "Mia", "Mochi", "Bellona", "Vincent", "Bunny", "Neil", "Elias", "Arthur", "Nini", "Seren", "Pip", "Stella", "Bodega", "Sonrisa", "Alek", "Dolce", "Sohee", "Ono Anna", "Lenn", "Emilien", "Andre", "Radio Gol"}
		out := make([]Voice, 0, len(names))
		for _, name := range names {
			out = append(out, Voice{ID: name, Name: name, DisplayName: name})
		}
		return out
	default:
		return []Voice{}
	}
}

func ValidVoice(modelID, voiceID string) bool {
	for _, voice := range Voices(modelID) {
		if voice.ID == strings.TrimSpace(voiceID) {
			return true
		}
	}
	return false
}

func NormalizeSettings(value Settings) Settings {
	defaults := DefaultSettings()
	if value.OCRModel == "" {
		value.OCRModel = defaults.OCRModel
	}
	if value.TranslationModel == "" {
		value.TranslationModel = defaults.TranslationModel
	}
	if value.TTSModel == "" {
		value.TTSModel = defaults.TTSModel
	}
	if value.TTSVoice == "" {
		if model, ok := Find(value.TTSModel); ok && model.DefaultVoice != "" {
			value.TTSVoice = model.DefaultVoice
		} else {
			value.TTSVoice = defaults.TTSVoice
		}
	}
	return value
}
