package gitops

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
	"gopkg.in/yaml.v3"
)

// Check the actual Helm output, including chart defaults, template/base overlays
// and validation overrides. Never include Helm output in errors: it can contain
// chart-rendered credentials. Disposable data currently requires temporary storage.
func (p *Publisher) checkDisposableValidation(ctx context.Context, repo string, app *domain.App, env AppPublishEnv, c domain.ComponentSpec, valuesPath string) error {
	if !c.DisposableInValidation(app.Spec.CD, env.EnvName) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ref := c.Template
	if ref == nil {
		ref = &app.Spec.Template
	}
	chart := p.outputDir(repo, "charts", chartPathFor(ref.Name, ref.Version))
	release := app.Name + "-" + c.Name
	if !app.Spec.IsComposed() {
		release = app.Name
	}
	cmd := exec.CommandContext(ctx, "helm", "template", release, chart, "--namespace", env.Namespace, "--values", filepath.Clean(valuesPath))
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("component %q: could not render disposable validation chart (%v); verify the chart and validation values", c.Name, err)
	}
	if err := checkDisposableManifests(output); err != nil {
		return fmt.Errorf("component %q: %w; disable persistence in release validation values or use persistent validation", c.Name, err)
	}
	return nil
}

func checkDisposableManifests(data []byte) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var resource map[string]any
		if err := decoder.Decode(&resource); err == io.EOF {
			return nil
		} else if err != nil {
			return fmt.Errorf("could not inspect rendered validation resources")
		}
		if err := inspectTemporaryStorage(resource); err != nil {
			return err
		}
	}
}
func inspectTemporaryStorage(value any) error {
	switch v := value.(type) {
	case map[string]any:
		if kind, _ := v["kind"].(string); kind == "PersistentVolumeClaim" || kind == "PersistentVolume" || kind == "VolumeSnapshot" || kind == "VolumeSnapshotContent" {
			return fmt.Errorf("disposable validation chart renders persistent storage (%s)", kind)
		}
		for key, child := range v {
			if key == "volumeClaimTemplates" {
				if templates, ok := child.([]any); ok && len(templates) > 0 {
					return fmt.Errorf("disposable validation chart declares volume claim templates")
				}
			}
			if key == "volumes" {
				if volumes, ok := child.([]any); ok {
					for _, raw := range volumes {
						volume, ok := raw.(map[string]any)
						if !ok {
							return fmt.Errorf("cannot inspect validation volumes")
						}
						for source := range volume {
							switch source {
							case "name", "emptyDir", "configMap", "secret", "projected", "downwardAPI":
							default:
								return fmt.Errorf("disposable validation chart uses a persistent or external volume (%s)", source)
							}
						}
					}
				}
			}
			if err := inspectTemporaryStorage(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range v {
			if err := inspectTemporaryStorage(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func hasDisposableValidation(app *domain.App, env string) bool {
	for _, c := range app.Spec.Components {
		if c.DisposableInValidation(app.Spec.CD, env) {
			return true
		}
	}
	return false
}
