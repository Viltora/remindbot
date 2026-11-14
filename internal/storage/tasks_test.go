package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSyncTasks(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	from, to := now.Add(-24*time.Hour), now.Add(8*24*time.Hour)
	post := Task{ID: "post", Type: TaskPost, Title: "афиши", PublishAt: now.Add(24 * time.Hour), Assignees: []string{"u1"}}
	story := Task{ID: "story", Type: TaskStories, Title: "репост", PublishAt: now.Add(2 * time.Hour)}
	users := []YouGileUser{{ID: "u1", RealName: "Катя"}}

	if err := s.SyncTasks(ctx, users, []Task{post, story}, from, to); err != nil {
		t.Fatal(err)
	}
	active, err := s.ActiveTasks(ctx, from, to)
	if err != nil || len(active) != 2 || active[0].ID != "story" || active[1].Assignees[0] != "u1" {
		t.Fatalf("active = %+v, err %v", active, err)
	}
	if names, _ := s.YouGileNames(ctx, []string{"u1", "nobody"}); names["u1"] != "Катя" || len(names) != 1 {
		t.Errorf("names = %v", names)
	}

	// A step is claimed once.
	if ok, _ := s.AdvanceMaterial(ctx, "post", 0); !ok {
		t.Fatal("material step 1 not claimed")
	}
	if ok, _ := s.AdvanceMaterial(ctx, "post", 0); ok {
		t.Fatal("material step 1 claimed twice")
	}
	if ok, _ := s.AdvanceReminders(ctx, "story", 0, 2); !ok {
		t.Fatal("reminders not claimed")
	}
	if ok, _ := s.AdvancePublishAttempt(ctx, "story", 0); !ok {
		t.Fatal("attempt 1 not claimed")
	}

	// The post moves a day later, the story is gone from YouGile.
	post.PublishAt = post.PublishAt.Add(24 * time.Hour)
	if err := s.SyncTasks(ctx, users, []Task{post}, from, to); err != nil {
		t.Fatal(err)
	}
	moved, err := s.GetTask(ctx, "post")
	if err != nil || moved.MaterialStep != 0 || !moved.PublishAt.Equal(post.PublishAt) {
		t.Errorf("moved post = %+v, err %v; want material steps from the start", moved, err)
	}
	if active, _ := s.ActiveTasks(ctx, from, to); len(active) != 1 {
		t.Errorf("active after the story was removed = %d, want 1", len(active))
	}
	if ok, _ := s.AdvancePublishAttempt(ctx, "story", 1); ok {
		t.Error("an inactive task got a question")
	}

	// Material: the first file counts, a move does not reset it.
	if _, first, err := s.ReceiveMaterial(ctx, "post", 42); err != nil || !first {
		t.Fatalf("receive: first %v, err %v", first, err)
	}
	if _, first, _ := s.ReceiveMaterial(ctx, "post", 43); first {
		t.Error("material received twice")
	}
	post.PublishAt = post.PublishAt.Add(time.Hour)
	if err := s.SyncTasks(ctx, users, []Task{post}, from, to); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetTask(ctx, "post"); got.MaterialReceivedAt.IsZero() {
		t.Error("material lost after a move")
	}

	// Messages about a task.
	if err := s.AddTaskMessage(ctx, -100, 7, "post", TaskMsgMaterial); err != nil {
		t.Fatal(err)
	}
	if id, kind, err := s.TaskForMessage(ctx, -100, 7); err != nil || id != "post" || kind != TaskMsgMaterial {
		t.Errorf("message 7: %q %q %v", id, kind, err)
	}
	if _, _, err := s.TaskForMessage(ctx, -100, 8); !errors.Is(err, ErrNotFound) {
		t.Errorf("message 8: err %v, want ErrNotFound", err)
	}
}

func TestAnswerPublished(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now()
	task := Task{ID: "tg", Type: TaskTelegram, Title: "кофейня", PublishAt: now}
	if err := s.SyncTasks(ctx, nil, []Task{task}, now.Add(-time.Hour), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for attempt := range 2 {
		if ok, _ := s.AdvancePublishAttempt(ctx, "tg", attempt); !ok {
			t.Fatalf("attempt %d not claimed", attempt+1)
		}
	}

	got, err := s.AnswerPublished(ctx, "tg", true)
	if err != nil || got.Status != PublishDone || got.AnsweredAttempt != 2 {
		t.Fatalf("yes: %+v, err %v", got, err)
	}
	// "No" under an earlier question does not undo "yes".
	if got, _ := s.AnswerPublished(ctx, "tg", false); got.Status != PublishDone {
		t.Errorf("no after yes: status %s", got.Status)
	}
	if ok, _ := s.AdvancePublishAttempt(ctx, "tg", 2); ok {
		t.Error("asked again after the answer")
	}
	if _, err := s.AnswerPublished(ctx, "nope", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown task: err %v", err)
	}
}

// A moved task is asked about again, unless it was answered "published".
func TestMoveResetsAnswer(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now()
	from, to := now.Add(-time.Hour), now.Add(72*time.Hour)
	no := Task{ID: "no", Type: TaskPost, Title: "афиши", PublishAt: now}
	yes := Task{ID: "yes", Type: TaskStories, Title: "репост", PublishAt: now}
	if err := s.SyncTasks(ctx, nil, []Task{no, yes}, from, to); err != nil {
		t.Fatal(err)
	}
	for id, published := range map[string]bool{"no": false, "yes": true} {
		if ok, _ := s.AdvancePublishAttempt(ctx, id, 0); !ok {
			t.Fatalf("%s: attempt not claimed", id)
		}
		if _, err := s.AnswerPublished(ctx, id, published); err != nil {
			t.Fatal(err)
		}
	}

	no.PublishAt, yes.PublishAt = now.Add(24*time.Hour), now.Add(24*time.Hour)
	if err := s.SyncTasks(ctx, nil, []Task{no, yes}, from, to); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetTask(ctx, "no"); got.Status != PublishPending || got.Attempts != 0 || got.AnsweredAttempt != 0 {
		t.Errorf("moved after «no»: %+v, want asked from the start", got)
	}
	if got, _ := s.GetTask(ctx, "yes"); got.Status != PublishDone || got.Attempts != 1 {
		t.Errorf("moved after «yes»: %+v, want the answer kept", got)
	}
}
