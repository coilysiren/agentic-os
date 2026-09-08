package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/urfave/cli/v3"
)

const (
	nativeSweepInterval         = 10 * time.Minute
	nativeDeadSessionGrace      = 24 * time.Hour
	nativeLockPoll              = 100 * time.Millisecond
	nativeLockNotice            = 5 * time.Second
	nativeLockWait              = 2 * time.Minute
	nativeLockGrace             = 5 * time.Second
	nativeDeleteScans           = 3
	nativeSessionIDAttempts     = 64
	nativeIDLetters             = "abcdefghjkmpqrstuvwxyz"
	nativeIDDigits              = "456789"
	agentComposeModelTierEnv    = "AGENT_COMPOSE_MODEL_TIER"
	agentComposeModelClassEnv   = "AGENT_COMPOSE_MODEL_CLASS"
	agentComposeRuntimeHomeEnv  = "AGENT_COMPOSE_RUNTIME_HOME"
	claudeDisableAutoUpdaterEnv = "DISABLE_AUTOUPDATER"
	nativeSessionEnv            = "AOS_NATIVE_SESSION"
	nativeSessionProjectsEnv    = "AOS_NATIVE_SESSION_PROJECTS"
	nativeSessionRootEnv        = "AOS_NATIVE_SESSION_ROOT"
	// A launcher inside a shadow needs the values the shadow replaced, so it
	// can hand a new session the canonical ones. docs/native-shadow.md
	nativeCanonicalHomeEnv     = "AOS_NATIVE_CANONICAL_HOME"
	nativeCanonicalProjectsEnv = "AOS_NATIVE_CANONICAL_PROJECTS"
	// Every session worktree is cut from this ref, so provenance verification
	// judges the same commit the session will read. docs/native-session-start.md
	nativeWorktreeBase = "origin/main"
)

type nativeArtifact struct {
	Repository string `json:"repository"`
	Worktree   string `json:"worktree"`
	Branch     string `json:"branch"`
}

type nativeLease struct {
	Format          string `json:"format"`
	ID              string `json:"id"`
	Harness         string `json:"harness"`
	PID             int    `json:"pid"`
	ProcessStart    string `json:"process_start"`
	OriginalCWD     string `json:"original_cwd"`
	SessionRoot     string `json:"session_root"`
	SessionProjects string `json:"session_projects"`
	SessionHome     string `json:"session_home,omitempty"`
	// The home and projects root this session was leased from, which no other
	// field carries: original_cwd is the launch directory, not the root.
	CanonicalHome     string     `json:"canonical_home,omitempty"`
	CanonicalProjects string     `json:"canonical_projects,omitempty"`
	DeadSince         *time.Time `json:"dead_since,omitempty"`
	// Released is the session saying it is finished, which skips the grace a
	// crashed session needs. See docs/native-shadow.md.
	Released  *time.Time       `json:"released,omitempty"`
	Artifacts []nativeArtifact `json:"artifacts"`
}

// nativeLockOwner identifies the launch holding the startup lock, so a later
// launch can tell a live cleanup from an interrupted one.
type nativeLockOwner struct {
	PID          int       `json:"pid"`
	ProcessStart string    `json:"process_start"`
	Acquired     time.Time `json:"acquired"`
}

type nativeCandidate struct {
	Fingerprint string `json:"fingerprint"`
	Scans       int    `json:"scans"`
}

type nativeSweepState struct {
	Format     string                     `json:"format"`
	LastSweep  time.Time                  `json:"last_sweep"`
	Candidates map[string]nativeCandidate `json:"candidates"`
}

type nativeRepository struct {
	Owner string
	Name  string
	Path  string
}

type nativeWorktree struct {
	Path   string
	Branch string
}

type nativeLiveWorktrees struct {
	paths     map[string]struct{}
	branches  map[string]struct{}
	uncertain bool
}

type nativeRuntime struct {
	Now          time.Time
	PID          int
	ProcessStart string
	CWD          string
	Home         string
	ProjectsRoot string
	StateRoot    string
	SessionsRoot string
	PlanFile     string
	FleetFile    string
	// Role narrows projection only. docs/native-agent-workspaces.md
	Role   string
	Random io.Reader
	// Stderr is wrapped by the progress seam. docs/native-session-start.md
	Stderr io.Writer
	// Progress narrates startup. A nil value stays silent, which keeps tests
	// and embedded callers quiet without a flag.
	Progress *nativeProgress
	// ClaudeKeyring is injected by tests. The zero value falls back to the
	// platform keyring.
	ClaudeKeyring claudeKeyringReader
}

func (runtime nativeRuntime) claudeKeyring() claudeKeyringReader {
	if runtime.ClaudeKeyring != nil {
		return runtime.ClaudeKeyring
	}
	return readClaudeKeyring
}

func runNativeShadow(ctx context.Context, cmd *cli.Command) error {
	if cmd.Bool("probe") {
		return nil
	}
	if lifecycle, handled := nativeShadowLifecycleVerb(cmd); handled {
		runtime, err := resolveNativeRuntime()
		if err != nil {
			return err
		}
		runtime.Progress = newNativeProgress(io.Discard, nil)
		runtime.Stderr = os.Stderr
		return lifecycle(runtime)
	}
	command := argvAfterDash(os.Args)
	if len(command) == 0 {
		return fmt.Errorf("_native-shadow needs a command after `--`")
	}
	harness := strings.TrimSpace(cmd.String("harness"))
	if !isSupportedHarness(harness) {
		return fmt.Errorf(
			"_native-shadow has unsupported harness %q: want %s",
			harness,
			nativeHarnessList(),
		)
	}
	role := strings.TrimSpace(cmd.String("role"))
	if role != "" && !safeRoleSlug(role) {
		return fmt.Errorf("_native-shadow has unsafe role slug %q", role)
	}
	runtime, err := resolveNativeRuntime()
	if err != nil {
		return err
	}
	runtime.Role = role
	runtime.Progress.Begin(harness, command)
	// Before any session state exists, and after `--probe` returned, so a
	// metadata read never waits. teable:coilyco-bridge/infrastructure#7054
	if err := gateNativeUpdate(ctx, runtime, defaultNativeUpdateGate()); err != nil {
		return err
	}
	if err := convergeNativeEnvironment(ctx, runtime); err != nil {
		return fmt.Errorf("converge native environment: %w", err)
	}
	workspace, err := prepareNativeLaunchWorkspaceWithOptions(runtime, harness, nativeLaunchOptions{
		WorkspaceRoot: cmd.Bool("assigned-role"),
	})
	if err != nil {
		return err
	}
	launchCWD := workspace.CWD
	if err := os.Chdir(launchCWD); err != nil {
		return fmt.Errorf("enter native session workspace %s: %w", launchCWD, err)
	}
	_, isolated := relativeWithin(runtime.SessionsRoot, launchCWD)
	if err := protectNativeHarnessInstall(harness, isolated); err != nil {
		return err
	}
	if isolated && harness == "codex" {
		command = trustNativeCodexWorkspace(command, harness, nativeCodexProject(launchCWD))
	}
	if err := clearDeprecatedModelSelectors(); err != nil {
		return err
	}
	if cmd.Bool("assigned-role") {
		if harness == "codex" {
			codexHome := filepath.Join(workspace.SessionHome, ".codex")
			if err := os.Setenv("CODEX_HOME", codexHome); err != nil {
				return fmt.Errorf("set native Codex home: %w", err)
			}
			trusted, err := trustNativeCodexAttributionHook(
				ctx,
				launchCWD,
				runtime.Home,
				codexHome,
			)
			if err != nil {
				fmt.Fprintf(
					runtime.Stderr,
					"aos: warning: trust native Codex Git attribution hook: %v\n",
					err,
				)
			} else if trusted > 0 {
				fmt.Fprintf(
					runtime.Stderr,
					"aos: trusted %d native Codex Git attribution hook(s)\n",
					trusted,
				)
			}
		}
	}
	if cmd.Bool("assigned-role") && harness == "codex" {
		if err := projectNativeCodexTerminalTitle(ctx, command); err != nil {
			fmt.Fprintf(runtime.Stderr, "aos: warning: project native Codex terminal title: %v\n", err)
		}
	}
	runtime.Progress.Ready()
	runtime.Progress.Exec(command)
	return execNative(command)
}

func convergeNativeEnvironment(ctx context.Context, runtime nativeRuntime) error {
	step := runtime.Progress.Step("converge environment")
	result, err := convergeEnvironment(ctx, environmentConvergeOptions{
		Home: runtime.Home,
	})
	if err != nil {
		step.Fail(err)
		return err
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(runtime.Stderr, "aos: warning: %s\n", warning)
	}
	if !result.Configured {
		step.Done("not configured")
		return nil
	}
	step.Done("%d catalogues, %d MCP servers", result.Catalogues, result.MCPServers)
	return nil
}

func protectNativeHarnessInstall(harness string, isolated bool) error {
	if harness != "claude" || !isolated {
		return nil
	}
	if err := os.Setenv(claudeDisableAutoUpdaterEnv, "1"); err != nil {
		return fmt.Errorf("disable Claude auto-updater in native shadow: %w", err)
	}
	return nil
}

