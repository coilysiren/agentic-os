package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures are real `agent-compose catalog` and `overlay` captures, so
// regenerate them from the pinned agent-compose rather than editing by hand.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return raw
}

func platformOverlay(t *testing.T) overlayDocument {
	t.Helper()
	document, err := parseOverlay(fixture(t, "platform-claude-overlay.json"), "platform", "claude", "acting")
	if err != nil {
		t.Fatalf("parse the platform overlay fixture: %v", err)
	}
	return document
}

type recordedSpawn struct {
	name string
	args []string
}

// stubDeps answers the two Agent Compose reads from fixtures and records the
// window it would have opened, so no test needs a terminal or a real harness.
func stubDeps(t *testing.T, spawns *[]recordedSpawn, shadowed bool) commandDeps {
	t.Helper()
	// A launch prefers the role's installed app, so a home with none is what
	// keeps these answers about the code rather than about this machine.
	t.Setenv("HOME", t.TempDir())
	return commandDeps{
		lookPath: func(name string) (string, error) {
			if strings.HasPrefix(name, "/missing") {
				return "", fmt.Errorf("not found")
			}
			return "/stub/" + filepath.Base(name), nil
		},
		output: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			switch {
			case len(args) > 0 && args[0] == "catalog":
				return fixture(t, "roster.json"), nil
			case len(args) > 0 && args[0] == "_launch-agent":
				// aos owns the launch profiles, and every fixture role seats claude.
				return []byte("claude\n"), nil
			case len(args) > 0 && args[0] == "--version":
				return []byte("stub 1.0\n"), nil
			case len(args) > 0 && args[0] == "overlay":
				role, seat := "platform", "claude"
				for index, value := range args {
					if index+1 >= len(args) {
						break
					}
					switch value {
					case "--role":
						role = args[index+1]
					case "--seat":
						seat = args[index+1]
					}
				}
				return fixture(t, role+"-"+seat+"-overlay.json"), nil
			}
			return nil, fmt.Errorf("unexpected read: %v", args)
		},
		run: func(_ context.Context, _ string, args ...string) error {
			// Only the shadow probe answers to the shadowed switch. A terminal
			// asked to parse its own config always can.
			if len(args) > 0 && args[0] == "+runpy" {
				return nil
			}
			if shadowed {
				return nil
			}
			return fmt.Errorf("no native shadow")
		},
		spawn: func(_ context.Context, name string, args ...string) error {
			*spawns = append(*spawns, recordedSpawn{name: name, args: args})
			return nil
		},
		self: func() (string, error) { return "/stub/aterm", nil },
		pick: func(rosterDocument) (string, string, error) {
			return "director", "codex", nil
		},
		tty: func() bool { return true },
	}
}

// runAterm always pins a working directory. The default is $PROJECTS_ROOT or
// ~/projects, which no CI container has, and inheriting it fails every test.
func runAterm(t *testing.T, deps commandDeps, argv ...string) (string, error) {
	t.Helper()
	// A test inherits whatever shadow it was started from, which would change
	// the launch under test. See nested_test.go for the shadow cases.
	clearShadowEnv(t)
	return runAtermRaw(t, deps, argv...)
}

func runAtermRaw(t *testing.T, deps commandDeps, argv ...string) (string, error) {
	t.Helper()
	stdout := &bytes.Buffer{}
	command := newCommand(deps)
	command.Writer = stdout
	full := append([]string{"aterm", "--working-directory", t.TempDir()}, argv...)
	err := command.Run(context.Background(), full)
	return stdout.String(), err
}

