package contracts

func init() {
	Register(Contract{
		Type: "redis",
		RequiredKeys: []string{
			"REDIS_URL",
			"REDIS_HOST",
			"REDIS_PORT",
			"REDIS_PASSWORD",
		},
		OptionalKeys: []string{
			"REDIS_USER",     // ACL user; default "" for unauth'd local valkey
			"REDIS_DATABASE", // numeric db index; defaults to "0"
		},
		Description: "Key-value store. Wrapped over valkey-operator " +
			"(K8s-local) or Crossplane ElastiCache (managed) at " +
			"deploy time per the env's AddonProfile binding.",
	})
}
