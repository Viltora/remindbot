package storage

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// TaskType is a kind of publication the bot handles. The types and what the
// bot does with each are defined here; which YouGile columns hold tasks of a
// type is matched in the task_type_columns table.
type TaskType string

const (
	TaskPost     TaskType = "post"
	TaskReels    TaskType = "reels"
	TaskStories  TaskType = "stories"
	TaskTelegram TaskType = "telegram"
)

// TaskTypes lists all types in display order.
var TaskTypes = []TaskType{TaskPost, TaskReels, TaskStories, TaskTelegram}

// Title is the type's name as in YouGile.
func (t TaskType) Title() string {
	switch t {
	case TaskPost:
		return "ПОСТЫ"
	case TaskReels:
		return "РИЛСЫ"
	case TaskStories:
		return "СТОРИС"
	case TaskTelegram:
		return "ТЕЛЕГРАМ"
	}
	return string(t)
}

// Label names the type in reports, with its icon: "📱 Сторис".
func (t TaskType) Label() string {
	switch t {
	case TaskPost:
		return "🖼 Пост"
	case TaskReels:
		return "🎬 Рилс"
	case TaskStories:
		return "📱 Сторис"
	case TaskTelegram:
		return "✈️ Телеграм"
	}
	return string(t)
}

// Noun names one publication of the type: "выложить сторис".
func (t TaskType) Noun() string {
	switch t {
	case TaskPost:
		return "пост"
	case TaskReels:
		return "рилс"
	case TaskStories:
		return "сторис"
	case TaskTelegram:
		return "пост в телеграме"
	}
	return string(t)
}

// NounOf is Noun in the genitive: "материал для поста".
func (t TaskType) NounOf() string {
	switch t {
	case TaskPost:
		return "поста"
	case TaskReels:
		return "рилса"
	case TaskTelegram:
		return "поста в телеграме"
	}
	return t.Noun()
}

// Published is "выложен" in the noun's gender: "сторис выложена".
func (t TaskType) Published() string {
	if t == TaskStories {
		return "выложена"
	}
	return "выложен"
}

// NeedsMaterial reports whether the bot asks for the material for approval a
// day before publishing.
func (t TaskType) NeedsMaterial() bool {
	return t == TaskPost || t == TaskReels
}

// TaskColumn matches a YouGile column to the type of tasks in it.
type TaskColumn struct {
	ColumnID string // YouGile column ID
	Title    string // column title in YouGile, for people reading the table
	Type     TaskType
}

// TaskColumns returns the YouGile columns the bot takes tasks from.
func (s *Store) TaskColumns(ctx context.Context) ([]TaskColumn, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT column_id, title, task_type FROM task_type_columns ORDER BY task_type, title`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (TaskColumn, error) {
		var c TaskColumn
		err := row.Scan(&c.ColumnID, &c.Title, &c.Type)
		return c, err
	})
}
