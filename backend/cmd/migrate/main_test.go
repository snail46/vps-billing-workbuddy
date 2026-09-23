package main

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// fakeVersionSource stands in for the migration runner so the reporting rules can
// be exercised without a database.
type fakeVersionSource struct {
	version uint
	dirty   bool
	err     error
}

func (f fakeVersionSource) Version() (uint, bool, error) {
	return f.version, f.dirty, f.err
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func TestReportVersionWritesResultToTheResultStream(t *testing.T) {
	out := &bytes.Buffer{}
	loggerOut := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(loggerOut, nil))

	code := reportVersion(fakeVersionSource{version: 1}, out, logger, "current schema version")

	if code != exitOK {
		t.Fatalf("expected exit %d, got %d", exitOK, code)
	}
	// The result stream must contain the number and nothing else. A caller reads
	// it with a command substitution, so any extra output — a log line, a banner —
	// would corrupt it.
	if got := out.String(); got != "1\n" {
		t.Fatalf("expected the result stream to hold exactly %q, got %q", "1\n", got)
	}
	// The diagnostic belongs on the logger's own stream, not beside the result.
	if !strings.Contains(loggerOut.String(), `"version":1`) {
		t.Fatalf("expected the version to be logged separately, got %q", loggerOut.String())
	}
}

func TestReportVersionReportsAZeroVersionWhenNoneIsApplied(t *testing.T) {
	out := &bytes.Buffer{}

	// A schema with no recorded version still has a definite state, and reporting
	// it is more useful than printing nothing.
	code := reportVersion(fakeVersionSource{version: 0}, out, discardLogger(), "current schema version")

	if code != exitOK {
		t.Fatalf("expected exit %d, got %d", exitOK, code)
	}
	if got := out.String(); got != "0\n" {
		t.Fatalf("expected %q, got %q", "0\n", got)
	}
}

func TestReportVersionFailsOnADirtySchema(t *testing.T) {
	out := &bytes.Buffer{}

	code := reportVersion(
		fakeVersionSource{version: 4, dirty: true},
		out,
		discardLogger(),
		"migrations applied",
	)

	// A dirty schema means a migration stopped part-way and needs a human. It must
	// never be reported as success, which is the whole point of the exit code.
	if code != exitFailure {
		t.Fatalf("expected exit %d for a dirty schema, got %d", exitFailure, code)
	}
	// The version is still published: it is what an operator needs in order to
	// decide how to recover.
	if got := out.String(); got != "4\n" {
		t.Fatalf("expected %q, got %q", "4\n", got)
	}
}

func TestReportVersionEmitsNothingWhenTheVersionCannotBeRead(t *testing.T) {
	out := &bytes.Buffer{}

	code := reportVersion(
		fakeVersionSource{err: errors.New("connection reset")},
		out,
		discardLogger(),
		"current schema version",
	)

	if code != exitFailure {
		t.Fatalf("expected exit %d, got %d", exitFailure, code)
	}
	// An unknown version must not be published as a number: a caller parsing the
	// result would otherwise read it as a real schema state.
	if got := out.String(); got != "" {
		t.Fatalf("expected no output on failure, got %q", got)
	}
}
