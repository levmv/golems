package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/levmv/golems/hugin/internal/config"
	"github.com/levmv/golems/hugin/internal/runner"
)

type Status string

const (
	StatusOK   Status = "OK"
	StatusWarn Status = "WARN"
	StatusFail Status = "FAIL"
)

type Options struct {
	CheckSSH   bool
	SSHTimeout time.Duration
}

type Item struct {
	Status  Status
	Area    string
	Message string
}

type Report struct {
	Items []Item
}

func (r Report) HasFailures() bool {
	for _, item := range r.Items {
		if item.Status == StatusFail {
			return true
		}
	}
	return false
}

func (r *Report) add(status Status, area, format string, args ...any) {
	r.Items = append(r.Items, Item{Status: status, Area: area, Message: fmt.Sprintf(format, args...)})
}

func Check(ctx context.Context, cfg *config.Config, opts Options) Report {
	if opts.SSHTimeout <= 0 {
		opts.SSHTimeout = 5 * time.Second
	}

	var report Report
	report.add(StatusOK, "config", "configuration loaded: %d target(s), %d check(s)", len(cfg.Targets), len(cfg.Checks))
	checkDataDir(&report, cfg.App.DataDir)
	checkLLM(&report, cfg.LLM)
	checkNotifiers(&report, cfg.Notifiers)
	checkTargets(ctx, &report, cfg.Targets, opts)
	checkChecks(&report, cfg.Checks)
	return report
}

func checkDataDir(report *Report, dataDir string) {
	file, err := os.CreateTemp(dataDir, ".doctor-*")
	if err != nil {
		report.add(StatusFail, "data", "data_dir %q is not writable: %v", dataDir, err)
		return
	}
	name := file.Name()
	closeErr := file.Close()
	removeErr := os.Remove(name)
	if closeErr != nil {
		report.add(StatusFail, "data", "data_dir %q write test failed: %v", dataDir, closeErr)
		return
	}
	if removeErr != nil {
		report.add(StatusWarn, "data", "data_dir %q write test file could not be removed: %v", dataDir, removeErr)
		return
	}
	report.add(StatusOK, "data", "data_dir %q is writable", dataDir)
}

func checkLLM(report *Report, llm config.LLMConfig) {
	if !config.LLMProviderNeedsToken(llm.Provider) {
		report.add(StatusOK, "llm", "no token required for %s/%s", llm.Provider, llm.Model)
		return
	}
	for _, env := range config.LLMTokenEnvCandidates(llm) {
		if os.Getenv(env) != "" {
			report.add(StatusOK, "llm", "token found in %s for %s/%s", env, llm.Provider, llm.Model)
			return
		}
	}
	report.add(StatusFail, "llm", "no LLM token found; set one of: %s", strings.Join(config.LLMTokenEnvCandidates(llm), ", "))
}

func checkNotifiers(report *Report, notifiers map[string]config.Notifier) {
	if len(notifiers) == 0 {
		report.add(StatusWarn, "notify", "no notifier configured; alerts will be logged only")
		return
	}

	names := make([]string, 0, len(notifiers))
	for name := range notifiers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		ntf := notifiers[name]
		if !ntf.Enabled {
			report.add(StatusWarn, "notify", "notifier %q is disabled", name)
			continue
		}
		switch name {
		case "telegram":
			checkTelegramNotifier(report, ntf)
		default:
			report.add(StatusWarn, "notify", "notifier %q is configured but doctor has no specific checks for it", name)
		}
	}
}

func checkTelegramNotifier(report *Report, ntf config.Notifier) {
	token := os.Getenv(ntf.BotTokenEnv)
	chatID := os.Getenv(ntf.ChatIDEnv)
	if token == "" {
		report.add(StatusFail, "notify", "telegram token env %s is empty", ntf.BotTokenEnv)
		return
	}
	if chatID == "" {
		report.add(StatusFail, "notify", "telegram chat env %s is empty", ntf.ChatIDEnv)
		return
	}
	if _, err := strconv.ParseInt(chatID, 10, 64); err != nil {
		report.add(StatusFail, "notify", "telegram chat env %s is not an integer: %v", ntf.ChatIDEnv, err)
		return
	}
	report.add(StatusOK, "notify", "telegram notifier env is present")
}

