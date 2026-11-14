package storage

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type SalesStatus string

const (
	SalesPending  SalesStatus = "pending"   // no answer yet
	SalesSent     SalesStatus = "sent"      // answered "yes"
	SalesNotSent  SalesStatus = "not_sent"  // answered "no"; asked again while attempts remain
	SalesNoAnswer SalesStatus = "no_answer" // the day closed without an answer
)

type SalesReport struct {
	Date            string // "2006-01-02"
	User            User
	Status          SalesStatus
	Attempts        int
	AnsweredAttempt int
	Closed          bool
}

// ErrClosed means the day is already summed up and answers are final.
var ErrClosed = errors.New("closed")

// Dates are passed as "2006-01-02" strings in the bot's time zone.

const salesReportColumns = `
	to_char(r.report_date, 'YYYY-MM-DD'), u.tg_id, COALESCE(u.username, ''), u.first_name, u.role,
	r.status, r.attempts, COALESCE(r.answered_attempt, 0), r.closed`

func scanSalesReport(row pgx.CollectableRow) (SalesReport, error) {
	var r SalesReport
	err := row.Scan(&r.Date, &r.User.TgID, &r.User.Username, &r.User.FirstName, &r.User.Role,
		&r.Status, &r.Attempts, &r.AnsweredAttempt, &r.Closed)
	return r, err
}

// StartSalesDay creates the day's rows for the given users and returns the
// users whose rows were created now. Calling it again is a no-op.
func (s *Store) StartSalesDay(ctx context.Context, date string, tgIDs []int64) ([]User, error) {
	rows, err := s.pool.Query(ctx, `
		WITH created AS (
			INSERT INTO sales_reports (report_date, tg_id)
			SELECT $1::date, unnest($2::bigint[])
			ON CONFLICT DO NOTHING
			RETURNING tg_id
		)
		SELECT u.tg_id, COALESCE(u.username, ''), u.first_name, u.role
		FROM created JOIN users u USING (tg_id)`, date, tgIDs)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanUser)
}

// AdvanceSalesAttempt moves still-unconfirmed reports to the given attempt
// and returns them. Rows already at that attempt are skipped, so a retried
// job does not ask twice.
func (s *Store) AdvanceSalesAttempt(ctx context.Context, date string, attempt int) ([]SalesReport, error) {
	rows, err := s.pool.Query(ctx, `
		WITH advanced AS (
			UPDATE sales_reports
			SET attempts = $2
			WHERE report_date = $1::date AND NOT closed AND attempts < $2
			  AND status IN ('pending', 'not_sent')
			RETURNING *
		)
		SELECT `+salesReportColumns+`
		FROM advanced r JOIN users u USING (tg_id)`, date, attempt)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanSalesReport)
}

// AnswerSales records a "yes"/"no" answer on the current attempt. "Yes" is
// final: buttons under earlier questions stay pressable, and a "no" there must
// not undo it. The returned report then still has SalesSent.
func (s *Store) AnswerSales(ctx context.Context, date string, tgID int64, sent bool) (SalesReport, error) {
	status := SalesNotSent
	if sent {
		status = SalesSent
	}
	// In SET, status refers to the value before the update.
	rows, err := s.pool.Query(ctx, `
		WITH answered AS (
			UPDATE sales_reports
			SET status           = CASE WHEN status = 'sent' THEN status ELSE $3 END,
			    answered_attempt = CASE WHEN status = 'sent' THEN answered_attempt ELSE attempts END,
			    answered_at      = CASE WHEN status = 'sent' THEN answered_at ELSE now() END
			WHERE report_date = $1::date AND tg_id = $2 AND NOT closed
			RETURNING *
		)
		SELECT `+salesReportColumns+`
		FROM answered r JOIN users u USING (tg_id)`, date, tgID, status)
	if err != nil {
		return SalesReport{}, err
	}
	r, err := pgx.CollectExactlyOneRow(rows, scanSalesReport)
	if errors.Is(err, pgx.ErrNoRows) {
		var closed bool
		err := s.pool.QueryRow(ctx, `
			SELECT closed FROM sales_reports WHERE report_date = $1::date AND tg_id = $2`,
			date, tgID).Scan(&closed)
		if errors.Is(err, pgx.ErrNoRows) {
			return SalesReport{}, ErrNotFound
		}
		if err != nil {
			return SalesReport{}, err
		}
		return SalesReport{}, ErrClosed
	}
	return r, err
}

// CloseSalesDay makes the day's answers final: unanswered reports become
// "no_answer". closedNow is false if the day was already closed, so the
// summary is sent once.
func (s *Store) CloseSalesDay(ctx context.Context, date string) (reports []SalesReport, closedNow bool, err error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE sales_reports
		SET closed = true,
		    status = CASE WHEN status = 'pending' THEN 'no_answer' ELSE status END
		WHERE report_date = $1::date AND NOT closed`, date)
	if err != nil {
		return nil, false, err
	}
	reports, err = s.SalesDay(ctx, date)
	return reports, tag.RowsAffected() > 0, err
}

// SalesDay returns the day's reports, problems first.
func (s *Store) SalesDay(ctx context.Context, date string) ([]SalesReport, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+salesReportColumns+`
		FROM sales_reports r JOIN users u USING (tg_id)
		WHERE r.report_date = $1::date
		ORDER BY CASE r.status WHEN 'no_answer' THEN 0 WHEN 'not_sent' THEN 1
		                       WHEN 'pending' THEN 2 ELSE 3 END,
		         u.username`, date)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanSalesReport)
}

// SalesReports returns the reports for the days from–to inclusive, by day.
func (s *Store) SalesReports(ctx context.Context, from, to string) ([]SalesReport, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+salesReportColumns+`
		FROM sales_reports r JOIN users u USING (tg_id)
		WHERE r.report_date BETWEEN $1::date AND $2::date
		ORDER BY r.report_date, u.username`, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanSalesReport)
}

// SalesDate formats a day for the queries above.
func SalesDate(t time.Time) string {
	return t.Format(time.DateOnly)
}
