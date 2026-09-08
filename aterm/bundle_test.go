package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func testSpec() bundleSpec {
	return bundleSpec{
		Role:             "platform",
		DisplayName:      "Agentic Platform Engineer",
		Person:           "Angie",
		Version:          "1.2.3",
		BakedPath:        "/opt/homebrew/bin:/usr/bin:/bin",
		WorkingDirectory: "/Users/kai/projects",
		ATermBin:         "/opt/homebrew/bin/aterm",
		AOSBin:           "/Users/kai/.local/bin/aos",
		AgentComposeBin:  "/opt/homebrew/bin/agent-compose",
		TerminalBin:      "/Applications/kitty.app/Contents/MacOS/kitty",
	}
}

// `agent-compose launch` resolves the harness off PATH, so pinning the three
// binaries aterm itself needs still left "claude not found" after shadow init.
func TestBundleLauncherRebuildsPathRatherThanOnlyPinningBinaries(t *testing.T) {
	launcher := bundleLauncher(testSpec())
	if !strings.Contains(launcher, "/opt/homebrew/bin:/usr/bin:/bin") {
		t.Fatalf("the generation-time PATH should be baked in as the fallback:\n%s", launcher)
	}
	if !strings.Contains(launcher, "/bin/zsh -lc") {
		t.Fatalf("a login shell should supply the current PATH:\n%s", launcher)
	}
	if !strings.Contains(launcher, "export PATH") {
		t.Fatalf("PATH has to be exported to reach the harness:\n%s", launcher)
	}
}

// A Finder launch starts on a login PATH carrying none of these, so a bare name
// in the wrapper is the failure this bundle is generated rather than written.
func TestBundleLauncherBakesInEveryResolvedBinary(t *testing.T) {
	launcher := bundleLauncher(testSpec())
	for _, binary := range []string{
		"/opt/homebrew/bin/aterm",
		"/Users/kai/.local/bin/aos",
		"/opt/homebrew/bin/agent-compose",
		"/Applications/kitty.app/Contents/MacOS/kitty",
	} {
		if !strings.Contains(launcher, binary) {
			t.Fatalf("the launcher should bake in %q:\n%s", binary, launcher)
		}
	}
}

// aterm stops reading flags at the first positional, so a working directory
// after the role would reach the harness as an argument instead.
func TestBundleLauncherPutsTheWorkingDirectoryBeforeTheRole(t *testing.T) {
	launcher := bundleLauncher(testSpec())
	directory := strings.Index(launcher, "--working-directory")
	role := strings.Index(launcher, "'platform'")
	if directory < 0 || role < 0 {
		t.Fatalf("the launcher should carry both the flag and the role:\n%s", launcher)
	}
	if directory > role {
		t.Fatalf("the working directory should precede the role:\n%s", launcher)
	}
}

func TestBundleLauncherQuotesAPathHoldingASingleQuote(t *testing.T) {
	spec := testSpec()
	spec.WorkingDirectory = "/Users/kai/kai's projects"
	if !strings.Contains(bundleLauncher(spec), `'/Users/kai/kai'\''s projects'`) {
		t.Fatal("the quote should be escaped rather than closing the string")
	}
}

// Hiding from the Dock was right while the window belonged to kitty. It is
// this bundle's now, and an accessory app cannot own the front window.
func TestBundleInfoPlistLetsTheSessionWindowClaimTheTile(t *testing.T) {
	plist := bundleInfoPlist(testSpec())
	if strings.Contains(plist, "LSUIElement") {
		t.Fatalf("an accessory app cannot own the window it opened:\n%s", plist)
	}
}

