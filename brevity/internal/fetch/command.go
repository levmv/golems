package fetch

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/levmv/golems/brevity/internal/source"
)

const commandTimeout = 45 * time.Second

type Command struct {
	name string
	args []string
}

func NewCommand(name string, args ...string) *Command {
	return &Command{name: name, args: args}
}

func (c *Command) Fetch(ctx context.Context, rawURL string) (source.Document, error) {
	parsed, err := validateHTTPURL(rawURL)
	if err != nil {
		return source.Document{}, err
	}
	if strings.TrimSpace(c.name) == "" {
		return source.Document{}, fmt.Errorf("external fetch command is empty")
	}

	cmdCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()

	args := append(append([]string{}, c.args...), parsed.String())
	cmd := exec.CommandContext(cmdCtx, c.name, args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			return source.Document{}, fmt.Errorf("external fetch command failed: %w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return source.Document{}, fmt.Errorf("external fetch command failed: %w", err)
	}

	text := strings.TrimSpace(string(out))
	if text == "" {
		return source.Document{}, fmt.Errorf("external fetch command returned empty text")
	}

	return source.Document{
		URL:       parsed.String(),
		FinalURL:  parsed.String(),
		Text:      text,
		FetchedAt: time.Now(),
	}, nil
}