func nativeCodexProject(cwd string) string {
	if root, err := nativeGit(cwd, "rev-parse", "--show-toplevel"); err == nil &&
		filepath.IsAbs(root) {
		cwd = root
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		return resolved
	}
	return filepath.Clean(cwd)
}

func trustNativeCodexWorkspace(command []string, harness, project string) []string {
	if harness != "codex" || len(command) == 0 {
		return command
	}
	override := "projects={" + tomlBasicString(project) + "={trust_level=\"trusted\"}}"
	insert := func(index int) []string {
		trusted := make([]string, 0, len(command)+2)
		trusted = append(trusted, command[:index]...)
		trusted = append(trusted, "--config", override)
		return append(trusted, command[index:]...)
	}
	base := func(value string) string {
		return strings.TrimSuffix(filepath.Base(value), filepath.Ext(value))
	}
	if base(command[0]) == "codex" {
		return insert(1)
	}
	if base(command[0]) != "agent-compose" {
		return command
	}
	if len(command) >= 4 && command[1] == "launch" && command[3] == "codex" {
		return insert(4)
	}
	for index := 1; index+1 < len(command); index++ {
		if command[index] == "--" && base(command[index+1]) == "codex" {
			return insert(index + 2)
		}
	}
	return command
}

func clearDeprecatedModelSelectors() error {
	for _, variable := range []string{
		agentComposeModelTierEnv,
		agentComposeModelClassEnv,
	} {
		if err := os.Unsetenv(variable); err != nil {
			return fmt.Errorf("unset deprecated agent-compose selector %s: %w", variable, err)
		}
	}
	return nil
}

func resolveNativeRuntime() (nativeRuntime, error) {
	cwd, err := filepath.Abs(".")
	if err != nil {
		return nativeRuntime{}, fmt.Errorf("resolve native launch directory: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nativeRuntime{}, fmt.Errorf("resolve native home: %w", err)
	}
	projects := strings.TrimSpace(os.Getenv("PROJECTS_ROOT"))
	if projects == "" {
		projects = filepath.Join(home, "projects")
	}
	projects, err = filepath.Abs(projects)
	if err != nil {
		return nativeRuntime{}, fmt.Errorf("resolve projects root: %w", err)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return nativeRuntime{}, fmt.Errorf("resolve native cache: %w", err)
	}
	stateRoot := strings.TrimSpace(os.Getenv("AOS_NATIVE_STATE_DIR"))
	if stateRoot == "" {
		stateRoot = filepath.Join(cache, "agentic-os", "native-shadow")
	}
	sessionsRoot := strings.TrimSpace(os.Getenv("AOS_NATIVE_SESSIONS_DIR"))
	if sessionsRoot == "" {
		sessionsRoot = filepath.Join(ensureAOSTempAlias(), "native")
	}
	config := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if config == "" {
		config = filepath.Join(home, ".config")
	}
	plan := strings.TrimSpace(os.Getenv("AOS_REPOSITORY_PLAN"))
	if plan == "" {
		plan = repositoryPlanPath(home)
	}
	fleet := strings.TrimSpace(os.Getenv("AOS_FLEET_ORGS"))
	if fleet == "" {
		fleet = filepath.Join(config, "agentic-os", "fleet-orgs.txt")
	}
	processStart, err := processStartIdentity(os.Getpid())
	if err != nil {
		return nativeRuntime{}, fmt.Errorf("identify native launch process: %w", err)
	}
	progress := newNativeProgress(os.Stderr, nil)
	return nativeRuntime{
		Now:          time.Now().UTC(),
		PID:          os.Getpid(),
		ProcessStart: processStart,
		CWD:          cwd,
		Home:         home,
		ProjectsRoot: projects,
		StateRoot:    stateRoot,
		SessionsRoot: sessionsRoot,
		PlanFile:     plan,
		FleetFile:    fleet,
		Random:       rand.Reader,
		Stderr:       progress.Writer(os.Stderr),
		Progress:     progress,
	}, nil
}

type nativeLaunchOptions struct {
	WorkspaceRoot  bool
	StandaloneHome bool
}

type nativeLaunchWorkspace struct {
	CWD             string
	SessionProjects string
	SessionHome     string
}

func prepareNativeLaunch(runtime nativeRuntime, harness string) (string, error) {
	return prepareNativeLaunchWithOptions(runtime, harness, nativeLaunchOptions{})
}

func prepareNativeLaunchWithOptions(
	runtime nativeRuntime,
	harness string,
	options nativeLaunchOptions,
) (string, error) {
	workspace, err := prepareNativeLaunchWorkspaceWithOptions(runtime, harness, options)
	if err != nil {
		return "", err
	}
	return workspace.CWD, nil
}

func prepareNativeLaunchWorkspaceWithOptions(
	runtime nativeRuntime,
	harness string,
	options nativeLaunchOptions,
) (nativeLaunchWorkspace, error) {
	if err := os.MkdirAll(runtime.StateRoot, 0o700); err != nil {
		return nativeLaunchWorkspace{}, fmt.Errorf("create native state root: %w", err)
	}
	var workspace nativeLaunchWorkspace
	err := withNativeStartupLock(runtime, func() error {
		reclaim := runtime.Progress.Step("reclaim finished sessions")
		live, err := cleanDeadNativeSessions(runtime)
		if err != nil {
			reclaim.Fail(err)
			return err
		}
		reclaim.Done("%d live worktrees", len(live.paths))
		resolve := runtime.Progress.Step("resolve resident repositories")
		projection, err := resolveExpectedRepositories(runtime)
		if err != nil {
			resolve.Fail(err)
			return err
		}
		resolve.Done("%d resident, %d projected",
			len(projection.Resident), len(projection.Projected))
		due, state := nativeSweepDue(runtime)
		if due {
			if err := runNativeWorkspaceSweep(
				runtime, projection.Resident, projection.Expected, live, state); err != nil {
				return err
			}
		} else {
			runtime.Progress.Skip("fleet pass", "last pass %s ago, interval %s",
				formatNativeDuration(runtime.Now.Sub(state.LastSweep)), nativeSweepInterval)
		}
		workspace, err = createNativeSession(
			runtime,
			harness,
			projection.Projected,
			options,
		)
		return err
	})
	if err != nil {
		return nativeLaunchWorkspace{}, err
	}
	if workspace.CWD == "" {
		workspace.CWD = runtime.CWD
	}
	return workspace, nil
}

// withNativeStartupLock serializes startup cleanup. The lock names its owner,
// so an interrupted launch is reclaimed at once. docs/native-session-start.md
func withNativeStartupLock(runtime nativeRuntime, action func() error) error {
	lock := filepath.Join(runtime.StateRoot, "startup.lock")
	began := time.Now()
	announced := time.Time{}
	for {
		err := os.Mkdir(lock, 0o700)
		if err == nil {
			defer os.RemoveAll(lock)
			if err := writeNativeJSON(nativeLockOwnerPath(lock), nativeLockOwner{
				PID:          runtime.PID,
				ProcessStart: runtime.ProcessStart,
				Acquired:     time.Now().UTC(),
			}); err != nil {
				return fmt.Errorf("claim native startup lock: %w", err)
			}
			return action()
		}
		if !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("acquire native startup lock: %w", err)
		}
		holder, live := inspectNativeStartupLock(lock)
		if !live {
			runtime.Progress.Wait("reclaiming startup lock abandoned by pid %d", holder.PID)
			if err := os.RemoveAll(lock); err != nil {
				return fmt.Errorf("reclaim abandoned native startup lock %s: %w", lock, err)
			}
			continue
		}
		waited := time.Since(began)
		if waited >= nativeLockWait {
			return fmt.Errorf(
				"native startup pid %d has held %s for %s; wait for that launch or remove the lock directory",
				holder.PID, lock, formatNativeDuration(waited))
		}
		if time.Since(announced) >= nativeLockNotice {
			announced = time.Now()
			runtime.Progress.Wait("native startup pid %d holds the lock, waited %s",
				holder.PID, formatNativeDuration(waited))
		}
		time.Sleep(nativeLockPoll)
	}
}

func nativeLockOwnerPath(lock string) string {
	return filepath.Join(lock, "owner.json")
}

// inspectNativeStartupLock reports the lock's owner and whether it still runs.
// A missing owner file is trusted only inside the creator's write window.
func inspectNativeStartupLock(lock string) (nativeLockOwner, bool) {
	var holder nativeLockOwner
	if err := readNativeJSON(nativeLockOwnerPath(lock), &holder); err != nil {
		info, statErr := os.Stat(lock)
		return holder, statErr == nil && time.Since(info.ModTime()) < nativeLockGrace
	}
	if holder.PID <= 0 || holder.ProcessStart == "" {
		return holder, false
	}
	identity, err := processStartIdentity(holder.PID)
	return holder, err == nil && identity == holder.ProcessStart
}

func nativeStatePath(runtime nativeRuntime, parts ...string) string {
	return filepath.Join(append([]string{runtime.StateRoot}, parts...)...)
}

func nativePathKey(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("native path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve absolute native path %s: %w", path, err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", errNativePathAbsent
		}
		return "", fmt.Errorf("resolve native path %s: %w", path, err)
	}
	return filepath.Clean(resolved), nil
}