func checkTargets(ctx context.Context, report *Report, targets map[string]config.Target, opts Options) {
	for _, name := range sortedTargetNames(targets) {
		target := targets[name]
		switch target.Type {
		case "local":
			report.add(StatusOK, "target", "%s is local", name)
		case "ssh":
			ready := checkSSHTarget(report, name, target)
			if opts.CheckSSH && ready {
				checkSSHHandshake(ctx, report, name, target, opts.SSHTimeout)
			} else if opts.CheckSSH && !ready {
				report.add(StatusWarn, "target", "%s SSH handshake skipped because local SSH prerequisites failed", name)
			}
		default:
			report.add(StatusFail, "target", "%s has unsupported type %q", name, target.Type)
		}
	}
}

func sortedTargetNames(targets map[string]config.Target) []string {
	names := make([]string, 0, len(targets))
	for name := range targets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func checkSSHTarget(report *Report, name string, target config.Target) bool {
	ready := true
	keyPath := expandTilde(target.Key)
	key, err := os.ReadFile(keyPath)
	if err != nil {
		report.add(StatusFail, "target", "%s SSH key %q is not readable: %v", name, target.Key, err)
		ready = false
	} else if _, err := ssh.ParsePrivateKey(key); err != nil {
		report.add(StatusFail, "target", "%s SSH key %q is not usable by Hugin: %v", name, target.Key, err)
		ready = false
	} else {
		report.add(StatusOK, "target", "%s SSH key %q is readable", name, target.Key)
	}

	if target.InsecureIgnoreHostKey {
		report.add(StatusWarn, "target", "%s disables SSH host key verification", name)
		return ready
	}

	knownHosts := target.KnownHosts
	if knownHosts == "" {
		knownHosts = "~/.ssh/known_hosts"
	}
	if _, err := knownhosts.New(expandTilde(knownHosts)); err != nil {
		report.add(StatusFail, "target", "%s known_hosts %q is not usable: %v", name, knownHosts, err)
		ready = false
	} else {
		report.add(StatusOK, "target", "%s known_hosts %q is usable", name, knownHosts)
	}
	return ready
}

func checkSSHHandshake(ctx context.Context, report *Report, name string, target config.Target, timeout time.Duration) {
	sshCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client, err := runner.DialSSH(sshCtx, target)
	if err != nil {
		report.add(StatusFail, "target", "%s SSH handshake failed: %v", name, err)
		return
	}
	if err := client.Close(); err != nil {
		report.add(StatusWarn, "target", "%s SSH connection opened but close failed: %v", name, err)
		return
	}
	report.add(StatusOK, "target", "%s SSH handshake succeeded", name)
}

func checkChecks(report *Report, checks []config.Check) {
	for _, check := range checks {
		area := "check " + check.ID
		if check.Analysis.History != "" && parseHistoryWindow(check.Analysis.History) <= 0 {
			report.add(StatusFail, area, "analysis.history %q is invalid", check.Analysis.History)
		}
		if check.Alert.Cooldown < 0 {
			report.add(StatusFail, area, "alert.cooldown must not be negative")
		}
		if check.Alert.RepeatAfter < 0 {
			report.add(StatusFail, area, "alert.repeat_after must not be negative")
		}
	}
}

func parseHistoryWindow(s string) time.Duration {
	if s == "" {
		return 0
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	if strings.HasSuffix(s, "d") {
		daysStr := strings.TrimSuffix(s, "d")
		if days, err := strconv.Atoi(daysStr); err == nil && days > 0 {
			return time.Duration(days) * 24 * time.Hour
		}
	}
	return -1
}

func expandTilde(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}
