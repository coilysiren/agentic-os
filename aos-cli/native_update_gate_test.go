package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func updateTestRuntime(t *testing.T) (nativeRuntime, *bytes.Buffer) {
	t.Helper()
	runtime := nativeTestRuntime(t, t.TempDir())
	stderr := &bytes.Buffer{}
	runtime.Stderr = stderr
	return runtime, stderr
}

func updateTestGate(updates []nativeUpdate, err error) (nativeUpdateGate, *[]string) {
	applied := &[]string{}
	return nativeUpdateGate{
		Check: func(context.Context) ([]nativeUpdate, error) { return updates, err },
		Apply: func(_ context.Context, formulae []string) error {
			*applied = append(*applied, formulae...)
			return nil
		},
		Stdin: strings.NewReader("n\n"),
	}, applied
}

func writeUpdateState(t *testing.T, runtime nativeRuntime, state nativeUpdateState) {
	t.Helper()
	if err := writeNativeJSON(nativeStatePath(runtime, "update.json"), state); err != nil {
		t.Fatal(err)
	}
}

func readUpdateState(t *testing.T, runtime nativeRuntime) nativeUpdateState {
	t.Helper()
	var state nativeUpdateState
	if err := readNativeJSON(nativeStatePath(runtime, "update.json"), &state); err != nil {
		t.Fatal(err)
	}
	return state
}

// A warning with no reader is not loud, and this gate exists because a stale
// binary ran unnoticed for 8.6 days.
func TestUpdateGateRefusesOffTTY(t *testing.T) {
	runtime, stderr := updateTestRuntime(t)
	gate, applied := updateTestGate([]nativeUpdate{{
		Formula: "aos", Installed: "0.313.0", Available: "0.314.0",
	}}, nil)
	gate.TTY = false
	err := gateNativeUpdate(context.Background(), runtime, gate)
	if err == nil {
		t.Fatal("want a refusal without a TTY, got a launch")
	}
	if len(*applied) != 0 {
		t.Fatalf("want nothing applied without a person present, applied %v", *applied)
	}
	for _, want := range []string{"0.313.0 -> 0.314.0", "brew upgrade", nativeSkipUpdateGateEnv, "ln -sfn"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("block message is missing %q:\n%s", want, stderr.String())
		}
	}
}

func TestUpdateGateAppliesOnAcceptThenStops(t *testing.T) {
	runtime, _ := updateTestRuntime(t)
	gate, applied := updateTestGate([]nativeUpdate{{
		Formula: "aos", Installed: "0.313.0", Available: "0.314.0",
	}}, nil)
	gate.TTY = true
	gate.Stdin = strings.NewReader("y\n")
	err := gateNativeUpdate(context.Background(), runtime, gate)
	if err == nil {
		t.Fatal("want the launch stopped for a restart after applying")
	}
	if !strings.Contains(err.Error(), "start the session again") {
		t.Errorf("want a restart instruction, got %v", err)
	}
	if len(*applied) != len(nativeUpdateFormulae) {
		t.Fatalf("want every formula upgraded, applied %v", *applied)
	}
}

func TestUpdateGateDeclineLaunchesStale(t *testing.T) {
	runtime, stderr := updateTestRuntime(t)
	gate, applied := updateTestGate([]nativeUpdate{{
		Formula: "aos", Installed: "0.313.0", Available: "0.314.0",
	}}, nil)
	gate.TTY = true
	gate.Stdin = strings.NewReader("n\n")
	if err := gateNativeUpdate(context.Background(), runtime, gate); err != nil {
		t.Fatalf("a decline is a person choosing to run stale, not an error: %v", err)
	}
	if len(*applied) != 0 {
		t.Fatalf("want nothing applied on a decline, applied %v", *applied)
	}
	if !strings.Contains(stderr.String(), "declined") {
		t.Errorf("a decline must still narrate the staleness:\n%s", stderr.String())
	}
}

// The failure mode of the fix must not be worse than the defect: a network
// blip cannot stop every seat on the machine.
func TestUpdateGateFailsOpenWhenTheCheckErrors(t *testing.T) {
	runtime, stderr := updateTestRuntime(t)
	gate, _ := updateTestGate(nil, fmt.Errorf("network unreachable"))
	gate.TTY = false
	if err := gateNativeUpdate(context.Background(), runtime, gate); err != nil {
		t.Fatalf("want the launch to proceed when the check itself fails, got %v", err)
	}
	if !strings.Contains(stderr.String(), "update check failed") {
		t.Errorf("a failed check must say so:\n%s", stderr.String())
	}
	state := readUpdateState(t, runtime)
	if !state.LastFailure.Equal(runtime.Now) {
		t.Errorf("want the failure recorded for the retry cadence, got %v", state.LastFailure)
	}
}

// The bypass is the recovery path, so it must skip the block and never the
// check. A bypass that hid the drift would recreate the defect.
func TestUpdateGateBypassSkipsTheBlockNotTheCheck(t *testing.T) {
	runtime, stderr := updateTestRuntime(t)
	checked := 0
	gate := nativeUpdateGate{
		Check: func(context.Context) ([]nativeUpdate, error) {
			checked++
			return []nativeUpdate{{Formula: "aos", Installed: "0.313.0", Available: "0.314.0"}}, nil
		},
		Apply: func(context.Context, []string) error { return nil },
		TTY:   false,
	}
	t.Setenv(nativeSkipUpdateGateEnv, "1")
	if err := gateNativeUpdate(context.Background(), runtime, gate); err != nil {
		t.Fatalf("want the bypass to launch, got %v", err)
	}
	if checked != 1 {
		t.Fatalf("want the check to still run under the bypass, ran %d times", checked)
	}
	if !strings.Contains(stderr.String(), "0.313.0 -> 0.314.0") {
		t.Errorf("the bypass must still narrate the drift:\n%s", stderr.String())
	}
}