// The linked terminal is what makes the window this app's rather than the
// terminal's, so the launcher runs the one inside the bundle it lives in.
func TestBundleLauncherOpensThroughTheBundlesOwnTerminal(t *testing.T) {
	launcher := bundleLauncher(testSpec())
	if !strings.Contains(launcher, `here=$(cd "$(dirname "$0")" && pwd)`) {
		t.Fatalf("the launcher should locate its own bundle:\n%s", launcher)
	}
	if !strings.Contains(launcher, `ATERM_TERMINAL_BIN="$here/kitty"`) {
		t.Fatalf("the session should open through the linked terminal:\n%s", launcher)
	}
	// A bundle whose link went missing still opens a window, unbranded.
	if !strings.Contains(launcher, "if [ ! -x \"$ATERM_TERMINAL_BIN\" ]") {
		t.Fatalf("a missing link should fall back rather than fail:\n%s", launcher)
	}
}

func TestWriteBundleLinksTheTerminalBesideTheLauncher(t *testing.T) {
	root := t.TempDir()
	terminal := filepath.Join(root, "kitty-real")
	if err := os.WriteFile(terminal, []byte{0xcf, 0xfa, 0xed, 0xfe}, 0o755); err != nil {
		t.Fatalf("write the terminal fixture: %v", err)
	}
	item := bundleItem{
		Path:       filepath.Join(root, "Angie :: Agentic Platform Engineer.app"),
		Executable: "aterm-platform",
		Terminal:   terminal,
		Launcher:   "#!/bin/sh\n" + bundleMarker + "\n",
		Plist:      bundleInfoPlist(testSpec()),
	}
	if err := writeBundle(item, "", ""); err != nil {
		t.Fatalf("write the bundle: %v", err)
	}
	link := filepath.Join(item.Path, "Contents", "MacOS", bundleTerminalName)
	got, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("the bundle should carry a linked terminal: %v", err)
	}
	if got != terminal {
		t.Fatalf("linked %q, want %q", got, terminal)
	}
	// The stale scan reads what is beside the launcher, and reading a linked
	// application binary to look for a marker is a hundred megabytes wasted.
	if role, ours := generatedRole(item.Path); !ours || role != "platform" {
		t.Fatalf("generatedRole = %q, %v", role, ours)
	}
}

// `brew upgrade aos` inside a session refused an arm-only formula because
// every session was an x86_64 one. See docs/aterm.md and agentic-os#1291.
func TestBundleInfoPlistNamesTheArchitectureLaunchServicesShouldPick(t *testing.T) {
	plist := bundleInfoPlist(testSpec())
	want := "<string>" + machineArchitecture(runtime.GOARCH) + "</string>"
	if !strings.Contains(plist, "<key>LSArchitecturePriority</key>") || !strings.Contains(plist, want) {
		t.Fatalf("the plist should name %s:\n%s", want, plist)
	}
}

func TestMachineArchitectureSpellsBothMacArchitectures(t *testing.T) {
	for goarch, want := range map[string]string{"arm64": "arm64", "amd64": "x86_64"} {
		if got := machineArchitecture(goarch); got != want {
			t.Fatalf("machineArchitecture(%q) = %q, want %q", goarch, got, want)
		}
	}
}

func TestBundleInfoPlistGivesEachRoleItsOwnIdentifier(t *testing.T) {
	second := testSpec()
	second.Role = "sysadmin"
	if !strings.Contains(bundleInfoPlist(testSpec()), "me.coilysiren.aterm.platform") {
		t.Fatal("the identifier should carry the role")
	}
	if strings.Contains(bundleInfoPlist(second), "me.coilysiren.aterm.platform") {
		t.Fatal("two roles sharing an identifier would collide in LaunchServices")
	}
}

func TestBundleInfoPlistEscapesTheIdentityLine(t *testing.T) {
	spec := testSpec()
	spec.DisplayName = "Research & Development"
	if !strings.Contains(bundleInfoPlist(spec), "Research &amp; Development") {
		t.Fatal("the display name should be XML-escaped")
	}
}

