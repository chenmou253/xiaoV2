package resource

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

type audioManifest struct {
	SchemaVersion int `json:"schema_version"`
	Items         []struct {
		Page   int    `json:"page"`
		ItemID string `json:"item_id"`
		Accent string `json:"accent"`
		Voice  string `json:"voice"`
		File   string `json:"file"`
	} `json:"items"`
	Failures []struct {
		Page   int    `json:"page"`
		ItemID string `json:"item_id"`
		Accent string `json:"accent"`
		Voice  string `json:"voice"`
	} `json:"failures"`
}

type AudioAccentStatus struct {
	Accent  string `json:"accent"`
	VoiceID string `json:"voice_id"`
	Ready   int    `json:"ready"`
	Failed  int    `json:"failed"`
	Total   int    `json:"total"`
	Status  string `json:"status"`
}

type AudioExpectedItem struct {
	Page   int
	ItemID string
}

// HasSpeakableText reports whether an OCR token contains a letter or digit.
// Standalone punctuation/symbols are not expected to have individual audio.
func HasSpeakableText(text string) bool {
	for _, character := range text {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			return true
		}
	}
	return false
}

func AudioStatusInDir(dir, accent, voice string, expected []AudioExpectedItem, legacy bool) AudioAccentStatus {
	status := AudioAccentStatus{Accent: accent, VoiceID: voice, Total: len(expected), Status: "not_generated"}
	manifest, err := readAudioManifest(dir)
	if err != nil {
		return status
	}
	ready := readyAudioItems(dir, manifest, accent, voice, legacy)
	for _, wanted := range expected {
		if ready[wanted] {
			status.Ready++
		}
	}
	expectedKeys := make(map[AudioExpectedItem]struct{}, len(expected))
	for _, item := range expected {
		expectedKeys[item] = struct{}{}
	}
	for _, failure := range manifest.Failures {
		key := AudioExpectedItem{Page: failure.Page, ItemID: failure.ItemID}
		if _, wanted := expectedKeys[key]; wanted && failure.Accent == accent && (legacy || failure.Voice == "" || strings.EqualFold(failure.Voice, voice)) {
			status.Failed++
		}
	}
	switch {
	case status.Total > 0 && status.Ready == status.Total && status.Failed == 0:
		status.Status = "ready"
	case status.Failed > 0:
		status.Status = "partial_failed"
	case status.Ready > 0:
		status.Status = "generating"
	}
	return status
}

func readyAudioItems(dir string, manifest audioManifest, accent, voice string, legacy bool) map[AudioExpectedItem]bool {
	ready := make(map[AudioExpectedItem]bool)
	for _, item := range manifest.Items {
		if item.Accent != accent || item.File == "" || (!legacy && !strings.EqualFold(item.Voice, voice)) {
			continue
		}
		if info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(item.File))); err == nil && info.Mode().IsRegular() {
			ready[AudioExpectedItem{Page: item.Page, ItemID: item.ItemID}] = true
		}
	}
	return ready
}

// AudioExpectedReadyInDir checks a whole page or draft against one manifest
// read. Callers that need many readiness checks must use this instead of
// repeatedly calling AudioItemReadyInDir.
func AudioExpectedReadyInDir(dir, accent, voice string, expected []AudioExpectedItem, legacy bool) bool {
	manifest, err := readAudioManifest(dir)
	if err != nil {
		return false
	}
	ready := readyAudioItems(dir, manifest, accent, voice, legacy)
	for _, item := range expected {
		if !ready[item] {
			return false
		}
	}
	return true
}

func readAudioManifest(dir string) (audioManifest, error) {
	var manifest audioManifest
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return manifest, err
	}
	if json.Unmarshal(raw, &manifest) != nil || manifest.SchemaVersion != 2 {
		return audioManifest{}, os.ErrNotExist
	}
	return manifest, nil
}

