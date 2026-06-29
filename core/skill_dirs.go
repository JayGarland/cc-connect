package core

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ParseSkillDirs extracts skill directory paths from a config value.
// TOML arrays decode as []any with string elements; []string is also accepted.
func ParseSkillDirs(v any) ([]string, error) {
	if v == nil {
		return nil, nil
	}
	switch t := v.(type) {
	case []string:
		return normalizeSkillDirList(t), nil
	case []any:
		out := make([]string, 0, len(t))
		for i, item := range t {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("skill_dirs[%d]: expected string, got %T", i, item)
			}
			out = append(out, s)
		}
		return normalizeSkillDirList(out), nil
	default:
		return nil, fmt.Errorf("skill_dirs: expected array of strings, got %T", v)
	}
}

// MergeSkillDirs returns config dirs first, then agent-native dirs.
// SkillRegistry scans in order; the first directory wins for duplicate skill names.
func MergeSkillDirs(configDirs, agentDirs []string) []string {
	merged := make([]string, 0, len(configDirs)+len(agentDirs))
	merged = append(merged, configDirs...)
	merged = append(merged, agentDirs...)
	return normalizeSkillDirList(merged)
}

func normalizeSkillDirList(dirs []string) []string {
	if len(dirs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(dirs))
	out := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
		key := strings.ToLower(filepath.Clean(dir))
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, dir)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
