package project

import (
	"fmt"
	"regexp"
)

var dnsNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

// Validate checks the Project for structural correctness.
func (p *Project) Validate() error {
	if p.APIVersion != CurrentAPIVersion {
		return fmt.Errorf("unsupported apiVersion %q (expected %q)", p.APIVersion, CurrentAPIVersion)
	}
	if p.Kind != ProjectKind {
		return fmt.Errorf("unsupported kind %q (expected %q)", p.Kind, ProjectKind)
	}

	if p.Metadata.Name == "" {
		return fmt.Errorf("metadata.name must not be empty")
	}
	if !dnsNameRE.MatchString(p.Metadata.Name) {
		return fmt.Errorf("metadata.name %q must be a valid DNS label (lowercase alphanumeric and hyphens, 2-63 chars)", p.Metadata.Name)
	}

	if err := validateEnvironments(p.Spec.Environments); err != nil {
		return err
	}

	envNames := environmentNameSet(p.Spec.Environments)
	if err := validateServices(p.Spec.Services, envNames); err != nil {
		return err
	}

	return nil
}

func validateEnvironments(envs []Environment) error {
	if len(envs) == 0 {
		return fmt.Errorf("spec.environments must contain at least one environment")
	}

	names := make(map[string]bool, len(envs))
	orders := make(map[int]string, len(envs))

	for i, env := range envs {
		if env.Name == "" {
			return fmt.Errorf("environments[%d]: name must not be empty", i)
		}
		if names[env.Name] {
			return fmt.Errorf("duplicate environment name %q", env.Name)
		}
		names[env.Name] = true

		if env.Order < 1 {
			return fmt.Errorf("environments[%d] %q: order must be >= 1", i, env.Name)
		}
		if existing, dup := orders[env.Order]; dup {
			return fmt.Errorf("environments %q and %q have duplicate order %d", existing, env.Name, env.Order)
		}
		orders[env.Order] = env.Name
	}

	return nil
}

func validateServices(services []Service, envNames map[string]bool) error {
	names := make(map[string]bool, len(services))

	for i, svc := range services {
		if svc.Name == "" {
			return fmt.Errorf("services[%d]: name must not be empty", i)
		}
		if !dnsNameRE.MatchString(svc.Name) {
			return fmt.Errorf("services[%d] %q: name must be a valid DNS label", i, svc.Name)
		}
		if names[svc.Name] {
			return fmt.Errorf("duplicate service name %q", svc.Name)
		}
		names[svc.Name] = true

		if svc.Template.Name == "" {
			return fmt.Errorf("services[%d] %q: template.name must not be empty", i, svc.Name)
		}

		if err := validateSecretRefs(svc.SecretRefs, fmt.Sprintf("services[%d] %q", i, svc.Name)); err != nil {
			return err
		}

		for envName, override := range svc.EnvironmentOverrides {
			if !envNames[envName] {
				return fmt.Errorf("services[%d] %q: environmentOverrides references unknown environment %q", i, svc.Name, envName)
			}
			if err := validateSecretRefs(override.SecretRefs, fmt.Sprintf("services[%d] %q env %q", i, svc.Name, envName)); err != nil {
				return err
			}
		}
	}

	return nil
}

func validateSecretRefs(refs []SecretRef, context string) error {
	names := make(map[string]bool, len(refs))
	for j, sr := range refs {
		if sr.Name == "" {
			return fmt.Errorf("%s: secretRefs[%d].name must not be empty", context, j)
		}
		if sr.SecretRef == "" {
			return fmt.Errorf("%s: secretRefs[%d].secretRef must not be empty", context, j)
		}
		if names[sr.Name] {
			return fmt.Errorf("%s: duplicate secretRef name %q", context, sr.Name)
		}
		names[sr.Name] = true
	}
	return nil
}

func environmentNameSet(envs []Environment) map[string]bool {
	m := make(map[string]bool, len(envs))
	for _, e := range envs {
		m[e.Name] = true
	}
	return m
}