func TestLaunchPlanRunsTheNativeSessionInsideTheWindow(t *testing.T) {
	var spawns []recordedSpawn
	deps := stubDeps(t, &spawns, true)
	out, err := runAterm(t, deps, "--dry-run", "--json", "platform", "claude")
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	var plan launchPlan
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	if plan.Format != launchFormat {
		t.Fatalf("plan format = %q", plan.Format)
	}
	if !plan.Shadowed {
		t.Fatal("a probing AOS should produce a shadowed launch")
	}
	want := []string{
		"/stub/aos", "_native-shadow", "--harness", "claude",
		"--role", "platform", "--assigned-role", "--",
		"/stub/agent-compose", "launch", "platform", "claude",
	}
	if strings.Join(plan.Child, " ") != strings.Join(want, " ") {
		t.Fatalf("child = %v, want %v", plan.Child, want)
	}
	// Derived from the fixture, not spelled out. Roster titles turn over, and a
	// hardcoded one makes an upstream retitle look like a launcher regression.
	if plan.Identity.Annotation != platformOverlay(t).Annotation {
		t.Fatalf("annotation = %q", plan.Identity.Annotation)
	}
	// The window runs the inner stage, never the child directly, or a failing
	// launch closes over the reason. kitty puts the program in the tail.
	stage := indexOf(plan.Arguments, "/stub/aterm")
	if stage < 0 || plan.Arguments[stage+1] != sessionCommand {
		t.Fatalf("terminal should exec the aterm session stage: %v", plan.Arguments)
	}
}

func TestLaunchDegradesWhenNoNativeShadowIsAvailable(t *testing.T) {
	var spawns []recordedSpawn
	out, err := runAterm(t, stubDeps(t, &spawns, false), "--dry-run", "--json", "platform", "claude")
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	var plan launchPlan
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	if plan.Shadowed {
		t.Fatal("a failing probe must not claim a shadow")
	}
	if plan.Child[0] != "/stub/agent-compose" || plan.Child[1] != "launch" {
		t.Fatalf("child should fall back to a direct launch: %v", plan.Child)
	}
}

func TestDryRunNeverOpensAWindow(t *testing.T) {
	var spawns []recordedSpawn
	if _, err := runAterm(t, stubDeps(t, &spawns, true), "--dry-run", "platform"); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if len(spawns) != 0 {
		t.Fatalf("dry run opened %d window(s)", len(spawns))
	}
}

func TestLaunchOpensExactlyOneWindow(t *testing.T) {
	var spawns []recordedSpawn
	out, err := runAterm(t, stubDeps(t, &spawns, true), "platform", "claude")
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if len(spawns) != 1 {
		t.Fatalf("opened %d window(s), want 1", len(spawns))
	}
	if spawns[0].name != "/stub/kitty" {
		t.Fatalf("spawned %q", spawns[0].name)
	}
	if !strings.Contains(out, "Angie") {
		t.Fatalf("launch should name the seat it opened: %q", out)
	}
}

func TestHoldFlagReachesTheSessionStage(t *testing.T) {
	var spawns []recordedSpawn
	out, err := runAterm(t, stubDeps(t, &spawns, true), "--dry-run", "--json", "--hold", "platform")
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !strings.Contains(out, `"--hold"`) {
		t.Fatal("--hold should be handed to the session stage")
	}
}

func TestBareInvocationAsksInsteadOfFailing(t *testing.T) {
	var spawns []recordedSpawn
	out, err := runAterm(t, stubDeps(t, &spawns, true), "--dry-run", "--json")
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	var plan launchPlan
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	if plan.Identity.Role != "director" || plan.Identity.Seat != "codex" {
		t.Fatalf("the picked role and seat should drive the launch: %+v", plan.Identity)
	}
}

func TestBareInvocationWithoutATerminalNamesTheListing(t *testing.T) {
	var spawns []recordedSpawn
	deps := stubDeps(t, &spawns, true)
	deps.tty = func() bool { return false }
	deps.pick = func(rosterDocument) (string, string, error) {
		t.Fatal("a non-interactive run must not open a picker")
		return "", "", nil
	}
	_, err := runAterm(t, deps, "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "--list") {
		t.Fatalf("error should point at the listing: %v", err)
	}
}

func TestListPrintsEveryLiveRole(t *testing.T) {
	var spawns []recordedSpawn
	out, err := runAterm(t, stubDeps(t, &spawns, true), "--list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, slug := range []string{"platform", "sysadmin", "science", "frontend", "gamedev", "director", "advocate", "analyst"} {
		if !strings.Contains(out, slug) {
			t.Fatalf("listing should name %q: %s", slug, out)
		}
	}
	if strings.Contains(out, "penpot") {
		t.Fatal("the listing should only offer launchable seats")
	}
}

