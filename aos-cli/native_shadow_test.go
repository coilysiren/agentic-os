package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"
)

func testGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git -C %s %s: %v\n%s",
			directory, strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func createNativeTestRepository(
	t *testing.T,
	root string,
	owner string,
	name string,
) (string, string) {
	t.Helper()
	remote := filepath.Join(root, "remotes", owner, name+".git")
	repository := filepath.Join(root, "projects", owner, name)
	if err := os.MkdirAll(filepath.Dir(remote), 0o755); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "init", "--bare", "--initial-branch=main", remote)
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	testGit(t, repository, "init", "--initial-branch=main")
	testGit(t, repository, "config", "user.email", "test@example.com")
	testGit(t, repository, "config", "user.name", "AOS Test")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, repository, "add", "README.md")
	testGit(t, repository, "commit", "-m", "initial")
	testGit(t, repository, "remote", "add", "origin", "file://"+remote)
	testGit(t, repository, "push", "-u", "origin", "main")
	return repository, remote
}

func nativeTestRuntime(t *testing.T, root string) nativeRuntime {
	t.Helper()
	start, err := processStartIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(root, "expected.txt")
	fleet := filepath.Join(root, "fleet.txt")
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	return nativeRuntime{
		Now:          time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC),
		PID:          os.Getpid(),
		ProcessStart: start,
		CWD:          filepath.Join(root, "projects"),
		Home:         home,
		ProjectsRoot: filepath.Join(root, "projects"),
		StateRoot:    filepath.Join(root, "state"),
		SessionsRoot: filepath.Join(root, "sessions"),
		PlanFile:     expected,
		FleetFile:    fleet,
		Stderr:       os.Stderr,
	}
}

