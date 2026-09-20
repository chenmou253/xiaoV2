package resource

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

var bookIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)

var ErrInvalidBookID = errors.New("invalid book id")

type Manager struct{ root string }

func New(root string) (*Manager, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve resource root: %w", err)
	}
	if err = os.MkdirAll(abs, 0o750); err != nil {
		return nil, fmt.Errorf("create resource root: %w", err)
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve resource root symlinks: %w", err)
	}
	return &Manager{root: abs}, nil
}

func ValidBookID(bookID string) bool { return bookIDPattern.MatchString(bookID) }

func (m *Manager) Root() string { return m.root }

func (m *Manager) BookRoot(bookID string) (string, error) {
	if !ValidBookID(bookID) {
		return "", ErrInvalidBookID
	}
	return m.safeJoin(m.root, bookID)
}

func (m *Manager) Dir(bookID, kind string) (string, error) {
	allowed := map[string]bool{"source": true, "pages": true, "audio": true, "tts": true, "ocr": true, "text": true, "metadata": true, "cache": true}
	if !allowed[kind] {
		return "", fmt.Errorf("unknown resource directory %q", kind)
	}
	root, err := m.BookRoot(bookID)
	if err != nil {
		return "", err
	}
	return m.safeJoin(root, kind)
}

func (m *Manager) Resolve(bookID, relative string) (string, error) {
	root, err := m.BookRoot(bookID)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(relative) || relative == "" {
		return "", errors.New("resource path must be relative")
	}
	return m.safeJoin(root, filepath.FromSlash(relative))
}

func (m *Manager) safeJoin(root string, parts ...string) (string, error) {
	target := filepath.Join(append([]string{root}, parts...)...)
	abs, err := filepath.Abs(filepath.Clean(target))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || filepath.IsAbs(rel) || (len(rel) > 3 && rel[:3] == ".."+string(filepath.Separator)) {
		return "", errors.New("resource path escapes root")
	}
	return abs, nil
}
