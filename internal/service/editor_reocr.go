package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// clearDraftPageArtifacts removes generated data that belongs exclusively to
// one page before a new OCR job is queued. The rendered page image is kept so
// the reviewer can continue seeing the original PDF page while OCR runs.
func (s *EditorService) clearDraftPageArtifacts(id, book string, page int) error {
	if page < 1 {
		return errors.New("invalid OCR page")
	}
	bookRoot := filepath.Join(s.cfg.EditorRoot, id, "work", book)
	for _, path := range []string{
		filepath.Join(bookRoot, "ocr", fmt.Sprintf("page-%03d.json", page)),
		filepath.Join(bookRoot, "metadata", "pages", fmt.Sprintf("page-%03d.json", page)),
	} {
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("clear page %d artifact: %w", page, err)
		}
	}
	return s.clearDraftPageAudioArtifacts(id, book, page)
}

// clearDraftPageAudioArtifacts removes only audio outputs for one page. OCR,
// translation, and the page image remain available for a focused regeneration.
func (s *EditorService) clearDraftPageAudioArtifacts(id, book string, page int) error {
	if page < 1 {
		return errors.New("invalid audio page")
	}
	bookRoot := filepath.Join(s.cfg.EditorRoot, id, "work", book)
	for _, path := range []string{
		filepath.Join(bookRoot, "tts", "qa", fmt.Sprintf("page-%03d.jsonl", page)),
		filepath.Join(bookRoot, "tts", "page-"+fmt.Sprintf("%03d", page)),
		filepath.Join(bookRoot, "tts", "audio_failed", "page-"+fmt.Sprintf("%03d", page)),
		filepath.Join(bookRoot, "audio", "page-"+fmt.Sprintf("%03d", page)),
	} {
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("clear page %d audio artifact: %w", page, err)
		}
	}
	for _, manifest := range []string{
		filepath.Join(bookRoot, "tts", "manifest.json"),
		filepath.Join(bookRoot, "audio", "manifest.json"),
	} {
		if err := removePageFromAudioManifest(manifest, page); err != nil {
			return err
		}
	}
	return nil
}

func removePageFromAudioManifest(path string, page int) error {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var manifest struct {
		SchemaVersion int              `json:"schema_version"`
		Items         []map[string]any `json:"items"`
		Failures      []map[string]any `json:"failures"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil || manifest.SchemaVersion != 2 {
		// Legacy manifests do not have a page-safe mapping. The page-specific
		// directories above are still removed, but the legacy manifest is left
		// untouched so unrelated audio cannot be damaged.
		return nil
	}
	ttsRoot := filepath.Dir(path)
	filtered := make([]map[string]any, 0, len(manifest.Items))
	removedFiles := make(map[string]struct{})
	for _, item := range manifest.Items {
		itemPage, _ := item["page"].(float64)
		if int(itemPage) != page {
			filtered = append(filtered, item)
			continue
		}
		if file, ok := item["file"].(string); ok && file != "" {
			removedFiles[file] = struct{}{}
		}
	}
	manifest.Items = filtered
	// Content-addressed word audio may be referenced by items on several pages.
	// Remove a file only after its last manifest reference disappears.
	for _, item := range filtered {
		if file, ok := item["file"].(string); ok {
			delete(removedFiles, file)
		}
	}
	filteredFailures := make([]map[string]any, 0, len(manifest.Failures))
	for _, failure := range manifest.Failures {
		failurePage, _ := failure["page"].(float64)
		if int(failurePage) != page {
			filteredFailures = append(filteredFailures, failure)
		}
	}
	manifest.Failures = filteredFailures
	updated, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".reocr.tmp"
	if err := os.WriteFile(tmp, append(updated, '\n'), 0640); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// Publish the new manifest before deleting now-unreferenced files. Readers
	// that load the new mapping can never observe a missing shared cache file.
	for file := range removedFiles {
		if target, ok := safeManifestTarget(ttsRoot, file); ok {
			if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

func safeManifestTarget(root, relative string) (string, bool) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	target, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(rootAbs, target)
	if err != nil || rel == ".." || filepath.IsAbs(rel) || (len(rel) > 3 && rel[:3] == ".."+string(filepath.Separator)) {
		return "", false
	}
	return target, true
}
