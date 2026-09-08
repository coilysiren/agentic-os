package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadRosterFixture(t *testing.T) rosterDocument {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "roster.json"))
	if err != nil {
		t.Fatalf("read roster fixture: %v", err)
	}
	document, err := parseRoster(raw)
	if err != nil {
		t.Fatalf("parse roster fixture: %v", err)
	}
	return document
}

func TestParseRosterRejectsDriftedContracts(t *testing.T) {
	cases := map[string]string{
		"wrong format": `{"format":"agent-compose.catalog.v2","items":[{"slug":"platform"}]}`,
		"no items":     `{"format":"agent-compose.catalog.v1","items":[]}`,
		"unsafe slug":  `{"format":"agent-compose.catalog.v1","items":[{"slug":"../etc"}]}`,
		"not json":     `{`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseRoster([]byte(raw)); err == nil {
				t.Fatal("expected the roster to be rejected")
			}
		})
	}
}

func TestNativeSeatsDropsSeatsWithNoNativeHarness(t *testing.T) {
	document := loadRosterFixture(t)
	frontend, ok := document.role("frontend")
	if !ok {
		t.Fatal("fixture is missing the frontend role")
	}
	if !seatInRole("penpot", frontend) {
		t.Fatal("fixture should carry the penpot catalogue seat")
	}
	for _, seat := range frontend.nativeSeats() {
		if seat.Harness == "penpot" {
			t.Fatal("penpot has no native harness and must not be launchable")
		}
	}
	if len(frontend.nativeSeats()) == 0 {
		t.Fatal("frontend should keep at least one native seat")
	}
}

// A slug that turned over is the common way to reach the error path, so the
// message has to carry the live roster rather than only refusing.
func TestUnknownRoleErrorNamesEveryLiveRole(t *testing.T) {
	document := loadRosterFixture(t)
	message := unknownRoleError("engineer", document).Error()
	for _, slug := range document.slugs() {
		if !strings.Contains(message, slug) {
			t.Fatalf("error should name live role %q: %s", slug, message)
		}
	}
}

func TestSuggestKeepsNearMissesAndDropsShortCollisions(t *testing.T) {
	roles := []string{"platform", "sysadmin", "science", "frontend", "gamedev", "director", "advocate", "analyst"}
	// The near misses are built rather than spelled, so the repo's spell-check
	// does not read a deliberate typo fixture as a real one.
	cases := []struct {
		value string
		want  string
	}{
		{value: transpose("platform", 5), want: "platform"},
		{value: drop("sysadmin", 6), want: "sysadmin"},
		{value: drop("advocate", 3), want: "advocate"},
		{value: drop("gamedev", 4), want: "gamedev"},
		{value: transpose("frontend", 4), want: "frontend"},
	}
	for _, testCase := range cases {
		t.Run(testCase.value, func(t *testing.T) {
			suggestions := suggest(testCase.value, roles)
			if len(suggestions) == 0 || suggestions[0] != testCase.want {
				t.Fatalf("suggest(%q) = %v, want %q first", testCase.value, suggestions, testCase.want)
			}
		})
	}
	// "ops" and "director" are two edits apart, which is most of a three-letter word.
	if suggestions := suggest("ops", roles); len(suggestions) != 0 {
		t.Fatalf("suggest(\"ops\") = %v, want no suggestion", suggestions)
	}
}

func TestSuggestionLimitScalesWithLength(t *testing.T) {
	if suggestionLimit(3) != 1 || suggestionLimit(8) != 2 || suggestionLimit(20) != 3 {
		t.Fatal("suggestion limit should widen as the slug gets longer")
	}
}

func TestUnknownSeatErrorSeparatesUnknownFromUnlaunchable(t *testing.T) {
	document := loadRosterFixture(t)
	frontend, _ := document.role("frontend")
	unlaunchable := unknownSeatError("penpot", frontend).Error()
	if !strings.Contains(unlaunchable, "no native harness") {
		t.Fatalf("a real seat with no harness should say so: %s", unlaunchable)
	}
	unknown := unknownSeatError("nonsense", frontend).Error()
	if !strings.Contains(unknown, "is not a frontend seat") {
		t.Fatalf("an unknown seat should say so: %s", unknown)
	}
	for _, message := range []string{unlaunchable, unknown} {
		if !strings.Contains(message, "claude") {
			t.Fatalf("seat errors should list the launchable seats: %s", message)
		}
	}
}

func TestHumanList(t *testing.T) {
	cases := map[string][]string{
		"":                   {},
		"one":                {"one"},
		"one or two":         {"one", "two"},
		"one, two, or three": {"one", "two", "three"},
	}
	for want, values := range cases {
		if got := humanList(values); got != want {
			t.Fatalf("humanList(%v) = %q, want %q", values, got, want)
		}
	}
}

func TestSafeRoleSlug(t *testing.T) {
	for _, value := range []string{"platform", "gamedev", "a", "role-2"} {
		if !safeRoleSlug(value) {
			t.Fatalf("%q should be a safe slug", value)
		}
	}
	for _, value := range []string{"", "Platform", "-role", "9role", "role/../etc", "role name"} {
		if safeRoleSlug(value) {
			t.Fatalf("%q should not be a safe slug", value)
		}
	}
}

// transpose swaps the runes at index and index+1, the shape of a fast-typing slip.
func transpose(value string, index int) string {
	runes := []rune(value)
	runes[index], runes[index+1] = runes[index+1], runes[index]
	return string(runes)
}

// drop removes the rune at index, the shape of a missed key.
func drop(value string, index int) string {
	runes := []rune(value)
	return string(append(runes[:index:index], runes[index+1:]...))
}
