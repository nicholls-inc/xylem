package skills

import (
	"path/filepath"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

func TestPropRenderParseRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		def := Definition{
			Directory:              rapid.StringMatching(`[a-z0-9-]{1,16}`).Draw(t, "directory"),
			Description:            rapid.StringMatching(`Use when [a-z ]{5,40}\.`).Draw(t, "description"),
			ArgumentHint:           rapid.SampledFrom([]string{"", "[vessel-id]", "[state]", "[workflow] [ref]"}).Draw(t, "argumentHint"),
			DisableModelInvocation: rapid.Bool().Draw(t, "disableModelInvocation"),
			AllowedTools: rapid.SampledFrom([][]string{
				nil,
				{"Read", "Grep"},
				{"Bash", "Read"},
			}).Draw(t, "allowedTools"),
			Body: strings.TrimSpace(rapid.StringMatching(`[A-Za-z0-9 .,/<>:\-\n]{10,200}`).Draw(t, "body")),
		}
		def.Name = def.Directory
		if def.Body == "" {
			def.Body = "fallback body"
		}
		if rapid.Bool().Draw(t, "includeUserInvocable") {
			value := rapid.Bool().Draw(t, "userInvocable")
			def.UserInvocable = &value
		}

		rendered, err := Render(def)
		if err != nil {
			t.Fatalf("Render() error = %v", err)
		}
		parsed, err := Parse(filepath.Join("repo", filepath.FromSlash(DirName), def.Directory, EntryFile), []byte(rendered))
		if err != nil {
			t.Fatalf("Parse() error = %v", err)
		}

		if parsed.Directory != def.Directory {
			t.Fatalf("Directory = %q, want %q", parsed.Directory, def.Directory)
		}
		if parsed.Name != def.Name {
			t.Fatalf("Name = %q, want %q", parsed.Name, def.Name)
		}
		if parsed.Description != def.Description {
			t.Fatalf("Description = %q, want %q", parsed.Description, def.Description)
		}
		if parsed.ArgumentHint != def.ArgumentHint {
			t.Fatalf("ArgumentHint = %q, want %q", parsed.ArgumentHint, def.ArgumentHint)
		}
		if parsed.DisableModelInvocation != def.DisableModelInvocation {
			t.Fatalf("DisableModelInvocation = %t, want %t", parsed.DisableModelInvocation, def.DisableModelInvocation)
		}
		if !equalBoolPtr(parsed.UserInvocable, def.UserInvocable) {
			t.Fatalf("UserInvocable = %v, want %v", parsed.UserInvocable, def.UserInvocable)
		}
		if !equalStrings(parsed.AllowedTools, def.AllowedTools) {
			t.Fatalf("AllowedTools = %v, want %v", parsed.AllowedTools, def.AllowedTools)
		}
		if parsed.Body != def.Body {
			t.Fatalf("Body = %q, want %q", parsed.Body, def.Body)
		}
	})
}

func equalBoolPtr(left, right *bool) bool {
	switch {
	case left == nil && right == nil:
		return true
	case left == nil || right == nil:
		return false
	default:
		return *left == *right
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