// errNativePathAbsent separates "this path is gone" from "this path could not
// be read". Absence is an answer; only the second is uncertainty.
var errNativePathAbsent = errors.New("native path does not exist")

func (live *nativeLiveWorktrees) add(path string) {
	key, err := nativePathKey(path)
	if err != nil {
		// Absence is an answer, not uncertainty. docs/native-session-start.md
		if errors.Is(err, errNativePathAbsent) {
			return
		}
		live.uncertain = true
		return
	}
	if live.paths == nil {
		live.paths = map[string]struct{}{}
	}
	live.paths[key] = struct{}{}
}

func (live *nativeLiveWorktrees) addArtifacts(artifacts []nativeArtifact) {
	for _, artifact := range artifacts {
		live.add(artifact.Worktree)
		if artifact.Branch == "" {
			continue
		}
		if live.branches == nil {
			live.branches = map[string]struct{}{}
		}
		live.branches[artifact.Branch] = struct{}{}
	}
}

// holdsBranch reports a branch some lease still claims, purged worktree
// included. Uncertainty answers yes, since the cost of keeping is a stale ref.
func (live nativeLiveWorktrees) holdsBranch(branch string) bool {
	if live.uncertain {
		return true
	}
	_, found := live.branches[branch]
	return found
}

func (live nativeLiveWorktrees) contains(path string) bool {
	if live.uncertain {
		return true
	}
	if len(live.paths) == 0 {
		return false
	}
	key, err := nativePathKey(path)
	if err != nil {
		// A path that is gone cannot be a live worktree, so say so rather than
		// shielding the very purged worktrees the caller wants released.
		return !errors.Is(err, errNativePathAbsent)
	}
	_, found := live.paths[key]
	return found
}

func readNativeJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func writeNativeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".native-state-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}

func nativeGit(directory string, args ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git -C %s %s: %w: %s",
			directory, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

// harvestNativeClaudeLease recovers a finished session's rotated credential.
// Failure warns, never blocks. docs/native-claude-credentials.md.
func harvestNativeClaudeLease(runtime nativeRuntime, lease *nativeLease) bool {
	reclaimed, err := reclaimSessionClaudeCredential(lease.SessionHome, runtime.Home)
	if err != nil {
		fmt.Fprintf(runtime.Stderr,
			"aos: native session Claude login not returned to the host: %v\n", err)
		return false
	}
	if reclaimed {
		return true
	}
	// The harness deletes the staged link when it cannot refresh the token, and
	// the rotation then lives only in the session Keychain item.
	harvested, err := harvestSessionClaudeKeychain(
		context.Background(), runtime.claudeKeyring(), lease.SessionHome, runtime.Home)
	if err != nil {
		fmt.Fprintf(runtime.Stderr,
			"aos: native session Claude login not harvested from the keychain: %v\n", err)
		return false
	}
	return harvested
}

// nativeHeldLease pairs a lease with its file so startup can read every lease
// once, resolve all owning processes in one query, then decide.
type nativeHeldLease struct {
	path  string
	lease nativeLease
}

func cleanDeadNativeSessions(runtime nativeRuntime) (nativeLiveWorktrees, error) {
	leaseDir := nativeStatePath(runtime, "leases")
	entries, err := os.ReadDir(leaseDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nativeLiveWorktrees{}, nil
	}
	if err != nil {
		return nativeLiveWorktrees{}, fmt.Errorf("read native leases: %w", err)
	}
	live := nativeLiveWorktrees{}
	released := 0
	stuck := []nativeStuckArtifact{}
	runtime.Progress.Note("%d lease(s) on disk", len(entries))
	held := make([]nativeHeldLease, 0, len(entries))
	pids := make([]int, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(leaseDir, entry.Name())
		var lease nativeLease
		if err := readNativeJSON(path, &lease); err != nil {
			live.uncertain = true
			fmt.Fprintf(runtime.Stderr, "aos: preserving unreadable native lease %s\n", path)
			continue
		}
		held = append(held, nativeHeldLease{path: path, lease: lease})
		pids = append(pids, lease.PID)
	}
	probe := probeNativeProcesses(pids)
	for _, entry := range held {
		path := entry.path
		lease := entry.lease
		if probe.leaseIsLive(lease) {
			if lease.DeadSince != nil {
				lease.DeadSince = nil
				if err := writeNativeJSON(path, lease); err != nil {
					return nativeLiveWorktrees{}, fmt.Errorf("restore live native lease: %w", err)
				}
			}
			live.addArtifacts(lease.Artifacts)
			continue
		}
		// A dead session's token is harvested at once rather than after the
		// worktree grace, because the next launch needs the refreshed value.
		if harvested := harvestNativeClaudeLease(runtime, &lease); harvested {
			if err := writeNativeJSON(path, lease); err != nil {
				return nativeLiveWorktrees{}, fmt.Errorf("record harvested native lease: %w", err)
			}
		}
		if lease.DeadSince == nil {
			deadSince := runtime.Now
			lease.DeadSince = &deadSince
			if err := writeNativeJSON(path, lease); err != nil {
				return nativeLiveWorktrees{}, fmt.Errorf("start dead native lease grace: %w", err)
			}
			// A session that said it was finished needs no second reading to
			// confirm what it already declared. agentic-os#1260
			if lease.Released == nil {
				live.addArtifacts(lease.Artifacts)
				continue
			}
		}
		// A second dead reading confirms the first, so a worktree holding no
		// local-only state is released now. docs/native-session-start.md
		expired := lease.Released != nil ||
			!runtime.Now.Before(lease.DeadSince.Add(nativeDeadSessionGrace))
		remaining := make([]nativeArtifact, 0, len(lease.Artifacts))
		for _, artifact := range lease.Artifacts {
			cleaned, err := cleanNativeArtifact(artifact)
			if err != nil {
				fmt.Fprintf(runtime.Stderr, "aos: preserving %s: %v\n", artifact.Worktree, err)
				remaining = append(remaining, artifact)
				continue
			}
			if !cleaned {
				remaining = append(remaining, artifact)
				if expired {
					if item, ok := nativeStuckReading(lease.ID, artifact); ok {
						stuck = append(stuck, item)
					}
				}
			}
		}
		if len(remaining) < len(lease.Artifacts) {
			runtime.Progress.Item("release", released+1, len(held),
				"session %s (%d of %d worktrees)", lease.ID,
				len(lease.Artifacts)-len(remaining), len(lease.Artifacts))
			released++
		}
		live.addArtifacts(remaining)
		if len(remaining) > 0 || !expired {
			if expired {
				lease.PID = 0
				lease.ProcessStart = ""
			}
			lease.Artifacts = remaining
			if err := writeNativeJSON(path, lease); err != nil {
				return nativeLiveWorktrees{}, fmt.Errorf("update preserved native lease: %w", err)
			}
			continue
		}
		_ = os.RemoveAll(lease.SessionRoot)
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nativeLiveWorktrees{}, fmt.Errorf("remove native lease: %w", err)
		}
	}
	reportNativeStuckLeases(runtime, stuck)
	return live, nil
}

// A purged worktree whose branch holds local-only commits is preserved forever,
// correctly. Silence about it is the defect. docs/native-session-start.md
type nativeStuckArtifact struct {
	session  string
	artifact nativeArtifact
	unpushed int
}

// Stuck only once the worktree is gone. One still on disk is work a later pass
// can release, which is a different and recoverable state.
func nativeStuckReading(session string, artifact nativeArtifact) (nativeStuckArtifact, bool) {
	if _, err := os.Stat(artifact.Worktree); !errors.Is(err, fs.ErrNotExist) {
		return nativeStuckArtifact{}, false
	}
	if artifact.Branch == "" || artifact.Branch == "main" {
		return nativeStuckArtifact{}, false
	}
	count, err := nativeGit(artifact.Repository, "rev-list", "--count",
		"refs/heads/"+artifact.Branch, "--not", "--remotes=origin")
	if err != nil {
		return nativeStuckArtifact{}, false
	}
	unpushed, err := strconv.Atoi(strings.TrimSpace(count))
	if err != nil || unpushed == 0 {
		return nativeStuckArtifact{}, false
	}
	return nativeStuckArtifact{session: session, artifact: artifact, unpushed: unpushed}, true
}

// One line: the branches are named so an operator acts without opening leases.
func reportNativeStuckLeases(runtime nativeRuntime, stuck []nativeStuckArtifact) {
	if len(stuck) == 0 {
		return
	}
	commits := 0
	repositories := map[string]bool{}
	names := make([]string, 0, len(stuck))
	for _, item := range stuck {
		commits += item.unpushed
		repositories[filepath.Base(item.artifact.Repository)] = true
		names = append(names, filepath.Base(item.artifact.Repository)+" "+item.artifact.Branch)
	}
	sort.Strings(names)
	scope := fmt.Sprintf("%d repositories", len(repositories))
	if len(repositories) == 1 {
		scope = "1 repository"
	}
	fmt.Fprintf(runtime.Stderr,
		"aos: %d dead session branch(es) hold %d unpushed commit(s) in %s and "+
			"nothing will release them: %s. Push or delete each branch.\n",
		len(stuck), commits, scope, strings.Join(names, ", "))
}

