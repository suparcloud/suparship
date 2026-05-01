package chartvalidate

import (
	"bytes"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/suparcloud/suparship/internal/addons/contracts"
	"github.com/suparcloud/suparship/internal/tpl"
)

// ValidateAddonContracts asserts that an addon wrapper template's
// chart renders a Secret with all keys required by each declared
// addon's connection contract (see internal/addons/contracts).
//
// Usage: invoked by tests / CI on wrapper templates (category=addon).
// Application templates that don't declare spec.addons are a no-op.
//
// Returns ErrHelmNotFound when helm is missing.
func ValidateAddonContracts(chartDir string, tmpl *tpl.Template) error {
	if _, err := exec.LookPath("helm"); err != nil {
		return ErrHelmNotFound
	}
	if len(tmpl.Spec.Addons) == 0 {
		return nil
	}

	rendered, err := helmTemplateAddon(chartDir, tmpl)
	if err != nil {
		return fmt.Errorf("helm template %s: %w", chartDir, err)
	}
	secrets := parseRenderedSecrets(rendered)

	var missing []string
	for _, a := range tmpl.Spec.Addons {
		c, ok := contracts.Lookup(a.Type)
		if !ok {
			return fmt.Errorf("template declares addon type %q but no contract is registered", a.Type)
		}
		// At least one rendered Secret must contain every required
		// key. We don't pin a single Secret name — wrappers may
		// pick any name; the contract is on key presence.
		matched := false
		for _, sec := range secrets {
			if hasAllKeys(sec, c.RequiredKeys) {
				matched = true
				break
			}
		}
		if !matched {
			missing = append(missing,
				fmt.Sprintf("addon %q (type %q): no rendered Secret contains all required keys %v",
					a.Name, a.Type, c.RequiredKeys))
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("contract assertion(s) failed in %s:\n  - %s",
			chartDir, strings.Join(missing, "\n  - "))
	}
	return nil
}

// helmTemplateAddon runs helm template against an addon wrapper chart
// with publisher-style values for the AddonInstanceValues shape.
func helmTemplateAddon(chartDir string, tmpl *tpl.Template) (string, error) {
	args := []string{
		"template",
		"validation-release",
		chartDir,
		"--set", "app.name=validation-app",
		"--set", "app.env=test",
		"--set", "addon.enabled=true",
		"--set", "addon.secretName=validation-app-addon-claim-conn",
	}
	if len(tmpl.Spec.Addons) > 0 {
		// Pin the type so the contract assertion has something
		// concrete to look up; multi-addon wrappers run the validator
		// once per declared addon.
		args = append(args, "--set", fmt.Sprintf("addon.type=%s", tmpl.Spec.Addons[0].Type))
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

// parseRenderedSecrets returns the stringData (or string-cast data)
// keys of every Secret in rendered helm output. Multi-doc safe.
func parseRenderedSecrets(rendered string) []map[string]string {
	out := []map[string]string{}
	dec := yaml.NewDecoder(strings.NewReader(rendered))
	for {
		var doc struct {
			Kind       string            `yaml:"kind"`
			StringData map[string]string `yaml:"stringData"`
			Data       map[string]string `yaml:"data"` // base64-encoded; we only want presence, not values
		}
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if doc.Kind != "Secret" {
			continue
		}
		merged := map[string]string{}
		for k, v := range doc.StringData {
			merged[k] = v
		}
		for k, v := range doc.Data {
			merged[k] = v
		}
		out = append(out, merged)
	}
	return out
}

func hasAllKeys(secret map[string]string, keys []string) bool {
	for _, k := range keys {
		if _, ok := secret[k]; !ok {
			return false
		}
	}
	return true
}
