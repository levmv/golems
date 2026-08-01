package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"golang.org/x/sync/singleflight"

	"github.com/levmv/golems/hugin/internal/config"
	"github.com/levmv/golems/hugin/internal/models"
)

const diagnosticOutputLimit = 4096

type Runner struct {
	mu         sync.Mutex
	sshClients map[string]*ssh.Client
	sshDials   singleflight.Group
}

func New() *Runner {
	return &Runner{
		sshClients: make(map[string]*ssh.Client),
	}
}

func (r *Runner) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	var err error
	for key, client := range r.sshClients {
		if closeErr := client.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
		delete(r.sshClients, key)
	}
	return err
}

// Execute runs the given check against its target and returns the structured output.
func Execute(ctx context.Context, check config.Check, target config.Target) (*models.CollectorOutput, error) {
	r := New()
	defer r.Close()
	return r.Execute(ctx, check, target)
}

// Execute runs the given check against its target and returns the structured output.
func (r *Runner) Execute(ctx context.Context, check config.Check, target config.Target) (*models.CollectorOutput, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	var err error
	if isLocalTarget(target) {
		err = executeLocal(ctx, check.ID, check.Command, &stdout, &stderr)
	} else {
		err = r.executeSSH(ctx, check.ID, check.Command, target, &stdout, &stderr)
	}

	// Even if the command failed (e.g., non-zero exit code), we still want to try
	// parsing the output, as the collector might have printed a valid JSON error payload.
	var output models.CollectorOutput
	if parseErr := parseCollectorOutput(stdout.Bytes(), &output); parseErr != nil {
		// If it's not valid JSON, preserve the failure as structured collector output.
		return collectorErrorOutput(check.ID, "EXECUTION_FAILED", fmt.Sprintf("Command failed or returned invalid JSON. Err: %v, Stderr: %s, Stdout: %s", err, truncateDiagnostic(stderr.String()), truncateDiagnostic(stdout.String()))), fmt.Errorf("failed to parse collector output: %w", parseErr)
	}

	if err != nil {
		output.Errors = append(output.Errors, models.ErrorDetail{
			Code:    "EXECUTION_NON_ZERO",
			Message: fmt.Sprintf("Collector exited with an error: %v. Stderr: %s", err, truncateDiagnostic(stderr.String())),
		})
		if output.Status == models.StatusOK {
			output.Status = models.StatusError
			return &output, fmt.Errorf("collector exited non-zero while reporting ok: %w", err)
		}
	}

	if validationErr := validateOutput(check.ID, &output); validationErr != nil {
		output.Status = models.StatusError
		output.Errors = append(output.Errors, models.ErrorDetail{
			Code:    "INVALID_OUTPUT",
			Message: validationErr.Error(),
		})
		return &output, validationErr
	}

	return &output, err
}

func isLocalTarget(target config.Target) bool {
	return target.Type == "local"
}

func truncateDiagnostic(s string) string {
	if len(s) <= diagnosticOutputLimit {
		return s
	}
	n := diagnosticOutputLimit
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "...(truncated)"
}

func collectorErrorOutput(checkID, code, message string) *models.CollectorOutput {
	return &models.CollectorOutput{
		Check:  checkID,
		Status: models.StatusError,
		Errors: []models.ErrorDetail{
			{Code: code, Message: message},
		},
	}
}

func parseCollectorOutput(stdout []byte, output *models.CollectorOutput) error {
	var firstErr error
	for offset := 0; offset < len(stdout); {
		idx := bytes.IndexByte(stdout[offset:], '{')
		if idx < 0 {
			break
		}
		start := offset + idx
		decoder := json.NewDecoder(bytes.NewReader(stdout[start:]))
		if err := decoder.Decode(output); err == nil {
			return nil
		} else if firstErr == nil {
			firstErr = err
		}
		offset = start + 1
	}
	if firstErr != nil {
		return firstErr
	}
	return fmt.Errorf("collector stdout did not contain a JSON object")
}