func nativeLeaseIsLive(lease nativeLease) bool {
	return nativeProcessProbe{}.leaseIsLive(lease)
}

// nativeProcessProbe answers liveness for a whole batch of leases. When batched
// is set, an absent PID is proof the process is gone, not a reason to re-ask.
type nativeProcessProbe struct {
	identities map[int]string
	batched    bool
}

// probeNativeProcesses resolves every PID once. A failed batch degrades to the
// per-PID path rather than reporting live sessions as dead.
func probeNativeProcesses(pids []int) nativeProcessProbe {
	if len(pids) == 0 {
		return nativeProcessProbe{identities: map[int]string{}, batched: true}
	}
	identities, err := processStartIdentities(pids)
	if err != nil {
		return nativeProcessProbe{}
	}
	return nativeProcessProbe{identities: identities, batched: true}
}

func (probe nativeProcessProbe) leaseIsLive(lease nativeLease) bool {
	if lease.PID <= 0 || lease.ProcessStart == "" {
		return false
	}
	if probe.batched {
		return probe.identities[lease.PID] == lease.ProcessStart
	}
	identity, err := processStartIdentity(lease.PID)
	return err == nil && identity == lease.ProcessStart
}

// batchProcessPIDs keeps one query's argument list well inside any platform
// limit while still collapsing a normal lease population into a single call.
func batchProcessPIDs(pids []int) [][]int {
	const size = 256
	unique := make([]int, 0, len(pids))
	seen := map[int]bool{}
	for _, pid := range pids {
		if pid > 0 && !seen[pid] {
			seen[pid] = true
			unique = append(unique, pid)
		}
	}
	var batches [][]int
	for start := 0; start < len(unique); start += size {
		end := min(start+size, len(unique))
		batches = append(batches, unique[start:end])
	}
	return batches
}

