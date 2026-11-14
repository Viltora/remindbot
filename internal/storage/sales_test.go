package storage

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"

	"remindbot/internal/config"
)

// Integration test against a real Postgres. Run with:
//
//	make test-db
func openTestStore(t *testing.T) *Store {
	t.Helper()
	if os.Getenv("REMINDBOT_TEST_DB") == "" {
		t.Skip("set REMINDBOT_TEST_DB=1 to run tests against Postgres (make test-db)")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := Open(ctx, cfg.DB, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx, log); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `TRUNCATE users, chats, tasks CASCADE`); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSalesDay(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const date = "2026-10-20"

	for _, u := range []User{{TgID: 1, Username: "ivan"}, {TgID: 2, Username: "petr"}, {TgID: 3, Username: "olga"}, {TgID: 4, Username: "admin"}} {
		if _, err := s.TouchUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	sales := []int64{1, 2, 3}
	for _, id := range sales {
		if err := s.SetRole(ctx, id, RoleSales); err != nil {
			t.Fatal(err)
		}
	}

	started, err := s.StartSalesDay(ctx, date, sales)
	if err != nil || len(started) != 3 {
		t.Fatalf("start: got %d users, err %v; want 3", len(started), err)
	}
	if again, _ := s.StartSalesDay(ctx, date, sales); len(again) != 0 {
		t.Fatalf("second start created %d rows, want 0", len(again))
	}

	if week, err := s.SalesReports(ctx, "2026-10-19", "2026-10-25"); err != nil || len(week) != 3 || week[0].Date != date {
		t.Fatalf("week: %+v, err %v", week, err)
	}

	// Attempt 1 asks all three; a retried job asks nobody.
	if asked, _ := s.AdvanceSalesAttempt(ctx, date, 1); len(asked) != 3 {
		t.Fatalf("attempt 1 asked %d, want 3", len(asked))
	}
	if asked, _ := s.AdvanceSalesAttempt(ctx, date, 1); len(asked) != 0 {
		t.Fatalf("retried attempt 1 asked %d, want 0", len(asked))
	}

	// ivan: yes on attempt 1; petr: no on attempt 1; olga: silent.
	if r, err := s.AnswerSales(ctx, date, 1, true); err != nil || r.Status != SalesSent || r.AnsweredAttempt != 1 {
		t.Fatalf("ivan answer: %+v %v", r, err)
	}
	if _, err := s.AnswerSales(ctx, date, 2, false); err != nil {
		t.Fatal(err)
	}

	// Attempt 2 asks petr (said no) and olga (silent), not ivan.
	asked, err := s.AdvanceSalesAttempt(ctx, date, 2)
	if err != nil || len(asked) != 2 {
		t.Fatalf("attempt 2 asked %d, err %v; want 2", len(asked), err)
	}

	// "Yes" is final: "no" under an earlier question keeps it, and a repeated
	// "yes" keeps the attempt it came on.
	if r, err := s.AnswerSales(ctx, date, 1, false); err != nil || r.Status != SalesSent || r.AnsweredAttempt != 1 {
		t.Fatalf("ivan stale no: %+v %v; want sent on attempt 1", r, err)
	}
	if r, err := s.AnswerSales(ctx, date, 1, true); err != nil || r.AnsweredAttempt != 1 {
		t.Fatalf("ivan repeated yes: %+v %v; want attempt 1", r, err)
	}

	reports, closedNow, err := s.CloseSalesDay(ctx, date)
	if err != nil || !closedNow {
		t.Fatalf("close: closedNow=%v err=%v", closedNow, err)
	}
	got := map[string]SalesReport{}
	for _, r := range reports {
		got[r.User.Username] = r
	}
	if got["ivan"].Status != SalesSent || got["petr"].Status != SalesNotSent || got["olga"].Status != SalesNoAnswer {
		t.Fatalf("statuses after close: %+v", got)
	}
	if got["olga"].Attempts != 2 {
		t.Fatalf("olga attempts = %d, want 2", got["olga"].Attempts)
	}
	// Problems come first in the summary.
	if reports[0].User.Username != "olga" {
		t.Fatalf("first in summary = %s, want olga", reports[0].User.Username)
	}

	if _, closedNow, _ := s.CloseSalesDay(ctx, date); closedNow {
		t.Fatal("second close reported closedNow; the summary would be sent twice")
	}
	if _, err := s.AnswerSales(ctx, date, 3, true); !errors.Is(err, ErrClosed) {
		t.Fatalf("late answer: err = %v, want ErrClosed", err)
	}
	if _, err := s.AnswerSales(ctx, date, 4, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("answer from non-sales user: err = %v, want ErrNotFound", err)
	}
}

func TestPrimaryAdmin(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for _, u := range []User{{TgID: 1, Username: "nastya"}, {TgID: 2, Username: "olga"}} {
		if _, err := s.TouchUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.PrimaryAdmin(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no admins: err = %v, want ErrNotFound", err)
	}

	// The first admin becomes primary, the second does not.
	if err := s.SetRole(ctx, 1, RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRole(ctx, 2, RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if p, err := s.PrimaryAdmin(ctx); err != nil || p.Username != "nastya" {
		t.Fatalf("primary = %+v, %v; want nastya", p, err)
	}

	if err := s.SetPrimaryAdmin(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.PrimaryAdmin(ctx); p.Username != "olga" {
		t.Fatalf("primary = %s, want olga", p.Username)
	}

	// Losing the admin role drops the mark.
	if err := s.SetRole(ctx, 2, RoleMember); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrimaryAdmin(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after demotion: err = %v, want ErrNotFound", err)
	}
	if err := s.SetPrimaryAdmin(ctx, 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("non-admin as primary: err = %v, want ErrNotFound", err)
	}
}

func TestChatPurpose(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.UpsertChat(ctx, -100, "SMM", true); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertChat(ctx, -200, "Продажи", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetChatPurpose(ctx, -100, PurposeContent); err != nil {
		t.Fatal(err)
	}
	if err := s.SetChatPurpose(ctx, -200, PurposeSales); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.ChatsFor(ctx, PurposeSales); len(ids) != 1 || ids[0] != -200 {
		t.Fatalf("sales chats = %v, want [-200]", ids)
	}

	// The bot left the sales chat; a late message from it must not bring it back.
	if err := s.UpsertChat(ctx, -200, "Продажи", false); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureChat(ctx, -200, "Продажи"); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.ChatsFor(ctx, PurposeSales); len(ids) != 0 {
		t.Fatalf("sales chats after leaving = %v, want none", ids)
	}

	// Re-adding keeps the purpose.
	if err := s.UpsertChat(ctx, -200, "Продажи", true); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.ChatsFor(ctx, PurposeSales); len(ids) != 1 {
		t.Fatalf("sales chats after re-adding = %v, want [-200]", ids)
	}

	if err := s.SetChatPurpose(ctx, -100, PurposeNone); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.ChatsFor(ctx, PurposeContent); len(ids) != 0 {
		t.Fatalf("content chats after clearing = %v, want none", ids)
	}
	if err := s.SetChatPurpose(ctx, -999, PurposeSales); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown chat: err = %v, want ErrNotFound", err)
	}
}
