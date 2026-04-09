package main

import "testing"

func TestBuildCommit_DefaultUnknown(t *testing.T) {
	// With no -ldflags override, commitHash is the default "unknown".
	if got := buildCommit(); got != "unknown" {
		t.Errorf("buildCommit() = %q, want %q (default)", got, "unknown")
	}
}

func TestBuildCommit_TruncatesLongHash(t *testing.T) {
	saved := commitHash
	defer func() { commitHash = saved }()

	commitHash = "7d209335a6fc1234567890abcdef"
	if got := buildCommit(); got != "7d209335a6fc" {
		t.Errorf("buildCommit() = %q, want %q", got, "7d209335a6fc")
	}
}

func TestBuildInfo_ShortIdentifier(t *testing.T) {
	saved := commitHash
	defer func() { commitHash = saved }()

	commitHash = "abcdef1234567890"
	if got := buildInfo(); got != "abcdef123456" {
		t.Errorf("buildInfo() = %q, want %q", got, "abcdef123456")
	}
}
