package gitops

import "strings"

// clearedImageTag detects an explicit empty image tag replacing a published tag
// for the same repository. This guard is independent of CD binding discovery:
// missing bindings must not turn a release into the chart's default image.
func clearedImageTag(previous, next map[string]any, prefix string) string {
	for key, value := range next {
		current, ok := value.(map[string]any)
		if !ok {
			continue
		}
		old, ok := previous[key].(map[string]any)
		if !ok {
			continue
		}
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		repo, _ := current["repository"].(string)
		oldRepo, _ := old["repository"].(string)
		tag, explicit := current["tag"].(string)
		oldTag, _ := old["tag"].(string)
		if repo != "" && repo == oldRepo && explicit && strings.TrimSpace(tag) == "" && oldTag != "" {
			return path + ".tag"
		}
		if path := clearedImageTag(old, current, path); path != "" {
			return path
		}
	}
	return ""
}
