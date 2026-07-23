package main

import (
	"context"

	"github.com/levmv/golems/pkg/golem"
)

type agentRunner interface {
	Stream(context.Context, string, golem.StreamFunc) (*golem.Turn, error)
}
