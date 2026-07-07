//go:build linux

package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/landlock-lsm/go-landlock/landlock"
)

// shellCommand builds the process that runs a shell command. With the sandbox
// on, it does not exec bash directly: it re-execs caliban itself into the
// SandboxArgv entrypoint (RunSandboxedShell), which applies a Landlock
// filesystem sandbox and only then becomes bash. The command, workdir, and
// policy travel by env; bash's own (scrubbed) env is rebuilt in the child, so
// the control vars never reach it.
//
// Landlock restricts the calling process irreversibly and is inherited across
// execve, and Go cannot run code between fork and exec — hence the re-exec
// trampoline rather than an in-process fork hook.
func shellCommand(workdir, command, sandbox string) (*exec.Cmd, error) {
	if sandbox == SandboxOff {
		cmd := exec.Command("bash", "-c", command)
		cmd.Env = scrubbedEnv(workdir)
		return cmd, nil
	}
	cmd := exec.Command("/proc/self/exe", SandboxArgv)
	env := append(scrubbedEnv(workdir),
		envSandboxCmd+"="+command,
		envSandboxWorkdir+"="+workdir,
		envSandboxPolicy+"="+sandbox,
	)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		env = append(env, envSandboxLocalBin+"="+filepath.Join(home, ".local", "bin"))
	}
	cmd.Env = env
	return cmd, nil
}

// RunSandboxedShell is the re-exec entrypoint (main dispatches argv[1] ==
// SandboxArgv here). It applies the Landlock sandbox to itself and execs bash;
// it never returns on success. On a sandbox failure under SandboxRequire it
// exits without running the command (fail closed).
func RunSandboxedShell() {
	command := os.Getenv(envSandboxCmd)
	workdir := os.Getenv(envSandboxWorkdir)
	policy := os.Getenv(envSandboxPolicy)
	localBin := os.Getenv(envSandboxLocalBin)

	// Resolve bash before locking down, while PATH lookups are still free.
	bash, err := exec.LookPath("bash")
	if err != nil {
		bash = "/bin/bash"
	}

	// Pin to the OS thread we restrict, so the following execve runs on it and
	// bash inherits the Landlock domain.
	runtime.LockOSThread()
	if err := applyLandlock(workdir, policy, landlockOptions{
		LocalBin:        localBin,
		WorkdirWritable: true,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "(sandbox: %v)", err)
		os.Exit(126)
	}

	// scrubbedEnv emits only HOME + the allow-listed vars, so the CALIBAN_SANDBOX_*
	// control vars are dropped here and never seen by bash.
	if err := syscall.Exec(bash, []string{"bash", "-c", command}, scrubbedEnv(workdir)); err != nil {
		fmt.Fprintf(os.Stderr, "(sandbox: exec bash: %v)", err)
		os.Exit(126)
	}
}

// trustedRunnerCommand builds the trusted runner process. When sandboxing is
// enabled it re-execs caliban into RunSandboxedRunner, which applies Landlock
// before execing the runner binary.
func trustedRunnerCommand(spec runnerCommandSpec) (*exec.Cmd, error) {
	if spec.Sandbox == SandboxOff {
		cmd := exec.Command(spec.Executable, spec.Args...)
		cmd.Dir = spec.Dir
		cmd.Env = spec.Env
		return cmd, nil
	}
	b, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("encode runner sandbox spec: %w", err)
	}
	cmd := exec.Command("/proc/self/exe", RunnerArgv)
	cmd.Dir = spec.Dir
	cmd.Env = append(scrubbedEnv(spec.Workdir), envSandboxRunner+"="+string(b))
	return cmd, nil
}

func RunSandboxedRunner() {
	var spec runnerCommandSpec
	if err := json.Unmarshal([]byte(os.Getenv(envSandboxRunner)), &spec); err != nil {
		fmt.Fprintf(os.Stderr, "(runner sandbox: invalid spec: %v)", err)
		os.Exit(126)
	}
	if spec.Executable == "" {
		fmt.Fprint(os.Stderr, "(runner sandbox: executable is required)")
		os.Exit(126)
	}
	if spec.Dir == "" {
		spec.Dir = spec.Workdir
	}

	runtime.LockOSThread()
	if err := applyLandlock(spec.Workdir, spec.Sandbox, landlockOptions{
		WorkdirWritable: spec.WorkdirWritable,
		RODirs:          spec.RODirs,
		RWDirs:          spec.RWDirs,
		ROFiles:         spec.ROFiles,
		RWFiles:         spec.RWFiles,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "(runner sandbox: %v)", err)
		os.Exit(126)
	}

	argv := append([]string{filepath.Base(spec.Executable)}, spec.Args...)
	if err := syscall.Exec(spec.Executable, argv, spec.Env); err != nil {
		fmt.Fprintf(os.Stderr, "(runner sandbox: exec %s: %v)", spec.Executable, err)
		os.Exit(126)
	}
}

type landlockOptions struct {
	LocalBin        string
	WorkdirWritable bool
	RODirs          []string
	RWDirs          []string
	ROFiles         []string
	RWFiles         []string
}

// applyLandlock confines the process to a filesystem allow-list: read+exec on
// the system directories needed to run programs, read-write on the workspace and
// temp dirs, and the common device files. The conversation DB and the credential
// directory are deliberately NOT granted, so the shell cannot read caliban's own
// secrets even though it runs as the same user.
//
// Under SandboxRequire the restriction is mandatory (V1, errors if the kernel
// lacks Landlock: fail closed). Under SandboxAuto it is best-effort and
// degrades to a no-op on kernels without Landlock.
func applyLandlock(workdir, policy string, opts landlockOptions) error {
	rules := []landlock.Rule{
		landlock.RODirs(
			"/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32",
			"/etc", "/opt", "/run/systemd/resolve",
		).IgnoreIfMissing(),
		landlock.RWDirs("/tmp", "/var/tmp").IgnoreIfMissing(),
		landlock.RWFiles(
			"/dev/null", "/dev/zero", "/dev/full",
			"/dev/random", "/dev/urandom", "/dev/tty",
		).IgnoreIfMissing(),
	}
	if opts.WorkdirWritable {
		rules = append(rules, landlock.RWDirs(workdir).IgnoreIfMissing())
	} else {
		rules = append(rules, landlock.RODirs(workdir).IgnoreIfMissing())
	}
	if opts.LocalBin != "" {
		rules = append(rules, landlock.RODirs(opts.LocalBin).IgnoreIfMissing())
	}
	if len(opts.RODirs) > 0 {
		rules = append(rules, landlock.RODirs(opts.RODirs...).IgnoreIfMissing())
	}
	if len(opts.RWDirs) > 0 {
		rules = append(rules, landlock.RWDirs(opts.RWDirs...).IgnoreIfMissing())
	}
	if len(opts.ROFiles) > 0 {
		rules = append(rules, landlock.ROFiles(opts.ROFiles...).IgnoreIfMissing())
	}
	if len(opts.RWFiles) > 0 {
		rules = append(rules, landlock.RWFiles(opts.RWFiles...).IgnoreIfMissing())
	}
	if policy == SandboxRequire {
		// V1 is enough to deny reads outside the allow-list (kernel >= 5.13);
		// strict enforcement, so an unsupported kernel is an error, not a no-op.
		return landlock.V1.RestrictPaths(rules...)
	}
	return landlock.V9.BestEffort().RestrictPaths(rules...)
}
