package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	// The check cadence Kai set, matching the SYNC_INTERVAL=86400 of the launch
	// wrapper this gate retires. teable:coilyco-bridge/infrastructure#7054
	nativeUpdateCheckInterval = 24 * time.Hour
	// A failed check retries on the wrapper's own retry cadence rather than
	// waiting out the day, so a network blip costs minutes and not a release.
	nativeUpdateRetryInterval = 15 * time.Minute
	nativeUpdateCheckTimeout  = 60 * time.Second
	// The recovery path rather than a convenience: the gate ships inside the
	// binary it gates. teable:coilyco-bridge/infrastructure#7054
	nativeSkipUpdateGateEnv = "AOS_SKIP_UPDATE_GATE"
	nativeUpdateStateFormat = "agentic-os.native-update.v1"
)

// The aos formula ships aterm, aosguard, aosward and aoscompose beside aos
// itself, so two names cover every binary a launch resolves.
var nativeUpdateFormulae = []string{"aos", "agent-compose"}

type nativeUpdateState struct {
	Format string `json:"format"`
	// LastCheck is when the check last completed, successfully or not.
	LastCheck time.Time `json:"last_check"`
	// LastFailure separates "checked, nothing available" from "could not
	// check", which the retry interval needs and one timestamp cannot carry.
	LastFailure time.Time `json:"last_failure,omitempty"`
}

type nativeUpdate struct {
	Formula   string
	Installed string
	Available string
}

// nativeUpdateGate carries the two seams a test replaces. The zero value is not
// usable: callers take defaultNativeUpdateGate.
type nativeUpdateGate struct {
	Check func(ctx context.Context) ([]nativeUpdate, error)
	Apply func(ctx context.Context, formulae []string) error
	Stdin io.Reader
	// TTY reports whether a person is present to answer. Split from Stdin so a
	// test can drive the interactive branch without a pty.
	TTY bool
}

func defaultNativeUpdateGate() nativeUpdateGate {
	return nativeUpdateGate{
		Check: brewOutdated,
		Apply: brewUpgrade,
		Stdin: os.Stdin,
		TTY:   isTerminal(os.Stdin),
	}
}

// A failed check means we do not know an update exists, and a refusal means we
// do, which is why one fails open and the other does not.
func gateNativeUpdate(ctx context.Context, runtime nativeRuntime, gate nativeUpdateGate) error {
	state, due := nativeUpdateDue(runtime)
	if !due {
		runtime.Progress.Skip("update check", "last check %s ago, interval %s",
			formatNativeAge(runtime.Now.Sub(state.LastCheck)),
			formatNativeAge(nativeUpdateCheckInterval))
		return nil
	}
	step := runtime.Progress.Step("check for a newer toolchain")
	checkCtx, cancel := context.WithTimeout(ctx, nativeUpdateCheckTimeout)
	defer cancel()
	updates, err := gate.Check(checkCtx)
	state.Format = nativeUpdateStateFormat
	state.LastCheck = runtime.Now
	if err != nil {
		state.LastFailure = runtime.Now
		writeNativeUpdateState(runtime, state)
		step.Done("check failed, continuing")
		fmt.Fprintf(runtime.Stderr,
			"aos: warning: update check failed, continuing on the installed toolchain: %v\n", err)
		return nil
	}
	state.LastFailure = time.Time{}
	writeNativeUpdateState(runtime, state)
	if len(updates) == 0 {
		step.Done("current")
		return nil
	}
	step.Done("%d update(s) available", len(updates))
	message := nativeUpdateBlockMessage(updates)
	if os.Getenv(nativeSkipUpdateGateEnv) != "" {
		// The bypass skips the block, never the check. A bypass that hid the
		// drift would recreate the defect this gate exists to catch.
		fmt.Fprintf(runtime.Stderr, "%s\naos: warning: %s is set, launching on the stale toolchain\n",
			message, nativeSkipUpdateGateEnv)
		return nil
	}
	if !gate.TTY {
		fmt.Fprint(runtime.Stderr, message)
		return fmt.Errorf(
			"refusing to launch on a stale toolchain with nobody present to accept the update")
	}
	fmt.Fprint(runtime.Stderr, message)
	if !nativeUpdateAccepted(runtime, gate.Stdin) {
		fmt.Fprintf(runtime.Stderr, "aos: warning: declined, launching on the stale toolchain\n")
		return nil
	}
	if err := gate.Apply(ctx, nativeUpdateFormulae); err != nil {
		return fmt.Errorf("apply toolchain update: %w", err)
	}
	return fmt.Errorf("toolchain updated, start the session again to run it")
}

