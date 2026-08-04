package main

import (
	"errors"
	"fmt"
	"testing"
)

func TestNormalizeSandboxPolicySupportsOnAndLegacyRequire(t *testing.T) {
	for _, input := range []string{sandboxOn, "require"} {
		got, err := normalizeSandboxPolicy(input)
		if err != nil || got != sandboxOn {
			t.Fatalf("normalizeSandboxPolicy(%q) = %q, %v", input, got, err)
		}
	}
}

func TestEffectiveSandboxPolicy(t *testing.T) {
	for _, test := range []struct {
		requested string
		container string
		want      string
	}{
		{requested: sandboxAuto, want: sandboxAuto},
		{requested: sandboxAuto, container: "podman", want: sandboxOff},
		{requested: sandboxOn, container: "podman", want: sandboxOn},
		{requested: sandboxOff, container: "podman", want: sandboxOff},
	} {
		got := effectiveSandboxPolicy(test.requested, test.container)
		if got != test.want {
			t.Fatalf("effectiveSandboxPolicy(%q, %q) = %q, want %q", test.requested, test.container, got, test.want)
		}
	}
}

func TestDetectContainerWithStrongSignals(t *testing.T) {
	for _, test := range []struct {
		name       string
		files      map[string]string
		detectVirt string
		wantID     string
	}{
		{
			name:   "podman marker",
			files:  map[string]string{"/run/.containerenv": ""},
			wantID: "podman",
		},
		{
			name:   "docker marker",
			files:  map[string]string{"/.dockerenv": ""},
			wantID: "docker",
		},
		{
			name:   "systemd lxc marker",
			files:  map[string]string{"/run/systemd/container": "lxc\n"},
			wantID: "lxc",
		},
		{
			name:   "pid one environment",
			files:  map[string]string{"/proc/1/environ": "PATH=/usr/bin\x00container=systemd-nspawn\x00"},
			wantID: "systemd-nspawn",
		},
		{
			name:       "systemd detect virt fallback",
			detectVirt: "podman\n",
			wantID:     "podman",
		},
		{
			name:       "wsl is not trusted isolation",
			detectVirt: "wsl\n",
		},
		{
			name: "unknown environment",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			probe := containerProbe{
				exists: func(path string) bool {
					_, ok := test.files[path]
					return ok
				},
				readFile: func(path string) ([]byte, error) {
					value, ok := test.files[path]
					if !ok {
						return nil, errors.New("not found")
					}
					return []byte(value), nil
				},
				detectVirt: func() (string, error) {
					if test.detectVirt == "" {
						return "", fmt.Errorf("not detected")
					}
					return test.detectVirt, nil
				},
			}
			got := detectContainerWith(probe)
			if got != test.wantID {
				t.Fatalf("detectContainerWith() = %q, want %q", got, test.wantID)
			}
		})
	}
}

func TestSecuritySummaryIncludesAutoContainer(t *testing.T) {
	state := SecurityState{Container: "podman"}
	if got, want := state.Compact(), "sandbox: off (podman) · network: open"; got != want {
		t.Fatalf("Compact() = %q, want %q", got, want)
	}
}

func TestUnavailableAutoSandboxFallsBackOff(t *testing.T) {
	state := unavailableSandbox(SecurityState{EffectivePolicy: sandboxAuto}, "probe failed")
	if state.EffectivePolicy != sandboxOff || state.Probe != "probe failed" {
		t.Fatalf("unavailableSandbox(auto) = %#v", state)
	}

	state = unavailableSandbox(SecurityState{EffectivePolicy: sandboxOn}, "probe failed")
	if state.EffectivePolicy != sandboxOn {
		t.Fatalf("unavailableSandbox(on) = %#v", state)
	}
}