func TestUpdateGateSkipsInsideTheCooldownAndNarratesTheAge(t *testing.T) {
	runtime, _ := updateTestRuntime(t)
	progress := &bytes.Buffer{}
	runtime.Progress = newNativeProgress(progress, func() time.Time { return runtime.Now })
	writeUpdateState(t, runtime, nativeUpdateState{
		Format:    nativeUpdateStateFormat,
		LastCheck: runtime.Now.Add(-2 * time.Hour),
	})
	checked := 0
	gate := nativeUpdateGate{
		Check: func(context.Context) ([]nativeUpdate, error) {
			checked++
			return nil, nil
		},
		Apply: func(context.Context, []string) error { return nil },
	}
	if err := gateNativeUpdate(context.Background(), runtime, gate); err != nil {
		t.Fatal(err)
	}
	if checked != 0 {
		t.Fatalf("want no check inside the cooldown, ran %d times", checked)
	}
	if !strings.Contains(progress.String(), "last check 2.0h ago, interval 1.0d") {
		t.Errorf("the skip must name the age and the interval:\n%s", progress.String())
	}
}

func TestUpdateDueCadences(t *testing.T) {
	runtime, _ := updateTestRuntime(t)
	cases := []struct {
		name  string
		state nativeUpdateState
		want  bool
	}{
		{"no marker at all is due", nativeUpdateState{}, true},
		{"inside the day is not due", nativeUpdateState{
			LastCheck: runtime.Now.Add(-23 * time.Hour)}, false},
		{"past the day is due", nativeUpdateState{
			LastCheck: runtime.Now.Add(-25 * time.Hour)}, true},
		{"a fresh failure waits out the retry", nativeUpdateState{
			LastCheck:   runtime.Now.Add(-5 * time.Minute),
			LastFailure: runtime.Now.Add(-5 * time.Minute)}, false},
		{"a failure past the retry is due", nativeUpdateState{
			LastCheck:   runtime.Now.Add(-20 * time.Minute),
			LastFailure: runtime.Now.Add(-20 * time.Minute)}, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			local := runtime
			local.StateRoot = t.TempDir()
			if !test.state.LastCheck.IsZero() {
				writeUpdateState(t, local, test.state)
			}
			if _, due := nativeUpdateDue(local); due != test.want {
				t.Errorf("want due=%v, got %v", test.want, due)
			}
		})
	}
}

// An unreadable marker is not evidence that a check happened, so it converges
// rather than skips. Matches catalogueMirrorStale.
func TestUpdateDueOnAnUnreadableMarker(t *testing.T) {
	runtime, _ := updateTestRuntime(t)
	path := nativeStatePath(runtime, "update.json")
	if err := os.MkdirAll(runtime.StateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, due := nativeUpdateDue(runtime); !due {
		t.Error("want an unreadable marker to be due")
	}
}

// fakeBrew puts a `brew` on PATH that answers with the given stdout, stderr
// and exit code, so the exec path is exercised rather than described.
func fakeBrew(t *testing.T, stdout, stderr string, code int) {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\ncat <<'EOF'\n%s\nEOF\ncat <<'EOF' >&2\n%s\nEOF\nexit %d\n",
		stdout, stderr, code)
	if err := os.WriteFile(dir+"/brew", []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// Exit 1 is the answer, and the auto-update banner brew writes to stderr must
// not reach the JSON decode.
func TestBrewOutdatedTreatsExitOneAsTheAnswer(t *testing.T) {
	fakeBrew(t,
		`{"formulae":[{"name":"coilyco-flight-deck/tap/aos",`+
			`"installed_versions":["0.313.0"],"current_version":"0.314.0"}],"casks":[]}`,
		"==> Auto-updating Homebrew...", 1)
	updates, err := brewOutdated(context.Background())
	if err != nil {
		t.Fatalf("exit 1 is the answer, not a failure: %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("want one update, got %d", len(updates))
	}
	want := nativeUpdate{Formula: "aos", Installed: "0.313.0", Available: "0.314.0"}
	if updates[0] != want {
		t.Errorf("got %+v, want %+v", updates[0], want)
	}
}

func TestBrewOutdatedReportsNothingWhenCurrent(t *testing.T) {
	fakeBrew(t, `{"formulae":[],"casks":[]}`, "", 0)
	updates, err := brewOutdated(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 0 {
		t.Errorf("want no updates, got %+v", updates)
	}
}

// A brew that cannot run at all is an error, so the gate fails open rather
// than reading an empty answer as "nothing available".
func TestBrewOutdatedErrorsOnUnusableOutput(t *testing.T) {
	fakeBrew(t, "", "Error: unknown command", 2)
	if _, err := brewOutdated(context.Background()); err == nil {
		t.Fatal("want an error when brew answers with no JSON")
	}
}

func TestFormatNativeAgeScales(t *testing.T) {
	cases := map[time.Duration]string{
		-time.Second:                   "0s",
		30 * time.Second:               "30s",
		90 * time.Second:               "2m",
		2 * time.Hour:                  "2.0h",
		24 * time.Hour:                 "1.0d",
		206*time.Hour + 24*time.Minute: "8.6d",
	}
	for age, want := range cases {
		if got := formatNativeAge(age); got != want {
			t.Errorf("formatNativeAge(%s) = %q, want %q", age, got, want)
		}
	}
}
