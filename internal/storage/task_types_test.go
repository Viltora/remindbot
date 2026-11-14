package storage

import (
	"context"
	"testing"
)

func TestTaskTypes(t *testing.T) {
	for _, tt := range TaskTypes {
		if tt.Title() == string(tt) || tt.Noun() == string(tt) {
			t.Errorf("%s has no title or noun", tt)
		}
	}
	for tt, want := range map[TaskType]bool{TaskPost: true, TaskReels: true, TaskStories: false, TaskTelegram: false} {
		if got := tt.NeedsMaterial(); got != want {
			t.Errorf("%s.NeedsMaterial() = %v, want %v", tt, got, want)
		}
	}
}

// Every type in code has a YouGile column, and every column has a known type.
func TestTaskColumns(t *testing.T) {
	s := openTestStore(t)
	cols, err := s.TaskColumns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	matched := map[TaskType]bool{}
	for _, c := range cols {
		matched[c.Type] = true
	}
	for _, tt := range TaskTypes {
		if !matched[tt] {
			t.Errorf("no YouGile column for %s", tt)
		}
	}
	if len(matched) != len(TaskTypes) {
		t.Errorf("columns have types %v, code knows %v", matched, TaskTypes)
	}
}