func validateOutput(checkID string, output *models.CollectorOutput) error {
	if output.Check != checkID {
		return fmt.Errorf("collector output check %q does not match configured check %q", output.Check, checkID)
	}
	switch output.Status {
	case models.StatusOK, models.StatusPartial, models.StatusError:
	default:
		return fmt.Errorf("collector output status %q is invalid", output.Status)
	}
	for name, value := range output.Metrics {
		if !isScalarMetric(value) {
			return fmt.Errorf("collector metric %q must be a number, string, or bool; got %T", name, value)
		}
	}
	return nil
}

func isScalarMetric(value any) bool {
	switch value.(type) {
	case float64, float32,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		json.Number,
		string,
		bool:
		return true
	default:
		return false
	}
}

func executeLocal(ctx context.Context, checkID, command string, stdout, stderr *bytes.Buffer) error {
	// Execute via bash to allow for simple shell pipelines in the command string
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Env = append(os.Environ(), "HUGIN_CHECK_ID="+checkID)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func (r *Runner) executeSSH(ctx context.Context, checkID, command string, target config.Target, stdout, stderr *bytes.Buffer) error {
	client, reused, err := r.sshClient(ctx, target)
	if err != nil {
		return err
	}
	command = withCheckIDEnv(checkID, command)
	err = runSSHSession(ctx, client, command, stdout, stderr)
	if reused && isDeadCachedClientSessionError(err) {
		r.evictSSHClient(target, client)
		client, _, retryErr := r.sshClient(ctx, target)
		if retryErr != nil {
			return retryErr
		}
		return runSSHSession(ctx, client, command, stdout, stderr)
	}
	return err
}

func withCheckIDEnv(checkID, command string) string {
	return "HUGIN_CHECK_ID=" + checkID + " " + command
}

func (r *Runner) sshClient(ctx context.Context, target config.Target) (*ssh.Client, bool, error) {
	key := sshClientKey(target)

	if client := r.cachedSSHClient(key); client != nil {
		return client, true, nil
	}

	for attempt := 0; attempt < 2; attempt++ {
		client, cached, err := r.sharedSSHClient(ctx, key, target)
		if err == nil {
			return client, cached, nil
		}
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		if attempt == 0 && isContextError(err) {
			r.sshDials.Forget(key)
			continue
		}
		return nil, false, err
	}

	return nil, false, fmt.Errorf("failed to establish SSH connection")
}

func (r *Runner) cachedSSHClient(key string) *ssh.Client {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sshClients[key]
}

type sshClientResult struct {
	client *ssh.Client
	cached bool
}

func (r *Runner) sharedSSHClient(ctx context.Context, key string, target config.Target) (*ssh.Client, bool, error) {
	ch := r.sshDials.DoChan(key, func() (any, error) {
		if client := r.cachedSSHClient(key); client != nil {
			return sshClientResult{client: client, cached: true}, nil
		}

		client, err := dialSSH(ctx, target)
		if err != nil {
			return sshClientResult{}, err
		}

		r.mu.Lock()
		defer r.mu.Unlock()
		if existing := r.sshClients[key]; existing != nil {
			_ = client.Close()
			return sshClientResult{client: existing, cached: true}, nil
		}
		r.sshClients[key] = client
		return sshClientResult{client: client}, nil
	})

	select {
	case result := <-ch:
		if result.Err != nil {
			return nil, false, result.Err
		}
		out, ok := result.Val.(sshClientResult)
		if !ok || out.client == nil {
			return nil, false, fmt.Errorf("SSH dial returned invalid client result")
		}
		return out.client, out.cached, nil
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}

func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (r *Runner) evictSSHClient(target config.Target, client *ssh.Client) {
	key := sshClientKey(target)

	r.mu.Lock()
	if r.sshClients[key] != client {
		r.mu.Unlock()
		return
	}
	delete(r.sshClients, key)
	r.mu.Unlock()

	_ = client.Close()
}

func hostKeyCallback(target config.Target) (ssh.HostKeyCallback, error) {
	if target.InsecureIgnoreHostKey {
		return ssh.InsecureIgnoreHostKey(), nil
	}

	path := target.KnownHosts
	if path == "" {
		path = "~/.ssh/known_hosts"
	}
	callback, err := knownhosts.New(expandTilde(path))
	if err != nil {
		return nil, fmt.Errorf("failed to load known_hosts %q: %w", path, err)
	}
	return callback, nil
}

// DialSSH opens an SSH client using Hugin target configuration.
func DialSSH(ctx context.Context, target config.Target) (*ssh.Client, error) {
	return dialSSH(ctx, target)
}

func dialSSH(ctx context.Context, target config.Target) (*ssh.Client, error) {
	key, err := os.ReadFile(expandTilde(target.Key))
	if err != nil {
		return nil, fmt.Errorf("unable to read private key: %w", err)
	}

	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("unable to parse private key: %w", err)
	}

	hostCallback, err := hostKeyCallback(target)
	if err != nil {
		return nil, err
	}

	sshConfig := &ssh.ClientConfig{
		User: target.User,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: hostCallback,
	}

	address := target.Host
	if !strings.Contains(address, ":") {
		address += ":22"
	}

	// 1. Use standard net.Dialer to establish TCP connection with context timeout
	dialer := net.Dialer{}
	netConn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("failed to dial tcp: %w", err)
	}

	// 2. Upgrade the TCP connection to an SSH connection
	if deadline, ok := ctx.Deadline(); ok {
		if err := netConn.SetDeadline(deadline); err != nil {
			netConn.Close()
			return nil, fmt.Errorf("failed to set SSH handshake deadline: %w", err)
		}
		defer netConn.SetDeadline(time.Time{})
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(netConn, address, sshConfig)
	if err != nil {
		netConn.Close() // Make sure to clean up the raw connection on error
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("failed to establish SSH connection: %w", ctxErr)
		}
		return nil, fmt.Errorf("failed to establish SSH connection: %w", err)
	}

	return ssh.NewClient(sshConn, chans, reqs), nil
}

