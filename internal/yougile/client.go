// Package yougile reads tasks from YouGile (REST API v2).
//
// The bot never changes anything in YouGile: the client only sends GET
// requests and has no methods that could write. Keep it that way — the API key
// can do everything its account can, so this rule is held here, in code.
package yougile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

const (
	baseURL  = "https://yougile.com/api-v2"
	pageSize = 1000
	// YouGile allows 50 requests a minute per company, shared with the people
	// using it; the bot stays at 40.
	minRequestGap = 1500 * time.Millisecond
	// On 429 the bot waits and tries again this many times.
	maxRetries        = 3
	defaultRetryDelay = 10 * time.Second
	maxRetryDelay     = time.Minute
)

type Client struct {
	log        *slog.Logger
	key        string
	baseURL    string
	http       *http.Client
	minGap     time.Duration
	retryDelay time.Duration

	mu   sync.Mutex
	next time.Time // when the next request may start
}

func New(key string, log *slog.Logger) *Client {
	return &Client{
		log:        log.With("component", "yougile"),
		key:        key,
		baseURL:    baseURL,
		http:       &http.Client{Timeout: 30 * time.Second},
		minGap:     minRequestGap,
		retryDelay: defaultRetryDelay,
	}
}

type User struct {
	ID       string `json:"id"`
	RealName string `json:"realName"`
	Email    string `json:"email"`
}

type Task struct {
	ID        string          `json:"id"`
	Title     string          `json:"title"`
	ColumnID  string          `json:"columnId"`
	Archived  bool            `json:"archived"`
	Completed bool            `json:"completed"`
	Deleted   json.RawMessage `json:"deleted"` // null, or set for a deleted task
	Assigned  []string        `json:"assigned"`
	Deadline  *Deadline       `json:"deadline"`
}

// Deadline is the task's slot in the calendar. Times are Unix milliseconds.
type Deadline struct {
	StartDate int64 `json:"startDate"`
	Deadline  int64 `json:"deadline"`
	WithTime  bool  `json:"withTime"`
}

func (t Task) IsDeleted() bool {
	s := string(t.Deleted)
	return s != "" && s != "null" && s != "false"
}

// Start returns when the task is scheduled: the start of its slot, or the
// deadline if there is no start. YouGile often stores 09:00 as 08:59:59.9,
// so the time is rounded to the minute. ok is false for tasks without a time.
func (t Task) Start() (at time.Time, ok bool) {
	d := t.Deadline
	if d == nil || !d.WithTime {
		return time.Time{}, false
	}
	ms := d.StartDate
	if ms == 0 {
		ms = d.Deadline
	}
	if ms == 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(ms).Round(time.Minute), true
}

// Tasks returns all tasks in the column.
func (c *Client) Tasks(ctx context.Context, columnID string) ([]Task, error) {
	return list[Task](ctx, c, "/task-list", url.Values{"columnId": {columnID}})
}

// Users returns the company's users.
func (c *Client) Users(ctx context.Context) ([]User, error) {
	return list[User](ctx, c, "/users", nil)
}

type page[T any] struct {
	Paging struct {
		Next bool `json:"next"`
	} `json:"paging"`
	Content []T `json:"content"`
}

func list[T any](ctx context.Context, c *Client, path string, query url.Values) ([]T, error) {
	var all []T
	for offset := 0; ; offset += pageSize {
		q := url.Values{}
		for k, v := range query {
			q[k] = v
		}
		q.Set("limit", strconv.Itoa(pageSize))
		q.Set("offset", strconv.Itoa(offset))

		var p page[T]
		if err := c.get(ctx, path, q, &p); err != nil {
			return nil, err
		}
		all = append(all, p.Content...)
		if !p.Paging.Next || len(p.Content) == 0 {
			return all, nil
		}
	}
}

// get is the only place that talks to YouGile, and it only reads.
func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	for attempt := 0; ; attempt++ {
		if err := c.wait(ctx); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path+"?"+query.Encode(), nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.key)

		start := time.Now()
		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("yougile %s: %w", path, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("yougile %s: read body: %w", path, err)
		}
		c.log.Info("GET", "path", path, "query", query.Encode(), "status", resp.StatusCode,
			"bytes", len(body), "took", time.Since(start).Round(time.Millisecond), "attempt", attempt+1)

		switch {
		case resp.StatusCode == http.StatusTooManyRequests && attempt < maxRetries:
			delay := c.retryAfter(resp)
			c.log.Warn("rate limited, retrying", "path", path, "in", delay)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
			continue
		case resp.StatusCode != http.StatusOK:
			return fmt.Errorf("yougile %s: HTTP %d: %.200s", path, resp.StatusCode, body)
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("yougile %s: decode: %w", path, err)
		}
		return nil
	}
}

// wait keeps requests at least minGap apart.
func (c *Client) wait(ctx context.Context) error {
	c.mu.Lock()
	start := time.Now()
	if c.next.After(start) {
		start = c.next
	}
	c.next = start.Add(c.minGap)
	c.mu.Unlock()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Until(start)):
		return nil
	}
}

func (c *Client) retryAfter(resp *http.Response) time.Duration {
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
		return min(time.Duration(s)*time.Second, maxRetryDelay)
	}
	return c.retryDelay
}