func TestBundleInfoPlistNamesAnIconOnlyWhenThereIsOne(t *testing.T) {
	if strings.Contains(bundleInfoPlist(testSpec()), "CFBundleIconFile") {
		t.Fatal("without an icon the bundle should fall back to the system one")
	}
	spec := testSpec()
	spec.Icon = true
	if !strings.Contains(bundleInfoPlist(spec), "CFBundleIconFile") {
		t.Fatal("an icon should be named in the plist")
	}
}

func writeFixtureBundle(t *testing.T, root, name, body string) string {
	t.Helper()
	path := filepath.Join(root, name+".app")
	executable := filepath.Join(path, "Contents", "MacOS", name)
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatalf("create the fixture: %v", err)
	}
	if err := os.WriteFile(executable, []byte(body), 0o755); err != nil {
		t.Fatalf("write the fixture: %v", err)
	}
	return path
}

// A slug that turned over leaves a tile opening nothing, and its refusal only
// fires once someone clicks it. Naming it at generation is the earlier warning.
func TestStaleBundlesNamesWhatThisRunNoLongerWritesAndSkipsAForeignApp(t *testing.T) {
	root := t.TempDir()
	retired := writeFixtureBundle(t, root, "Rex :: Retired Role", "#!/bin/sh\n"+bundleMarker+"\n")
	kept := writeFixtureBundle(t, root, "Angie :: Agentic Platform Engineer",
		"#!/bin/sh\n"+bundleMarker+"\n")
	writeFixtureBundle(t, root, "Some Other App", "#!/bin/sh\nexit 0\n")
	stale := staleBundles(root, map[string]bool{kept: true})
	if len(stale) != 1 || stale[0] != retired {
		t.Fatalf("only the bundle this run no longer writes is stale, got %v", stale)
	}
}

// The scheme renamed once already. Matching on a filename would have orphaned
// every bundle the previous release wrote instead of reporting it.
func TestStaleBundlesFindsABundleWrittenUnderTheOldNamingScheme(t *testing.T) {
	root := t.TempDir()
	old := writeFixtureBundle(t, root, "aterm platform", "#!/bin/sh\n"+bundleMarker+"\n")
	stale := staleBundles(root, map[string]bool{})
	if len(stale) != 1 || stale[0] != old {
		t.Fatalf("an old-scheme bundle should still be recognized as ours, got %v", stale)
	}
}

func TestReplaceableGuardsAnAppThisCommandDidNotWrite(t *testing.T) {
	root := t.TempDir()
	mine := writeFixtureBundle(t, root, "Angie :: Agentic Platform Engineer",
		"#!/bin/sh\n"+bundleMarker+"\n")
	theirs := writeFixtureBundle(t, root, "Vera :: Systems Administrator",
		"#!/bin/sh\nexit 0\n")
	if err := replaceable(mine); err != nil {
		t.Fatalf("regenerating our own bundle should be allowed: %v", err)
	}
	if err := replaceable(theirs); err == nil {
		t.Fatal("an app this command did not write should stay untouched")
	}
	if err := replaceable(filepath.Join(root, "Nobody.app")); err != nil {
		t.Fatalf("a missing bundle is not a conflict: %v", err)
	}
}

func TestWriteBundleProducesAnExecutableLauncherAndAValidLayout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the bundle layout is macOS only")
	}
	spec := testSpec()
	item := bundleItem{
		Role:       spec.Role,
		Person:     spec.Person,
		Identifier: spec.identifier(),
		Path:       filepath.Join(t.TempDir(), spec.name()+".app"),
		Executable: spec.executable(),
		Launcher:   bundleLauncher(spec),
		Plist:      bundleInfoPlist(spec),
	}
	if err := writeBundle(item, "", ""); err != nil {
		t.Fatalf("write the bundle: %v", err)
	}
	info, err := os.Stat(filepath.Join(item.Path, "Contents", "MacOS", item.Executable))
	if err != nil {
		t.Fatalf("stat the launcher: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("the launcher must be executable, got %v", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(item.Path, "Contents", "Info.plist")); err != nil {
		t.Fatalf("stat the plist: %v", err)
	}
}

