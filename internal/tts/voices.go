package tts

import "strings"

const (
	AccentUS = "en-US"
	AccentGB = "en-GB"

	// Keep the accent-to-speaker mapping aligned with the current project
	// configuration.
	DefaultAmericanVoice = "aiden"
	DefaultBritishVoice  = "ryan"
)

// VoiceOption is the single backend-owned list exposed to every admin UI.
// IDs are the real Qwen3-TTS CustomVoice speaker IDs currently used by the
// Python engine; adding another supported speaker only requires adding it here.
type VoiceOption struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

var voiceOptions = []VoiceOption{
	{ID: "aiden", Name: "aiden", DisplayName: "aiden"},
	{ID: "ryan", Name: "ryan", DisplayName: "ryan"},
}

func VoiceOptions() []VoiceOption {
	result := make([]VoiceOption, len(voiceOptions))
	copy(result, voiceOptions)
	return result
}

func ValidVoice(id string) bool {
	for _, option := range voiceOptions {
		if option.ID == strings.TrimSpace(id) {
			return true
		}
	}
	return false
}

type Config struct {
	AmericanEnabled bool
	BritishEnabled  bool
	AmericanVoiceID string
	BritishVoiceID  string
}

func (c Config) Voice(accent string) (string, bool) {
	switch accent {
	case AccentUS:
		return DefaultAmericanVoice, c.AmericanEnabled
	case AccentGB:
		return DefaultBritishVoice, c.BritishEnabled
	default:
		return "", false
	}
}

func (c Config) EnabledVoices() map[string]string {
	result := make(map[string]string, 2)
	if c.AmericanEnabled {
		result[AccentUS] = DefaultAmericanVoice
	}
	if c.BritishEnabled {
		result[AccentGB] = DefaultBritishVoice
	}
	return result
}
