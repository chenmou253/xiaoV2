package tts

import "testing"

func TestEnabledVoices(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		want   map[string]string
	}{
		{"american only ignores stale role", Config{AmericanEnabled: true, AmericanVoiceID: "ryan", BritishVoiceID: "aiden"}, map[string]string{AccentUS: "aiden"}},
		{"british only ignores stale role", Config{BritishEnabled: true, AmericanVoiceID: "ryan", BritishVoiceID: "aiden"}, map[string]string{AccentGB: "ryan"}},
		{"both locked regardless of stored values", Config{AmericanEnabled: true, BritishEnabled: true, AmericanVoiceID: "ryan", BritishVoiceID: "aiden"}, map[string]string{AccentUS: "aiden", AccentGB: "ryan"}},
		{"neither", Config{AmericanVoiceID: "aiden", BritishVoiceID: "ryan"}, map[string]string{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.config.EnabledVoices()
			if len(got) != len(test.want) {
				t.Fatalf("got %v want %v", got, test.want)
			}
			for accent, voice := range test.want {
				if got[accent] != voice {
					t.Fatalf("got %v want %v", got, test.want)
				}
			}
		})
	}
}

func TestVoiceOptionsAreRealConfiguredSpeakers(t *testing.T) {
	options := VoiceOptions()
	if len(options) != 2 || options[0].ID != "aiden" || options[0].Name != "aiden" || options[0].DisplayName != "aiden" || options[1].ID != "ryan" || options[1].Name != "ryan" || options[1].DisplayName != "ryan" {
		t.Fatalf("unexpected Qwen speakers: %#v", options)
	}
	if !ValidVoice("aiden") || !ValidVoice("ryan") || ValidVoice("invented") {
		t.Fatal("voice validation does not match configured speakers")
	}
}
