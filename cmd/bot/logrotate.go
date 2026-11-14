package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// rotatingFile is an append-only log file that is rotated by size: when the
// next write would make it bigger than maxSize, it becomes path.1, the older
// copies move up (path.1 → path.2 …) and path.<keep> is dropped. So the log
// never takes more than about maxSize × (keep+1) on disk.
type rotatingFile struct {
	mu      sync.Mutex
	path    string
	maxSize int64
	keep    int
	f       *os.File
	size    int64
}

const (
	logMaxSize = 10 << 20 // 10 MB per file
	logKeep    = 5        // remindbot.log.1 … remindbot.log.5
)

func openRotatingFile(path string, maxSize int64, keep int) (*rotatingFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	r := &rotatingFile{path: path, maxSize: maxSize, keep: keep}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size > 0 && r.size+int64(len(p)) > r.maxSize {
		if err := r.rotate(); err != nil {
			// Keep logging into the current file rather than lose lines.
			fmt.Fprintf(os.Stderr, "log rotation failed: %v\n", err)
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotatingFile) rotate() error {
	if err := r.f.Close(); err != nil {
		return err
	}
	os.Remove(fmt.Sprintf("%s.%d", r.path, r.keep))
	for i := r.keep - 1; i >= 1; i-- {
		os.Rename(fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1))
	}
	if err := os.Rename(r.path, r.path+".1"); err != nil && !os.IsNotExist(err) {
		r.open() // reopen the old file so writes go on
		return err
	}
	return r.open()
}

func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}
