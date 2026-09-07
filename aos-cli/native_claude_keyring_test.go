package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// stubKeyring serves one canned secret and records the services asked for.
func stubKeyring(secret []byte, err error) (claudeKeyringReader, *[]string) {
	asked := &[]string{}
	return func(_ context.Context, service, _ string) ([]byte, error) {
		*asked = append(*asked, service)
		if err != nil {
			return nil, err
		}
		return secret, nil
	}, asked
}

// The suffix is Claude Code's own, so a golden digest guards against silently
// drifting away from the service name the harness actually reads.
func TestNativeClaudeKeychainServiceMatchesHarnessNaming(t *testing.T) {
	home := "/home/example"
	if got, want := nativeClaudeKeychainService(
		home,
		filepath.Join(home, ".claude"),
	), claudeCredentialService; got != want {
		t.Fatalf("default config dir service = %q, want %q", got, want)
	}
	if got, want := nativeClaudeKeychainService(
		home,
		"/tmp/example-session/home/.claude",
	), claudeCredentialService+"-0a5e8662"; got != want {
		t.Fatalf("session config dir service = %q, want %q", got, want)
	}
	if got, want := nativeClaudeKeychainService(home, ""), claudeCredentialService; got != want {
		t.Fatalf("empty config dir service = %q, want %q", got, want)
	}
}

func TestSeedCanonicalClaudeCredentialWritesTheKeychainLogin(t *testing.T) {
	home := t.TempDir()
	read, asked := stubKeyring([]byte(`{"claudeAiOauth":{"accessToken":"t"}}`), nil)

	seeded, err := seedCanonicalClaudeCredential(context.Background(), read, home)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if !seeded {
		t.Fatal("seed reported no write")
	}
	target := canonicalClaudeCredentialPath(home)
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read seeded file: %v", err)
	}
	if string(body) != `{"claudeAiOauth":{"accessToken":"t"}}` {
		t.Fatalf("seeded body = %q", body)
	}
	// The credential is readable only by its owner.
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %v, want 0600", perm)
	}
	// It reads the default service, never a session-scoped digest.
	if len(*asked) != 1 || (*asked)[0] != claudeCredentialService {
		t.Fatalf("services read = %v", *asked)
	}
}