var errNewSSHSession = errors.New("failed to create SSH session")

const sshSessionCleanupGrace = 2 * time.Second

func runSSHSession(ctx context.Context, client *ssh.Client, command string, stdout, stderr *bytes.Buffer) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("%w: %w", errNewSSHSession, err)
	}
	defer session.Close()

	session.Stdout = stdout
	session.Stderr = stderr

	done := make(chan error, 1)
	go func() {
		done <- session.Run(command)
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// x/crypto/ssh has no context-aware Session.Run. Ask the remote
		// process to terminate, close the channel, and wait briefly for Run to
		// return. If the server never replies, the Run goroutine can still live
		// until the SSH channel unblocks.
		_ = session.Signal(ssh.SIGTERM)
		_ = session.Close()
		timer := time.NewTimer(sshSessionCleanupGrace)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
		}
		return ctx.Err()
	}
}

func isDeadCachedClientSessionError(err error) bool {
	if !errors.Is(err, errNewSSHSession) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection lost") ||
		strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "eof")
}

func sshClientKey(target config.Target) string {
	knownHostsPolicy := "insecure"
	if !target.InsecureIgnoreHostKey {
		knownHostsPath := target.KnownHosts
		if knownHostsPath == "" {
			knownHostsPath = "~/.ssh/known_hosts"
		}
		knownHostsPolicy = "known-hosts:" + normalizedSSHPath(knownHostsPath)
	}
	return strings.Join([]string{
		target.User,
		target.Host,
		normalizedSSHPath(target.Key),
		knownHostsPolicy,
	}, "\x00")
}

func normalizedSSHPath(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Clean(expandTilde(path))
}

// expandTilde handles the common `~/.ssh/id_rsa` pattern in configs.
func expandTilde(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return strings.Replace(path, "~", home, 1)
		}
	}
	return path
}
