package main

import (
	"flag"
	"fmt"
	"strings"

	"github.com/levmv/golems/cy/internal/session"
)

type cliInvocation struct {
	Resume    bool
	SessionID string
	Args      []string
}

func parseInvocation(args []string, explicitPrompt bool) cliInvocation {
	if explicitPrompt || len(args) == 0 || strings.ToLower(args[0]) != "resume" {
		return cliInvocation{Args: args}
	}
	invocation := cliInvocation{Resume: true}
	if len(args) > 1 {
		invocation.SessionID = strings.TrimSpace(args[1])
		invocation.Args = args[2:]
	}
	return invocation
}

func latestSessionID(home, workspace string) (string, error) {
	summaries, err := session.List(home, workspace)
	if err != nil {
		return "", fmt.Errorf("list sessions: %w", err)
	}
	if len(summaries) == 0 {
		return "", fmt.Errorf("no resumable sessions in workspace %s", workspace)
	}
	return summaries[0].ID, nil
}

// hasFlagTerminator mirrors just enough of flag.Parse to tell whether -- was a
// delimiter. In particular, -- may legitimately be consumed as a string flag's
// value, and parsing stops at the first positional argument.
func hasFlagTerminator(flags *flag.FlagSet, args []string) bool {
	for len(args) > 0 {
		arg := args[0]
		if arg == "--" {
			return true
		}
		if len(arg) < 2 || arg[0] != '-' || arg == "-" {
			return false
		}

		name := strings.TrimPrefix(arg, "-")
		name = strings.TrimPrefix(name, "-")
		if name == "" || strings.HasPrefix(name, "-") {
			return false
		}
		name, _, hasValue := strings.Cut(name, "=")
		registered := flags.Lookup(name)
		if registered == nil {
			return false
		}

		args = args[1:]
		if hasValue || isBooleanFlag(registered) {
			continue
		}
		if len(args) == 0 {
			return false
		}
		args = args[1:]
	}
	return false
}

func isBooleanFlag(value *flag.Flag) bool {
	boolean, ok := value.Value.(interface{ IsBoolFlag() bool })
	return ok && boolean.IsBoolFlag()
}

func writeCLIUsage(flags *flag.FlagSet) {
	out := flags.Output()
	fmt.Fprintln(out, "Cy is a terminal coding agent.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  cy [flags] [prompt...]")
	fmt.Fprintln(out, "  cy [flags] resume")
	fmt.Fprintln(out, "  cy [flags] resume <id-or-prefix> [prompt...]")
	fmt.Fprintln(out, "  cy [flags] -- [prompt...]")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "With no prompt, Cy opens the interactive UI. Bare resume continues the")
	fmt.Fprintln(out, "most recent session in the current workspace. Flags must precede the prompt.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Flags:")
	flags.VisitAll(func(value *flag.Flag) {
		label := "--" + value.Name
		if len(value.Name) == 1 {
			label = "-" + value.Name
		}
		if !isBooleanFlag(value) {
			label += " <" + value.Name + ">"
		}
		fmt.Fprintf(out, "  %-24s %s\n", label, value.Usage)
	})
	fmt.Fprintln(out, "  -h, --help               show this help and exit")
}
