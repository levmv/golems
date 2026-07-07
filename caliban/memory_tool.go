package main

import (
	"github.com/levmv/golems/caliban/internal/tools"
	"github.com/levmv/golems/caliban/internal/workspace"
)

type memoryToolStore struct {
	ws *workspace.Workspace
}

func (m memoryToolStore) UpsertMemory(title, body, summary string) (tools.MemoryUpsertResult, error) {
	res, err := m.ws.UpsertMemory(title, body, summary)
	return tools.MemoryUpsertResult{
		Path:         res.Path,
		Created:      res.Created,
		IndexUpdated: res.IndexUpdated,
	}, err
}
