package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func completionLines(t *testing.T, args ...string) []string {
	t.Helper()
	out := &bytes.Buffer{}
	writeCompletions(out, loadRosterFixture(t), args)
	text := strings.TrimSpace(out.String())
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func TestCompletionOffersEveryLiveRoleFirst(t *testing.T) {
	lines := completionLines(t)
	slugs := make([]string, 0, len(lines))
	for _, line := range lines {
		slug, detail, found := strings.Cut(line, ":")
		if !found || detail == "" {
			t.Fatalf("every candidate needs a description: %q", line)
		}
		slugs = append(slugs, slug)
	}
	for _, want := range []string{"platform", "sysadmin", "science", "frontend", "gamedev", "director", "advocate", "analyst"} {
		if !contains(slugs, want) {
			t.Fatalf("completion should offer %q: %v", want, slugs)
		}
	}
}

func TestCompletionOffersOnlyTheChosenRoleSeats(t *testing.T) {
	lines := completionLines(t, "sysadmin")
	seats := make([]string, 0, len(lines))
	for _, line := range lines {
		seat, _, _ := strings.Cut(line, ":")
		seats = append(seats, seat)
	}
	if !contains(seats, "goose") {
		t.Fatalf("sysadmin should offer its goose seat: %v", seats)
	}
	// goose belongs to sysadmin alone, so it must not leak into another role.
	platform := completionLines(t, "platform")
	for _, line := range platform {
		if strings.HasPrefix(line, "goose:") {
			t.Fatalf("goose is not a platform seat: %v", platform)
		}
	}
}

// A catalogue seat with no native harness cannot be launched, so completing it
// would offer a candidate the launcher then refuses.
func TestCompletionNeverOffersAnUnlaunchableSeat(t *testing.T) {
	for _, line := range completionLines(t, "frontend") {
		if strings.HasPrefix(line, "penpot:") {
			t.Fatal("penpot has no native harness and must not be completable")
		}
	}
}

func TestCompletionIsSilentPastTheSeat(t *testing.T) {
	if lines := completionLines(t, "platform", "claude"); lines != nil {
		t.Fatalf("harness arguments are not ours to complete: %v", lines)
	}
}

func TestCompletionIsSilentForARoleThatLeftTheRoster(t *testing.T) {
	if lines := completionLines(t, "engineer"); lines != nil {
		t.Fatalf("a stale role has no seats to offer: %v", lines)
	}
}

// Both shipped shell scripts split a candidate on its first colon, so a colon
// or newline in a description would truncate or forge a candidate.
func TestCompletionDetailSurvivesTheShellScripts(t *testing.T) {
	cases := map[string]string{
		"plain":              "plain",
		"with: a colon":      "with  a colon",
		"first\nsecond":      "first",
		"  padded  ":         "padded",
		"trailing\r\nsecond": "trailing",
	}
	for input, want := range cases {
		if got := completionDetail(input); got != want {
			t.Fatalf("completionDetail(%q) = %q, want %q", input, got, want)
		}
	}
	for _, line := range completionLines(t) {
		if strings.Count(line, ":") != 1 {
			t.Fatalf("a candidate must carry exactly one colon: %q", line)
		}
	}
}

func TestSeatDetailDegradesWithoutPronounsOrTier(t *testing.T) {
	cases := []struct {
		seat rosterSeat
		want string
	}{
		{rosterSeat{Name: "Angie", Pronouns: "she", Tier: "frontier"}, "Angie [she] // frontier"},
		{rosterSeat{Name: "Angie", Pronouns: "she"}, "Angie [she]"},
		{rosterSeat{Name: "Angie"}, "Angie"},
		{rosterSeat{Tier: "frontier"}, "frontier"},
		{rosterSeat{}, ""},
	}
	for _, testCase := range cases {
		if got := seatDetail(testCase.seat); got != testCase.want {
			t.Fatalf("seatDetail(%+v) = %q, want %q", testCase.seat, got, testCase.want)
		}
	}
}

// Completion runs on a keystroke, so an unreachable roster has to be silence
// rather than a diagnostic printed mid-word. See docs/aterm.md.
func TestCompletionStaysSilentWhenTheRosterIsUnreachable(t *testing.T) {
	var spawns []recordedSpawn
	deps := stubDeps(t, &spawns, true)
	deps.output = func(context.Context, string, ...string) ([]byte, error) {
		return nil, context.DeadlineExceeded
	}
	command := newCommand(deps)
	out := &bytes.Buffer{}
	command.Writer = out
	err := command.Run(context.Background(), []string{"aterm", "--generate-shell-completion"})
	if err != nil {
		t.Fatalf("completion must not fail the shell: %v", err)
	}
	if out.String() != "" {
		t.Fatalf("an unreachable roster should complete nothing: %q", out.String())
	}
}

// Enabling completion adds a `completion` subcommand, which must not capture
// the first positional the launcher reads as a role.
func TestCompletionSubcommandDoesNotShadowTheRolePositional(t *testing.T) {
	var spawns []recordedSpawn
	out, err := runAterm(t, stubDeps(t, &spawns, true), "--dry-run", "--json", "platform", "claude")
	if err != nil {
		t.Fatalf("a role positional should still launch: %v", err)
	}
	if !strings.Contains(out, `"role": "platform"`) {
		t.Fatalf("the role positional was lost: %s", out)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
