package migrations

import (
	"strings"
	"testing"
)

// TestLatestVersionMatchesTheEmbeddedHistory pins the derivation to the migrations
// that are actually embedded, so it needs no literal and cannot go stale: adding a
// migration changes the expectation with it.
//
// The invariant it relies on — that versions run contiguously from 1, so the
// highest equals the count — is asserted by scripts/check-migrations.py, which
// fails on a gap.
func TestLatestVersionMatchesTheEmbeddedHistory(t *testing.T) {
	entries, err := FS.ReadDir(Dir)
	if err != nil {
		t.Fatalf("reading the embedded directory: %v", err)
	}

	var up, down int
	for _, entry := range entries {
		switch {
		case entry.IsDir():
		case strings.HasSuffix(entry.Name(), ".up.sql"):
			up++
		case strings.HasSuffix(entry.Name(), ".down.sql"):
			down++
		}
	}

	if up == 0 {
		t.Fatal("no up migrations are embedded, so the rest of this test proves nothing")
	}
	// If this is zero, the suffix filter is not discriminating and TestLatestVersion…
	// below would pass for the wrong reason.
	if down == 0 {
		t.Fatal("no down migrations are embedded; the up/down distinction is untested")
	}

	latest, err := LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}

	if latest != uint(up) {
		t.Errorf("LatestVersion returned %d but %d up migrations are embedded; with "+
			"contiguous numbering from 1 those must agree", latest, up)
	}
	// The down migrations are the trap: counting them would report a version that no
	// database ever reaches.
	if latest == uint(up+down) {
		t.Errorf("LatestVersion returned %d, which counts the down migrations too", latest)
	}
}

// TestLatestVersionIsNotZero guards the "no version recorded" case: a zero would be
// indistinguishable from a schema that has never been migrated, and the integration
// test that compares against this value would then accept both.
func TestLatestVersionIsNotZero(t *testing.T) {
	latest, err := LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if latest == 0 {
		t.Fatal("a history root is recorded as version 1, so the latest version cannot be 0")
	}
}
