package rotator

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	DefaultMaxSize    = 100 * 1024 * 1024 // 100 MB
	DefaultMaxBackups = 5
)

type Rotator struct {
	mu         sync.Mutex
	path       string
	maxSize    int64
	maxBackups int
	file       *os.File
	size       int64
}

func New(path string, maxSize int64, maxBackups int) *Rotator {
	if maxSize <= 0 {
		maxSize = DefaultMaxSize
	}
	if maxBackups <= 0 {
		maxBackups = DefaultMaxBackups
	}
	return &Rotator{
		path:       path,
		maxSize:    maxSize,
		maxBackups: maxBackups,
	}
}

func (r *Rotator) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.openFile(); err != nil {
		return 0, fmt.Errorf("rotator: open: %w", err)
	}

	if r.size+int64(len(p)) > r.maxSize {
		if err := r.rotate(); err != nil {
			return 0, fmt.Errorf("rotator: rotate: %w", err)
		}
	}

	n, err := r.file.Write(p)
	if err != nil {
		return n, err
	}
	r.size += int64(n)
	return n, nil
}

func (r *Rotator) openFile() error {
	if r.file != nil {
		return nil
	}

	dir := filepath.Dir(r.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}

	stat, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}

	r.file = f
	r.size = stat.Size()
	return nil
}

func (r *Rotator) rotate() error {
	if r.file != nil {
		r.file.Close()
		r.file = nil
	}

	// Delete oldest if at maxBackups
	r.pruneBackups()

	// Rename current log to backup
	backupName := fmt.Sprintf("%s.%s", r.path, timestampSuffix())
	if err := os.Rename(r.path, backupName); err != nil && !os.IsNotExist(err) {
		return err
	}

	r.size = 0
	return r.openFile()
}

func timestampSuffix() string {
	return time.Now().Format("20060102-150405")
}

func (r *Rotator) pruneBackups() {
	dir := filepath.Dir(r.path)
	base := filepath.Base(r.path)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	type backup struct {
		name string
		info os.FileInfo
	}

	var backups []backup
	for _, e := range entries {
		name := e.Name()
		if len(name) > len(base) && name[:len(base)] == base && name[len(base)] == '.' {
			info, err := e.Info()
			if err == nil {
				backups = append(backups, backup{name: filepath.Join(dir, name), info: info})
			}
		}
	}

	if len(backups) <= r.maxBackups {
		return
	}

	// Sort by modification time, oldest first
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].info.ModTime().Before(backups[j].info.ModTime())
	})

	// Remove oldest
	toRemove := len(backups) - r.maxBackups
	for i := 0; i < toRemove; i++ {
		os.Remove(backups[i].name)
	}
}

// Close closes the underlying file.
func (r *Rotator) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file != nil {
		err := r.file.Close()
		r.file = nil
		return err
	}
	return nil
}