func TestStaleRoleAndSeatAreRejectedBeforeAnyWindowOpens(t *testing.T) {
	cases := map[string][]string{
		"stale role":        {"engineer", "claude"},
		"seat outside role": {"platform", "penpot"},
		"unsafe role":       {"../etc", "claude"},
	}
	for name, argv := range cases {
		t.Run(name, func(t *testing.T) {
			var spawns []recordedSpawn
			_, err := runAterm(t, stubDeps(t, &spawns, true), argv...)
			if err == nil {
				t.Fatal("expected the invocation to be rejected")
			}
			if len(spawns) != 0 {
				t.Fatal("a rejected invocation must not open a window")
			}
		})
	}
}

func TestHarnessArgumentsSurviveToTheLaunch(t *testing.T) {
	var spawns []recordedSpawn
	out, err := runAterm(t, stubDeps(t, &spawns, true), "--dry-run", "--json", "platform", "codex", "--", "--model", "opus")
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	var plan launchPlan
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	tail := strings.Join(plan.Child[len(plan.Child)-2:], " ")
	if tail != "--model opus" {
		t.Fatalf("harness arguments were dropped: %v", plan.Child)
	}
}

// aos owns the launch profiles, so the seat aterm defaults to is whatever that
// binary reports rather than anything this one parses.
func TestDefaultSeatTakesTheAgentAosReports(t *testing.T) {
	document := loadRosterFixture(t)
	role, _ := document.role("platform")
	var asked []string
	deps := commandDeps{
		lookPath: func(name string) (string, error) { return "/stub/" + name, nil },
		output: func(_ context.Context, name string, args ...string) ([]byte, error) {
			asked = append(asked, name+" "+strings.Join(args, " "))
			return []byte("codex\n"), nil
		},
	}
	if got := defaultSeat(context.Background(), deps, "aos", role); got != "codex" {
		t.Fatalf("default seat = %q, want the reported codex", got)
	}
	if len(asked) != 1 || !strings.Contains(asked[0], "_launch-agent platform") {
		t.Fatalf("aos should be asked for the role's agent: %v", asked)
	}
}

func TestDefaultSeatFallsBackWhenAosCannotAnswer(t *testing.T) {
	document := loadRosterFixture(t)
	role, _ := document.role("platform")
	want := role.nativeSeats()[0].Harness
	cases := map[string]commandDeps{
		"no aos on PATH": {
			lookPath: func(string) (string, error) { return "", fmt.Errorf("absent") },
		},
		"aos too old for the verb": {
			lookPath: func(name string) (string, error) { return "/stub/" + name, nil },
			output: func(context.Context, string, ...string) ([]byte, error) {
				return nil, fmt.Errorf("unknown command")
			},
		},
		"reports a seat outside the role": {
			lookPath: func(name string) (string, error) { return "/stub/" + name, nil },
			output: func(context.Context, string, ...string) ([]byte, error) {
				return []byte("goose\n"), nil
			},
		},
		"reports something unlaunchable": {
			lookPath: func(name string) (string, error) { return "/stub/" + name, nil },
			output: func(context.Context, string, ...string) ([]byte, error) {
				return []byte("penpot\n"), nil
			},
		},
	}
	for name, deps := range cases {
		t.Run(name, func(t *testing.T) {
			if got := defaultSeat(context.Background(), deps, "aos", role); got != want {
				t.Fatalf("default seat = %q, want the catalogue's %q", got, want)
			}
		})
	}
}

func TestParseOverlayRejectsContractAndSelectionDrift(t *testing.T) {
	raw := fixture(t, "platform-claude-overlay.json")
	cases := map[string][3]string{
		"role drift":       {"director", "claude", "acting"},
		"seat drift":       {"platform", "codex", "acting"},
		"expression drift": {"platform", "claude", "reviewing"},
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseOverlay(raw, want[0], want[1], want[2]); err == nil {
				t.Fatal("expected the overlay to be rejected")
			}
		})
	}
	if _, err := parseOverlay([]byte(`{"format":"other","schema_version":1}`), "platform", "claude", "acting"); err == nil {
		t.Fatal("expected an unsupported contract to be rejected")
	}
}

