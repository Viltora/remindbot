package yougile

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New("test-key", slog.New(slog.DiscardHandler))
	c.baseURL, c.minGap, c.retryDelay = srv.URL, 0, time.Millisecond
	return c
}

func TestTasksPagesAndOnlyReads(t *testing.T) {
	var mu sync.Mutex
	var offsets []string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method %s, want GET", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		if r.URL.Path != "/task-list" || r.URL.Query().Get("columnId") != "col" {
			t.Errorf("unexpected request %s", r.URL)
		}
		off := r.URL.Query().Get("offset")
		mu.Lock()
		offsets = append(offsets, off)
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{
			"paging":  map[string]any{"next": off == "0"},
			"content": []map[string]any{{"id": "t" + off}},
		})
	})

	tasks, err := c.Tasks(context.Background(), "col")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 || tasks[0].ID != "t0" || tasks[1].ID != "t1000" {
		t.Errorf("tasks = %+v", tasks)
	}
	if strings.Join(offsets, ",") != "0,1000" {
		t.Errorf("offsets = %v", offsets)
	}
}

func TestRetriesOn429(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{"paging":{"next":false},"content":[{"id":"u1","realName":"Катя"}]}`))
	})
	users, err := c.Users(context.Background())
	if err != nil || len(users) != 1 || users[0].RealName != "Катя" {
		t.Fatalf("users = %+v, err %v", users, err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestStart(t *testing.T) {
	nine := time.Date(2026, 10, 22, 6, 0, 0, 0, time.UTC) // 09:00 in Moscow
	for _, tc := range []struct {
		name string
		d    *Deadline
		want time.Time
		ok   bool
	}{
		{"start rounded to the minute", &Deadline{StartDate: nine.UnixMilli() - 100, Deadline: nine.Add(time.Hour).UnixMilli(), WithTime: true}, nine, true},
		{"deadline without start", &Deadline{Deadline: nine.UnixMilli(), WithTime: true}, nine, true},
		{"date without time", &Deadline{StartDate: nine.UnixMilli()}, time.Time{}, false},
		{"no deadline", nil, time.Time{}, false},
	} {
		got, ok := Task{Deadline: tc.d}.Start()
		if ok != tc.ok || !got.Equal(tc.want) {
			t.Errorf("%s: got %v %v, want %v %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestIsDeleted(t *testing.T) {
	for raw, want := range map[string]bool{``: false, `null`: false, `false`: false, `true`: true} {
		if got := (Task{Deleted: json.RawMessage(raw)}).IsDeleted(); got != want {
			t.Errorf("deleted %q: got %v, want %v", raw, got, want)
		}
	}
}

// The bot must never change anything in YouGile: no request method other
// than GET may appear in the package.
func TestReadOnly(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	writes := regexp.MustCompile(`http\.Method(Post|Put|Patch|Delete)|"(POST|PUT|PATCH|DELETE)"|\.(Post|PostForm)\(`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if m := writes.Find(src); m != nil {
			t.Errorf("%s: %s — the YouGile client must only read", f, m)
		}
	}
}
