package main

// commitHash is the git commit the binary was built from. Populated at build
// time via ldflags:
//
//	go build -ldflags "-X main.commitHash=$(git rev-parse HEAD)" ./cmd/xylem
//
// Defaults to "unknown" when not set (e.g., from `go test`). The daemon's
// auto-upgrade rebuild includes this flag.
var commitHash = "unknown"

// buildCommit returns the short form of commitHash (first 12 characters).
func buildCommit() string {
	if len(commitHash) >= 12 {
		return commitHash[:12]
	}
	return commitHash
}

// buildInfo returns a human-readable build identifier for startup logs.
// Examples: "7d209335a6fc", "unknown".
func buildInfo() string {
	return buildCommit()
}