func TestBuildTitleRejectsControlCharactersAndTruncates(t *testing.T) {
	document := platformOverlay(t)
	if _, err := buildTitle(document, "bad\x07title", ""); err == nil {
		t.Fatal("a control character in the task title should be rejected")
	}
	title, err := buildTitle(document, strings.Repeat("x", 400), "")
	if err != nil {
		t.Fatalf("build title: %v", err)
	}
	if length := len([]rune(title)); length != maxTitleRunes {
		t.Fatalf("title length = %d, want %d", length, maxTitleRunes)
	}
	if !strings.HasSuffix(title, "…") {
		t.Fatal("a truncated title should show it was cut")
	}
}

func TestColorDerivationIsDeterministicAndReadable(t *testing.T) {
	// The local tint is what a document with no solved background falls back to.
	document := platformOverlay(t)
	document.Background = ""
	brand, err := buildBrand(document, "", "")
	if err != nil {
		t.Fatalf("build brand: %v", err)
	}
	if brand.Accent != "#9c8b31" {
		t.Fatalf("accent = %q", brand.Accent)
	}
	if brand.Background != "#1b1c18" {
		t.Fatalf("background = %q", brand.Background)
	}
	accent, err := parseHex(brand.Accent)
	if err != nil {
		t.Fatalf("parse accent: %v", err)
	}
	selection, err := parseHex(brand.SelectionText)
	if err != nil {
		t.Fatalf("parse selection text: %v", err)
	}
	if ratio := contrastRatio(accent, selection); ratio < 4.5 {
		t.Fatalf("selection contrast ratio = %f, want at least 4.5", ratio)
	}
}

func TestSeatAnnotationFallsBackForAnOlderAgentCompose(t *testing.T) {
	document := platformOverlay(t)
	composed := document.Annotation
	name := document.Seat.Name
	label := document.RoleDisplayName
	subject, _, _ := strings.Cut(document.Seat.Pronouns, "/")

	document.Annotation = ""
	if got := seatAnnotation(document); got != composed {
		t.Fatalf("the fallback should rebuild what agent-compose composed: %q", got)
	}
	document.Seat.Pronouns = ""
	if got := seatAnnotation(document); got != name+" ("+label+")" {
		t.Fatalf("annotation without pronouns = %q", got)
	}
	document.RoleDisplayName = ""
	if got := seatAnnotation(document); got != name+" ("+document.Role+")" {
		t.Fatalf("annotation without a display name = %q", got)
	}
	if subject == "" {
		t.Fatal("the fixture should carry pronouns for this to mean anything")
	}
}

func TestDefaultWorkingDirectoryPrefersTheProjectsRoot(t *testing.T) {
	t.Setenv(defaultWorkingEnvVar, "/tmp/projects-root")
	if got := defaultWorkingDirectory(); got != "/tmp/projects-root" {
		t.Fatalf("working directory = %q", got)
	}
	t.Setenv(defaultWorkingEnvVar, "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	if got := defaultWorkingDirectory(); got != filepath.Join(home, "projects") {
		t.Fatalf("working directory fallback = %q", got)
	}
}

func TestValidateWorkingDirectoryRejectsFilesAndGaps(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, value := range []string{"", file, filepath.Join(t.TempDir(), "absent")} {
		if _, err := validateWorkingDirectory(value); err == nil {
			t.Fatalf("%q should be rejected", value)
		}
	}
}

func TestVersionFlagReportsTheReleaseVersion(t *testing.T) {
	previous := version
	version = "aos-v9.9.9"
	t.Cleanup(func() { version = previous })
	var spawns []recordedSpawn
	out, err := runAterm(t, stubDeps(t, &spawns, true), "--version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.Contains(out, "aos-v9.9.9") {
		t.Fatalf("version output = %q", out)
	}
}

func indexOf(values []string, want string) int {
	for index, value := range values {
		if value == want {
			return index
		}
	}
	return -1
}