// reconcileNativeArtifactBranch trusts the worktree over the lease, which may
// name a branch a mid-session switch already removed. Detached yields "".
func reconcileNativeArtifactBranch(artifact nativeArtifact) string {
	actual, err := nativeGit(artifact.Worktree, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil {
		return ""
	}
	return actual
}

func cleanNativeArtifact(artifact nativeArtifact) (bool, error) {
	if _, err := os.Stat(artifact.Worktree); errors.Is(err, fs.ErrNotExist) {
		_, _ = nativeGit(artifact.Repository, "worktree", "prune")
		return deleteNativeBranchIfRemote(artifact.Repository, artifact.Branch)
	}
	if humanWorkdir(artifact.Worktree) {
		return false, fmt.Errorf("human workdir is outside automation")
	}
	artifact.Branch = reconcileNativeArtifactBranch(artifact)
	clean, err := nativeWorktreeClean(artifact.Worktree, false)
	if err != nil || !clean {
		return false, err
	}
	safe, err := nativeHeadIsRemote(artifact.Worktree)
	if err != nil || !safe {
		return false, err
	}
	if _, err := nativeGit(artifact.Repository, "worktree", "remove", artifact.Worktree); err != nil {
		return false, err
	}
	return deleteNativeBranchIfRemote(artifact.Repository, artifact.Branch)
}

func nativeWorktreeClean(path string, includeIgnored bool) (bool, error) {
	status, err := nativeGit(path, "status", "--porcelain=v2", "--untracked-files=all")
	if err != nil || status != "" {
		return false, err
	}
	if !includeIgnored {
		return true, nil
	}
	ignored, err := nativeGit(path, "ls-files", "--others", "--ignored", "--exclude-standard")
	return ignored == "", err
}

func nativeHeadIsRemote(path string) (bool, error) {
	output, err := nativeGit(path, "rev-list", "HEAD", "--not", "--remotes=origin")
	return output == "", err
}

func deleteNativeBranchIfRemote(repository, branch string) (bool, error) {
	if branch == "" || branch == "main" {
		return true, nil
	}
	if _, err := nativeGit(repository, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		return true, nil
	}
	output, err := nativeGit(repository,
		"rev-list", "refs/heads/"+branch, "--not", "--remotes=origin")
	if err != nil {
		return false, err
	}
	if output != "" && !nativeBranchLanded(repository, branch) {
		return false, nil
	}
	if _, err := nativeGit(repository, "branch", "-D", branch); err != nil {
		return false, err
	}
	return true, nil
}

func nativeSweepDue(runtime nativeRuntime) (bool, nativeSweepState) {
	state := nativeSweepState{
		Format:     "agentic-os.native-sweep.v1",
		Candidates: map[string]nativeCandidate{},
	}
	if err := readNativeJSON(nativeStatePath(runtime, "sweep.json"), &state); err != nil {
		return true, state
	}
	if state.Candidates == nil {
		state.Candidates = map[string]nativeCandidate{}
	}
	return runtime.Now.Sub(state.LastSweep) >= nativeSweepInterval, state
}

func runNativeWorkspaceSweep(
	runtime nativeRuntime,
	repositories []nativeRepository,
	expected nativeExpected,
	live nativeLiveWorktrees,
	state nativeSweepState,
) error {
	pass := runtime.Progress.Step("fleet pass over %d repositories", len(repositories))
	for index, repository := range repositories {
		identity := repository.Owner + "/" + repository.Name
		runtime.Progress.Item("fetch", index+1, len(repositories), "%s", identity)
		began := time.Now()
		_, err := nativeGit(repository.Path, "fetch", "--prune", "origin")
		pass.Track(identity, time.Since(began))
		if err != nil {
			fmt.Fprintf(runtime.Stderr, "aos: fetch skipped for %s: %v\n", identity, err)
			continue
		}
		if err := normalizeNativeRepository(runtime, repository, live); err != nil {
			fmt.Fprintf(runtime.Stderr, "aos: normalization skipped for %s: %v\n",
				repository.Path, err)
		}
	}
	pass.Done("")
	reportNativeResidentDrift(runtime, repositories, live)
	reportNativeOrphanBranches(runtime, repositories, live)
	scan := runtime.Progress.Step("scan for unexpected clones")
	// Counters must not advance either, or three unverified scans delete on the
	// fourth exactly as three verified ones do.
	if !expected.Authoritative {
		scan.Done("skipped, no verified repository plan")
		fmt.Fprintf(runtime.Stderr,
			"aos: no repository plan at %s, so unexpected-clone cleanup is skipped\n",
			runtime.PlanFile)
		return nil
	}
	next := map[string]nativeCandidate{}
	for _, repository := range scanNativeRepositories(runtime.ProjectsRoot) {
		if expected.matches(repository.Owner, repository.Name) {
			continue
		}
		eligible, fingerprint := unexpectedCloneEligible(runtime, repository, live, expected.FleetOrgs)
		if !eligible {
			continue
		}
		candidate := state.Candidates[repository.Path]
		if candidate.Fingerprint == fingerprint {
			candidate.Scans++
		} else {
			candidate = nativeCandidate{Fingerprint: fingerprint, Scans: 1}
		}
		if candidate.Scans >= nativeDeleteScans {
			if err := os.RemoveAll(repository.Path); err != nil {
				err = fmt.Errorf("remove unexpected clone %s: %w", repository.Path, err)
				scan.Fail(err)
				return err
			}
			fmt.Fprintf(runtime.Stderr, "aos: removed unexpected clone %s after three startup scans\n",
				repository.Path)
			continue
		}
		next[repository.Path] = candidate
		fmt.Fprintf(runtime.Stderr, "aos: unexpected clone %s eligible for cleanup (%d/3)\n",
			repository.Path, candidate.Scans)
	}
	scan.Done("%d candidate(s)", len(next))
	state.LastSweep = runtime.Now
	state.Candidates = next
	if err := writeNativeJSON(nativeStatePath(runtime, "sweep.json"), state); err != nil {
		return fmt.Errorf("write native sweep state: %w", err)
	}
	return nil
}

// nativeBehindOrigin counts commits the checkout is missing, or 0 when there is
// no upstream to compare against.
func nativeBehindOrigin(path, branch string) int {
	output, err := nativeGit(path, "rev-list", "--count",
		"HEAD.."+"origin/"+branch)
	if err != nil {
		return 0
	}
	behind, err := strconv.Atoi(strings.TrimSpace(output))
	if err != nil {
		return 0
	}
	return behind
}

// Normalization leaves exactly these cases alone, and they change what a tool
// reading the checkout composes. docs/native-session-start.md
type nativeResidentDrift struct {
	path    string
	branch  string
	reasons []string
}

// readNativeResidentDrift reports only what normalization could not correct. A
// detached HEAD is deliberate here, since that is how a shadow releases main.
func readNativeResidentDrift(repository nativeRepository) (nativeResidentDrift, bool) {
	if humanWorkdir(repository.Path) {
		return nativeResidentDrift{}, false
	}
	branch, err := nativeGit(repository.Path, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil || branch == "" {
		return nativeResidentDrift{}, false
	}
	drift := nativeResidentDrift{path: repository.Path, branch: branch}
	if clean, err := nativeWorktreeClean(repository.Path, false); err == nil && !clean {
		drift.reasons = append(drift.reasons, "dirty")
	}
	// One untracked file stops normalization, and the checkout then falls behind
	// silently. "dirty" alone understates 421 commits. docs/native-session-start.md
	if behind := nativeBehindOrigin(repository.Path, branch); behind > 0 {
		drift.reasons = append(drift.reasons, fmt.Sprintf("%d behind origin", behind))
	}
	if safe, err := nativeHeadIsRemote(repository.Path); err == nil && !safe {
		drift.reasons = append(drift.reasons, "unpushed")
	}
	if branch == "main" && len(drift.reasons) == 0 {
		return nativeResidentDrift{}, false
	}
	return drift, true
}

// One line, matching the dead-lease report: drift is only actionable once
// someone can see all of it at once.
func reportNativeResidentDrift(
	runtime nativeRuntime,
	repositories []nativeRepository,
	live nativeLiveWorktrees,
) {
	readings := make([]string, 0, len(repositories))
	for _, repository := range repositories {
		if live.contains(repository.Path) {
			continue
		}
		drift, ok := readNativeResidentDrift(repository)
		if !ok {
			continue
		}
		reading := filepath.Base(drift.path) + " on " + drift.branch
		if len(drift.reasons) > 0 {
			reading += " (" + strings.Join(drift.reasons, ", ") + ")"
		}
		readings = append(readings, reading)
	}
	if len(readings) == 0 {
		return
	}
	sort.Strings(readings)
	fmt.Fprintf(runtime.Stderr,
		"aos: %d resident checkout(s) are not clean on main, so a tool reading one "+
			"composes something other than origin: %s\n",
		len(readings), strings.Join(readings, ", "))
}

// A branch no lease recorded is reported by nothing else.
// docs/native-session-start.md
type nativeOrphanBranch struct {
	repository string
	branch     string
	unlanded   int
}

// nativeCheckedOutBranches names the branches this repository's worktrees hold.
func nativeCheckedOutBranches(path string) map[string]struct{} {
	held := map[string]struct{}{}
	listed, err := nativeGit(path, "worktree", "list", "--porcelain")
	if err != nil {
		return held
	}
	for _, line := range strings.Split(listed, "\n") {
		reference, found := strings.CutPrefix(strings.TrimSpace(line), "branch ")
		if found {
			held[strings.TrimPrefix(reference, "refs/heads/")] = struct{}{}
		}
	}
	return held
}

// Patch-id, not reachability: a squashed branch is reachable from no origin ref
// while holding no work. docs/native-session-start.md
func nativeUnlandedCount(path, branch string) int {
	ahead, err := nativeGit(path, "rev-list", "--count",
		"refs/heads/"+branch, "--not", "--remotes=origin")
	if err != nil {
		return 0
	}
	if count, err := strconv.Atoi(strings.TrimSpace(ahead)); err != nil || count == 0 {
		return 0
	}
	listed, err := nativeGit(path, "cherry", "origin/main", "refs/heads/"+branch)
	if err != nil {
		return 0
	}
	unlanded := 0
	for _, line := range strings.Split(listed, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "+") {
			unlanded++
		}
	}
	return unlanded
}

// readNativeOrphanBranches lists local branches carrying work origin does not.
func readNativeOrphanBranches(
	repository nativeRepository,
	live nativeLiveWorktrees,
) []nativeOrphanBranch {
	if humanWorkdir(repository.Path) {
		return nil
	}
	listed, err := nativeGit(repository.Path, "for-each-ref",
		"--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil
	}
	held := nativeCheckedOutBranches(repository.Path)
	orphans := []nativeOrphanBranch{}
	for _, line := range strings.Split(listed, "\n") {
		branch := strings.TrimSpace(line)
		if branch == "" || branch == "main" || live.holdsBranch(branch) {
			continue
		}
		if _, found := held[branch]; found {
			continue
		}
		// Origin carrying it is what "released" means here.
		pushed, err := nativeGitRefExists(repository.Path, "refs/remotes/origin/"+branch)
		if err != nil || pushed {
			continue
		}
		unlanded := nativeUnlandedCount(repository.Path, branch)
		if unlanded == 0 {
			continue
		}
		orphans = append(orphans, nativeOrphanBranch{
			repository: filepath.Base(repository.Path),
			branch:     branch,
			unlanded:   unlanded,
		})
	}
	return orphans
}

// One line, matching the two readings beside it.
func reportNativeOrphanBranches(
	runtime nativeRuntime,
	repositories []nativeRepository,
	live nativeLiveWorktrees,
) {
	orphans := []nativeOrphanBranch{}
	for _, repository := range repositories {
		if live.contains(repository.Path) {
			continue
		}
		orphans = append(orphans, readNativeOrphanBranches(repository, live)...)
	}
	if len(orphans) == 0 {
		return
	}
	commits := 0
	names := make([]string, 0, len(orphans))
	for _, orphan := range orphans {
		commits += orphan.unlanded
		names = append(names, orphan.repository+" "+orphan.branch)
	}
	sort.Strings(names)
	fmt.Fprintf(runtime.Stderr,
		"aos: %d untracked local branch(es) hold %d unlanded commit(s) that no "+
			"session lease will report: %s. Push or delete each branch.\n",
		len(orphans), commits, strings.Join(names, ", "))
}

func normalizeNativeRepository(
	runtime nativeRuntime,
	repository nativeRepository,
	live nativeLiveWorktrees,
) error {
	if live.contains(repository.Path) || humanWorkdir(repository.Path) {
		return nil
	}
	if inProgress, err := nativeGitOperationInProgress(repository.Path); err != nil || inProgress {
		return err
	}
	clean, err := nativeWorktreeClean(repository.Path, false)
	if err != nil || !clean {
		return err
	}
	branch, err := nativeGit(repository.Path, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil {
		return err
	}
	if branch != "main" {
		safe, err := nativeHeadIsRemote(repository.Path)
		if err != nil || !safe {
			return err
		}
		if err := cleanUnleasedNativeWorktrees(runtime, repository, live); err != nil {
			return err
		}
		if err := releaseNativeDefaultBranch(runtime, repository); err != nil {
			return err
		}
		if _, err := nativeGit(repository.Path, "switch", "main"); err != nil {
			return err
		}
		if _, err := nativeGit(repository.Path, "branch", "-D", branch); err != nil {
			return err
		}
		fmt.Fprintf(runtime.Stderr, "aos: returned %s to main and removed local branch %s\n",
			repository.Path, branch)
	}
	if err := repairNativeMainUpstream(runtime, repository); err != nil {
		return err
	}
	if _, err := nativeGit(repository.Path, "merge", "--ff-only", "origin/main"); err != nil {
		return err
	}
	if branch == "main" {
		if err := cleanUnleasedNativeWorktrees(runtime, repository, live); err != nil {
			return err
		}
	}
	return reapNativeMergedBranches(runtime, repository, live)
}

// reapNativeMergedBranches deletes local branches already wholly on origin.
// Rationale and the safety test: docs/native-session-start.md
func reapNativeMergedBranches(
	runtime nativeRuntime,
	repository nativeRepository,
	live nativeLiveWorktrees,
) error {
	worktrees, err := listNativeWorktrees(repository.Path)
	if err != nil {
		return err
	}
	held := map[string]bool{}
	for _, worktree := range worktrees {
		if worktree.Branch != "" {
			held[worktree.Branch] = true
		}
	}
	listing, err := nativeGit(repository.Path,
		"for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if err != nil {
		return err
	}
	reaped := 0
	for _, branch := range strings.Split(listing, "\n") {
		branch = strings.TrimSpace(branch)
		if branch == "" || branch == "main" || held[branch] {
			continue
		}
		// Session-ID uniqueness reads these refs, so one goes only once no lease
		// and no worktree still names it. See docs/native-shadow.md.
		if strings.HasPrefix(branch, "aos/") && live.holdsBranch(branch) {
			continue
		}
		cleaned, err := deleteNativeBranchIfRemote(repository.Path, branch)
		if err != nil {
			fmt.Fprintf(runtime.Stderr, "aos: preserving branch %s: %v\n", branch, err)
			continue
		}
		if cleaned {
			reaped++
		}
	}
	if reaped > 0 {
		fmt.Fprintf(runtime.Stderr, "aos: reaped %d fully-pushed branch(es) in %s\n",
			reaped, repository.Path)
	}
	return nil
}

// repairNativeMainUpstream puts `main` back on `origin/main` after a squatting
// worktree repointed it. docs/native-session-start.md
func repairNativeMainUpstream(runtime nativeRuntime, repository nativeRepository) error {
	merge, err := nativeGit(repository.Path, "config", "--get", "branch.main.merge")
	if err != nil || merge == "refs/heads/main" {
		// A missing key exits non-zero. Absent tracking is not corruption.
		return nil
	}
	if _, err := nativeGit(repository.Path,
		"branch", "--set-upstream-to=origin/main", "main"); err != nil {
		return fmt.Errorf("repair main upstream in %s: %w", repository.Path, err)
	}
	fmt.Fprintf(runtime.Stderr,
		"aos: repaired %s main upstream, was tracking %s\n", repository.Path, merge)
	return nil
}

// releaseNativeDefaultBranch detaches a worktree squatting on `main`, live ones
// included. Detaching at HEAD leaves files alone. docs/native-session-start.md
func releaseNativeDefaultBranch(runtime nativeRuntime, repository nativeRepository) error {
	worktrees, err := listNativeWorktrees(repository.Path)
	if err != nil {
		return err
	}
	for _, worktree := range worktrees {
		if worktree.Branch != "main" || samePath(worktree.Path, repository.Path) {
			continue
		}
		if humanWorkdir(worktree.Path) {
			continue
		}
		if _, err := nativeGit(worktree.Path, "switch", "--detach"); err != nil {
			return fmt.Errorf("detach %s from main: %w", worktree.Path, err)
		}
		fmt.Fprintf(runtime.Stderr,
			"aos: detached worktree %s from main so %s can hold it\n",
			worktree.Path, repository.Path)
	}
	return nil
}

func cleanUnleasedNativeWorktrees(
	runtime nativeRuntime,
	repository nativeRepository,
	live nativeLiveWorktrees,
) error {
	worktrees, err := listNativeWorktrees(repository.Path)
	if err != nil {
		return err
	}
	for _, worktree := range worktrees {
		if samePath(worktree.Path, repository.Path) || live.contains(worktree.Path) ||
			humanWorkdir(worktree.Path) {
			continue
		}
		artifact := nativeArtifact{
			Repository: repository.Path,
			Worktree:   worktree.Path,
			Branch:     worktree.Branch,
		}
		cleaned, err := cleanNativeArtifact(artifact)
		if err != nil {
			fmt.Fprintf(runtime.Stderr, "aos: preserving worktree %s: %v\n", worktree.Path, err)
			continue
		}
		if cleaned {
			fmt.Fprintf(runtime.Stderr, "aos: removed inactive worktree %s\n", worktree.Path)
		}
	}
	return nil
}

func nativeGitOperationInProgress(path string) (bool, error) {
	for _, name := range []string{
		"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG",
		"rebase-apply", "rebase-merge", "sequencer",
	} {
		gitPath, err := nativeGit(path, "rev-parse", "--git-path", name)
		if err != nil {
			return false, err
		}
		if !filepath.IsAbs(gitPath) {
			gitPath = filepath.Join(path, gitPath)
		}
		if _, err := os.Stat(gitPath); err == nil {
			return true, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}

func listNativeWorktrees(repository string) ([]nativeWorktree, error) {
	output, err := nativeGit(repository, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var result []nativeWorktree
	current := nativeWorktree{}
	flush := func() {
		if current.Path != "" {
			result = append(result, current)
		}
		current = nativeWorktree{}
	}
	for _, line := range strings.Split(output, "\n") {
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			current.Path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch refs/heads/"):
			current.Branch = strings.TrimPrefix(line, "branch refs/heads/")
		}
	}
	flush()
	return result, nil
}

func unexpectedCloneEligible(
	runtime nativeRuntime,
	repository nativeRepository,
	live nativeLiveWorktrees,
	fleetOrgs map[string]bool,
) (bool, string) {
	if !fleetOrgs[repository.Owner] || live.contains(repository.Path) ||
		humanWorkdir(repository.Path) {
		return false, ""
	}
	if _, err := nativeGit(repository.Path, "fetch", "--prune", "origin"); err != nil {
		return false, ""
	}
	origin, err := nativeGit(repository.Path, "remote", "get-url", "origin")
	if err != nil || originOwner(origin) != repository.Owner {
		return false, ""
	}
	branch, err := nativeGit(repository.Path, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil || branch != "main" {
		return false, ""
	}
	clean, err := nativeWorktreeClean(repository.Path, true)
	if err != nil || !clean {
		return false, ""
	}
	inProgress, err := nativeGitOperationInProgress(repository.Path)
	if err != nil || inProgress {
		return false, ""
	}
	head, err := nativeGit(repository.Path, "rev-parse", "HEAD")
	if err != nil {
		return false, ""
	}
	remoteHead, err := nativeGit(repository.Path, "rev-parse", "origin/main")
	if err != nil || head != remoteHead {
		return false, ""
	}
	worktrees, err := listNativeWorktrees(repository.Path)
	if err != nil || len(worktrees) != 1 || !samePath(worktrees[0].Path, repository.Path) {
		return false, ""
	}
	localOnly, err := nativeGit(repository.Path,
		"rev-list", "--all", "--reflog", "--not", "--remotes=origin")
	if err != nil || localOnly != "" {
		return false, ""
	}
	if _, err := os.Stat(filepath.Join(repository.Path, ".gitmodules")); err == nil {
		return false, ""
	}
	fingerprint := strings.Join([]string{origin, head, branch}, "\x00")
	return true, fingerprint
}

func createNativeSession(
	runtime nativeRuntime,
	harness string,
	repositories []nativeRepository,
	options nativeLaunchOptions,
) (nativeLaunchWorkspace, error) {
	relative, inside := relativeWithin(runtime.ProjectsRoot, runtime.CWD)
	id, sessionRoot, err := reserveNativeSession(runtime, harness, repositories)
	if err != nil {
		return nativeLaunchWorkspace{}, err
	}
	sessionProjects := filepath.Join(sessionRoot, "projects")
	sessionHome := ""
	if options.WorkspaceRoot || options.StandaloneHome {
		// Before staging, because the stager links whatever the host .claude
		// holds at stage time and the credential has to be there to be linked.
		if harness == "claude" {
			seeded, err := seedCanonicalClaudeCredential(
				context.Background(), runtime.claudeKeyring(), runtime.Home)
			if err != nil {
				fmt.Fprintf(runtime.Stderr,
					"aos: canonical Claude login not seeded: %v\n", err)
			} else if seeded {
				runtime.Progress.Note("seeded %s from the keychain",
					canonicalClaudeCredentialPath(runtime.Home))
			}
		}
		sessionHome = filepath.Join(sessionRoot, "home")
		stage := runtime.Progress.Step("stage session home")
		stageErr := error(nil)
		if options.StandaloneHome {
			stageErr = stageStandaloneRoleHome(runtime.Home, sessionHome)
		} else {
			stageErr = stageNativeRoleHome(runtime.Home, sessionHome, runtime.ProjectsRoot)
		}
		if stageErr != nil {
			stage.Fail(stageErr)
			_ = os.RemoveAll(sessionRoot)
			return nativeLaunchWorkspace{}, stageErr
		}
		stage.Done("%s", sessionHome)
	}
	branch := "aos/" + harness + "/" + id
	artifacts := make([]nativeArtifact, 0, len(repositories))
	created := map[string]string{}
	link := runtime.Progress.Step("link %d session worktrees", len(repositories))
	for index, repository := range repositories {
		identity := repository.Owner + "/" + repository.Name
		target := filepath.Join(sessionProjects, repository.Owner, repository.Name)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			link.Fail(err)
			return nativeLaunchWorkspace{}, err
		}
		runtime.Progress.Item("worktree", index+1, len(repositories), "%s", identity)
		began := time.Now()
		_, err := nativeGit(repository.Path,
			"worktree", "add", "--quiet", "-b", branch, target, nativeWorktreeBase)
		link.Track(identity, time.Since(began))
		if err != nil {
			fmt.Fprintf(runtime.Stderr, "aos: worktree skipped for %s: %v\n", identity, err)
			continue
		}
		artifacts = append(artifacts, nativeArtifact{
			Repository: repository.Path,
			Worktree:   target,
			Branch:     branch,
		})
		created[filepath.Join(repository.Owner, repository.Name)] = target
	}
	link.Done("%d linked", len(artifacts))
	if len(artifacts) == 0 {
		if sessionHome == "" {
			_ = os.RemoveAll(sessionRoot)
			return nativeLaunchWorkspace{CWD: runtime.CWD}, nil
		}
		if err := writeNativeJSON(nativeStatePath(runtime, "leases", id+".json"), nativeLease{
			Format:            "agentic-os.native-lease.v1",
			ID:                id,
			Harness:           harness,
			PID:               runtime.PID,
			ProcessStart:      runtime.ProcessStart,
			OriginalCWD:       runtime.CWD,
			SessionRoot:       sessionRoot,
			SessionProjects:   sessionProjects,
			SessionHome:       sessionHome,
			CanonicalHome:     runtime.Home,
			CanonicalProjects: runtime.ProjectsRoot,
			Artifacts:         artifacts,
		}); err != nil {
			return nativeLaunchWorkspace{}, fmt.Errorf("write native lease: %w", err)
		}
		if err := os.Setenv(agentComposeRuntimeHomeEnv, sessionHome); err != nil {
			return nativeLaunchWorkspace{}, fmt.Errorf("set agent-compose runtime home: %w", err)
		}
		return nativeLaunchWorkspace{CWD: runtime.CWD, SessionHome: sessionHome}, nil
	}
	lease := nativeLease{
		Format:            "agentic-os.native-lease.v1",
		ID:                id,
		Harness:           harness,
		PID:               runtime.PID,
		ProcessStart:      runtime.ProcessStart,
		OriginalCWD:       runtime.CWD,
		SessionRoot:       sessionRoot,
		SessionProjects:   sessionProjects,
		SessionHome:       sessionHome,
		CanonicalHome:     runtime.Home,
		CanonicalProjects: runtime.ProjectsRoot,
		Artifacts:         artifacts,
	}
	if err := writeNativeJSON(nativeStatePath(runtime, "leases", id+".json"), lease); err != nil {
		return nativeLaunchWorkspace{}, fmt.Errorf("write native lease: %w", err)
	}
	// A session with no linked worktree launches in the canonical checkout, so
	// only this path may tell the agent it is isolated.
	if err := os.Setenv(nativeSessionEnv, id); err != nil {
		return nativeLaunchWorkspace{}, fmt.Errorf("set native session id: %w", err)
	}
	if err := os.Setenv(nativeSessionProjectsEnv, sessionProjects); err != nil {
		return nativeLaunchWorkspace{}, fmt.Errorf("set native session projects: %w", err)
	}
	// A launcher inside the shadow reads these to build a new session on the
	// canonical values rather than on this one's. agentic-os#1460
	for variable, value := range map[string]string{
		nativeSessionRootEnv:       sessionRoot,
		nativeCanonicalHomeEnv:     runtime.Home,
		nativeCanonicalProjectsEnv: runtime.ProjectsRoot,
	} {
		if err := os.Setenv(variable, value); err != nil {
			return nativeLaunchWorkspace{}, fmt.Errorf("set %s: %w", variable, err)
		}
	}
	if sessionHome != "" {
		if err := os.Setenv(agentComposeRuntimeHomeEnv, sessionHome); err != nil {
			return nativeLaunchWorkspace{}, fmt.Errorf("set agent-compose runtime home: %w", err)
		}
	}
	launch := runtime.CWD
	if options.WorkspaceRoot {
		launch = sessionProjects
	} else if inside {
		launch = sessionProjects
		parts := strings.Split(relative, string(filepath.Separator))
		switch {
		case len(parts) == 1 && parts[0] != ".":
			candidate := filepath.Join(sessionProjects, parts[0])
			if info, err := os.Stat(candidate); err == nil && info.IsDir() {
				launch = candidate
			}
		case len(parts) >= 2:
			if target := created[filepath.Join(parts[0], parts[1])]; target != "" {
				launch = target
				candidate := filepath.Join(append([]string{target}, parts[2:]...)...)
				if info, err := os.Stat(candidate); err == nil && info.IsDir() {
					launch = candidate
				}
			}
		}
	}
	if harness == "claude" {
		config := nativeClaudeConfigPath(runtime.Home)
		if sessionHome != "" {
			config = nativeClaudeSessionConfigPath(sessionHome)
		}
		if err := seedNativeClaudeTrust(config, []string{launch, sessionProjects}); err != nil {
			fmt.Fprintf(runtime.Stderr, "aos: native session trust not seeded: %v\n", err)
		}
	}
	runtime.Progress.Workspace(sessionProjects, len(artifacts))
	return nativeLaunchWorkspace{
		CWD:             launch,
		SessionProjects: sessionProjects,
		SessionHome:     sessionHome,
	}, nil
}

func stageNativeRoleHome(source, target, projectsRoot string) error {
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("inspect native host home %s: %w", source, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("native host home %s is not a directory", source)
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		return fmt.Errorf("create native role home: %w", err)
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return fmt.Errorf("read native host home: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		// Left absent so ~/projects fails instead of resolving past the session
		// worktrees. See docs/native-session-start.md.
		if projectsRoot != "" && samePath(filepath.Join(source, name), projectsRoot) {
			continue
		}
		if nativeStagedConfigPaths[name] {
			if err := stageNativeRoleConfigDirectory(
				filepath.Join(source, name),
				filepath.Join(target, name),
				name,
			); err != nil {
				return err
			}
			continue
		}
		if err := os.Symlink(
			filepath.Join(source, name),
			filepath.Join(target, name),
		); err != nil {
			return fmt.Errorf("link native home entry %s: %w", name, err)
		}
	}
	for _, name := range []string{".agents", ".claude"} {
		skills := filepath.Join(target, name, "skills")
		if err := os.MkdirAll(skills, 0o700); err != nil {
			return fmt.Errorf("create filtered native skill directory %s: %w", skills, err)
		}
	}
	return linkNativeClaudeConfig(source, target)
}

func stageStandaloneRoleHome(source, target string) error {
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("inspect standalone host home %s: %w", source, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("standalone host home %s is not a directory", source)
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		return fmt.Errorf("create standalone role home: %w", err)
	}
	for _, spec := range []struct {
		name    string
		blocked map[string]bool
	}{
		{name: ".agents", blocked: map[string]bool{"skills": true}},
		{name: ".claude", blocked: map[string]bool{"skills": true, ".credentials.json": true}},
	} {
		if err := copyStandaloneHomeDirectory(
			filepath.Join(source, spec.name),
			filepath.Join(target, spec.name),
			spec.blocked,
		); err != nil {
			return err
		}
	}
	for _, name := range []string{".agents", ".claude"} {
		skills := filepath.Join(target, name, "skills")
		if err := os.MkdirAll(skills, 0o700); err != nil {
			return fmt.Errorf("create standalone skill directory %s: %w", skills, err)
		}
	}
	return nil
}

func copyStandaloneHomeDirectory(source, target string, blocked map[string]bool) error {
	entries, err := os.ReadDir(source)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read standalone home directory %s: %w", source, err)
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		return fmt.Errorf("create standalone home directory %s: %w", target, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if blocked[name] || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		sourcePath := filepath.Join(source, name)
		targetPath := filepath.Join(target, name)
		if entry.IsDir() {
			if err := copyStandaloneHomeDirectory(sourcePath, targetPath, nil); err != nil {
				return err
			}
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect standalone home entry %s: %w", sourcePath, err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if err := copyFile(sourcePath, targetPath, info.Mode().Perm()); err != nil {
			return fmt.Errorf("copy standalone home entry %s: %w", sourcePath, err)
		}
	}
	return nil
}

// nativeStagedConfigPaths are the home-relative directories the session owns
// outright, so projection cannot write through. See docs/native-shadow.md.
var nativeStagedConfigPaths = map[string]bool{
	".agents":          true,
	".claude":          true,
	".codex":           true,
	".config":          true,
	".config/goose":    true,
	".config/opencode": true,
}

// nativeProjectedLoadPoints mirrors agent-compose's home layout registry. A
// host copy at one of these paths would shadow the projected role bundle.
var nativeProjectedLoadPoints = map[string]bool{
	".agents/skills":             true,
	".claude/CLAUDE.md":          true,
	".claude/skills":             true,
	".codex/AGENTS.md":           true,
	".config/goose/.goosehints":  true,
	".config/opencode/AGENTS.md": true,
}

func stageNativeRoleConfigDirectory(source, target, relative string) error {
	if err := os.MkdirAll(target, 0o700); err != nil {
		return fmt.Errorf("create filtered native config %s: %w", target, err)
	}
	entries, err := os.ReadDir(source)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read native config %s: %w", source, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		child := relative + "/" + name
		if nativeProjectedLoadPoints[child] {
			continue
		}
		if nativeStagedConfigPaths[child] {
			if err := stageNativeRoleConfigDirectory(
				filepath.Join(source, name),
				filepath.Join(target, name),
				child,
			); err != nil {
				return err
			}
			continue
		}
		if err := os.Symlink(
			filepath.Join(source, name),
			filepath.Join(target, name),
		); err != nil {
			return fmt.Errorf("link native config entry %s: %w", name, err)
		}
	}
	return nil
}

func reserveNativeSession(
	runtime nativeRuntime,
	harness string,
	repositories []nativeRepository,
) (string, string, error) {
	if err := os.MkdirAll(runtime.SessionsRoot, 0o700); err != nil {
		return "", "", fmt.Errorf("create native sessions root: %w", err)
	}
	branchCollisions := 0
	leaseCollisions := 0
	directoryCollisions := 0
	for attempt := 0; attempt < nativeSessionIDAttempts; attempt++ {
		id, err := nativeSessionID(runtime)
		if err != nil {
			return "", "", err
		}
		branch := "aos/" + harness + "/" + id
		occupied := false
		for _, repository := range repositories {
			for _, reference := range []string{
				"refs/heads/" + branch,
				"refs/remotes/origin/" + branch,
			} {
				exists, err := nativeGitRefExists(repository.Path, reference)
				if err != nil {
					return "", "", err
				}
				if exists {
					occupied = true
					break
				}
			}
			if occupied {
				break
			}
		}
		if occupied {
			branchCollisions++
			continue
		}
		leasePath := nativeStatePath(runtime, "leases", id+".json")
		if _, err := os.Lstat(leasePath); err == nil {
			leaseCollisions++
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", "", fmt.Errorf("inspect native lease candidate: %w", err)
		}
		sessionRoot := filepath.Join(runtime.SessionsRoot, id)
		if err := os.Mkdir(sessionRoot, 0o700); err == nil {
			return id, sessionRoot, nil
		} else if !errors.Is(err, fs.ErrExist) {
			return "", "", fmt.Errorf("reserve native session root: %w", err)
		}
		directoryCollisions++
	}
	return "", "", fmt.Errorf(
		"reserve native session id after %d attempts (%d branch, %d lease, %d directory collisions)",
		nativeSessionIDAttempts,
		branchCollisions,
		leaseCollisions,
		directoryCollisions,
	)
}

func nativeGitRefExists(directory, reference string) (bool, error) {
	arguments := []string{"-C", directory, "show-ref", "--verify", "--quiet", reference}
	command := exec.Command("git", arguments...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err == nil {
		return true, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf(
		"git %s: %w: %s",
		strings.Join(arguments, " "),
		err,
		strings.TrimSpace(string(output)),
	)
}

func nativeSessionID(runtime nativeRuntime) (string, error) {
	reader := runtime.Random
	if reader == nil {
		reader = rand.Reader
	}
	alphabets := []string{
		nativeIDLetters,
		nativeIDLetters,
		nativeIDDigits,
		nativeIDDigits,
	}
	id := make([]byte, len(alphabets))
	for index, alphabet := range alphabets {
		character, err := nativeRandomCharacter(reader, alphabet)
		if err != nil {
			return "", fmt.Errorf("generate native session id: %w", err)
		}
		id[index] = character
	}
	return string(id), nil
}

func nativeRandomCharacter(reader io.Reader, alphabet string) (byte, error) {
	limit := 256 - 256%len(alphabet)
	for {
		var sample [1]byte
		if _, err := io.ReadFull(reader, sample[:]); err != nil {
			return 0, err
		}
		if int(sample[0]) < limit {
			return alphabet[int(sample[0])%len(alphabet)], nil
		}
	}
}

type nativeExpected struct {
	Full      map[string]bool
	FleetOrgs map[string]bool
	// Authoritative is false when no plan produced this set. Deleting a
	// checkout on an unverified expectation is the hazard, not the cleanup.
	Authoritative bool
}

func (expected nativeExpected) matches(owner, name string) bool {
	return expected.Full[filepath.Join(owner, name)]
}

// nativeSerializedIdentities invert workspace isolation instead of receiving
// it: one editor lock, one world, one writer. docs/native-agent-workspaces.md
var nativeSerializedIdentities = map[string]bool{
	"coilyco-gaming/eco-app":  true,
	"coilyco-gaming/eco-mods": true,
	"coilyco-gaming/eco-ops":  true,
}

// nativeSerializedGOOS scopes the exemption to the tower running the editor.
// A variable so the test reaches both branches from either platform.
var nativeSerializedGOOS = runtime.GOOS

func nativeSerialized(owner, name string) bool {
	return nativeSerializedGOOS == "windows" && nativeSerializedIdentities[owner+"/"+name]
}

// seedNativeExpected marks serialized checkouts as belonging on disk, so the
// unexpected-clone scan never deletes what projection deliberately skipped.
func seedNativeExpected() map[string]bool {
	seed := map[string]bool{}
	for identity := range nativeSerializedIdentities {
		owner, name, _ := strings.Cut(identity, "/")
		if nativeSerialized(owner, name) {
			seed[filepath.Join(owner, name)] = true
		}
	}
	return seed
}

// nativeProjection splits what a launch links from what the pass maintains.
// Role scope reaches only the first. docs/native-agent-workspaces.md
type nativeProjection struct {
	Resident  []nativeRepository
	Projected []nativeRepository
	Expected  nativeExpected
}

func resolveExpectedRepositories(
	runtime nativeRuntime,
) (nativeProjection, error) {
	plan, err := verifiedRepositoryPlan(runtime)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Projection runs off the seed, cleanup does not: an absent plan
			// expects almost nothing. docs/native-session-start.md
			return nativeProjection{Expected: nativeExpected{
				Full:      seedNativeExpected(),
				FleetOrgs: readNativeListSet(runtime.FleetFile),
			}}, nil
		}
		return nativeProjection{}, err
	}
	if plan.ProjectsRoot == "" || !samePath(plan.ProjectsRoot, runtime.ProjectsRoot) {
		return nativeProjection{}, fmt.Errorf("Agent Compose repository plan projects_root %q does not match %s", plan.ProjectsRoot, runtime.ProjectsRoot)
	}
	expected := nativeExpected{
		Full:      seedNativeExpected(),
		FleetOrgs: readNativeListSet(runtime.FleetFile),
		// An unverified plan expects nothing it can prove, so cleanup stays off
		// even though projection runs. docs/native-session-start.md
		Authoritative: !plan.Unverified,
	}
	prior := ""
	for _, entry := range plan.Residency {
		parts := strings.Split(entry.Identity, "/")
		if len(parts) != 2 || !safePathSegment(parts[0]) || !safePathSegment(parts[1]) || entry.Identity <= prior {
			return nativeProjection{}, fmt.Errorf("Agent Compose repository plan has invalid, unsorted, or duplicate residency identity %q", entry.Identity)
		}
		prior = entry.Identity
		expected.Full[filepath.FromSlash(entry.Identity)] = true
	}
	reportMissingRequiredRepositories(runtime, plan)
	var repositories []nativeRepository
	for _, repository := range scanNativeRepositories(runtime.ProjectsRoot) {
		if nativeSerialized(repository.Owner, repository.Name) {
			continue
		}
		if expected.matches(repository.Owner, repository.Name) {
			repositories = append(repositories, repository)
		}
	}
	sort.Slice(repositories, func(i, j int) bool {
		return repositories[i].Path < repositories[j].Path
	})
	return nativeProjection{
		Resident:  repositories,
		Projected: projectRoleRepositories(runtime.Role, plan, repositories),
		Expected:  expected,
	}, nil
}

// projectRoleRepositories narrows the linked set to the role's own plan
// selections, and falls back to full residency. docs/native-agent-workspaces.md
func projectRoleRepositories(
	role string,
	plan aosRepositoryPlan,
	resident []nativeRepository,
) []nativeRepository {
	selections := plan.Roles[strings.TrimSpace(role)]
	if strings.TrimSpace(role) == "" || len(selections) == 0 {
		return resident
	}
	selected := make(map[string]bool, len(selections))
	for _, selection := range selections {
		selected[filepath.FromSlash(selection.Identity)] = true
	}
	projected := make([]nativeRepository, 0, len(selections))
	for _, repository := range resident {
		if selected[filepath.Join(repository.Owner, repository.Name)] {
			projected = append(projected, repository)
		}
	}
	// Linking nothing drops the session into the canonical tree.
	if len(projected) == 0 {
		return resident
	}
	return projected
}

// A required repository that is not on disk is silently omitted from
// projection, so a role composes less than the plan promised.
func reportMissingRequiredRepositories(runtime nativeRuntime, plan aosRepositoryPlan) {
	missing := make([]string, 0)
	for _, entry := range plan.Residency {
		if !entry.Required {
			continue
		}
		if _, err := os.Stat(filepath.Join(entry.Path, ".git")); err != nil {
			missing = append(missing, entry.Identity)
		}
	}
	if len(missing) == 0 {
		return
	}
	sort.Strings(missing)
	scope := fmt.Sprintf("%d repositories", len(missing))
	if len(missing) == 1 {
		scope = "1 repository"
	}
	fmt.Fprintf(runtime.Stderr,
		"aos: the plan marks %s required and not checked out: %s\n",
		scope, strings.Join(missing, ", "))
}

func readNativeListSet(path string) map[string]bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]bool{}
	}
	result := map[string]bool{}
	for _, entry := range parseNativeList(data) {
		result[entry] = true
	}
	return result
}

func parseNativeList(data []byte) []string {
	var result []string
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(strings.SplitN(raw, "#", 2)[0])
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

func scanNativeRepositories(projectsRoot string) []nativeRepository {
	var result []nativeRepository
	owners, err := os.ReadDir(projectsRoot)
	if err != nil {
		return nil
	}
	for _, owner := range owners {
		if !owner.IsDir() || humanWorkdir(owner.Name()) {
			continue
		}
		ownerPath := filepath.Join(projectsRoot, owner.Name())
		repos, err := os.ReadDir(ownerPath)
		if err != nil {
			continue
		}
		for _, repo := range repos {
			if !repo.IsDir() || humanWorkdir(repo.Name()) {
				continue
			}
			path := filepath.Join(ownerPath, repo.Name())
			if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
				continue
			}
			result = append(result, nativeRepository{
				Owner: owner.Name(),
				Name:  repo.Name(),
				Path:  path,
			})
		}
	}
	return result
}

func originOwner(remote string) string {
	remote = strings.TrimSuffix(strings.TrimSpace(remote), ".git")
	if index := strings.LastIndex(remote, ":"); index >= 0 &&
		!strings.Contains(remote[index+1:], "\\") {
		remote = remote[index+1:]
	}
	remote = strings.TrimSuffix(remote, "/")
	parts := strings.Split(strings.ReplaceAll(remote, "\\", "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2]
}

func relativeWithin(root, path string) (string, bool) {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return relative, true
}

func samePath(left, right string) bool {
	leftInfo, leftStatErr := os.Stat(left)
	rightInfo, rightStatErr := os.Stat(right)
	if leftStatErr == nil && rightStatErr == nil {
		return os.SameFile(leftInfo, rightInfo)
	}
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	return leftErr == nil && rightErr == nil && filepath.Clean(leftAbs) == filepath.Clean(rightAbs)
}

func humanWorkdir(path string) bool {
	return strings.HasSuffix(filepath.Base(filepath.Clean(path)), "-workdir")
}