// `--dry-run` renders for a person and `--dry-run --json` carries the machine
// plan, matching what the launcher's own dry run does. See docs/aterm.md.
func TestRenderBundlePlanNamesEveryBundleAndItsStaleLeftovers(t *testing.T) {
	plan := bundlePlan{
		Output: "/Users/kai/Applications",
		Items: []bundleItem{{
			Role:   "platform",
			Person: "Angie",
			Name:   "Angie // Agentic Platform Engineer",
			Path:   "/Users/kai/Applications/Angie :: Agentic Platform Engineer.app",
		}},
		Stale: []string{"/Users/kai/Applications/Rex :: Retired.app"},
	}
	rendered := &strings.Builder{}
	if err := renderBundlePlan(rendered, plan); err != nil {
		t.Fatalf("render: %v", err)
	}
	// The rendered name is the one the Dock draws, not the one on disk.
	for _, want := range []string{"/Users/kai/Applications", "platform",
		"Angie // Agentic Platform Engineer", "Retired"} {
		if !strings.Contains(rendered.String(), want) {
			t.Fatalf("the rendered plan should name %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered.String(), "\"format\"") {
		t.Fatal("the rendered plan is for a person, not the JSON one")
	}
}

func TestRenderBundlePlanSaysWhenThereIsNoIcon(t *testing.T) {
	rendered := &strings.Builder{}
	if err := renderBundlePlan(rendered, bundlePlan{Output: "/tmp"}); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(rendered.String(), "system app icon") {
		t.Fatalf("an absent icon should be named rather than blank:\n%s", rendered)
	}
}

// Kai asked for "Angie // Agentic Platform Engineer", and a POSIX filename
// cannot hold a slash. macOS renders a stored colon as one, so it round-trips.
func TestBundleNameStoresTheHouseSeparatorAsMacOSRendersIt(t *testing.T) {
	spec := testSpec()
	if got := spec.name(); got != "Angie :: Agentic Platform Engineer" {
		t.Fatalf("on-disk name is %q", got)
	}
	if got := spec.displayName(); got != "Angie // Agentic Platform Engineer" {
		t.Fatalf("displayed name is %q", got)
	}
	if strings.Contains(spec.name(), "/") {
		t.Fatal("a slash in the basename would read as a path separator")
	}
}

// A display name reaching the filesystem is the one place a stray slash could
// still arrive, since the roster is not this binary's to constrain.
func TestBundleNameNeutralizesASlashComingFromTheRoster(t *testing.T) {
	spec := testSpec()
	spec.DisplayName = "Research/Development"
	if strings.Contains(spec.name(), "/") {
		t.Fatalf("the roster's slash should not survive into the basename: %q", spec.name())
	}
}

func TestBundleInfoPlistCarriesTheDisplayNameAndAPlainExecutable(t *testing.T) {
	plist := bundleInfoPlist(testSpec())
	if !strings.Contains(plist, "Angie // Agentic Platform Engineer") {
		t.Fatalf("the plist should carry the rendered name:\n%s", plist)
	}
	if !strings.Contains(plist, "<string>aterm-platform</string>") {
		t.Fatalf("the executable should stay a plain slug:\n%s", plist)
	}
}

// A generating session's scratch directories must not outlive it inside seven
// bundles, and a duplicate entry is noise in a file a person may read.
func TestLivePathEntriesKeepsOnlyRealDirectoriesAndDropsRepeats(t *testing.T) {
	real := t.TempDir()
	file := filepath.Join(real, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	joined := strings.Join([]string{real, "/nope/gone", file, real, ""},
		string(filepath.ListSeparator))
	got := livePathEntries(joined)
	if got != real {
		t.Fatalf("only the real directory should survive, got %q", got)
	}
}

// Kai's window options landed while the installed aterm stayed three releases
// back, so half the fix worked and nothing said why. See docs/aterm.md.
func TestBundlePlanWarnsWhenTheBundlesCallADifferentBuild(t *testing.T) {
	plan := bundlePlan{
		Output:        "/Users/kai/Applications",
		Launcher:      "/opt/homebrew/bin/aterm",
		LauncherBuild: "aos-v0.231.0",
		Build:         "aos-v0.242.0",
		Items:         []bundleItem{{Role: "platform", Name: "Angie // X"}},
	}
	if !plan.staleLauncher() {
		t.Fatal("a bundle calling an older aterm carries only what that one does")
	}
	written := &strings.Builder{}
	if err := announceBundles(written, plan); err != nil {
		t.Fatalf("announce: %v", err)
	}
	for _, want := range []string{"aos-v0.231.0", "aos-v0.242.0", "/opt/homebrew/bin/aterm"} {
		if !strings.Contains(written.String(), want) {
			t.Fatalf("the warning should name %q:\n%s", want, written)
		}
	}
	rendered := &strings.Builder{}
	if err := renderBundlePlan(rendered, plan); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(rendered.String(), "aos-v0.231.0") {
		t.Fatalf("the rendered plan should warn too:\n%s", rendered)
	}
}

func TestBundlePlanStaysQuietWhenTheBuildsMatchOrAreUnknown(t *testing.T) {
	same := bundlePlan{LauncherBuild: "aos-v0.242.0", Build: "aos-v0.242.0"}
	if same.staleLauncher() {
		t.Fatal("matching builds are the normal case and earn no warning")
	}
	unknown := bundlePlan{LauncherBuild: "", Build: "aos-v0.242.0"}
	if unknown.staleLauncher() {
		t.Fatal("a version that could not be read is not evidence of a mismatch")
	}
}

// roles.kdl rather than a hand-written list, which agreed with the icons
// whatever either said, and rather than the roster, which lags a release.
func TestEveryDeclaredRoleHasArtAndNoArtIsOrphaned(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", ".agents", "roles.kdl"))
	if err != nil {
		t.Fatalf("read the declared roles: %v", err)
	}
	declared := map[string]bool{}
	for _, match := range regexp.MustCompile(`(?m)^\s*role ([a-z-]+) \{`).
		FindAllStringSubmatch(string(raw), -1) {
		declared[match[1]] = true
	}
	if len(declared) == 0 {
		t.Fatal("no roles parsed out of roles.kdl, so this test proves nothing")
	}

	shipped := map[string]bool{}
	entries, err := roleIcons.ReadDir("icons")
	if err != nil {
		t.Fatalf("read the embedded icons: %v", err)
	}
	for _, entry := range entries {
		shipped[strings.TrimSuffix(entry.Name(), ".icns")] = true
	}

	for role := range declared {
		if !shipped[role] {
			t.Errorf("role %q is declared and has no icon, so it falls back to the system one", role)
		}
	}
	for role := range shipped {
		if !declared[role] {
			t.Errorf("icon %q.icns names no declared role, so nothing will ever read it", role)
		}
	}
	if roleIcon("retired-seat") != nil {
		t.Fatal("a role with no art must resolve to nil, not a broken reference")
	}
}

func TestWriteBundleUsesRoleArtAndPrefersTheSharedIcon(t *testing.T) {
	root := t.TempDir()
	item := bundleItem{
		Role:       "gamedev",
		Path:       filepath.Join(root, "Gale.app"),
		Executable: "gale",
		Launcher:   "#!/bin/sh\n",
		Plist:      "<plist></plist>",
	}
	if err := writeBundle(item, "", ""); err != nil {
		t.Fatalf("write with role art: %v", err)
	}
	written, err := os.ReadFile(
		filepath.Join(item.Path, "Contents", "Resources", bundleIconName+".icns"))
	if err != nil {
		t.Fatalf("role art should have been written: %v", err)
	}
	if !bytes.Equal(written, roleIcon("gamedev")) {
		t.Fatal("the bundle should carry that role's own art")
	}

	shared := filepath.Join(root, "shared.icns")
	if err := os.WriteFile(shared, []byte("shared-icon"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeBundle(item, shared, ""); err != nil {
		t.Fatalf("write with the shared override: %v", err)
	}
	written, err = os.ReadFile(
		filepath.Join(item.Path, "Contents", "Resources", bundleIconName+".icns"))
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != "shared-icon" {
		t.Fatal("--icon should still win over a role's own art")
	}

	item.Role = "retired-seat"
	item.Path = filepath.Join(root, "Nobody.app")
	if err := writeBundle(item, "", ""); err != nil {
		t.Fatalf("a role with no art must still generate: %v", err)
	}
	if _, err := os.Stat(
		filepath.Join(item.Path, "Contents", "Resources")); !os.IsNotExist(err) {
		t.Fatal("no art means no Resources directory, not an empty icns")
	}
}

// brewTree lays out the two halves of a Homebrew install: the versioned Cellar
// a shell may have resolved, and the opt symlink target that outlives it.
func brewTree(t *testing.T, formula, version, tail string) (string, string, string) {
	t.Helper()
	prefix := t.TempDir()
	cellar := filepath.Join(prefix, "Cellar", formula, version, tail)
	opt := filepath.Join(prefix, "opt", formula, tail)
	for _, dir := range []string{cellar, opt} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("make %s: %v", dir, err)
		}
	}
	return prefix, cellar, opt
}

// A Cellar path is a dead directory the moment the formula upgrades, and this
// one gets baked into a generated launcher. agentic-os#1440
func TestBakedPathTradesCellarForTheOptSymlink(t *testing.T) {
	_, cellar, opt := brewTree(t, "go", "1.26.5", filepath.Join("libexec", "bin"))
	got := livePathEntries(cellar)
	if got != opt {
		t.Fatalf("baked %q, want the stable %q", got, opt)
	}
	if strings.Contains(got, "/Cellar/") {
		t.Fatalf("a version-pinned path survived into the bake: %q", got)
	}
}

// Two versions of one formula are the same opt path, so the bake should carry
// it once rather than twice.
func TestBakedPathCollapsesTwoCellarVersions(t *testing.T) {
	prefix, cellar, opt := brewTree(t, "go", "1.26.5", "bin")
	older := filepath.Join(prefix, "Cellar", "go", "1.25.0", "bin")
	if err := os.MkdirAll(older, 0o755); err != nil {
		t.Fatalf("make %s: %v", older, err)
	}
	got := livePathEntries(strings.Join([]string{cellar, older}, string(filepath.ListSeparator)))
	if got != opt {
		t.Fatalf("baked %q, want the single stable %q", got, opt)
	}
}

// An unlinked formula has no opt path, and dropping it would take the only
// route to the binary with it.
func TestBakedPathKeepsACellarWithNoOptEquivalent(t *testing.T) {
	prefix := t.TempDir()
	cellar := filepath.Join(prefix, "Cellar", "unlinked", "0.1.0", "bin")
	if err := os.MkdirAll(cellar, 0o755); err != nil {
		t.Fatalf("make %s: %v", cellar, err)
	}
	if got := livePathEntries(cellar); got != cellar {
		t.Fatalf("baked %q, want the Cellar path kept as the only route", got)
	}
}

// The generated launcher is the artifact that outlives the shell it was
// written from, so the assertion belongs on its bytes too.
func TestGeneratedLauncherCarriesNoVersionPinnedPath(t *testing.T) {
	_, cellar, _ := brewTree(t, "go", "1.26.5", filepath.Join("libexec", "bin"))
	spec := bundleSpec{
		Role: "platform", DisplayName: "Platform Engineer", Person: "Angie",
		BakedPath:       livePathEntries(cellar),
		ATermBin:        "/opt/homebrew/bin/aterm",
		AOSBin:          "/opt/homebrew/bin/aos",
		AgentComposeBin: "/opt/homebrew/bin/agent-compose",
		TerminalBin:     "/opt/homebrew/bin/kitty",
	}
	if launcher := bundleLauncher(spec); strings.Contains(launcher, "/Cellar/") {
		t.Fatalf("the launcher baked a version-pinned path:\n%s", launcher)
	}
}

// A label the width of the column leaves no separator, so the slug runs into
// the name it labels. Both inputs are synthetic: no live slug is long enough.
func TestRenderBundlePlanSeparatesALongSlugFromItsName(t *testing.T) {
	for _, role := range []string{"long-enough", "a-considerably-longer-slug"} {
		plan := bundlePlan{
			Output: "/tmp",
			Items: []bundleItem{{
				Role: role,
				Name: "Cassandra // AI Risk Analyst",
			}},
		}
		rendered := &strings.Builder{}
		if err := renderBundlePlan(rendered, plan); err != nil {
			t.Fatalf("render: %v", err)
		}
		if strings.Contains(rendered.String(), role+"Cassandra") {
			t.Fatalf("%q abuts its name with no separator:\n%s", role, rendered)
		}
		if !strings.Contains(rendered.String(), role+" ") {
			t.Fatalf("%q should be followed by a separator:\n%s", role, rendered)
		}
	}
}

// Every label shares one column, so a long role must not leave the fixed
// labels flush against their values either.
func TestRenderBundlePlanKeepsEveryLabelInOneColumn(t *testing.T) {
	plan := bundlePlan{
		Output: "/tmp",
		Items:  []bundleItem{{Role: "long-enough", Name: "Cassandra // AI Risk Analyst"}},
		Stale:  []string{"/tmp/Rex :: Retired.app"},
	}
	rendered := &strings.Builder{}
	if err := renderBundlePlan(rendered, plan); err != nil {
		t.Fatalf("render: %v", err)
	}
	width := bundleLabelWidth(plan)
	if width <= len("long-enough") {
		t.Fatalf("column %d cannot separate an 11-character slug", width)
	}
	for _, line := range strings.Split(strings.TrimSpace(rendered.String()), "\n")[1:] {
		body := strings.TrimPrefix(line, "  ")
		if len(body) > width && body[width-1] != ' ' {
			t.Fatalf("label column is not clear on %q", line)
		}
	}
}

// A session shadow is reaped, so baking one of its directories into a launcher
// that outlives the session leaves a dead entry. agentic-os#7083
func TestBakedPathDropsASessionShadowEntry(t *testing.T) {
	root := t.TempDir()
	shadow := filepath.Join(root, "aos", "native", "zz99", "home", ".local", "bin")
	durable := filepath.Join(root, "durable", "bin")
	for _, dir := range []string{shadow, durable} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("make %s: %v", dir, err)
		}
	}
	// Both directories exist, so only the shadow rule can tell them apart.
	got := livePathEntries(strings.Join([]string{shadow, durable}, string(filepath.ListSeparator)))
	if got != durable {
		t.Fatalf("baked %q, want only the durable %q", got, durable)
	}
}

// The shadow of the seat running the generator is named in its own environment,
// and the marker alone would miss a root that does not carry it.
func TestBakedPathDropsTheDeclaredSessionRoot(t *testing.T) {
	root := t.TempDir()
	shadow := filepath.Join(root, "session-root", "bin")
	durable := filepath.Join(root, "durable", "bin")
	for _, dir := range []string{shadow, durable} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("make %s: %v", dir, err)
		}
	}
	if got := livePathEntries(shadow); got != shadow {
		t.Fatalf("without the declared root the entry should survive, got %q", got)
	}
	t.Setenv(nativeSessionRootEnv, filepath.Join(root, "session-root"))
	got := livePathEntries(strings.Join([]string{shadow, durable}, string(filepath.ListSeparator)))
	if got != durable {
		t.Fatalf("baked %q, want only the durable %q", got, durable)
	}
}
