package deploy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/levmv/golems/hugin/internal/config"
	"github.com/levmv/golems/hugin/internal/runner"
)

const DefaultCollectorsDest = "/opt/hugin/collectors"

type CollectorsOptions struct {
	Source string
	Dest   string
}

type CollectorsResult struct {
	Source string
	Dest   string
	Files  int
}

func Collectors(ctx context.Context, target config.Target, opts CollectorsOptions) (CollectorsResult, error) {
	if target.Type == "local" {
		return CollectorsResult{}, fmt.Errorf("deploy requires an ssh target")
	}

	source, err := ResolveCollectorsSource(opts.Source)
	if err != nil {
		return CollectorsResult{}, err
	}
	dest := opts.Dest
	if dest == "" {
		dest = DefaultCollectorsDest
	}

	archive, files, err := archiveCollectors(source)
	if err != nil {
		return CollectorsResult{}, err
	}

	client, err := runner.DialSSH(ctx, target)
	if err != nil {
		return CollectorsResult{}, err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return CollectorsResult{}, fmt.Errorf("create ssh session: %w", err)
	}
	defer session.Close()

	var stderr bytes.Buffer
	session.Stdin = archive
	session.Stderr = &stderr

	command := "set -eu; dest=" + shellQuote(dest) + "; case \"$dest\" in \"~\") dest=$HOME ;; \"~\"/*) dest=$HOME/${dest#~/} ;; esac; mkdir -p \"$dest\"; tar -xzf - -C \"$dest\" --strip-components=1"
	if err := session.Run(command); err != nil {
		return CollectorsResult{}, fmt.Errorf("install collectors on remote host: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	return CollectorsResult{Source: source, Dest: dest, Files: files}, nil
}

func ResolveCollectorsSource(explicit string) (string, error) {
	if explicit != "" {
		return validateCollectorsSource(explicit)
	}

	var candidates []string
	candidates = append(candidates, "hugin/collectors", "collectors")
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "collectors"))
	}

	for _, candidate := range candidates {
		if source, err := validateCollectorsSource(candidate); err == nil {
			return source, nil
		}
	}
	return "", fmt.Errorf("collectors source not found; pass --source hugin/collectors")
}

func validateCollectorsSource(source string) (string, error) {
	info, err := os.Stat(source)
	if err != nil {
		return "", fmt.Errorf("collectors source %q: %w", source, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("collectors source %q is not a directory", source)
	}
	if _, err := os.Stat(filepath.Join(source, "lib.sh")); err != nil {
		return "", fmt.Errorf("collectors source %q does not look like a Hugin collectors directory: %w", source, err)
	}
	abs, err := filepath.Abs(source)
	if err != nil {
		return "", err
	}
	return abs, nil
}

func archiveCollectors(source string) (*bytes.Buffer, int, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	files := 0

	rootName := filepath.Base(source)
	if err := filepath.WalkDir(source, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(source, filePath)
		if err != nil {
			return err
		}
		name := rootName
		if rel != "." {
			name = path.Join(rootName, filepath.ToSlash(rel))
		}

		var link string
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(filePath)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		header.Name = name
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		file, err := os.Open(filePath)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		files++
		return nil
	}); err != nil {
		_ = tw.Close()
		_ = gz.Close()
		return nil, 0, err
	}
	if err := tw.Close(); err != nil {
		_ = gz.Close()
		return nil, 0, err
	}
	if err := gz.Close(); err != nil {
		return nil, 0, err
	}
	return &buf, files, nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
