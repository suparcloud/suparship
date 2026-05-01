// Package chartvalidate asserts that a Helm chart renders the resource
// kinds its companion template.yaml declares via `produces`.
//
// This is a build-time / CI check, not a runtime path. Charts and
// templates evolve in different files and drift silently — declaring
// `components: [{name: web, produces: [Deployment, Service]}]` and
// then deleting the Service template should fail validation, not
// quietly ship.
//
// The package shells out to the `helm` binary; expect it on PATH in
// CI. Failure to find helm is treated as a setup error, not a
// validation failure (so contributors without helm installed don't
// get spurious red).
package chartvalidate

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/suparcloud/suparship/internal/tpl"
)

// ErrHelmNotFound is returned when the helm binary is not on PATH.
// Callers can choose to skip validation rather than fail when running
// in environments without helm (e.g. an IDE-side test runner).
var ErrHelmNotFound = errors.New("helm binary not found on PATH")

// ValidateProduces renders the chart at chartDir using publisher-style
// values that enable every defaultEnabled component declared in tmpl,
// then asserts the rendered manifest contains every Kind named in each
// component's `produces` list.
//
// extraSet provides additional `--set` arguments specific to the
// chart (e.g. agent.type for voiceai-agent). May be nil.
//
// Returns nil when validation passes. Returns ErrHelmNotFound when
// helm is missing. Otherwise returns a descriptive error.
func ValidateProduces(chartDir string, tmpl *tpl.Template, extraSet map[string]string) error {
	if _, err := exec.LookPath("helm"); err != nil {
		return ErrHelmNotFound
	}

	required := requiredKinds(tmpl)
	if len(required) == 0 {
		// No declarations → nothing to assert. Templates without
		// `produces` are tolerated (backwards compatibility).
		return nil
	}

	rendered, err := helmTemplate(chartDir, tmpl, extraSet)
	if err != nil {
		return fmt.Errorf("helm template %s: %w", chartDir, err)
	}

	have := renderedKinds(rendered)

	var missing []string
	for k := range required {
		if !have[k] {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("chart at %s does not render declared produces kinds: %v (have: %v)",
			chartDir, missing, sortedKeys(have))
	}
	return nil
}

// requiredKinds collects the union of `produces` across every
// defaultEnabled component. Disabled components don't contribute.
func requiredKinds(tmpl *tpl.Template) map[string]bool {
	out := map[string]bool{}
	for _, c := range tmpl.Spec.Components {
		if !c.IsDefaultEnabled() {
			continue
		}
		for _, k := range c.Produces {
			out[k] = true
		}
	}
	return out
}

// helmTemplate runs `helm template` with publisher-style values and
// returns the multi-doc YAML it renders.
func helmTemplate(chartDir string, tmpl *tpl.Template, extraSet map[string]string) (string, error) {
	args := []string{
		"template",
		"validation-release",
		chartDir,
		"--set", "app.name=validation-app",
		"--set", "app.env=test",
		"--set", "routing.host=validation.test",
	}

	// Set image.repository and image.tag for every declared component
	// so canonical-schema requirements are satisfied. Skip components
	// that aren't defaultEnabled (would be left disabled by the
	// publisher and shouldn't render resources).
	for _, c := range tmpl.Spec.Components {
		if !c.IsDefaultEnabled() {
			continue
		}
		args = append(args,
			"--set", fmt.Sprintf("components.%s.enabled=true", c.Name),
			"--set", fmt.Sprintf("components.%s.image.repository=validation/%s", c.Name, c.Name),
			"--set", fmt.Sprintf("components.%s.image.tag=v0", c.Name),
			"--set", fmt.Sprintf("components.%s.replicas=1", c.Name),
		)
	}

	for k, v := range extraSet {
		args = append(args, "--set", fmt.Sprintf("%s=%s", k, v))
	}

	cmd := exec.Command("helm", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// renderedKinds parses helm template output and returns the set of
// distinct `kind:` values it contains. Documents without a kind
// (separators, comments) are ignored.
func renderedKinds(rendered string) map[string]bool {
	out := map[string]bool{}
	dec := yaml.NewDecoder(strings.NewReader(rendered))
	for {
		var doc struct {
			Kind string `yaml:"kind"`
		}
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if doc.Kind != "" {
			out[doc.Kind] = true
		}
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// IsErrHelmNotFound is a convenience for tests that want to skip
// validation when helm is unavailable.
func IsErrHelmNotFound(err error) bool {
	return errors.Is(err, ErrHelmNotFound) || (err != nil && slices.Contains([]string{ErrHelmNotFound.Error()}, err.Error()))
}
