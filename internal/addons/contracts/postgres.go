package contracts

func init() {
	Register(Contract{
		Type: "postgres",
		RequiredKeys: []string{
			"DATABASE_URL",
			"DATABASE_HOST",
			"DATABASE_PORT",
			"DATABASE_NAME",
			"DATABASE_USER",
			"DATABASE_PASSWORD",
		},
		OptionalKeys: []string{
			"DATABASE_SSL_MODE",  // disable / require / verify-full
			"DATABASE_REPLICA_URL", // read replica connection string
		},
		Description: "Relational database. Wrapped over CloudNativePG " +
			"(K8s-local) or Crossplane RDS (managed) at deploy time " +
			"per the env's AddonProfile binding. Scaffolded for step 4.",
	})
}