func writeNativeTestList(t *testing.T, path string, values ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(values, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The policy source is a plain directory: the digest Agent Compose seals is of
// the file, and a checkout here would join the scan every caller counts.
func writeNativeTestPolicySource(t *testing.T, projects string) aosRepositoryPlanInput {
	t.Helper()
	policy := filepath.Join(projects, "owner", "policy", ".agents", "roles.kdl")
	if err := os.MkdirAll(filepath.Dir(policy), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("role \"platform\" {}\n")
	if err := os.WriteFile(policy, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return aosRepositoryPlanInput{
		Identity: "owner/policy",
		Revision: "0123456789abcdef0123456789abcdef01234567",
		Policy: aosRepositoryPolicyInput{
			Path:   ".agents/roles.kdl",
			SHA256: fmt.Sprintf("sha256:%x", sha256.Sum256(body)),
		},
	}
}

func writeNativeTestPlan(t *testing.T, path string, values ...string) {
	t.Helper()
	identities := make([]string, 0, len(values))
	for _, value := range values {
		if !strings.Contains(value, "/") {
			value = "owner/" + value
		}
		identities = append(identities, value)
	}
	slices.Sort(identities)
	residency := make([]aosRepositorySelection, 0, len(identities))
	for _, identity := range identities {
		residency = append(residency, aosRepositorySelection{
			Identity: identity,
			Path:     filepath.Join(filepath.Dir(path), "projects", filepath.FromSlash(identity)),
			Source:   "test", Scope: "role-union", Reason: "test repository",
		})
	}
	payload := aosRepositoryPlan{
		Format:       agentComposeRepositoryPlanYAMLFormat,
		ProjectsRoot: filepath.Join(filepath.Dir(path), "projects"),
		Inputs: []aosRepositoryPlanInput{
			writeNativeTestPolicySource(t, filepath.Join(filepath.Dir(path), "projects")),
		},
		Roles:     map[string][]aosRepositorySelection{},
		Residency: residency,
	}
	raw, err := yaml.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func onlyNativeLease(t *testing.T, runtime nativeRuntime) (string, nativeLease) {
	t.Helper()
	entries, err := os.ReadDir(nativeStatePath(runtime, "leases"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d leases, want 1", len(entries))
	}
	path := nativeStatePath(runtime, "leases", entries[0].Name())
	var lease nativeLease
	if err := readNativeJSON(path, &lease); err != nil {
		t.Fatal(err)
	}
	return path, lease
}

func TestNativeLaunchCreatesFleetWorkspaceFromProjectsRoot(t *testing.T) {
	root := t.TempDir()
	createNativeTestRepository(t, root, "owner", "one")
	createNativeTestRepository(t, root, "owner", "two")
	runtime := nativeTestRuntime(t, root)
	writeNativeTestPlan(t, runtime.PlanFile, "one", "two")
	writeNativeTestList(t, runtime.FleetFile, "owner")

	launch, err := prepareNativeLaunch(runtime, "codex")
	if err != nil {
		t.Fatal(err)
	}

	if samePath(launch, runtime.ProjectsRoot) {
		t.Fatal("launch stayed in canonical projects root")
	}
	for _, name := range []string{"one", "two"} {
		if _, err := os.Stat(filepath.Join(launch, "owner", name, "README.md")); err != nil {
			t.Fatalf("%s worktree missing: %v", name, err)
		}
	}
	_, lease := onlyNativeLease(t, runtime)
	if len(lease.Artifacts) != 2 {
		t.Fatalf("got %d artifacts, want 2", len(lease.Artifacts))
	}
}

func TestNativeLaunchMapsRepositorySubdirectoryIntoSession(t *testing.T) {
	root := t.TempDir()
	repository, _ := createNativeTestRepository(t, root, "owner", "one")
	if err := os.MkdirAll(filepath.Join(repository, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "docs", "README.md"), []byte("docs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, repository, "add", "docs/README.md")
	testGit(t, repository, "commit", "-m", "add docs")
	testGit(t, repository, "push", "origin", "main")
	runtime := nativeTestRuntime(t, root)
	runtime.CWD = filepath.Join(repository, "docs")
	writeNativeTestPlan(t, runtime.PlanFile, "one")
	writeNativeTestList(t, runtime.FleetFile, "owner")

	launch, err := prepareNativeLaunch(runtime, "claude")
	if err != nil {
		t.Fatal(err)
	}

	if filepath.Base(launch) != "docs" || strings.Contains(launch, repository) {
		t.Fatalf("mapped launch = %s", launch)
	}
}

func TestNativeLaunchCanStartAtSessionProjectsRoot(t *testing.T) {
	root := t.TempDir()
	repository, _ := createNativeTestRepository(t, root, "owner", "one")
	runtime := nativeTestRuntime(t, root)
	runtime.CWD = repository
	writeNativeTestPlan(t, runtime.PlanFile, "one")
	writeNativeTestList(t, runtime.FleetFile, "owner")
	t.Setenv(agentComposeRuntimeHomeEnv, "")

	launch, err := prepareNativeLaunchWithOptions(
		runtime,
		"codex",
		nativeLaunchOptions{WorkspaceRoot: true},
	)
	if err != nil {
		t.Fatal(err)
	}

	_, lease := onlyNativeLease(t, runtime)
	if !samePath(launch, lease.SessionProjects) {
		t.Fatalf("launch = %s, want session projects %s", launch, lease.SessionProjects)
	}
	if got := os.Getenv(agentComposeRuntimeHomeEnv); got != lease.SessionHome {
		t.Fatalf("runtime home = %s, want %s", got, lease.SessionHome)
	}
}

func TestNativeLaunchExportsSessionMarkers(t *testing.T) {
	root := t.TempDir()
	repository, _ := createNativeTestRepository(t, root, "owner", "one")
	runtime := nativeTestRuntime(t, root)
	runtime.CWD = repository
	writeNativeTestPlan(t, runtime.PlanFile, "one")
	writeNativeTestList(t, runtime.FleetFile, "owner")
	t.Setenv(nativeSessionEnv, "")
	t.Setenv(nativeSessionProjectsEnv, "")

	if _, err := prepareNativeLaunch(runtime, "claude"); err != nil {
		t.Fatal(err)
	}

	_, lease := onlyNativeLease(t, runtime)
	if got := os.Getenv(nativeSessionEnv); got != lease.ID {
		t.Fatalf("session id = %s, want %s", got, lease.ID)
	}
	if got := os.Getenv(nativeSessionProjectsEnv); got != lease.SessionProjects {
		t.Fatalf("session projects = %s, want %s", got, lease.SessionProjects)
	}
}

func TestStageNativeRoleHomeFiltersUserSkills(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	target := filepath.Join(t.TempDir(), "target")
	for _, path := range []string{
		filepath.Join(source, ".agents", "skills", "role-other"),
		filepath.Join(source, ".claude", "skills", "role-other"),
		filepath.Join(source, ".config"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{
		filepath.Join(source, ".agents", "settings.json"),
		filepath.Join(source, ".claude", "settings.json"),
		filepath.Join(source, ".gitconfig"),
	} {
		if err := os.WriteFile(path, []byte("test\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := stageNativeRoleHome(source, target, ""); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		filepath.Join(target, ".agents", "skills"),
		filepath.Join(target, ".claude", "skills"),
	} {
		entries, err := os.ReadDir(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("filtered skills remain under %s: %+v", path, entries)
		}
	}
	for _, path := range []string{
		filepath.Join(target, ".agents", "settings.json"),
		filepath.Join(target, ".claude", "settings.json"),
		filepath.Join(target, ".gitconfig"),
	} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("preserved native home entry is not a symlink: %s", path)
		}
	}
	// .config holds the goose and opencode load points, so the session owns it
	// outright rather than resolving through to the host.
	info, err := os.Lstat(filepath.Join(target, ".config"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Errorf("a linked .config lets projection write the host copy")
	}
}

// A home-scope projection resolves symlinks, so any host copy standing at a
// load point would be written through instead of replaced.
func TestStageNativeRoleHomeReservesProjectedLoadPoints(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	target := filepath.Join(t.TempDir(), "target")
	for _, dir := range []string{
		filepath.Join(source, ".claude"),
		filepath.Join(source, ".codex"),
		filepath.Join(source, ".config", "goose"),
		filepath.Join(source, ".config", "opencode"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	loadPoints := []string{
		filepath.Join(".claude", "CLAUDE.md"),
		filepath.Join(".codex", "AGENTS.md"),
		filepath.Join(".config", "goose", ".goosehints"),
		filepath.Join(".config", "opencode", "AGENTS.md"),
	}
	for _, rel := range loadPoints {
		if err := os.WriteFile(filepath.Join(source, rel), []byte("host\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, ".codex", "auth.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := stageNativeRoleHome(source, target, ""); err != nil {
		t.Fatal(err)
	}

	for _, rel := range loadPoints {
		if _, err := os.Lstat(filepath.Join(target, rel)); !os.IsNotExist(err) {
			t.Errorf("host copy still shadows the projected load point %s: %v", rel, err)
		}
	}
	// Everything beside a load point still reaches the host.
	if _, err := os.Lstat(filepath.Join(target, ".codex", "auth.json")); err != nil {
		t.Errorf("staging dropped a neighbouring config entry: %v", err)
	}
}

func TestStageNativeRoleHomeLeavesProjectsRootAbsent(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	target := filepath.Join(t.TempDir(), "target")
	projects := filepath.Join(source, "projects")
	for _, path := range []string{projects, filepath.Join(source, "projects-oss")} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, ".gitconfig"), []byte("test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := stageNativeRoleHome(source, target, projects); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Lstat(filepath.Join(target, "projects")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("projects root must stay absent from the shadow home, got %v", err)
	}
	for _, name := range []string{"projects-oss", ".gitconfig"} {
		info, err := os.Lstat(filepath.Join(target, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("unrelated native home entry is not a symlink: %s", name)
		}
	}
}

func TestStageStandaloneRoleHomeCopiesSafeConfigAndDeniesSensitivePaths(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	target := filepath.Join(t.TempDir(), "target")
	for _, path := range []string{
		filepath.Join(source, ".agents", "skills", "role-other"),
		filepath.Join(source, ".agents", "profiles"),
		filepath.Join(source, ".claude", "skills", "role-other"),
		filepath.Join(source, ".aws"),
		filepath.Join(source, ".codex"),
		filepath.Join(source, ".config", "goose"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{
		filepath.Join(source, ".agents", "settings.json"),
		filepath.Join(source, ".agents", "profiles", "platform.yaml"),
		filepath.Join(source, ".agents", "skills", "role-other", "SKILL.md"),
		filepath.Join(source, ".claude", "settings.json"),
		filepath.Join(source, ".claude", ".credentials.json"),
		filepath.Join(source, ".aws", "config"),
		filepath.Join(source, ".codex", "auth.json"),
		filepath.Join(source, ".config", "goose", "config.yaml"),
		filepath.Join(source, ".gitconfig"),
	} {
		if err := os.WriteFile(path, []byte("test\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := stageStandaloneRoleHome(source, target); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		filepath.Join(target, ".agents", "settings.json"),
		filepath.Join(target, ".agents", "profiles", "platform.yaml"),
		filepath.Join(target, ".claude", "settings.json"),
	} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			t.Errorf("standalone home entry should be copied, not symlinked: %s", path)
		}
	}
	for _, path := range []string{
		filepath.Join(target, ".agents", "skills", "role-other", "SKILL.md"),
		filepath.Join(target, ".claude", "skills", "role-other"),
		filepath.Join(target, ".claude", ".credentials.json"),
		filepath.Join(target, ".aws", "config"),
		filepath.Join(target, ".codex", "auth.json"),
		filepath.Join(target, ".config", "goose", "config.yaml"),
		filepath.Join(target, ".gitconfig"),
	} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("standalone home projected denied path %s: %v", path, err)
		}
	}
	for _, path := range []string{
		filepath.Join(target, ".agents", "skills"),
		filepath.Join(target, ".claude", "skills"),
	} {
		entries, err := os.ReadDir(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("standalone skill directory is not empty under %s: %+v", path, entries)
		}
	}
}

func TestNativeShadowClearsDeprecatedModelSelectors(t *testing.T) {
	t.Setenv(agentComposeModelClassEnv, "legacy-model-class")
	t.Setenv(agentComposeModelTierEnv, "legacy-model-tier")

	if err := clearDeprecatedModelSelectors(); err != nil {
		t.Fatal(err)
	}
	for _, variable := range []string{agentComposeModelTierEnv, agentComposeModelClassEnv} {
		if _, found := os.LookupEnv(variable); found {
			t.Fatalf("deprecated %s remains set", variable)
		}
	}
}

func TestConvergeNativeEnvironmentAppliesHostMCPProjection(t *testing.T) {
	root := t.TempDir()
	runtime := nativeTestRuntime(t, root)
	config := filepath.Join(runtime.Home, ".config", "aos", "converge.yaml")
	inventory := filepath.Join(runtime.Home, ".config", "mcporter", "mcporter.json")
	writeNativeMCPTestFile(
		t,
		config,
		"mcp:\n  inventory: ~/.config/mcporter/mcporter.json\n",
	)
	writeNativeMCPTestFile(
		t,
		inventory,
		`{"imports":[],"mcpServers":{"forgejo":{"baseUrl":"https://mcp.example.test/mcp","x-codex":{"defaultToolsApprovalMode":"approve"}}}}`+"\n",
	)

	if err := convergeNativeEnvironment(context.Background(), runtime); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(runtime.Home, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		aosMCPBlockBegin,
		`[mcp_servers."forgejo"]`,
		`default_tools_approval_mode = "approve"`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("native convergence missing %q:\n%s", want, raw)
		}
	}
}

func TestNativeShadowProtectsClaudeInstall(t *testing.T) {
	tests := []struct {
		name     string
		harness  string
		isolated bool
		want     string
	}{
		{name: "isolated Claude", harness: "claude", isolated: true, want: "1"},
		{name: "host Claude", harness: "claude", isolated: false, want: "original"},
		{name: "isolated Codex", harness: "codex", isolated: true, want: "original"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(claudeDisableAutoUpdaterEnv, "original")

			if err := protectNativeHarnessInstall(test.harness, test.isolated); err != nil {
				t.Fatal(err)
			}
			if got := os.Getenv(claudeDisableAutoUpdaterEnv); got != test.want {
				t.Fatalf("%s = %q, want %q", claudeDisableAutoUpdaterEnv, got, test.want)
			}
		})
	}
}

func TestNativeCodexWorkspaceTrustIsScopedToGeneratedProject(t *testing.T) {
	project := "/tmp/aos/native/session/projects"
	override := `projects={"/tmp/aos/native/session/projects"={trust_level="trusted"}}`
	tests := []struct {
		name    string
		command []string
		want    []string
	}{
		{
			name:    "assigned role",
			command: []string{"agent-compose", "launch", "platform", "codex", "--model", "gpt"},
			want:    []string{"agent-compose", "launch", "platform", "codex", "--config", override, "--model", "gpt"},
		},
		{
			name:    "inferred role",
			command: []string{"agent-compose", "compose", "--", "codex", "--model", "gpt"},
			want:    []string{"agent-compose", "compose", "--", "codex", "--config", override, "--model", "gpt"},
		},
		{
			name:    "direct harness",
			command: []string{"codex", "--model", "gpt"},
			want:    []string{"codex", "--config", override, "--model", "gpt"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := trustNativeCodexWorkspace(test.command, "codex", project)
			if strings.Join(got, "\x00") != strings.Join(test.want, "\x00") {
				t.Fatalf("command = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestNativeCodexProjectResolvesWorkspaceSymlinks(t *testing.T) {
	actual := filepath.Join(t.TempDir(), "projects")
	if err := os.MkdirAll(actual, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "projects")
	if err := os.Symlink(actual, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(actual)
	if err != nil {
		t.Fatal(err)
	}

	if got := nativeCodexProject(link); got != resolved {
		t.Fatalf("project = %q, want canonical path %q", got, resolved)
	}
}

func TestNativeWorkspaceTrustDoesNotChangeOtherHarnesses(t *testing.T) {
	command := []string{"agent-compose", "launch", "platform", "claude"}
	got := trustNativeCodexWorkspace(command, "claude", "/tmp/aos/native/session/projects")
	if strings.Join(got, "\x00") != strings.Join(command, "\x00") {
		t.Fatalf("command = %#v, want unchanged %#v", got, command)
	}
}

func TestNativeLaunchOutsideProjectsStillCreatesFleetWorkspace(t *testing.T) {
	root := t.TempDir()
	createNativeTestRepository(t, root, "owner", "one")
	runtime := nativeTestRuntime(t, root)
	runtime.CWD = root
	writeNativeTestPlan(t, runtime.PlanFile, "one")
	writeNativeTestList(t, runtime.FleetFile, "owner")

	launch, err := prepareNativeLaunch(runtime, "codex")
	if err != nil {
		t.Fatal(err)
	}

	if launch != runtime.CWD {
		t.Fatalf("launch = %s, want original directory %s", launch, runtime.CWD)
	}
	_, lease := onlyNativeLease(t, runtime)
	if len(lease.Artifacts) != 1 {
		t.Fatalf("got %d artifacts, want 1", len(lease.Artifacts))
	}
}

func TestNativeLaunchMapsOwnerDirectoryIntoSession(t *testing.T) {
	root := t.TempDir()
	createNativeTestRepository(t, root, "owner", "one")
	runtime := nativeTestRuntime(t, root)
	runtime.CWD = filepath.Join(runtime.ProjectsRoot, "owner")
	writeNativeTestPlan(t, runtime.PlanFile, "one")
	writeNativeTestList(t, runtime.FleetFile, "owner")

	launch, err := prepareNativeLaunch(runtime, "codex")
	if err != nil {
		t.Fatal(err)
	}

	if filepath.Base(launch) != "owner" || samePath(launch, runtime.CWD) {
		t.Fatalf("mapped owner launch = %s", launch)
	}
}

func TestConfirmedDeadSessionReleasesRecoverableWorktree(t *testing.T) {
	root := t.TempDir()
	repository, _ := createNativeTestRepository(t, root, "owner", "one")
	runtime := nativeTestRuntime(t, root)
	writeNativeTestPlan(t, runtime.PlanFile, "one")
	writeNativeTestList(t, runtime.FleetFile, "owner")
	if _, err := prepareNativeLaunch(runtime, "codex"); err != nil {
		t.Fatal(err)
	}
	leasePath, lease := onlyNativeLease(t, runtime)
	oldWorktree := lease.Artifacts[0].Worktree
	oldBranch := lease.Artifacts[0].Branch
	lease.PID = 0
	lease.ProcessStart = ""
	if err := writeNativeJSON(leasePath, lease); err != nil {
		t.Fatal(err)
	}
	runtime.Now = runtime.Now.Add(time.Minute)

	live, err := cleanDeadNativeSessions(runtime)
	if err != nil {
		t.Fatal(err)
	}
	var retained nativeLease
	if err := readNativeJSON(leasePath, &retained); err != nil {
		t.Fatal(err)
	}
	if retained.DeadSince == nil || !retained.DeadSince.Equal(runtime.Now) {
		t.Fatalf("dead since = %v, want %s", retained.DeadSince, runtime.Now)
	}
	if !live.contains(oldWorktree) {
		t.Fatal("newly dead worktree was not protected from the fleet sweep")
	}
	if _, err := os.Stat(oldWorktree); err != nil {
		t.Fatalf("newly dead worktree was removed: %v", err)
	}

	runtime.Now = runtime.Now.Add(time.Minute)
	if _, err := cleanDeadNativeSessions(runtime); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(oldWorktree); !os.IsNotExist(err) {
		t.Fatalf("confirmed dead worktree remains: %v", err)
	}
	if output := testGit(t, repository, "branch", "--list", oldBranch); output != "" {
		t.Fatalf("confirmed dead branch remains: %s", output)
	}
	if _, err := os.Stat(lease.SessionRoot); err != nil {
		t.Fatalf("session root was removed before grace expiry: %v", err)
	}
	if _, err := os.Stat(leasePath); err != nil {
		t.Fatalf("lease was removed before grace expiry: %v", err)
	}

	runtime.Now = runtime.Now.Add(nativeDeadSessionGrace)
	if _, err := cleanDeadNativeSessions(runtime); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(lease.SessionRoot); !os.IsNotExist(err) {
		t.Fatalf("expired session root remains: %v", err)
	}
	if _, err := os.Stat(leasePath); !os.IsNotExist(err) {
		t.Fatalf("expired lease remains: %v", err)
	}
}

func TestDeadSessionGraceSurvivesDueFleetSweep(t *testing.T) {
	root := t.TempDir()
	repository, _ := createNativeTestRepository(t, root, "owner", "one")
	runtime := nativeTestRuntime(t, root)
	writeNativeTestPlan(t, runtime.PlanFile, "one")
	writeNativeTestList(t, runtime.FleetFile, "owner")
	if _, err := prepareNativeLaunch(runtime, "codex"); err != nil {
		t.Fatal(err)
	}
	leasePath, lease := onlyNativeLease(t, runtime)
	worktree := lease.Artifacts[0].Worktree
	branch := lease.Artifacts[0].Branch
	lease.PID = 0
	lease.ProcessStart = ""
	if err := writeNativeJSON(leasePath, lease); err != nil {
		t.Fatal(err)
	}
	runtime.Now = runtime.Now.Add(nativeSweepInterval)

	if _, err := prepareNativeLaunch(runtime, "claude"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("grace-period worktree was removed by fleet sweep: %v", err)
	}
	if output := testGit(t, repository, "branch", "--list", branch); output == "" {
		t.Fatal("grace-period branch was removed by fleet sweep")
	}
	var retained nativeLease
	if err := readNativeJSON(leasePath, &retained); err != nil {
		t.Fatal(err)
	}
	if retained.DeadSince == nil || !retained.DeadSince.Equal(runtime.Now) {
		t.Fatalf("dead since = %v, want %s", retained.DeadSince, runtime.Now)
	}
}

func TestLiveNativeSessionIsNotCleaned(t *testing.T) {
	root := t.TempDir()
	createNativeTestRepository(t, root, "owner", "one")
	runtime := nativeTestRuntime(t, root)
	writeNativeTestPlan(t, runtime.PlanFile, "one")
	writeNativeTestList(t, runtime.FleetFile, "owner")
	if _, err := prepareNativeLaunch(runtime, "codex"); err != nil {
		t.Fatal(err)
	}
	leasePath, lease := onlyNativeLease(t, runtime)
	deadSince := runtime.Now.Add(-nativeDeadSessionGrace)
	lease.DeadSince = &deadSince
	if err := writeNativeJSON(leasePath, lease); err != nil {
		t.Fatal(err)
	}

	live, err := cleanDeadNativeSessions(runtime)
	if err != nil {
		t.Fatal(err)
	}

	if !live.contains(lease.Artifacts[0].Worktree) {
		t.Fatal("live worktree was not leased")
	}
	if _, err := os.Stat(lease.Artifacts[0].Worktree); err != nil {
		t.Fatalf("live worktree was removed: %v", err)
	}
	var restored nativeLease
	if err := readNativeJSON(leasePath, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.DeadSince != nil {
		t.Fatalf("live lease kept stale dead since %s", restored.DeadSince)
	}
}

func TestNativeSweepPreservesLiveWorktreeAcrossPathAlias(t *testing.T) {
	root := t.TempDir()
	repository, _ := createNativeTestRepository(t, root, "owner", "one")
	runtime := nativeTestRuntime(t, root)
	writeNativeTestPlan(t, runtime.PlanFile, "one")
	writeNativeTestList(t, runtime.FleetFile, "owner")
	if err := os.MkdirAll(runtime.SessionsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	aliasRoot := filepath.Join(root, "sessions-alias")
	if err := os.Symlink(runtime.SessionsRoot, aliasRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := prepareNativeLaunch(runtime, "codex"); err != nil {
		t.Fatal(err)
	}
	leasePath, lease := onlyNativeLease(t, runtime)
	active := lease.Artifacts[0]
	relative, err := filepath.Rel(runtime.SessionsRoot, active.Worktree)
	if err != nil {
		t.Fatal(err)
	}
	lease.Artifacts[0].Worktree = filepath.Join(aliasRoot, relative)
	if !samePath(active.Worktree, lease.Artifacts[0].Worktree) {
		t.Fatal("lease alias does not resolve to the active worktree")
	}
	if filepath.Clean(active.Worktree) == filepath.Clean(lease.Artifacts[0].Worktree) {
		t.Fatal("lease alias is not textually distinct from the active worktree")
	}
	if err := writeNativeJSON(leasePath, lease); err != nil {
		t.Fatal(err)
	}

	controlBranch := "inactive-control"
	control := filepath.Join(root, "inactive-control")
	testGit(t, repository, "worktree", "add", "-b", controlBranch, control, "origin/main")
	testGit(t, control, "push", "-u", "origin", controlBranch)
	runtime.Now = runtime.Now.Add(nativeSweepInterval)

	if _, err := prepareNativeLaunch(runtime, "claude"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(active.Worktree); err != nil {
		t.Fatalf("live worktree was removed through its path alias: %v", err)
	}
	if output := testGit(t, repository, "branch", "--list", active.Branch); output == "" {
		t.Fatal("live worktree branch was removed")
	}
	if _, err := os.Stat(control); !os.IsNotExist(err) {
		t.Fatalf("inactive control worktree remains: %v", err)
	}
	if output := testGit(t, repository, "branch", "--list", controlBranch); output != "" {
		t.Fatalf("inactive control branch remains: %s", output)
	}
}

func TestNativeLiveWorktreesFailClosedWhenPathIdentityIsUncertain(t *testing.T) {
	// Unreadable for a reason other than absence: a path under a regular file
	// resolves ENOTDIR, so identity is genuinely unknown and the set fails closed.
	file := filepath.Join(t.TempDir(), "regular")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	live := nativeLiveWorktrees{}
	live.add(filepath.Join(file, "child"))

	if !live.contains(t.TempDir()) {
		t.Fatal("uncertain live path allowed workspace cleanup")
	}
}

// Conflating a purged path with uncertainty disabled the whole pass.
// agentic-os#1084
func TestNativeLiveWorktreesTreatAbsentPathAsGoneNotUncertain(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if (nativeLiveWorktrees{}).contains(missing) {
		t.Fatal("empty live set preserved an unrelated missing path")
	}

	live := nativeLiveWorktrees{}
	live.add(missing)

	if live.contains(t.TempDir()) {
		t.Fatal("a purged worktree path marked every other path live")
	}
	if live.contains(missing) {
		t.Fatal("a purged worktree path reported itself live")
	}
}

func TestUnreadableNativeLeaseFailsClosed(t *testing.T) {
	runtime := nativeTestRuntime(t, t.TempDir())
	leasePath := nativeStatePath(runtime, "leases", "unreadable.json")
	if err := os.MkdirAll(filepath.Dir(leasePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(leasePath, []byte("not JSON\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	live, err := cleanDeadNativeSessions(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if !live.contains(t.TempDir()) {
		t.Fatal("unreadable lease allowed workspace cleanup")
	}
}

func TestExpiredDeadSessionPreservesDirtyWorktree(t *testing.T) {
	root := t.TempDir()
	createNativeTestRepository(t, root, "owner", "one")
	runtime := nativeTestRuntime(t, root)
	writeNativeTestPlan(t, runtime.PlanFile, "one")
	writeNativeTestList(t, runtime.FleetFile, "owner")
	if _, err := prepareNativeLaunch(runtime, "codex"); err != nil {
		t.Fatal(err)
	}
	leasePath, lease := onlyNativeLease(t, runtime)
	worktree := lease.Artifacts[0].Worktree
	if err := os.WriteFile(filepath.Join(worktree, "unfinished.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lease.PID = 0
	lease.ProcessStart = ""
	deadSince := runtime.Now.Add(-nativeDeadSessionGrace)
	lease.DeadSince = &deadSince
	if err := writeNativeJSON(leasePath, lease); err != nil {
		t.Fatal(err)
	}

	if _, err := cleanDeadNativeSessions(runtime); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(worktree, "unfinished.txt")); err != nil {
		t.Fatalf("dirty worktree was removed: %v", err)
	}
}

func TestExpiredDeadSessionPreservesUnpushedWorktree(t *testing.T) {
	root := t.TempDir()
	createNativeTestRepository(t, root, "owner", "one")
	runtime := nativeTestRuntime(t, root)
	writeNativeTestPlan(t, runtime.PlanFile, "one")
	writeNativeTestList(t, runtime.FleetFile, "owner")
	if _, err := prepareNativeLaunch(runtime, "codex"); err != nil {
		t.Fatal(err)
	}
	leasePath, lease := onlyNativeLease(t, runtime)
	worktree := lease.Artifacts[0].Worktree
	testGit(t, worktree, "config", "user.email", "test@example.com")
	testGit(t, worktree, "config", "user.name", "AOS Test")
	if err := os.WriteFile(filepath.Join(worktree, "local.txt"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, worktree, "add", "local.txt")
	testGit(t, worktree, "commit", "-m", "local only")
	lease.PID = 0
	lease.ProcessStart = ""
	deadSince := runtime.Now.Add(-nativeDeadSessionGrace)
	lease.DeadSince = &deadSince
	if err := writeNativeJSON(leasePath, lease); err != nil {
		t.Fatal(err)
	}

	if _, err := cleanDeadNativeSessions(runtime); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("unpushed worktree was removed: %v", err)
	}
}

func TestNativeSweepReturnsExpectedCheckoutToMain(t *testing.T) {
	root := t.TempDir()
	repository, _ := createNativeTestRepository(t, root, "owner", "one")
	testGit(t, repository, "switch", "-c", "task")
	if err := os.WriteFile(filepath.Join(repository, "task.txt"), []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, repository, "add", "task.txt")
	testGit(t, repository, "commit", "-m", "task")
	testGit(t, repository, "push", "-u", "origin", "task")
	runtime := nativeTestRuntime(t, root)
	writeNativeTestPlan(t, runtime.PlanFile, "one")
	writeNativeTestList(t, runtime.FleetFile, "owner")

	if err := normalizeNativeRepository(runtime, nativeRepository{
		Owner: "owner", Name: "one", Path: repository,
	}, nativeLiveWorktrees{}); err != nil {
		t.Fatal(err)
	}

	if branch := testGit(t, repository, "branch", "--show-current"); branch != "main" {
		t.Fatalf("branch = %s, want main", branch)
	}
	if output := testGit(t, repository, "branch", "--list", "task"); output != "" {
		t.Fatalf("task branch remains: %s", output)
	}
}

func TestNativeSweepReclaimsMainBeforeSwitchingCanonicalCheckout(t *testing.T) {
	root := t.TempDir()
	repository, _ := createNativeTestRepository(t, root, "owner", "one")
	testGit(t, repository, "switch", "-c", "task")
	testGit(t, repository, "push", "-u", "origin", "task")
	mainWorktree := filepath.Join(root, "main-worktree")
	testGit(t, repository, "worktree", "add", mainWorktree, "main")
	runtime := nativeTestRuntime(t, root)

	if err := normalizeNativeRepository(runtime, nativeRepository{
		Owner: "owner", Name: "one", Path: repository,
	}, nativeLiveWorktrees{}); err != nil {
		t.Fatal(err)
	}

	if branch := testGit(t, repository, "branch", "--show-current"); branch != "main" {
		t.Fatalf("branch = %s, want main", branch)
	}
	if _, err := os.Stat(mainWorktree); !os.IsNotExist(err) {
		t.Fatalf("main worktree remains: %v", err)
	}
}

func TestSerializedRepositoryIsNotProjected(t *testing.T) {
	nativeSerializedGOOS = "windows"
	t.Cleanup(func() { nativeSerializedGOOS = runtime.GOOS })
	root := t.TempDir()
	createNativeTestRepository(t, root, "coilyco-gaming", "eco-mods")
	createNativeTestRepository(t, root, "owner", "one")
	testRuntime := nativeTestRuntime(t, root)
	writeNativeTestPlan(t, testRuntime.PlanFile, "coilyco-gaming/eco-mods", "owner/one")
	writeNativeTestList(t, testRuntime.FleetFile, "coilyco-gaming", "owner")

	projection, err := resolveExpectedRepositories(testRuntime)
	if err != nil {
		t.Fatal(err)
	}
	repositories, expected := projection.Projected, projection.Expected

	for _, repository := range repositories {
		if repository.Name == "eco-mods" {
			t.Fatal("serialized repository was projected into the session")
		}
	}
	if len(repositories) != 1 {
		t.Fatalf("projected %d repositories, want 1", len(repositories))
	}
	if !expected.matches("coilyco-gaming", "eco-mods") {
		t.Fatal("serialized repository is not expected on disk")
	}
}

func TestSerializedRepositorySurvivesUnexpectedCloneScan(t *testing.T) {
	nativeSerializedGOOS = "windows"
	t.Cleanup(func() { nativeSerializedGOOS = runtime.GOOS })
	root := t.TempDir()
	repository, _ := createNativeTestRepository(t, root, "coilyco-gaming", "eco-ops")
	testRuntime := nativeTestRuntime(t, root)
	writeNativeTestPlan(t, testRuntime.PlanFile)
	writeNativeTestList(t, testRuntime.FleetFile, "coilyco-gaming")

	projection, err := resolveExpectedRepositories(testRuntime)
	if err != nil {
		t.Fatal(err)
	}
	expected := projection.Expected
	state := nativeSweepState{
		Format: "agentic-os.native-sweep.v1", Candidates: map[string]nativeCandidate{},
	}
	for scan := 1; scan <= nativeDeleteScans; scan++ {
		testRuntime.Now = testRuntime.Now.Add(nativeSweepInterval)
		if err := runNativeWorkspaceSweep(
			testRuntime, nil, expected, nativeLiveWorktrees{}, state,
		); err != nil {
			t.Fatal(err)
		}
		_ = readNativeJSON(nativeStatePath(testRuntime, "sweep.json"), &state)
	}

	if _, err := os.Stat(repository); err != nil {
		t.Fatalf("serialized checkout deleted by the unexpected-clone scan: %v", err)
	}
}

func TestSerializedExemptionIsWindowsOnly(t *testing.T) {
	nativeSerializedGOOS = "linux"
	t.Cleanup(func() { nativeSerializedGOOS = runtime.GOOS })
	root := t.TempDir()
	createNativeTestRepository(t, root, "coilyco-gaming", "eco-mods")
	testRuntime := nativeTestRuntime(t, root)
	writeNativeTestPlan(t, testRuntime.PlanFile, "coilyco-gaming/eco-mods")
	writeNativeTestList(t, testRuntime.FleetFile, "coilyco-gaming")

	projection, err := resolveExpectedRepositories(testRuntime)
	if err != nil {
		t.Fatal(err)
	}
	repositories := projection.Projected

	if len(repositories) != 1 {
		t.Fatalf("projected %d repositories off Windows, want 1", len(repositories))
	}
}

func TestUnexpectedCloneDeletedOnThirdSweep(t *testing.T) {
	root := t.TempDir()
	repository, _ := createNativeTestRepository(t, root, "owner", "extra")
	runtime := nativeTestRuntime(t, root)
	writeNativeTestPlan(t, runtime.PlanFile)
	writeNativeTestList(t, runtime.FleetFile, "owner")
	expected := nativeExpected{
		Full:          map[string]bool{},
		FleetOrgs:     map[string]bool{"owner": true},
		Authoritative: true,
	}
	if eligible, _ := unexpectedCloneEligible(
		runtime,
		nativeRepository{Owner: "owner", Name: "extra", Path: repository},
		nativeLiveWorktrees{},
		expected.FleetOrgs,
	); !eligible {
		t.Fatal("clean unexpected clone was not eligible")
	}
	state := nativeSweepState{
		Format: "agentic-os.native-sweep.v1", Candidates: map[string]nativeCandidate{},
	}

	for scan := 1; scan <= 3; scan++ {
		runtime.Now = runtime.Now.Add(nativeSweepInterval)
		if err := runNativeWorkspaceSweep(
			runtime, nil, expected, nativeLiveWorktrees{}, state,
		); err != nil {
			t.Fatal(err)
		}
		_ = readNativeJSON(nativeStatePath(runtime, "sweep.json"), &state)
		if scan < 3 {
			if _, err := os.Stat(repository); err != nil {
				t.Fatalf("repository removed on scan %d: %v", scan, err)
			}
		}
	}
	if _, err := os.Stat(repository); !os.IsNotExist(err) {
		t.Fatalf("repository remains after third scan: %v", err)
	}
}

func TestUnexpectedCloneCounterResetsWhenStateChanges(t *testing.T) {
	root := t.TempDir()
	repository, _ := createNativeTestRepository(t, root, "owner", "extra")
	runtime := nativeTestRuntime(t, root)
	writeNativeTestPlan(t, runtime.PlanFile)
	writeNativeTestList(t, runtime.FleetFile, "owner")
	expected := nativeExpected{
		Full:          map[string]bool{},
		FleetOrgs:     map[string]bool{"owner": true},
		Authoritative: true,
	}
	state := nativeSweepState{
		Format: "agentic-os.native-sweep.v1", Candidates: map[string]nativeCandidate{},
	}

	runtime.Now = runtime.Now.Add(nativeSweepInterval)
	if err := runNativeWorkspaceSweep(runtime, nil, expected, nativeLiveWorktrees{}, state); err != nil {
		t.Fatal(err)
	}
	state = nativeSweepState{}
	if err := readNativeJSON(nativeStatePath(runtime, "sweep.json"), &state); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "untracked.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runtime.Now = runtime.Now.Add(nativeSweepInterval)
	if err := runNativeWorkspaceSweep(runtime, nil, expected, nativeLiveWorktrees{}, state); err != nil {
		t.Fatal(err)
	}
	state = nativeSweepState{}
	if err := readNativeJSON(nativeStatePath(runtime, "sweep.json"), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Candidates) != 0 {
		t.Fatalf("candidate survived changed state: %#v", state.Candidates)
	}
	if err := os.Remove(filepath.Join(repository, "untracked.txt")); err != nil {
		t.Fatal(err)
	}
	runtime.Now = runtime.Now.Add(nativeSweepInterval)
	if err := runNativeWorkspaceSweep(runtime, nil, expected, nativeLiveWorktrees{}, state); err != nil {
		t.Fatal(err)
	}
	state = nativeSweepState{}
	if err := readNativeJSON(nativeStatePath(runtime, "sweep.json"), &state); err != nil {
		t.Fatal(err)
	}
	if candidate := state.Candidates[repository]; candidate.Scans != 1 {
		t.Fatalf("candidate count = %d, want reset to 1", candidate.Scans)
	}
}

func TestNativeSweepCacheIsFreshForTenMinutes(t *testing.T) {
	root := t.TempDir()
	runtime := nativeTestRuntime(t, root)
	state := nativeSweepState{
		Format: "agentic-os.native-sweep.v1", LastSweep: runtime.Now,
		Candidates: map[string]nativeCandidate{},
	}
	if err := writeNativeJSON(nativeStatePath(runtime, "sweep.json"), state); err != nil {
		t.Fatal(err)
	}

	runtime.Now = runtime.Now.Add(nativeSweepInterval - time.Second)
	if due, _ := nativeSweepDue(runtime); due {
		t.Fatal("sweep became due before ten minutes")
	}
	runtime.Now = runtime.Now.Add(time.Second)
	if due, _ := nativeSweepDue(runtime); !due {
		t.Fatal("sweep was not due at ten minutes")
	}
}

func TestOriginOwner(t *testing.T) {
	for remote, want := range map[string]string{
		"https://forge.example/owner/repo.git": "owner",
		"ssh://git@forge.example/owner/repo":   "owner",
		"git@forge.example:owner/repo.git":     "owner",
	} {
		if got := originOwner(remote); got != want {
			t.Errorf("originOwner(%q) = %q, want %q", remote, got, want)
		}
	}
}

func TestHumanWorkdirIsOutsideAutomation(t *testing.T) {
	if !humanWorkdir("/tmp/infrastructure-workdir") {
		t.Fatal("human workdir was not recognized")
	}
	if humanWorkdir("/tmp/infrastructure-agent") {
		t.Fatal("ordinary worktree was recognized as human workdir")
	}
}

func TestNativeGitOperationInProgressUsesRepositoryRelativePath(t *testing.T) {
	root := t.TempDir()
	repository, _ := createNativeTestRepository(t, root, "owner", "one")
	gitDirectory := testGit(t, repository, "rev-parse", "--git-dir")
	if !filepath.IsAbs(gitDirectory) {
		gitDirectory = filepath.Join(repository, gitDirectory)
	}
	if err := os.WriteFile(filepath.Join(gitDirectory, "MERGE_HEAD"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	inProgress, err := nativeGitOperationInProgress(repository)
	if err != nil {
		t.Fatal(err)
	}
	if !inProgress {
		t.Fatal("relative Git operation marker was not detected")
	}
}

// A live squatter is skipped by cleanup, so only detaching frees main.
// agentic-os#1086
func TestNativeSweepDetachesLiveWorktreeSquattingOnMain(t *testing.T) {
	root := t.TempDir()
	repository, _ := createNativeTestRepository(t, root, "owner", "one")
	testGit(t, repository, "switch", "-c", "task")
	testGit(t, repository, "push", "-u", "origin", "task")
	squatter := filepath.Join(root, "live-worktree")
	testGit(t, repository, "worktree", "add", squatter, "main")
	runtime := nativeTestRuntime(t, root)

	live := nativeLiveWorktrees{}
	live.add(squatter)

	if err := normalizeNativeRepository(runtime, nativeRepository{
		Owner: "owner", Name: "one", Path: repository,
	}, live); err != nil {
		t.Fatal(err)
	}

	if branch := testGit(t, repository, "branch", "--show-current"); branch != "main" {
		t.Fatalf("canonical branch = %s, want main", branch)
	}
	if _, err := os.Stat(squatter); err != nil {
		t.Fatalf("live worktree was removed rather than detached: %v", err)
	}
	if head := testGit(t, squatter, "branch", "--show-current"); head != "" {
		t.Fatalf("live worktree branch = %q, want detached", head)
	}
}

// Branch config is repository-global, so a worktree that pushed with -u while
// sitting on main repoints branch.main.merge for every checkout.
func TestNativeSweepRepairsMainUpstream(t *testing.T) {
	root := t.TempDir()
	repository, _ := createNativeTestRepository(t, root, "owner", "one")
	testGit(t, repository, "branch", "session")
	testGit(t, repository, "push", "-u", "origin", "session")
	testGit(t, repository, "config", "branch.main.merge", "refs/heads/session")
	runtime := nativeTestRuntime(t, root)

	if err := normalizeNativeRepository(runtime, nativeRepository{
		Owner: "owner", Name: "one", Path: repository,
	}, nativeLiveWorktrees{}); err != nil {
		t.Fatal(err)
	}

	if got := testGit(t, repository, "config", "--get", "branch.main.merge"); got != "refs/heads/main" {
		t.Fatalf("branch.main.merge = %q, want refs/heads/main", got)
	}
}

// Fully-pushed task branches accumulated for the life of a clone because
// nothing reaped them. See agentic-os#1084.
func TestNativeSweepReapsFullyPushedBranchesOnly(t *testing.T) {
	root := t.TempDir()
	repository, _ := createNativeTestRepository(t, root, "owner", "one")
	testGit(t, repository, "branch", "ops/pushed")
	testGit(t, repository, "push", "-u", "origin", "ops/pushed")
	testGit(t, repository, "switch", "-c", "ops/local-only")
	if err := os.WriteFile(filepath.Join(repository, "local.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, repository, "add", "local.txt")
	testGit(t, repository, "commit", "-m", "local only")
	testGit(t, repository, "switch", "main")
	testGit(t, repository, "branch", "aos/claude/zz99")
	testGit(t, repository, "branch", "aos/claude/yy88")
	runtime := nativeTestRuntime(t, root)

	// A lease still naming zz99 is what makes the ID taken, so that one has to
	// survive. yy88 names a session nothing holds any more. agentic-os#1260
	live := nativeLiveWorktrees{}
	live.addArtifacts([]nativeArtifact{
		{Repository: repository, Worktree: filepath.Join(root, "zz99", "one"), Branch: "aos/claude/zz99"},
	})

	if err := normalizeNativeRepository(runtime, nativeRepository{
		Owner: "owner", Name: "one", Path: repository,
	}, live); err != nil {
		t.Fatal(err)
	}

	branches := testGit(t, repository, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if strings.Contains(branches, "ops/pushed") {
		t.Fatalf("fully-pushed branch survived the reap: %s", branches)
	}
	if !strings.Contains(branches, "ops/local-only") {
		t.Fatalf("branch holding a local-only commit was reaped: %s", branches)
	}
	if !strings.Contains(branches, "aos/claude/zz99") {
		t.Fatalf("a leased session branch was reaped, which breaks ID uniqueness: %s", branches)
	}
	if strings.Contains(branches, "aos/claude/yy88") {
		t.Fatalf("an unleased fully-pushed session branch survived: %s", branches)
	}
}

// Ten leases sat eleven days holding commits with no second copy, and nothing
// said so. Preserving them is right; the silence is the defect. agentic-os#1084
func TestAPurgedWorktreeHoldingUnpushedCommitsIsReported(t *testing.T) {
	root := t.TempDir()
	createNativeTestRepository(t, root, "owner", "one")
	runtime := nativeTestRuntime(t, root)
	stderr := captureNativeStderr(t, &runtime)
	writeNativeTestPlan(t, runtime.PlanFile, "one")
	writeNativeTestList(t, runtime.FleetFile, "owner")
	if _, err := prepareNativeLaunch(runtime, "codex"); err != nil {
		t.Fatal(err)
	}
	leasePath, lease := onlyNativeLease(t, runtime)
	worktree := lease.Artifacts[0].Worktree
	testGit(t, worktree, "config", "user.email", "test@example.com")
	testGit(t, worktree, "config", "user.name", "AOS Test")
	if err := os.WriteFile(filepath.Join(worktree, "local.txt"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, worktree, "add", "local.txt")
	testGit(t, worktree, "commit", "-m", "local only")

	// The state the issue measured: the worktree is gone, so the branch ref is
	// the only copy of that commit.
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatal(err)
	}
	lease.PID = 0
	lease.ProcessStart = ""
	deadSince := runtime.Now.Add(-nativeDeadSessionGrace)
	lease.DeadSince = &deadSince
	if err := writeNativeJSON(leasePath, lease); err != nil {
		t.Fatal(err)
	}

	if _, err := cleanDeadNativeSessions(runtime); err != nil {
		t.Fatal(err)
	}

	report := stderr()
	if !strings.Contains(report, "hold 1 unpushed commit(s)") {
		t.Fatalf("stuck lease was not surfaced: %q", report)
	}
	if !strings.Contains(report, lease.Artifacts[0].Branch) {
		t.Fatalf("report does not name the branch to act on: %q", report)
	}
	// Still preserved. Reporting must not become deleting.
	if _, err := nativeGit(lease.Artifacts[0].Repository,
		"show-ref", "--verify", "--quiet", "refs/heads/"+lease.Artifacts[0].Branch); err != nil {
		t.Fatal("the branch holding the only copy was deleted")
	}
}

func TestAFullyPushedPurgedWorktreeIsNotReported(t *testing.T) {
	// The control. A branch git can recover from origin releases silently, and
	// a line about it every launch is noise that trains the eye past the real one.
	root := t.TempDir()
	createNativeTestRepository(t, root, "owner", "one")
	runtime := nativeTestRuntime(t, root)
	stderr := captureNativeStderr(t, &runtime)
	writeNativeTestPlan(t, runtime.PlanFile, "one")
	writeNativeTestList(t, runtime.FleetFile, "owner")
	if _, err := prepareNativeLaunch(runtime, "codex"); err != nil {
		t.Fatal(err)
	}
	leasePath, lease := onlyNativeLease(t, runtime)
	if err := os.RemoveAll(lease.Artifacts[0].Worktree); err != nil {
		t.Fatal(err)
	}
	lease.PID = 0
	lease.ProcessStart = ""
	deadSince := runtime.Now.Add(-nativeDeadSessionGrace)
	lease.DeadSince = &deadSince
	if err := writeNativeJSON(leasePath, lease); err != nil {
		t.Fatal(err)
	}

	if _, err := cleanDeadNativeSessions(runtime); err != nil {
		t.Fatal(err)
	}

	if report := stderr(); strings.Contains(report, "unpushed commit") {
		t.Fatalf("a recoverable branch was reported as stuck: %q", report)
	}
}

// nativeRuntime.Stderr is an *os.File, so a test reads the report back off disk.
func captureNativeStderr(t *testing.T, runtime *nativeRuntime) func() string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stderr.log")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	runtime.Stderr = file
	return func() string {
		if err := file.Sync(); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
}

// Six of twelve resident checkouts were off main and nothing said so, and one
// of them silently changed what a composed artifact recorded. agentic-os#1033
func TestAResidentCheckoutOffMainIsReported(t *testing.T) {
	root := t.TempDir()
	createNativeTestRepository(t, root, "owner", "one")
	repository := nativeRepository{
		Owner: "owner", Name: "one",
		Path: filepath.Join(root, "projects", "owner", "one"),
	}
	testGit(t, repository.Path, "switch", "-c", "feature/drifted")

	drift, ok := readNativeResidentDrift(repository)

	if !ok {
		t.Fatal("a checkout off main was not reported")
	}
	if drift.branch != "feature/drifted" {
		t.Fatalf("wrong branch reported: %q", drift.branch)
	}
}

func TestACleanCheckoutOnMainIsSilent(t *testing.T) {
	// The control. A line every launch on a healthy fleet trains the eye past
	// the one that matters.
	root := t.TempDir()
	createNativeTestRepository(t, root, "owner", "one")
	repository := nativeRepository{
		Owner: "owner", Name: "one",
		Path: filepath.Join(root, "projects", "owner", "one"),
	}

	if _, ok := readNativeResidentDrift(repository); ok {
		t.Fatal("a clean checkout on main was reported as drifted")
	}
}

func TestADirtyCheckoutOnMainIsStillReported(t *testing.T) {
	// Being on main is not enough: uncommitted changes alter what a tool reads.
	root := t.TempDir()
	createNativeTestRepository(t, root, "owner", "one")
	repository := nativeRepository{
		Owner: "owner", Name: "one",
		Path: filepath.Join(root, "projects", "owner", "one"),
	}
	if err := os.WriteFile(filepath.Join(repository.Path, "scratch.txt"),
		[]byte("uncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	drift, ok := readNativeResidentDrift(repository)

	if !ok || !slices.Contains(drift.reasons, "dirty") {
		t.Fatalf("a dirty checkout on main was not reported: %+v", drift)
	}
}

func TestADetachedCheckoutIsNotDrift(t *testing.T) {
	// Detaching is how a shadow releases main, so it must not read as drift.
	root := t.TempDir()
	createNativeTestRepository(t, root, "owner", "one")
	repository := nativeRepository{
		Owner: "owner", Name: "one",
		Path: filepath.Join(root, "projects", "owner", "one"),
	}
	testGit(t, repository.Path, "switch", "--detach")

	if _, ok := readNativeResidentDrift(repository); ok {
		t.Fatal("a detached checkout was reported as drifted")
	}
}

func TestALiveSessionsCheckoutIsNotReported(t *testing.T) {
	root := t.TempDir()
	createNativeTestRepository(t, root, "owner", "one")
	runtime := nativeTestRuntime(t, root)
	stderr := captureNativeStderr(t, &runtime)
	repository := nativeRepository{
		Owner: "owner", Name: "one",
		Path: filepath.Join(root, "projects", "owner", "one"),
	}
	testGit(t, repository.Path, "switch", "-c", "feature/drifted")
	live := nativeLiveWorktrees{}
	live.add(repository.Path)

	reportNativeResidentDrift(runtime, []nativeRepository{repository}, live)

	if report := stderr(); strings.Contains(report, "resident checkout") {
		t.Fatalf("a checkout a live session holds was reported: %q", report)
	}
}

// An absent plan fell back to a seed expecting almost nothing, so every clean
// fleet checkout became a deletion candidate. agentic-os#903
func TestAnAbsentPlanNeverDeletesACheckout(t *testing.T) {
	root := t.TempDir()
	repository, _ := createNativeTestRepository(t, root, "coilyco-gaming", "steam-ops")
	testRuntime := nativeTestRuntime(t, root)
	// No plan file at all, and a fleet list that admits the org.
	writeNativeTestList(t, testRuntime.FleetFile, "coilyco-gaming")

	projection, err := resolveExpectedRepositories(testRuntime)
	if err != nil {
		t.Fatal(err)
	}
	expected := projection.Expected
	if expected.Authoritative {
		t.Fatal("an absent plan reported itself authoritative")
	}
	state := nativeSweepState{
		Format: "agentic-os.native-sweep.v1", Candidates: map[string]nativeCandidate{},
	}
	for scan := 1; scan <= nativeDeleteScans; scan++ {
		testRuntime.Now = testRuntime.Now.Add(nativeSweepInterval)
		if err := runNativeWorkspaceSweep(
			testRuntime, nil, expected, nativeLiveWorktrees{}, state,
		); err != nil {
			t.Fatal(err)
		}
		_ = readNativeJSON(nativeStatePath(testRuntime, "sweep.json"), &state)
	}

	if _, err := os.Stat(repository); err != nil {
		t.Fatalf("an unverified plan deleted a checkout: %v", err)
	}
	if len(state.Candidates) != 0 {
		t.Fatalf("candidate state was written from an unverified plan: %v", state.Candidates)
	}
}

func TestAValidPlanStaysAuthoritative(t *testing.T) {
	// The control. Failing closed must not disable the scan outright.
	root := t.TempDir()
	createNativeTestRepository(t, root, "owner", "one")
	testRuntime := nativeTestRuntime(t, root)
	writeNativeTestPlan(t, testRuntime.PlanFile, "one")
	writeNativeTestList(t, testRuntime.FleetFile, "owner")

	projection, err := resolveExpectedRepositories(testRuntime)
	if err != nil {
		t.Fatal(err)
	}

	if !projection.Expected.Authoritative {
		t.Fatal("a valid plan did not report itself authoritative")
	}
}

func TestAMissingRequiredRepositoryIsNamed(t *testing.T) {
	// Silently omitting a required repository is how a role composes less than
	// the plan promised, with nothing saying so.
	root := t.TempDir()
	testRuntime := nativeTestRuntime(t, root)
	stderr := captureNativeStderr(t, &testRuntime)
	writeNativeRequiredTestPlan(t, testRuntime.PlanFile, "owner/absent")
	writeNativeTestList(t, testRuntime.FleetFile, "owner")

	if _, err := resolveExpectedRepositories(testRuntime); err != nil {
		t.Fatal(err)
	}

	report := stderr()
	if !strings.Contains(report, "owner/absent") {
		t.Fatalf("a missing required repository was not named: %q", report)
	}
}

func TestAPresentRequiredRepositoryIsSilent(t *testing.T) {
	root := t.TempDir()
	createNativeTestRepository(t, root, "owner", "one")
	testRuntime := nativeTestRuntime(t, root)
	stderr := captureNativeStderr(t, &testRuntime)
	writeNativeRequiredTestPlan(t, testRuntime.PlanFile, "owner/one")
	writeNativeTestList(t, testRuntime.FleetFile, "owner")

	if _, err := resolveExpectedRepositories(testRuntime); err != nil {
		t.Fatal(err)
	}

	if report := stderr(); strings.Contains(report, "required") {
		t.Fatalf("a present required repository was reported: %q", report)
	}
}

// writeNativeRequiredTestPlan writes a plan whose residency entries are all
// required, which is the case a missing checkout has to be loud about.
func writeNativeRequiredTestPlan(t *testing.T, path string, identities ...string) {
	t.Helper()
	slices.Sort(identities)
	projects := filepath.Join(filepath.Dir(path), "projects")
	residency := make([]aosRepositorySelection, 0, len(identities))
	for _, identity := range identities {
		residency = append(residency, aosRepositorySelection{
			Identity: identity,
			Path:     filepath.Join(projects, filepath.FromSlash(identity)),
			Source:   "test", Scope: "role-union", Reason: "test repository",
			Required: true,
		})
	}
	payload := aosRepositoryPlan{
		Format:       agentComposeRepositoryPlanYAMLFormat,
		ProjectsRoot: projects,
		Roles:        map[string][]aosRepositorySelection{},
		Residency:    residency,
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := yaml.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// A resident deploy sat 421 commits behind on one untracked file, clean on main
// by every other reading, and "dirty" alone does not convey that. agentic-os#1033
func TestABehindCheckoutReportsHowFarBehind(t *testing.T) {
	root := t.TempDir()
	repository := nativeRepository{
		Owner: "owner", Name: "one",
		Path: filepath.Join(root, "projects", "owner", "one"),
	}
	createNativeTestRepository(t, root, "owner", "one")
	// Push a commit, then step the checkout back, so origin is ahead exactly as
	// it is on a host whose normalization has been skipped for a while.
	if err := os.WriteFile(filepath.Join(repository.Path, "ahead.txt"),
		[]byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, repository.Path, "add", "ahead.txt")
	testGit(t, repository.Path, "commit", "-m", "ahead of the checkout")
	testGit(t, repository.Path, "push", "origin", "main")
	testGit(t, repository.Path, "reset", "--hard", "HEAD~1")

	drift, ok := readNativeResidentDrift(repository)

	if !ok {
		t.Fatal("a checkout behind origin was not reported")
	}
	if !slices.Contains(drift.reasons, "1 behind origin") {
		t.Fatalf("the gap was not quantified: %+v", drift.reasons)
	}
}

func TestAnUpToDateCheckoutReportsNoGap(t *testing.T) {
	root := t.TempDir()
	createNativeTestRepository(t, root, "owner", "one")
	repository := nativeRepository{
		Owner: "owner", Name: "one",
		Path: filepath.Join(root, "projects", "owner", "one"),
	}

	if _, ok := readNativeResidentDrift(repository); ok {
		t.Fatal("an up-to-date checkout was reported as drifted")
	}
}

// writeNativeTestRolePlan writes a plan whose residency holds every identity
// and whose named role selects only the ones listed for it.
func writeNativeTestRolePlan(
	t *testing.T,
	path string,
	role string,
	selected []string,
	residency []string,
) {
	t.Helper()
	projects := filepath.Join(filepath.Dir(path), "projects")
	build := func(identities []string) []aosRepositorySelection {
		sorted := slices.Clone(identities)
		slices.Sort(sorted)
		selections := make([]aosRepositorySelection, 0, len(sorted))
		for _, identity := range sorted {
			selections = append(selections, aosRepositorySelection{
				Identity: identity,
				Path:     filepath.Join(projects, filepath.FromSlash(identity)),
				Source:   "test", Scope: "role", Reason: "test repository",
			})
		}
		return selections
	}
	payload := aosRepositoryPlan{
		Format:       agentComposeRepositoryPlanYAMLFormat,
		ProjectsRoot: projects,
		Roles:        map[string][]aosRepositorySelection{role: build(selected)},
		Residency:    build(residency),
	}
	raw, err := yaml.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRoleProjectionLinksOnlyTheRoleSelections(t *testing.T) {
	root := t.TempDir()
	createNativeTestRepository(t, root, "coilyco-gaming", "galaxy-gen")
	createNativeTestRepository(t, root, "owner", "one")
	testRuntime := nativeTestRuntime(t, root)
	testRuntime.Role = "platform"
	writeNativeTestRolePlan(t, testRuntime.PlanFile, "platform",
		[]string{"owner/one"},
		[]string{"coilyco-gaming/galaxy-gen", "owner/one"})
	writeNativeTestList(t, testRuntime.FleetFile, "coilyco-gaming", "owner")

	projection, err := resolveExpectedRepositories(testRuntime)
	if err != nil {
		t.Fatal(err)
	}

	if len(projection.Projected) != 1 || projection.Projected[0].Name != "one" {
		t.Fatalf("platform projected %v, want only owner/one", projection.Projected)
	}
	// The fleet pass keeps every resident checkout current whatever the role
	// composes, so narrowing projection must not narrow residency.
	if len(projection.Resident) != 2 {
		t.Fatalf("resident set was narrowed to %d, want 2", len(projection.Resident))
	}
	if !projection.Expected.matches("coilyco-gaming", "galaxy-gen") {
		t.Fatal("a repository outside the role stopped belonging on disk")
	}
}

func TestGamedevProjectionLinksTheGamingCheckouts(t *testing.T) {
	root := t.TempDir()
	createNativeTestRepository(t, root, "coilyco-gaming", "galaxy-gen")
	createNativeTestRepository(t, root, "owner", "one")
	testRuntime := nativeTestRuntime(t, root)
	testRuntime.Role = "gamedev"
	writeNativeTestRolePlan(t, testRuntime.PlanFile, "gamedev",
		[]string{"coilyco-gaming/galaxy-gen", "owner/one"},
		[]string{"coilyco-gaming/galaxy-gen", "owner/one"})
	writeNativeTestList(t, testRuntime.FleetFile, "coilyco-gaming", "owner")

	projection, err := resolveExpectedRepositories(testRuntime)
	if err != nil {
		t.Fatal(err)
	}

	if len(projection.Projected) != 2 {
		t.Fatalf("gamedev projected %d repositories, want 2", len(projection.Projected))
	}
}

func TestAnUnknownRoleKeepsFullResidency(t *testing.T) {
	root := t.TempDir()
	createNativeTestRepository(t, root, "coilyco-gaming", "galaxy-gen")
	createNativeTestRepository(t, root, "owner", "one")
	testRuntime := nativeTestRuntime(t, root)
	testRuntime.Role = "absent"
	writeNativeTestRolePlan(t, testRuntime.PlanFile, "platform",
		[]string{"owner/one"},
		[]string{"coilyco-gaming/galaxy-gen", "owner/one"})
	writeNativeTestList(t, testRuntime.FleetFile, "coilyco-gaming", "owner")

	projection, err := resolveExpectedRepositories(testRuntime)
	if err != nil {
		t.Fatal(err)
	}

	if len(projection.Projected) != 2 {
		t.Fatalf("an unnamed role projected %d repositories, want the full 2",
			len(projection.Projected))
	}
}

// A role whose every selection is missing from disk would otherwise link
// nothing, which drops the session into the canonical checkout silently.
func TestARoleWithNoCheckoutOnDiskKeepsFullResidency(t *testing.T) {
	root := t.TempDir()
	createNativeTestRepository(t, root, "owner", "one")
	testRuntime := nativeTestRuntime(t, root)
	testRuntime.Role = "platform"
	writeNativeTestRolePlan(t, testRuntime.PlanFile, "platform",
		[]string{"owner/absent"},
		[]string{"owner/absent", "owner/one"})
	writeNativeTestList(t, testRuntime.FleetFile, "owner")

	projection, err := resolveExpectedRepositories(testRuntime)
	if err != nil {
		t.Fatal(err)
	}

	if len(projection.Projected) != 1 || projection.Projected[0].Name != "one" {
		t.Fatalf("projected %v, want the full residency fallback", projection.Projected)
	}
}

// 78 local branches, 13 reported: a hand-made branch holding real work was
// silent forever. agentic-os#1286
func TestABranchNoLeaseRecordedIsReported(t *testing.T) {
	root := t.TempDir()
	repository, _ := createNativeTestRepository(t, root, "owner", "one")
	target := nativeRepository{Owner: "owner", Name: "one", Path: repository}
	testGit(t, repository, "switch", "-c", "task/by-hand")
	if err := os.WriteFile(filepath.Join(repository, "work.txt"),
		[]byte("unlanded\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, repository, "add", "work.txt")
	testGit(t, repository, "commit", "-m", "work nothing carries")
	testGit(t, repository, "switch", "main")

	orphans := readNativeOrphanBranches(target, nativeLiveWorktrees{})

	if len(orphans) != 1 || orphans[0].branch != "task/by-hand" {
		t.Fatalf("a branch no lease recorded was not reported: %+v", orphans)
	}
	if orphans[0].unlanded != 1 {
		t.Fatalf("wrong unlanded count: %+v", orphans[0])
	}
}

// The reachability test the lease reading uses calls a squash-merged branch
// unpushed, which on this fleet is every landed branch. agentic-os#1286
func TestASquashMergedBranchIsNotAnOrphan(t *testing.T) {
	root := t.TempDir()
	repository, _ := createNativeTestRepository(t, root, "owner", "one")
	target := nativeRepository{Owner: "owner", Name: "one", Path: repository}
	testGit(t, repository, "switch", "-c", "feature/landed")
	if err := os.WriteFile(filepath.Join(repository, "landed.txt"),
		[]byte("landed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, repository, "add", "landed.txt")
	testGit(t, repository, "commit", "-m", "work that lands")
	testGit(t, repository, "switch", "main")
	testGit(t, repository, "merge", "--squash", "feature/landed")
	testGit(t, repository, "commit", "-m", "work that lands")
	testGit(t, repository, "push", "origin", "main")

	if orphans := readNativeOrphanBranches(target, nativeLiveWorktrees{}); len(orphans) != 0 {
		t.Fatalf("a squash-merged branch was reported as holding work: %+v", orphans)
	}
}

func TestAPushedBranchIsNotAnOrphan(t *testing.T) {
	// Origin carries it, so something will release it. That is the whole test.
	root := t.TempDir()
	repository, _ := createNativeTestRepository(t, root, "owner", "one")
	target := nativeRepository{Owner: "owner", Name: "one", Path: repository}
	testGit(t, repository, "switch", "-c", "feature/pushed")
	if err := os.WriteFile(filepath.Join(repository, "pushed.txt"),
		[]byte("pushed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, repository, "add", "pushed.txt")
	testGit(t, repository, "commit", "-m", "work origin carries")
	testGit(t, repository, "push", "origin", "feature/pushed")
	testGit(t, repository, "switch", "main")

	if orphans := readNativeOrphanBranches(target, nativeLiveWorktrees{}); len(orphans) != 0 {
		t.Fatalf("a pushed branch was reported: %+v", orphans)
	}
}

func TestABranchUnderAWorktreeIsNotAnOrphan(t *testing.T) {
	// Someone's current work, not residue. The checked-out branch is excluded
	// whether or not a lease recorded it.
	root := t.TempDir()
	repository, _ := createNativeTestRepository(t, root, "owner", "one")
	target := nativeRepository{Owner: "owner", Name: "one", Path: repository}
	testGit(t, repository, "switch", "-c", "feature/in-progress")
	if err := os.WriteFile(filepath.Join(repository, "wip.txt"),
		[]byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, repository, "add", "wip.txt")
	testGit(t, repository, "commit", "-m", "work in progress")

	if orphans := readNativeOrphanBranches(target, nativeLiveWorktrees{}); len(orphans) != 0 {
		t.Fatalf("the checked-out branch was reported: %+v", orphans)
	}
}

func TestACleanCheckoutReportsNoOrphanBranches(t *testing.T) {
	// The control. A line every launch trains the eye past the one that matters.
	root := t.TempDir()
	repository, _ := createNativeTestRepository(t, root, "owner", "one")
	runtime := nativeTestRuntime(t, root)
	stderr := captureNativeStderr(t, &runtime)
	target := nativeRepository{Owner: "owner", Name: "one", Path: repository}

	reportNativeOrphanBranches(runtime, []nativeRepository{target}, nativeLiveWorktrees{})

	if report := stderr(); strings.Contains(report, "untracked local branch") {
		t.Fatalf("a clean checkout reported orphan branches: %q", report)
	}
}

// The standalone case only pins that the credential is NOT copied.
// teable:coilyco-flight-deck/agentic-os#7021.
func TestStageNativeRoleHomeLinksClaudeCredential(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	target := filepath.Join(t.TempDir(), "target")
	if err := os.MkdirAll(filepath.Join(source, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(source, ".claude", ".credentials.json")
	if err := os.WriteFile(credential, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := stageNativeRoleHome(source, target, ""); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(target, ".claude", ".credentials.json")
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("staged session home has no credential: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("credential should link to the host, got mode %s", info.Mode())
	}
	resolved, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(credential)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != want {
		t.Fatalf("credential links to %s, want %s", resolved, want)
	}
}