// AudioItemReadyInDir checks the logical item mapping, selected speaker and
// referenced file. Several word items may safely reference one immutable,
// content-addressed audio file while retaining independent page/item IDs.
func AudioItemReadyInDir(dir string, page int, itemID, accent, voice string, legacy bool) bool {
	manifest, err := readAudioManifest(dir)
	if err != nil {
		return false
	}
	for _, item := range manifest.Items {
		if item.Page != page || item.ItemID != itemID || item.Accent != accent || item.File == "" {
			continue
		}
		if !legacy && !strings.EqualFold(item.Voice, voice) {
			continue
		}
		if info, statErr := os.Stat(filepath.Join(dir, filepath.FromSlash(item.File))); statErr == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// ManifestVoice returns the most common recorded speaker for an accent. It is
// used when an old published book is copied into the editor, preserving the
// actual voice rather than imposing a new default.
func ManifestVoice(dir, accent string) string {
	manifest, err := readAudioManifest(dir)
	if err != nil {
		return ""
	}
	counts := map[string]int{}
	best, bestCount := "", 0
	for _, item := range manifest.Items {
		if item.Accent == accent && item.Voice != "" {
			counts[item.Voice]++
			if counts[item.Voice] > bestCount {
				best, bestCount = item.Voice, counts[item.Voice]
			}
		}
	}
	return best
}

// AudioItemFileInDir resolves a logical audio item from the v2 TTS manifest.
// Page and item ID remain part of the lookup even when repeated words share
// the same immutable file.
func AudioItemFileInDir(dir string, page int, itemID, accent string) (string, error) {
	if page < 1 || itemID == "" || (accent != "en-US" && accent != "en-GB") {
		return "", os.ErrNotExist
	}
	manifest, err := readAudioManifest(dir)
	if err != nil {
		return "", err
	}
	for _, item := range manifest.Items {
		if item.Page != page || item.ItemID != itemID || item.Accent != accent || item.File == "" {
			continue
		}
		return audioManifestFile(dir, item.File)
	}
	return "", os.ErrNotExist
}

func AudioAccentsInDir(dir string) []string {
	manifest, err := readAudioManifest(dir)
	if err != nil {
		return nil
	}
	found := map[string]bool{}
	for _, item := range manifest.Items {
		if (item.Accent != "en-US" && item.Accent != "en-GB") || item.Page < 1 || item.ItemID == "" || item.File == "" {
			continue
		}
		if _, err := audioManifestFile(dir, item.File); err == nil {
			found[item.Accent] = true
		}
	}
	result := make([]string, 0, len(found))
	for _, accent := range []string{"en-US", "en-GB"} {
		if found[accent] {
			result = append(result, accent)
		}
	}
	return result
}

func audioManifestFile(dir, file string) (string, error) {
	target := filepath.Join(dir, filepath.FromSlash(file))
	absoluteDir, dirErr := filepath.Abs(dir)
	absoluteTarget, targetErr := filepath.Abs(filepath.Clean(target))
	if dirErr != nil || targetErr != nil {
		return "", os.ErrNotExist
	}
	relative, relErr := filepath.Rel(absoluteDir, absoluteTarget)
	if relErr != nil || relative == ".." || filepath.IsAbs(relative) || (len(relative) > 3 && relative[:3] == ".."+string(filepath.Separator)) {
		return "", errors.New("audio manifest path escapes tts directory")
	}
	if info, statErr := os.Stat(absoluteTarget); statErr == nil && info.Mode().IsRegular() {
		return absoluteTarget, nil
	}
	return "", os.ErrNotExist
}

func (m *Manager) AudioItemFile(bookID string, page int, itemID, accent string) (string, error) {
	for _, kind := range []string{"tts", "audio"} {
		dir, err := m.Dir(bookID, kind)
		if err != nil {
			return "", err
		}
		if path, findErr := AudioItemFileInDir(dir, page, itemID, accent); findErr == nil {
			return path, nil
		}
	}
	return "", os.ErrNotExist
}

func (m *Manager) AudioAccents(bookID string) []string {
	found := map[string]bool{}
	for _, kind := range []string{"tts", "audio"} {
		dir, err := m.Dir(bookID, kind)
		if err != nil {
			continue
		}
		for _, accent := range AudioAccentsInDir(dir) {
			found[accent] = true
		}
	}
	result := make([]string, 0, len(found))
	for _, accent := range []string{"en-US", "en-GB"} {
		if found[accent] {
			result = append(result, accent)
		}
	}
	return result
}
