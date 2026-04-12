package profiles

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed core/** self-hosting-xylem/**
var profileFS embed.FS

var profileVersions = map[string]int{
	"core":               3,
	"self-hosting-xylem": 2,
}

var coreForbiddenDaemonFields = map[string]struct{}{
	"auto_upgrade":    {},
	"auto_merge":      {},
	"auto_merge_repo": {},
}

type Profile struct {
	Name    string
	Version int
	FS      fs.FS
}

type ComposedProfile struct {
	Profiles       []Profile
	Workflows      map[string][]byte
	Prompts        map[string][]byte
	Scripts        map[string][]byte
	Sources        map[string][]byte
	ConfigOverlays [][]byte
}

type overlayConfig struct {
	Daemon  map[string]any `yaml:"daemon"`
	Sources map[string]any `yaml:"sources"`
}

func Load(name string) (*Profile, error) {
	version, ok := profileVersions[name]
	if !ok {
		return nil, fmt.Errorf("load profile %q: unknown profile", name)
	}

	subFS, err := fs.Sub(profileFS, name)
	if err != nil {
		return nil, fmt.Errorf("load profile %q: %w", name, err)
	}
	if _, err := fs.Stat(subFS, "."); err != nil {
		return nil, fmt.Errorf("load profile %q: %w", name, err)
	}

	return &Profile{
		Name:    name,
		Version: version,
		FS:      subFS,
	}, nil
}

func Compose(names ...string) (*ComposedProfile, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("compose profiles: no profiles requested")
	}

	composed := &ComposedProfile{
		Workflows: make(map[string][]byte),
		Prompts:   make(map[string][]byte),
		Scripts:   make(map[string][]byte),
		Sources:   make(map[string][]byte),
	}

	sourceOwners := make(map[string]string)

	for _, name := range names {
		profile, err := Load(name)
		if err != nil {
			return nil, fmt.Errorf("compose profiles: %w", err)
		}
		composed.Profiles = append(composed.Profiles, *profile)

		if err := fs.WalkDir(profile.FS, ".", func(filePath string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return fmt.Errorf("walk profile %q: %w", profile.Name, walkErr)
			}
			if d.IsDir() {
				return nil
			}

			data, err := fs.ReadFile(profile.FS, filePath)
			if err != nil {
				return fmt.Errorf("read profile %q asset %q: %w", profile.Name, filePath, err)
			}

			switch {
			case filePath == "xylem.yml.tmpl" || filePath == "xylem.overlay.yml":
				if err := validateOverlay(profile.Name, filePath, data); err != nil {
					return err
				}
				if err := mergeSources(profile.Name, filePath, data, composed.Sources, sourceOwners); err != nil {
					return err
				}
				composed.ConfigOverlays = append(composed.ConfigOverlays, cloneBytes(data))
			case strings.HasPrefix(filePath, "workflows/") && strings.HasSuffix(filePath, ".yaml"):
				workflowName := strings.TrimSuffix(path.Base(filePath), path.Ext(filePath))
				composed.Workflows[workflowName] = cloneBytes(data)
			case strings.HasPrefix(filePath, "prompts/") && strings.HasSuffix(filePath, ".md"):
				promptKey := strings.TrimSuffix(strings.TrimPrefix(filePath, "prompts/"), ".md")
				composed.Prompts[promptKey] = cloneBytes(data)
			case strings.HasPrefix(filePath, "scripts/"):
				scriptKey := strings.TrimPrefix(filePath, "scripts/")
				composed.Scripts[scriptKey] = cloneBytes(data)
			case strings.HasPrefix(filePath, "sources/") && strings.HasSuffix(filePath, ".yaml"):
				sourceName := strings.TrimSuffix(path.Base(filePath), path.Ext(filePath))
				if prevOwner, ok := sourceOwners[sourceName]; ok {
					return fmt.Errorf("compose profiles: source %q conflicts between %q and %q", sourceName, prevOwner, profile.Name)
				}
				composed.Sources[sourceName] = cloneBytes(data)
				sourceOwners[sourceName] = profile.Name
			}

			return nil
		}); err != nil {
			return nil, err
		}
	}

	return composed, nil
}

func validateOverlay(profileName, filePath string, data []byte) error {
	var overlay overlayConfig
	if err := yaml.Unmarshal(data, &overlay); err != nil {
		return fmt.Errorf("compose profiles: parse %q overlay %q: %w", profileName, filePath, err)
	}

	if profileName != "core" {
		return nil
	}
	for field := range overlay.Daemon {
		if _, forbidden := coreForbiddenDaemonFields[field]; forbidden {
			return fmt.Errorf("compose profiles: core overlay %q must not set daemon.%s", filePath, field)
		}
	}

	return nil
}

func mergeSources(profileName, filePath string, data []byte, dest map[string][]byte, owners map[string]string) error {
	var overlay overlayConfig
	if err := yaml.Unmarshal(data, &overlay); err != nil {
		return fmt.Errorf("compose profiles: parse %q overlay %q: %w", profileName, filePath, err)
	}

	names := make([]string, 0, len(overlay.Sources))
	for sourceName := range overlay.Sources {
		names = append(names, sourceName)
	}
	sort.Strings(names)

	for _, sourceName := range names {
		if prevOwner, ok := owners[sourceName]; ok {
			return fmt.Errorf("compose profiles: source %q conflicts between %q and %q", sourceName, prevOwner, profileName)
		}
		payload, err := yaml.Marshal(overlay.Sources[sourceName])
		if err != nil {
			return fmt.Errorf("compose profiles: marshal source %q from %q overlay %q: %w", sourceName, profileName, filePath, err)
		}
		dest[sourceName] = payload
		owners[sourceName] = profileName
	}

	return nil
}

// ComputeEmbeddedDigest returns a deterministic SHA-256 hex digest over all
// workflow, prompt, and script content in composed. Keys are sorted before
// hashing so the result is independent of map iteration order.
// Returns "" if composed is nil.
func ComputeEmbeddedDigest(composed *ComposedProfile) string {
	if composed == nil {
		return ""
	}
	var entries []string
	for _, name := range sortedByteMapKeys(composed.Workflows) {
		entries = append(entries, "workflows/"+name+":"+hexHash(composed.Workflows[name]))
	}
	for _, name := range sortedByteMapKeys(composed.Prompts) {
		entries = append(entries, "prompts/"+name+":"+hexHash(composed.Prompts[name]))
	}
	for _, name := range sortedByteMapKeys(composed.Scripts) {
		entries = append(entries, "scripts/"+name+":"+hexHash(composed.Scripts[name]))
	}
	sort.Strings(entries)
	sum := sha256.Sum256([]byte(strings.Join(entries, "\n")))
	return fmt.Sprintf("%x", sum)
}

// ComputeRuntimeDigest returns a deterministic SHA-256 hex digest over the
// workflow YAML files, prompt MD files, and script files currently on disk
// under stateDir. Returns "" if stateDir does not exist or is unreadable.
func ComputeRuntimeDigest(stateDir string) string {
	var entries []string

	// workflows/<name>.yaml → key "workflows/<name>"
	workflowsDir := filepath.Join(stateDir, "workflows")
	if infos, err := os.ReadDir(workflowsDir); err == nil {
		for _, info := range infos {
			if info.IsDir() || !strings.HasSuffix(info.Name(), ".yaml") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(workflowsDir, info.Name()))
			if err != nil {
				continue
			}
			name := strings.TrimSuffix(info.Name(), ".yaml")
			entries = append(entries, "workflows/"+name+":"+hexHash(data))
		}
	}

	// prompts/**/*.md → key "prompts/<rel-without-ext>" (forward slashes)
	promptsDir := filepath.Join(stateDir, "prompts")
	//nolint:errcheck // non-existent promptsDir and internal walk errors are intentionally ignored
	_ = filepath.WalkDir(promptsDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		rel, err := filepath.Rel(promptsDir, p)
		if err != nil {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		key := strings.TrimSuffix(filepath.ToSlash(rel), ".md")
		entries = append(entries, "prompts/"+key+":"+hexHash(data))
		return nil
	})

	// scripts/<rel> → key "scripts/<rel>" (forward slashes)
	scriptsDir := filepath.Join(stateDir, "scripts")
	//nolint:errcheck // non-existent scriptsDir and internal walk errors are intentionally ignored
	_ = filepath.WalkDir(scriptsDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(scriptsDir, p)
		if err != nil {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		key := filepath.ToSlash(rel)
		entries = append(entries, "scripts/"+key+":"+hexHash(data))
		return nil
	})

	sort.Strings(entries)
	sum := sha256.Sum256([]byte(strings.Join(entries, "\n")))
	return fmt.Sprintf("%x", sum)
}

func hexHash(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum)
}

func sortedByteMapKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func cloneBytes(data []byte) []byte {
	return append([]byte(nil), data...)
}

func Version(name string) (int, bool) {
	version, ok := profileVersions[name]
	return version, ok
}
