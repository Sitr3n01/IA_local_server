package rotatelog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Writer is a small, dependency-free rotating file writer for metadata-only
// service logs. Rotation targets are derived only from the configured path.
type Writer struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	backups  int
	maxAge   time.Duration
	file     *os.File
	size     int64
}

func Open(path string, maxBytes int64, backups int, maxAge time.Duration) (*Writer, error) {
	if path == "" {
		return nil, errors.New("log path is required")
	}
	if maxBytes <= 0 || backups < 1 || maxAge <= 0 {
		return nil, errors.New("invalid log rotation policy")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve log path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	w := &Writer{path: abs, maxBytes: maxBytes, backups: backups, maxAge: maxAge}
	if err := w.openLocked(); err != nil {
		return nil, err
	}
	if err := w.cleanupExpiredLocked(time.Now()); err != nil {
		_ = w.file.Close()
		return nil, err
	}
	return w, nil
}

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return 0, errors.New("log writer is closed")
	}
	if w.size > 0 && w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *Writer) openLocked() error {
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("stat log: %w", err)
	}
	w.file = file
	w.size = info.Size()
	return nil
}

func (w *Writer) rotateLocked() error {
	if err := w.file.Close(); err != nil {
		return fmt.Errorf("close log for rotation: %w", err)
	}
	w.file = nil

	oldest := w.backupPath(w.backups)
	if err := os.Remove(oldest); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove oldest log backup: %w", err)
	}
	for index := w.backups - 1; index >= 1; index-- {
		from := w.backupPath(index)
		to := w.backupPath(index + 1)
		if err := os.Rename(from, to); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("rotate log backup %d: %w", index, err)
		}
	}
	if err := os.Rename(w.path, w.backupPath(1)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("rotate current log: %w", err)
	}
	if err := w.cleanupExpiredLocked(time.Now()); err != nil {
		return err
	}
	return w.openLocked()
}

func (w *Writer) cleanupExpiredLocked(now time.Time) error {
	cutoff := now.Add(-w.maxAge)
	for index := 1; index <= w.backups; index++ {
		path := w.backupPath(index)
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("stat log backup: %w", err)
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove expired log backup: %w", err)
			}
		}
	}
	return nil
}

func (w *Writer) backupPath(index int) string {
	return fmt.Sprintf("%s.%d", w.path, index)
}
