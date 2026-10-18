package domain

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

const ManagedValidationEnv = "release-validation"

// ManagedValidation uses an app-owned validation instance, not an org environment.
func (cd CDConfig) ManagedValidation() bool {
	return cd.ReleaseValidationManaged && cd.ReleaseValidationEnvironment == ManagedValidationEnv
}

// ValidationIdentity folds overlapping project/app prefixes, retaining a hash
// when truncating so long app names do not collide at Kubernetes' DNS limit.
func ValidationIdentity(project, app string) string {
	name := app
	if project != "" && app != project && !strings.HasPrefix(app, project+"-") {
		name = project + "-" + app
	}
	name += "-release-validation"
	if len(name) > 63 {
		sum := sha256.Sum256([]byte(name))
		name = strings.TrimRight(name[:50], "-") + fmt.Sprintf("-%x", sum[:6])
	}
	return name
}

func BaseEnvironment(envs []*AppEnvironment, validation string) *AppEnvironment {
	var base *AppEnvironment
	for _, env := range envs {
		if env.EnvType == AppEnvPreview || env.EnvName == validation {
			continue
		}
		if base == nil || env.Order < base.Order || (env.Order == base.Order && env.EnvName < base.EnvName) {
			base = env
		}
	}
	return base
}
