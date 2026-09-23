// Package version exposes build metadata for the running binary.
//
// Values are injected at build time via -ldflags:
//
//	go build -ldflags "-X <module>/internal/version.Version=1.2.3 \
//	  -X <module>/internal/version.Commit=$(git rev-parse HEAD) \
//	  -X <module>/internal/version.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
//
// Defaults are deliberately non-committal so an un-instrumented local build is
// never mistaken for a release artefact.
package version

import "runtime/debug"

var (
	// Version is the semantic version or release tag.
	Version = "dev"
	// Commit is the source revision the binary was built from.
	Commit = "unknown"
	// BuildTime is the RFC3339 UTC timestamp of the build.
	BuildTime = "unknown"
)

// Info returns the build metadata as a flat map suitable for log records and
// health payloads.
func Info() map[string]string {
	return map[string]string{
		"version":    Version,
		"commit":     Commit,
		"build_time": BuildTime,
		"go_version": goVersion(),
	}
}

func goVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		return bi.GoVersion
	}
	return "unknown"
}
