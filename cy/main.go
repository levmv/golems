package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/levmv/golems/cy/internal/session"
	"github.com/levmv/golems/cy/internal/state"
	toolruntime "github.com/levmv/golems/cy/internal/tools"
	"github.com/levmv/golems/cy/internal/ui"
	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/llm"
)

const maxPipedInputBytes = 8 * 1024 * 1024

func main() {
	if toolruntime.RunSandboxChildIfRequested() {
		return
	}
	if err := runMain(); err != nil {
		fmt.Fprintf(os.Stderr, "cy: %v\n", err)
		os.Exit(1)
	}
}

func runMain() (returnErr error) {
	toolruntime.HardenSupervisor()
	cfg := LoadConfig()

	modelURI := flag.String("model", cfg.ModelURI, "model URI in provider/model format")
	rootDir := flag.String("root", cfg.RootDir, "workspace root for file and search tools")
	home := flag.String("home", cfg.Home, "Cy home directory (defaults to CY_HOME or ~/.cy)")
	verbose := flag.Bool("v", false, "show progress and usage in one-shot mode")
	saveSession := flag.Bool("save-session", cfg.SaveSession, "keep a resumable session for a one-shot invocation")
	jsonOutput := flag.Bool("json", cfg.JSON, "emit one versioned JSON result on stdout")
	profile := flag.String("profile", cfg.CapabilityProfile, "capability profile: full, edit, or read-only")
	sandbox := flag.String("sandbox", cfg.SandboxPolicy, "Bash sandbox policy: auto, require, or off")
	showVersion := flag.Bool("version", false, "print the Cy version and exit")
	flag.Parse()
	setFlags := make(map[string]bool)
	flag.Visit(func(value *flag.Flag) { setFlags[value.Name] = true })
	if *showVersion {
		fmt.Fprintf(os.Stdout, "cy %s\n", version)
		return nil
	}
	cfg.ModelURI = strings.TrimSpace(*modelURI)
	cfg.RootDir = *rootDir
	cfg.Home = *home
	cfg.Verbose = *verbose
	cfg.SaveSession = *saveSession
	cfg.JSON = *jsonOutput
	normalizedProfile, err := toolruntime.NormalizeCapabilityProfile(*profile)
	if err != nil {
		return err
	}
	cfg.CapabilityProfile = normalizedProfile
	cfg.SandboxPolicy, err = normalizeSandboxPolicy(*sandbox)
	if err != nil {
		return err
	}
	store, err := state.Open(cfg.Home)
	if err != nil {
		return fmt.Errorf("initialize state: %w", err)
	}
	storedSettings, err := store.Config()
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	if !setFlags["model"] && strings.TrimSpace(os.Getenv("CY_MODEL")) == "" && storedSettings.Model != "" {
		cfg.ModelURI = storedSettings.Model
	}
	if !setFlags["profile"] && strings.TrimSpace(os.Getenv("CY_PROFILE")) == "" && storedSettings.Profile != "" {
		cfg.CapabilityProfile, err = toolruntime.NormalizeCapabilityProfile(storedSettings.Profile)
		if err != nil {
			return fmt.Errorf("load saved profile: %w", err)
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if handled, err := handleControlInvocation(flag.Args(), cfg, store); handled {
		return err
	}

	resumeID, args, err := parseInvocation(flag.Args())
	if err != nil {
		return err
	}
	pipedInput := ""
	hasPipe := false
	if len(args) == 0 {
		pipedInput, hasPipe, err = readPipedInput(os.Stdin)
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
	}
	cfg.PrintMode = len(args) > 0 || hasPipe
	cfg.Ephemeral = useEphemeralSession(cfg, resumeID)

	var journal *session.Session
	var resumed session.State
	journalHandedOff := false
	if resumeID != "" {
		journal, err = session.Open(cfg.Home, resumeID)
		if err != nil {
			return fmt.Errorf("resume session: %w", err)
		}
		defer func(opened *session.Session) {
			if !journalHandedOff {
				returnErr = errors.Join(returnErr, opened.ClosePruningEmpty())
			}
		}(journal)
		resumed, err = journal.Replay()
		if err != nil {
			return fmt.Errorf("replay session: %w", err)
		}
		applyResumedConfig(&cfg, resumed, setFlags)
	}

	tools, root, err := toolruntime.NewWorkspaceTools(cfg.RootDir)
	if err != nil {
		return fmt.Errorf("initialize workspace tools: %w", err)
	}
	cfg.Security = buildSecurityState(ctx, cfg, root, store)
	if cfg.SandboxPolicy == sandboxRequire && cfg.Security.Sandbox != "landlock" {
		return fmt.Errorf("required sandbox probe failed: %s", cfg.Security.Probe)
	}
	// The interactive UI must be able to start without a credential so the
	// user can authenticate with /login. sessionAgent checks the credential at
	// the model-call boundary, before anything is appended to the journal.
	model, err := buildModel(cfg, store, false)
	if err != nil {
		return fmt.Errorf("initialize model: %w", err)
	}
	if journal != nil && resumed.Header.Workspace != "" && root != resumed.Header.Workspace {
		return fmt.Errorf("session workspace is %s, not %s", resumed.Header.Workspace, root)
	}
	if journal == nil {
		sessionHome := cfg.Home
		if cfg.Ephemeral {
			temporaryHome, tempErr := os.MkdirTemp("", "cy-run-")
			if tempErr != nil {
				return fmt.Errorf("create temporary run state: %w", tempErr)
			}
			defer func() { returnErr = errors.Join(returnErr, os.RemoveAll(temporaryHome)) }()
			sessionHome = temporaryHome
		}
		journal, err = session.Create(session.CreateOptions{
			Home:      sessionHome,
			Workspace: root,
			Model:     cfg.ModelURI,
		})
		if err != nil {
			return fmt.Errorf("create session: %w", err)
		}
		defer func(created *session.Session) {
			if !journalHandedOff {
				returnErr = errors.Join(returnErr, created.ClosePruningEmpty())
			}
		}(journal)
	} else if resumed.Model != "" && cfg.ModelURI != resumed.Model {
		if _, err := journal.Append(session.RecordModelChanged, session.ModelChanged{Model: cfg.ModelURI}); err != nil {
			return fmt.Errorf("record model change: %w", err)
		}
	}
	agent, err := newSessionAgent(cfg, model, root, tools, journal, store)
	if err != nil {
		return err
	}
	journalHandedOff = true
	defer func() { returnErr = errors.Join(returnErr, agent.Close()) }()
	defer func() {
		if shouldPrintResumeHint(cfg, agent.HasUserTurn()) {
			fmt.Fprintf(os.Stderr, "Resume with: cy resume %s\n", agent.SessionID())
		}
	}()

	if len(args) > 0 {
		if err := runPrintTurn(ctx, agent, cfg, strings.Join(args, " "), os.Stdout, os.Stderr); err != nil {
			return err
		}
		return nil
	}

	if hasPipe {
		if pipedInput == "" {
			return nil
		}
		if err := runPrintTurn(ctx, agent, cfg, pipedInput, os.Stdout, os.Stderr); err != nil {
			return err
		}
		return nil
	}

	inFile, outFile, ok := ui.CanUseScreen(os.Stdin, os.Stdout)
	if !ok {
		return errors.New("interactive mode requires a terminal; use ssh -t, allocate a TTY, or pass a prompt")
	}
	return ui.RunScreen(ctx, agent, ui.Config{
		ModelURI:          cfg.ModelURI,
		CapabilityProfile: cfg.CapabilityProfile,
		SecuritySummary:   cfg.Security.Compact(),
		Providers:         loginProviderCatalog(),
	}, root, inFile, outFile)
}

func applyResumedConfig(cfg *Config, resumed session.State, setFlags map[string]bool) {
	if !setFlags["model"] && strings.TrimSpace(os.Getenv("CY_MODEL")) == "" && resumed.Model != "" {
		cfg.ModelURI = resumed.Model
	}
	if !setFlags["root"] && strings.TrimSpace(os.Getenv("CY_ROOT")) == "" && resumed.Header.Workspace != "" {
		cfg.RootDir = resumed.Header.Workspace
	}
}

func buildModel(cfg Config, store *state.Store, requireCredential bool) (llm.Model, error) {
	uri := strings.TrimSpace(cfg.ModelURI)
	provider, modelID, ok := strings.Cut(uri, "/")
	if !ok || provider == "" || strings.TrimSpace(modelID) == "" ||
		provider != strings.TrimSpace(provider) || modelID != strings.TrimSpace(modelID) {
		return llm.Model{}, fmt.Errorf("invalid model URI %q; expected provider/model", cfg.ModelURI)
	}

	registry := llm.NewRegistry()
	switch provider {
	case "deepseek", "openai", "openrouter":
		token, _, err := credentialForProvider(store, provider)
		if err != nil {
			return llm.Model{}, err
		}
		if requireCredential && token == "" {
			return llm.Model{}, fmt.Errorf("%s API key is empty for model %q; use /login %s", provider, cfg.ModelURI, provider)
		}
		if provider == "openrouter" {
			registry.WithProvider(provider, token, llm.WithAppAttribution("Cy", "https://github.com/levmv/golems"))
		} else {
			registry.WithProvider(provider, token)
		}
	case "ollama":
		registry.WithProvider(provider, "ollama")
	default:
		return llm.Model{}, fmt.Errorf("unsupported provider %q", provider)
	}

	registryURI := uri
	if provider == "openrouter" {
		registryURI = provider + "/" + canonicalOpenRouterModelID(modelID)
	}
	model, err := registry.Model(registryURI)
	if err != nil {
		return llm.Model{}, err
	}

	return model, nil
}

// runPrintTurn keeps stdout suitable for shell pipelines: it contains either
// the final assistant message or one final JSON result. Progress belongs on
// stderr.
func runPrintTurn(ctx context.Context, agent agentRunner, cfg Config, input string, out, errOut io.Writer) error {
	diagnostics := cfg.Verbose
	diagnosticConsole := ui.NewConsole(errOut)

	turn, err := agent.Stream(ctx, input, func(event golem.StreamEvent) {
		if diagnostics {
			printOneShotDiagnostic(diagnosticConsole, event)
		}
	})
	diagnosticConsole.FlushCompactToolEvents()
	if err != nil {
		return err
	}
	if diagnostics {
		diagnosticConsole.PrintChangeSummary()
	}
	if cfg.JSON {
		return writeJSONResult(out, agent, cfg, turn)
	}
	console := ui.NewConsole(out)
	console.PrintMarkdown(turn.Reply)
	fmt.Fprintln(out)
	if cfg.Verbose {
		fmt.Fprintf(errOut, "[usage: %s]\n", golem.FormatUsage(turn.Usage))
	}
	return nil
}

func printOneShotDiagnostic(console *ui.Console, event golem.StreamEvent) {
	switch event.Kind {
	case golem.EventToolCall, golem.EventToolResult, golem.EventToolError:
		console.PrintCompactToolEvent(event)
	case golem.EventModelRetry:
		console.FlushCompactToolEvents()
		console.PrintRetry(event.Text)
	case golem.EventStatus:
		console.PrintStatus(event.Text)
	case golem.EventAttemptReset:
		if event.Text == "" {
			break
		}
		console.PrintDiscardedAttempt()
	}
}

func shouldPrintResumeHint(cfg Config, hasUserTurn bool) bool {
	return hasUserTurn && (!cfg.PrintMode || cfg.SaveSession)
}

func useEphemeralSession(cfg Config, resumeID string) bool {
	return cfg.PrintMode && !cfg.SaveSession && strings.TrimSpace(resumeID) == ""
}

func readPipedInput(stdin *os.File) (string, bool, error) {
	info, err := stdin.Stat()
	if err != nil {
		return "", false, err
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		return "", false, nil
	}

	data, err := io.ReadAll(io.LimitReader(stdin, maxPipedInputBytes+1))
	if err != nil {
		return "", true, err
	}
	if len(data) > maxPipedInputBytes {
		return "", true, fmt.Errorf("piped input exceeds %d bytes", maxPipedInputBytes)
	}
	return strings.TrimSpace(string(data)), true, nil
}

func modelProvider(uri string) string {
	provider, modelID, ok := strings.Cut(strings.TrimSpace(uri), "/")
	if !ok || strings.TrimSpace(provider) == "" || strings.TrimSpace(modelID) == "" {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(provider))
}

func parseInvocation(args []string) (resumeID string, remaining []string, err error) {
	if len(args) == 0 || strings.ToLower(args[0]) != "resume" {
		return "", args, nil
	}
	if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
		return "", nil, errors.New("resume requires a session id or unique prefix")
	}
	return args[1], args[2:], nil
}

func handleControlInvocation(args []string, cfg Config, store *state.Store) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch strings.ToLower(args[0]) {
	case "login":
		if len(args) < 2 {
			statuses, err := listProviderStatus(store)
			if err != nil {
				return true, err
			}
			for _, status := range statuses {
				fmt.Fprintf(os.Stdout, "%s\t%s\n", status.Name, status.Source)
			}
			return true, nil
		}
		provider := strings.ToLower(strings.TrimSpace(args[1]))
		if !isLoginProvider(provider) {
			return true, fmt.Errorf("unsupported login provider %q", provider)
		}
		if providerEnvToken(provider) != "" {
			return true, fmt.Errorf("%s is supplied by an environment override; unset it to log in", provider)
		}
		key, err := promptAPIKey(provider)
		if err != nil {
			return true, err
		}
		if _, err := storeProviderCredential(store, provider, key); err != nil {
			return true, err
		}
		fmt.Fprintf(os.Stdout, "logged in to %s\n", provider)
		return true, nil
	case "logout":
		if len(args) < 2 {
			return true, errors.New("logout requires a provider")
		}
		provider := strings.ToLower(strings.TrimSpace(args[1]))
		if err := deleteProviderCredential(store, provider); err != nil {
			return true, err
		}
		fmt.Fprintf(os.Stdout, "logged out of %s\n", provider)
		return true, nil
	case "model":
		if len(args) == 1 {
			for _, model := range knownModels(store) {
				fmt.Fprintln(os.Stdout, model)
			}
			return true, nil
		}
		cfg.ModelURI = strings.TrimSpace(args[1])
		if _, err := buildModel(cfg, store, true); err != nil {
			return true, err
		}
		resolveModelSpec(cfg.ModelURI, store, true)
		if err := store.SetDefaultModel(cfg.ModelURI); err != nil {
			return true, err
		}
		fmt.Fprintf(os.Stdout, "default model: %s\n", cfg.ModelURI)
		return true, nil
	default:
		return false, nil
	}
}