// Nothing else checks that the brand survives into the terminal's arguments, so
// a flag-dialect change could drop every color and still pass the suite.
func TestBrandReachesTheTerminalArguments(t *testing.T) {
	var spawns []recordedSpawn
	out, err := runAterm(t, stubDeps(t, &spawns, true), "--dry-run", "--json", "platform", "claude")
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	var plan launchPlan
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	joined := strings.Join(plan.Arguments, " ")
	for _, want := range []string{
		"background=" + plan.Brand.Background,
		"cursor=" + plan.Brand.Accent,
		"selection_background=" + plan.Brand.Accent,
		"selection_foreground=" + plan.Brand.SelectionText,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("terminal arguments are missing %q: %v", want, plan.Arguments)
		}
	}
	if title := indexOf(plan.Arguments, "--title"); title < 0 || plan.Arguments[title+1] != plan.Brand.Title {
		t.Fatalf("terminal arguments should carry the composed title: %v", plan.Arguments)
	}
	// The stage is the tail: kitty has no -e, so a stray leading flag is its own.
	if plan.Arguments[len(plan.Arguments)-1] != strings.Join(plan.Child, " ") &&
		indexOf(plan.Arguments, sessionCommand) < indexOf(plan.Arguments, "--title") {
		t.Fatalf("session stage should follow the terminal's own flags: %v", plan.Arguments)
	}
}

// Nothing else pins the opening state, so a default edit could revert it and
// still pass. Kai asked for fullscreen. See docs/aterm.md.
func TestTheWindowOpensFullscreenByDefault(t *testing.T) {
	var spawns []recordedSpawn
	out, err := runAterm(t, stubDeps(t, &spawns, true), "--dry-run", "--json", "platform", "claude")
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	var plan launchPlan
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	state := indexOf(plan.Arguments, "--start-as")
	if state < 0 || plan.Arguments[state+1] != "fullscreen" {
		t.Fatalf("the window should open fullscreen: %v", plan.Arguments)
	}
}

// A session window that opens small enough to need resizing before the first
// prompt is the thing Kai reported. See docs/aterm.md.
func TestWindowOptionsReachKittyAndRefuseNonsense(t *testing.T) {
	if err := validateWindow("maximized", "14.5"); err != nil {
		t.Fatalf("the defaults should validate: %v", err)
	}
	if err := validateWindow("embiggened", "14.5"); err == nil {
		t.Fatal("an unknown window state should be refused before kitty sees it")
	}
	if err := validateWindow("maximized", "huge"); err == nil {
		t.Fatal("a non-numeric font size should be refused")
	}
	if err := validateWindow("maximized", "0"); err == nil {
		t.Fatal("a zero font size should be refused")
	}
}

// A kitty outliving its last window leaves the bundle registered, so a reopen
// activates an empty app instead of a session. See docs/aterm.md.
func TestClosingTheWindowQuitsTheTerminalSoAReopenRelaunches(t *testing.T) {
	var spawns []recordedSpawn
	out, err := runAterm(t, stubDeps(t, &spawns, true), "--dry-run", "--json", "platform", "claude")
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	var plan launchPlan
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	want := "macos_quit_when_last_window_closed=yes"
	if !strings.Contains(strings.Join(plan.Arguments, " "), want) {
		t.Fatalf("terminal arguments are missing %q: %v", want, plan.Arguments)
	}
	// The option only binds ahead of the child, since kitty reads the tail as
	// the program to run.
	if indexOf(plan.Arguments, want) > indexOf(plan.Arguments, sessionCommand) {
		t.Fatalf("the quit option must precede the session stage: %v", plan.Arguments)
	}
}

// The roster solves the background across the whole set, which a launcher
// holding one overlay cannot. agentic-os#1245, agent-compose#358
func TestTheRosterSolvedBackgroundWinsOverTheLocalTint(t *testing.T) {
	document := platformOverlay(t)
	document.Background = ""
	tinted, err := buildBrand(document, "", "")
	if err != nil {
		t.Fatalf("build brand: %v", err)
	}
	if tinted.Background != "#1b1c18" {
		t.Fatalf("an overlay with no background should still tint: %q", tinted.Background)
	}
	document.Background = "#1F2000"
	solved, err := buildBrand(document, "", "")
	if err != nil {
		t.Fatalf("build brand: %v", err)
	}
	if solved.Background != "#1f2000" {
		t.Fatalf("background = %q, want the roster's own", solved.Background)
	}
	if solved.Accent != tinted.Accent {
		t.Fatal("the accent is authored identity and must not move")
	}
	document.Background = "not a color"
	if _, err := buildBrand(document, "", ""); err == nil {
		t.Fatal("an unparsable roster background should be refused, not tinted over")
	}
}
