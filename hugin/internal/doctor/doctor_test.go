package doctor

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/levmv/golems/hugin/internal/config"
)

func TestCheckReportsMissingLLMTokenAndLocalTarget(t *testing.T) {
	clearLLMEnv(t)
	cfg := testConfig(t)

	report := Check(context.Background(), cfg, Options{CheckSSH: false})

	if !report.HasFailures() {
		t.Fatal("expected missing LLM token to fail doctor")
	}
	assertItem(t, report, StatusFail, "llm", "no LLM token")
	assertItem(t, report, StatusOK, "target", "local is local")
}

func TestCheckReportsReadyMinimalConfig(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("OPENAI_API_KEY", "token")
	cfg := testConfig(t)

	report := Check(context.Background(), cfg, Options{CheckSSH: false})

	if report.HasFailures() {
		t.Fatalf("expected no failures, got %+v", report.Items)
	}
	assertItem(t, report, StatusOK, "llm", "token found")
	assertItem(t, report, StatusWarn, "notify", "no notifier configured")
}

func TestCheckUsesProviderAwareLLMTokenFallback(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("OPENAI_API_KEY", "token")
	cfg := testConfig(t)
	cfg.LLM.Provider = "deepseek"
	cfg.LLM.Model = "deepseek-test"

	report := Check(context.Background(), cfg, Options{CheckSSH: false})

	assertItem(t, report, StatusFail, "llm", "DEEPSEEK_API_KEY")
	if hasItem(report, StatusOK, "llm", "OPENAI_API_KEY") {
		t.Fatalf("doctor accepted OPENAI_API_KEY for deepseek: %+v", report.Items)
	}

	t.Setenv("DEEPSEEK_API_KEY", "token")
	report = Check(context.Background(), cfg, Options{CheckSSH: false})
	assertItem(t, report, StatusOK, "llm", "DEEPSEEK_API_KEY")
}

func TestCheckAllowsOllamaWithoutToken(t *testing.T) {
	clearLLMEnv(t)
	cfg := testConfig(t)
	cfg.LLM.Provider = "ollama"
	cfg.LLM.Model = "llama-test"

	report := Check(context.Background(), cfg, Options{CheckSSH: false})

	if report.HasFailures() {
		t.Fatalf("expected no failures, got %+v", report.Items)
	}
	assertItem(t, report, StatusOK, "llm", "no token required")
}

func TestCheckReportsSSHPrerequisites(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("OPENAI_API_KEY", "token")
	cfg := testConfig(t)
	cfg.Targets["web1"] = config.Target{
		Type:       "ssh",
		Host:       "web1.example.test",
		User:       "hugin",
		Key:        filepath.Join(t.TempDir(), "missing-key"),
		KnownHosts: filepath.Join(t.TempDir(), "missing-known-hosts"),
	}

	report := Check(context.Background(), cfg, Options{CheckSSH: true, SSHTimeout: time.Millisecond})

	assertItem(t, report, StatusFail, "target", "web1 SSH key")
	assertItem(t, report, StatusFail, "target", "web1 known_hosts")
	assertItem(t, report, StatusWarn, "target", "handshake skipped")
}

func TestCheckReportsInvalidCheckDetails(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("OPENAI_API_KEY", "token")
	cfg := testConfig(t)
	cfg.Checks[0].Analysis.History = "seven days"
	cfg.Checks[0].Alert.Cooldown = -time.Minute
	cfg.Checks[0].Command = "/opt/hugin/collectors/disk"

	report := Check(context.Background(), cfg, Options{CheckSSH: false})

	assertItem(t, report, StatusFail, "check disk", "analysis.history")
	assertItem(t, report, StatusFail, "check disk", "alert.cooldown")
	if hasItem(report, StatusWarn, "check disk", "HUGIN_CHECK_ID") {
		t.Fatalf("doctor should not warn about runner-managed HUGIN_CHECK_ID: %+v", report.Items)
	}
}

func clearLLMEnv(t *testing.T) {
	t.Helper()
	for _, env := range []string{"OPENAI_API_KEY", "HUGIN_LLM_TOKEN", "DEEPSEEK_API_KEY", "OPENROUTER_API_KEY"} {
		t.Setenv(env, "")
	}
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		App: config.AppConfig{DataDir: t.TempDir(), Timezone: "UTC"},
		LLM: config.LLMConfig{Provider: "openai", Model: "gpt-test"},
		Targets: map[string]config.Target{
			"local": {Type: "local", Host: "localhost"},
		},
		Checks: []config.Check{
			{
				ID:       "disk",
				Target:   "local",
				Command:  "printf '{}'",
				Schedule: "*/5 * * * *",
				Timeout:  time.Second,
			},
		},
	}
}

func assertItem(t *testing.T, report Report, status Status, area string, contains string) {
	t.Helper()
	for _, item := range report.Items {
		if item.Status == status && item.Area == area && containsIn(item.Message, contains) {
			return
		}
	}
	t.Fatalf("missing item status=%s area=%s contains=%q in %+v", status, area, contains, report.Items)
}

func hasItem(report Report, status Status, area string, contains string) bool {
	for _, item := range report.Items {
		if item.Status == status && item.Area == area && containsIn(item.Message, contains) {
			return true
		}
	}
	return false
}

func containsIn(s, substr string) bool {
	return strings.Contains(s, substr)
}