// An unreadable marker is due rather than skipped, matching catalogueMirrorStale:
// a marker nobody can read is not evidence that a check happened.
func nativeUpdateDue(runtime nativeRuntime) (nativeUpdateState, bool) {
	state := nativeUpdateState{Format: nativeUpdateStateFormat}
	if err := readNativeJSON(nativeStatePath(runtime, "update.json"), &state); err != nil {
		return state, true
	}
	if !state.LastFailure.IsZero() && !state.LastFailure.Before(state.LastCheck) {
		return state, runtime.Now.Sub(state.LastFailure) >= nativeUpdateRetryInterval
	}
	return state, runtime.Now.Sub(state.LastCheck) >= nativeUpdateCheckInterval
}

func writeNativeUpdateState(runtime nativeRuntime, state nativeUpdateState) {
	if err := writeNativeJSON(nativeStatePath(runtime, "update.json"), state); err != nil {
		fmt.Fprintf(runtime.Stderr, "aos: warning: write native update state: %v\n", err)
	}
}

func nativeUpdateAccepted(runtime nativeRuntime, stdin io.Reader) bool {
	if stdin == nil {
		return false
	}
	fmt.Fprintf(runtime.Stderr, "aos: apply the update and close this launch? [y/N] ")
	scanner := bufio.NewScanner(stdin)
	if !scanner.Scan() {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return answer == "y" || answer == "yes"
}

// The only surface a blocked operator sees, so both recoveries print inline:
// documentation you need a working seat to read is not a recovery path.
func nativeUpdateBlockMessage(updates []nativeUpdate) string {
	var out strings.Builder
	out.WriteString("\naos: a newer toolchain is installed-behind:\n")
	for _, update := range updates {
		fmt.Fprintf(&out, "aos:   %s %s -> %s\n", update.Formula, update.Installed, update.Available)
	}
	fmt.Fprintf(&out, "aos: upgrade   brew upgrade %s\n", strings.Join(nativeUpdateFormulae, " "))
	fmt.Fprintf(&out, "aos: bypass    %s=1\n", nativeSkipUpdateGateEnv)
	for _, update := range updates {
		if update.Formula != "aos" {
			continue
		}
		fmt.Fprintf(&out,
			"aos: rollback  ln -sfn ../Cellar/aos/%s/bin/aos \"$(brew --prefix)/bin/aos\"\n",
			update.Installed)
	}
	return out.String()
}

// Brew's own auto-update decides whether to refresh the tap. A gate reading a
// tap nothing refreshes would be this same defect one layer down.
func brewOutdated(ctx context.Context) ([]nativeUpdate, error) {
	args := append([]string{"outdated", "--json=v2"}, nativeUpdateFormulae...)
	command := exec.CommandContext(ctx, "brew", args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	runErr := command.Run()
	// Exit 1 means something IS outdated, so reading it as a failure would fail
	// the gate open on exactly the state it exists to catch.
	var payload struct {
		Formulae []struct {
			Name              string   `json:"name"`
			InstalledVersions []string `json:"installed_versions"`
			CurrentVersion    string   `json:"current_version"`
		} `json:"formulae"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		if runErr != nil {
			return nil, fmt.Errorf("brew outdated: %w: %s", runErr, strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("decode brew outdated: %w", err)
	}
	updates := make([]nativeUpdate, 0, len(payload.Formulae))
	for _, formula := range payload.Formulae {
		installed := ""
		if len(formula.InstalledVersions) > 0 {
			installed = formula.InstalledVersions[len(formula.InstalledVersions)-1]
		}
		updates = append(updates, nativeUpdate{
			Formula:   brewFormulaName(formula.Name),
			Installed: installed,
			Available: formula.CurrentVersion,
		})
	}
	return updates, nil
}

// brewFormulaName drops the tap qualifier brew answers with, so the name
// matches what the upgrade and rollback lines tell the operator to type.
func brewFormulaName(name string) string {
	if index := strings.LastIndex(name, "/"); index >= 0 {
		return name[index+1:]
	}
	return name
}

func brewUpgrade(ctx context.Context, formulae []string) error {
	command := exec.CommandContext(ctx, "brew", append([]string{"upgrade"}, formulae...)...)
	command.Stdout = os.Stderr
	command.Stderr = os.Stderr
	return command.Run()
}

// formatNativeDuration would print a day as six figures of seconds: it times
// steps, where sub-second precision is the point.
func formatNativeAge(age time.Duration) string {
	if age < 0 {
		age = 0
	}
	switch {
	case age >= 24*time.Hour:
		return fmt.Sprintf("%.1fd", age.Hours()/24)
	case age >= time.Hour:
		return fmt.Sprintf("%.1fh", age.Hours())
	case age >= time.Minute:
		return fmt.Sprintf("%.0fm", age.Minutes())
	default:
		return fmt.Sprintf("%.0fs", age.Seconds())
	}
}
