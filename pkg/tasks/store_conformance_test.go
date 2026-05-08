package tasks_test

import (
	"testing"

	"github.com/levmv/golems/pkg/tasks"
	"github.com/levmv/golems/pkg/tasks/tasktest"
)

func TestMemoryStoreConformance(t *testing.T) {
	tasktest.Run(t, func(t *testing.T) tasks.Store {
		return tasks.NewMemoryStore()
	})
}
