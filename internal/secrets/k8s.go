package secrets

const (
	// SystemNamespace is where suparship control-plane config (org ConfigMap,
	// SA-token Secret, sealing certs) lives.
	SystemNamespace = "suparship-system"

	labelManagedBy = "suparship.io/managed-by"
	labelType      = "suparship.io/type"
)
