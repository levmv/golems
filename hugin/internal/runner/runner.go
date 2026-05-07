package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/levmv/golems/hugin/internal/config"
	"github.com/levmv/golems/hugin/internal/models"
)

// Execute runs the given check against its target and returns the structured output.
func Execute(ctx context.Context, check config.Check, target config.Target) (*models.CollectorOutput, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	isLocal := target.Host == "localhost" || target.Host == "127.0.0.1"

	var err error
	if isLocal {
		err = executeLocal(ctx, check.Command, &stdout, &stderr)
	} else {
		err = executeSSH(ctx, check.Command, target, &stdout, &stderr)
	}

	// Even if the command failed (e.g., non-zero exit code), we still want to try
	// parsing the output, as the collector might have printed a valid JSON error payload.
	var output models.CollectorOutput
	if parseErr := json.Unmarshal(stdout.Bytes(), &output); parseErr != nil {
		// If it's not valid JSON, preserve the failure as structured collector output.
		return collectorErrorOutput(check.ID, "EXECUTION_FAILED", fmt.Sprintf("Command failed or returned invalid JSON. Err: %v, Stderr: %s, Stdout: %s", err, stderr.String(), stdout.String())), fmt.Errorf("failed to parse collector output: %w", parseErr)
	}

	if err != nil {
		output.Errors = append(output.Errors, models.ErrorDetail{
			Code:    "EXECUTION_NON_ZERO",
			Message: fmt.Sprintf("Collector exited with an error: %v. Stderr: %s", err, stderr.String()),
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

func collectorErrorOutput(checkID, code, message string) *models.CollectorOutput {
	return &models.CollectorOutput{
		Check:  checkID,
		Status: models.StatusError,
		Errors: []models.ErrorDetail{
			{Code: code, Message: message},
		},
	}
}

func validateOutput(checkID string, output *models.CollectorOutput) error {
	if output.Check != checkID {
		return fmt.Errorf("collector output check %q does not match configured check %q", output.Check, checkID)
	}
	switch output.Status {
	case models.StatusOK, models.StatusPartial, models.StatusError:
		return nil
	default:
		return fmt.Errorf("collector output status %q is invalid", output.Status)
	}
}

func executeLocal(ctx context.Context, command string, stdout, stderr *bytes.Buffer) error {
	// Execute via bash to allow for simple shell pipelines in the command string
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func executeSSH(ctx context.Context, command string, target config.Target, stdout, stderr *bytes.Buffer) error {
	key, err := os.ReadFile(expandTilde(target.Key))
	if err != nil {
		return fmt.Errorf("unable to read private key: %w", err)
	}

	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return fmt.Errorf("unable to parse private key: %w", err)
	}

	sshConfig := &ssh.ClientConfig{
		User: target.User,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	address := target.Host
	if !strings.Contains(address, ":") {
		address += ":22"
	}

	// 1. Use standard net.Dialer to establish TCP connection with context timeout
	dialer := net.Dialer{}
	netConn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("failed to dial tcp: %w", err)
	}

	// 2. Upgrade the TCP connection to an SSH connection
	sshConn, chans, reqs, err := ssh.NewClientConn(netConn, address, sshConfig)
	if err != nil {
		netConn.Close() // Make sure to clean up the raw connection on error
		return fmt.Errorf("failed to establish SSH connection: %w", err)
	}

	// 3. Create the SSH client wrapper
	client := ssh.NewClient(sshConn, chans, reqs)
	defer client.Close()

	// 4. Create the session
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	session.Stdout = stdout
	session.Stderr = stderr

	// Execute the command on the remote machine
	return session.Run(command)
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
