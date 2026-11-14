package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotatingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "bot.log")
	r, err := openRotatingFile(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("x", 39) + "\n" // 40 bytes: two lines fit, the third rotates
	for range 7 {
		if _, err := r.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	r.Close()

	size := func(p string) int64 {
		st, err := os.Stat(p)
		if err != nil {
			return -1
		}
		return st.Size()
	}
	// 7 lines: [1 2] → .2 is dropped later; [3 4] → .2, [5 6] → .1, [7] current.
	if got := size(path); got != 40 {
		t.Errorf("current file is %d bytes, want 40", got)
	}
	if size(path+".1") != 80 || size(path+".2") != 80 {
		t.Errorf("rotated files: .1 %d, .2 %d bytes, want 80 and 80", size(path+".1"), size(path+".2"))
	}
	if size(path+".3") != -1 {
		t.Error("only 2 old files should be kept")
	}

	// Reopening continues the current file.
	r, err = openRotatingFile(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	r.Write([]byte(line))
	r.Close()
	if got := size(path); got != 80 {
		t.Errorf("after reopening: %d bytes, want 80", got)
	}
}
