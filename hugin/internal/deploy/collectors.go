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

	bundled "github.com/levmv/golems/hugin/collectors"
	"github.com/levmv/golems/hugin/internal/config"
	"github.com/levmv/golems/hugin/internal/runner"
	"golang.org/x/crypto/ssh"
)

const DefaultCollectorsDest = "/opt/hugin/collectors"

type CollectorsOptions struct {
	Source string
	Dest   string
}

type CollectorsResult struct {
	Source          string
	Dest            string
	Files           int
	IncludesWrapper bool
}

func Collectors(ctx context.Context, target config.Target, opts CollectorsOptions) (CollectorsResult, error) {
	if target.Type == "local" {
		return CollectorsResult{}, fmt.Errorf("deploy requires an ssh target")
	}

	source := "embedded collectors"
	includesWrapper := true
	var archive *bytes.Buffer
	var files int
	var err error
	if opts.Source != "" {
		source, err = validateCollectorsSource(opts.Source)
		if err != nil {
			return CollectorsResult{}, err
		}
		includesWrapper = sourceIncludesWrapper(source)
		archive, files, err = archiveCollectors(source)
	} else {
		archive, files, err = archiveBundledCollectors()
	}
	if err != nil {
		return CollectorsResult{}, err
	}

	dest := opts.Dest
	if dest == "" {
		dest = DefaultCollectorsDest
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

	return CollectorsResult{Source: source, Dest: dest, Files: files, IncludesWrapper: includesWrapper}, nil
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

func archiveBundledCollectors() (*bytes.Buffer, int, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	if err := tw.WriteHeader(&tar.Header{Name: "collectors", Mode: 0755, Typeflag: tar.TypeDir}); err != nil {
		_ = gz.Close()
		return nil, 0, err
	}

	for _, file := range bundled.Files {
		data, err := bundled.FS.ReadFile(file.Name)
		if err != nil {
			_ = tw.Close()
			_ = gz.Close()
			return nil, 0, err
		}
		header := &tar.Header{
			Name: path.Join("collectors", file.Name),
			Mode: file.Mode,
			Size: int64(len(data)),
		}
		if err := tw.WriteHeader(header); err != nil {
			_ = gz.Close()
			return nil, 0, err
		}
		if _, err := tw.Write(data); err != nil {
			_ = gz.Close()
			return nil, 0, err
		}
	}
	if err := tw.Close(); err != nil {
		_ = gz.Close()
		return nil, 0, err
	}
	if err := gz.Close(); err != nil {
		return nil, 0, err
	}
	return &buf, len(bundled.Files), nil
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

func AuthorizedKeyLine(target config.Target, collectorsDest, comment string) (string, error) {
	dest, err := normalizeAuthorizedKeyDest(collectorsDest)
	if err != nil {
		return "", err
	}

	key, err := os.ReadFile(expandLocalTilde(target.Key))
	if err != nil {
		return "", fmt.Errorf("read private key for authorized_keys hint: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return "", fmt.Errorf("parse private key for authorized_keys hint: %w", err)
	}

	forcedCommand := path.Join(dest, "hugin-collector-wrapper")
	if dest != DefaultCollectorsDest {
		forcedCommand = "HUGIN_COLLECTOR_DIR=" + dest + " " + forcedCommand
	}

	line := "restrict,command=" + authorizedKeysQuote(forcedCommand) + " " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	if comment = sanitizeAuthorizedKeyComment(comment); comment != "" {
		line += " " + comment
	}
	return line, nil
}

func normalizeAuthorizedKeyDest(dest string) (string, error) {
	if dest == "" {
		dest = DefaultCollectorsDest
	}
	dest = strings.TrimRight(dest, "/")
	if dest == "" {
		return "", fmt.Errorf("authorized_keys hint requires a non-empty collectors destination")
	}
	if !strings.HasPrefix(dest, "/") {
		return "", fmt.Errorf("authorized_keys hint requires an absolute collectors destination, got %q", dest)
	}
	for _, r := range dest {
		if !isCollectorCommandChar(r) {
			return "", fmt.Errorf("authorized_keys hint requires a collectors destination without shell-special characters, got %q", dest)
		}
	}
	return dest, nil
}

func sourceIncludesWrapper(source string) bool {
	info, err := os.Stat(filepath.Join(source, "hugin-collector-wrapper"))
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0
}

func authorizedKeysQuote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

func sanitizeAuthorizedKeyComment(comment string) string {
	comment = strings.TrimSpace(comment)
	if comment == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range comment {
		if r <= ' ' || r == '"' || r == '\'' || r == '\\' {
			b.WriteByte('-')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isCollectorCommandChar(r rune) bool {
	return (r >= 'A' && r <= 'Z') ||
		(r >= 'a' && r <= 'z') ||
		(r >= '0' && r <= '9') ||
		strings.ContainsRune("_./:=@%+,-", r)
}

func expandLocalTilde(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return strings.Replace(path, "~", home, 1)
		}
	}
	return path
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