// The Keychain goes stale once the file is authoritative, so overwriting would
// retire the token the sessions are actually using.
func TestSeedCanonicalClaudeCredentialNeverOverwrites(t *testing.T) {
	home := t.TempDir()
	target := canonicalClaudeCredentialPath(home)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(target, []byte("live"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	read, asked := stubKeyring([]byte("stale"), nil)

	seeded, err := seedCanonicalClaudeCredential(context.Background(), read, home)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if seeded {
		t.Fatal("seed overwrote an existing credential")
	}
	if body, _ := os.ReadFile(target); string(body) != "live" {
		t.Fatalf("body = %q, want the untouched live value", body)
	}
	if len(*asked) != 0 {
		t.Fatalf("keychain was read despite an existing file: %v", *asked)
	}
}

func TestSeedCanonicalClaudeCredentialStaysQuietWithoutAKeyring(t *testing.T) {
	home := t.TempDir()
	for _, absent := range []error{errClaudeKeyringUnsupported, errClaudeKeyringNotFound} {
		read, _ := stubKeyring(nil, absent)
		seeded, err := seedCanonicalClaudeCredential(context.Background(), read, home)
		if err != nil {
			t.Fatalf("%v: %v", absent, err)
		}
		if seeded {
			t.Fatalf("%v: reported a write", absent)
		}
		if _, err := os.Stat(canonicalClaudeCredentialPath(home)); !os.IsNotExist(err) {
			t.Fatalf("%v: wrote a file anyway", absent)
		}
	}
}

func TestSeedCanonicalClaudeCredentialSurfacesRealKeyringFailures(t *testing.T) {
	home := t.TempDir()
	boom := errors.New("keychain locked")
	read, _ := stubKeyring(nil, boom)
	if _, err := seedCanonicalClaudeCredential(context.Background(), read, home); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

// The ordinary case: the session wrote through its symlink, so the canonical
// file is already current and there is nothing to reclaim.
func TestReclaimSessionClaudeCredentialIgnoresAnIntactSymlink(t *testing.T) {
	home := t.TempDir()
	sessionHome := t.TempDir()
	target := canonicalClaudeCredentialPath(home)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(target, []byte("canonical"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	link := filepath.Join(sessionHome, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	reclaimed, err := reclaimSessionClaudeCredential(sessionHome, home)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if reclaimed {
		t.Fatal("reclaimed through an intact symlink")
	}
}

// The guard's reason for existing: a write that replaced the link would strand
// the rotated token in a session directory about to be reaped.
func TestReclaimSessionClaudeCredentialRecoversAReplacedLink(t *testing.T) {
	home := t.TempDir()
	sessionHome := t.TempDir()
	target := canonicalClaudeCredentialPath(home)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	replaced := filepath.Join(sessionHome, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(replaced), 0o700); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	if err := os.WriteFile(replaced, []byte("rotated"), 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}

	reclaimed, err := reclaimSessionClaudeCredential(sessionHome, home)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if !reclaimed {
		t.Fatal("reclaim reported nothing recovered")
	}
	if body, _ := os.ReadFile(target); string(body) != "rotated" {
		t.Fatalf("canonical body = %q, want the rotated value", body)
	}
}

func TestReclaimSessionClaudeCredentialToleratesAnAbsentSession(t *testing.T) {
	home := t.TempDir()
	for _, sessionHome := range []string{"", t.TempDir()} {
		reclaimed, err := reclaimSessionClaudeCredential(sessionHome, home)
		if err != nil {
			t.Fatalf("reclaim %q: %v", sessionHome, err)
		}
		if reclaimed {
			t.Fatalf("reclaim %q reported a recovery", sessionHome)
		}
	}
}

// stampedCredential renders the envelope the harness writes, at one expiry.
func stampedCredential(expiresAt int64) []byte {
	return []byte(fmt.Sprintf(
		`{"claudeAiOauth":{"accessToken":"t","expiresAt":%d}}`, expiresAt))
}

func TestHarvestSessionClaudeKeychainRecoversTheDeletedLink(t *testing.T) {
	home := t.TempDir()
	sessionHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sessionHome, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	read, asked := stubKeyring(stampedCredential(200), nil)

	harvested, err := harvestSessionClaudeKeychain(
		context.Background(), read, sessionHome, home)
	if err != nil {
		t.Fatalf("harvest: %v", err)
	}
	if !harvested {
		t.Fatal("harvest reported no write")
	}
	body, err := os.ReadFile(canonicalClaudeCredentialPath(home))
	if err != nil {
		t.Fatalf("read canonical: %v", err)
	}
	if string(body) != string(stampedCredential(200)) {
		t.Fatalf("canonical body = %q", body)
	}
	// It must ask for the session digest, never the shared host item.
	want := nativeClaudeKeychainService(home, filepath.Join(sessionHome, ".claude"))
	if len(*asked) != 1 || (*asked)[0] != want {
		t.Fatalf("services read = %v, want %q", *asked, want)
	}
}

// The failure that retired the previous design: a reaped session's older token
// overwriting the one every live session is using.
func TestHarvestSessionClaudeKeychainKeepsTheLongerLivedToken(t *testing.T) {
	home := t.TempDir()
	sessionHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sessionHome, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	canonical := canonicalClaudeCredentialPath(home)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonical, stampedCredential(500), 0o600); err != nil {
		t.Fatal(err)
	}
	read, _ := stubKeyring(stampedCredential(200), nil)

	harvested, err := harvestSessionClaudeKeychain(
		context.Background(), read, sessionHome, home)
	if err != nil {
		t.Fatalf("harvest: %v", err)
	}
	if harvested {
		t.Fatal("harvest retired a token that outlives the candidate")
	}
	body, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != string(stampedCredential(500)) {
		t.Fatalf("canonical was overwritten: %q", body)
	}
}

func TestHarvestSessionClaudeKeychainAdvancesToTheFresherToken(t *testing.T) {
	home := t.TempDir()
	sessionHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sessionHome, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	canonical := canonicalClaudeCredentialPath(home)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonical, stampedCredential(200), 0o600); err != nil {
		t.Fatal(err)
	}
	read, _ := stubKeyring(stampedCredential(900), nil)

	harvested, err := harvestSessionClaudeKeychain(
		context.Background(), read, sessionHome, home)
	if err != nil {
		t.Fatalf("harvest: %v", err)
	}
	if !harvested {
		t.Fatal("harvest declined a fresher token")
	}
	body, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != string(stampedCredential(900)) {
		t.Fatalf("canonical body = %q", body)
	}
}

// A link still in place means the session never lost it, so the file path owns
// the write-back and the Keychain must not be consulted at all.
func TestHarvestSessionClaudeKeychainSkipsWhenTheLinkSurvives(t *testing.T) {
	home := t.TempDir()
	sessionHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sessionHome, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	canonical := canonicalClaudeCredentialPath(home)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonical, stampedCredential(200), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		canonical,
		filepath.Join(sessionHome, ".claude", ".credentials.json"),
	); err != nil {
		t.Fatal(err)
	}
	read, asked := stubKeyring(stampedCredential(900), nil)

	harvested, err := harvestSessionClaudeKeychain(
		context.Background(), read, sessionHome, home)
	if err != nil {
		t.Fatalf("harvest: %v", err)
	}
	if harvested {
		t.Fatal("harvest wrote while the session link was intact")
	}
	if len(*asked) != 0 {
		t.Fatalf("keychain consulted while the link survived: %v", *asked)
	}
}

// An unstamped payload cannot be compared, so it must lose rather than win by
// counting as expiry zero.
func TestHarvestSessionClaudeKeychainRefusesAnUncomparablePayload(t *testing.T) {
	home := t.TempDir()
	sessionHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sessionHome, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	canonical := canonicalClaudeCredentialPath(home)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonical, stampedCredential(200), 0o600); err != nil {
		t.Fatal(err)
	}
	read, _ := stubKeyring([]byte(`{"claudeAiOauth":{"accessToken":"t"}}`), nil)

	harvested, err := harvestSessionClaudeKeychain(
		context.Background(), read, sessionHome, home)
	if err != nil {
		t.Fatalf("harvest: %v", err)
	}
	if harvested {
		t.Fatal("harvest wrote an uncomparable payload over a stamped one")
	}
}
