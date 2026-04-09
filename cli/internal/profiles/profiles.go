package profiles

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed core/** self-hosting-xylem/**
var profileFS embed.FS

var profileVersions = map[string]int{
	"core":               1,
	"self-hosting-xylem": 1,
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
		Sources:   make(map[string][]byte),
	}

	workflowOwners := make(map[string]string)
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
				if prev, ok := composed.Workflows[workflowName]; ok && !bytes.Equal(prev, data) {
					return fmt.Errorf("compose profiles: workflow %q conflicts between %q and %q", workflowName, workflowOwners[workflowName], profile.Name)
				}
				composed.Workflows[workflowName] = cloneBytes(data)
				workflowOwners[workflowName] = profile.Name
			case strings.HasPrefix(filePath, "prompts/") && strings.HasSuffix(filePath, ".md"):
				promptKey := strings.TrimSuffix(strings.TrimPrefix(filePath, "prompts/"), ".md")
				composed.Prompts[promptKey] = cloneBytes(data)
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

func cloneBytes(data []byte) []byte {
	return append([]byte(nil), data...)
}

func Version(name string) (int, bool) {
	version, ok := profileVersions[name]
	return version, ok
}
