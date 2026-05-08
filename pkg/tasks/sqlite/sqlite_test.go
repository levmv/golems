package tasksqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/levmv/golems/pkg/tasks"
	"github.com/levmv/golems/pkg/tasks/tasktest"
	_ "modernc.org/sqlite"
)

func TestStoreConformance(t *testing.T) {
	tasktest.Run(t, func(t *testing.T) tasks.Store {
		t.Helper()
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatalf("sql.Open returned error: %v", err)
		}
		t.Cleanup(func() { db.Close() })
		if err := EnsureSchema(context.Background(), db, Options{}); err != nil {
			t.Fatalf("EnsureSchema returned error: %v", err)
		}
		store, err := New(db, Options{})
		if err != nil {
			t.Fatalf("New returned error: %v", err)
		}
		return store
	})
}

func TestInvalidTableName(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open returned error: %v", err)
	}
	defer db.Close()

	if _, err := New(db, Options{Table: "tasks; DROP TABLE tasks"}); !errors.Is(err, tasks.ErrInvalid) {
		t.Fatalf("expected ErrInvalid from New, got %v", err)
	}
	if _, err := Schema(Options{Table: "bad-name"}); !errors.Is(err, tasks.ErrInvalid) {
		t.Fatalf("expected ErrInvalid from Schema, got %v", err)
	}
	if err := EnsureSchema(context.Background(), db, Options{Table: "bad name"}); !errors.Is(err, tasks.ErrInvalid) {
		t.Fatalf("expected ErrInvalid from EnsureSchema, got %v", err)
	}
}
