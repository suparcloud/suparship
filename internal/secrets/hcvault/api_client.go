package hcvault

import (
	"context"
	"errors"
	"fmt"
	"strings"

	vaultapi "github.com/hashicorp/vault/api"

	"github.com/suparcloud/suparship/internal/secrets"
)

// APIConfig configures the real Vault client. Address and Token are required;
// Mount defaults to secrets.DefaultVaultMount; Namespace and CACert are
// optional (Vault Enterprise / private CA).
type APIConfig struct {
	Address   string
	Token     string
	Mount     string
	Namespace string
	CACert    string
}

// APIClient implements Client against a real Vault server's KV v2 mount using
// the official API client.
type APIClient struct {
	kv  *vaultapi.KVv2
	api *vaultapi.Client
}

var _ Client = (*APIClient)(nil)

// NewAPIClient builds a client for the configured mount. It performs no
// network I/O — connectivity is checked by Probe.
func NewAPIClient(cfg APIConfig) (*APIClient, error) {
	if strings.TrimSpace(cfg.Address) == "" {
		return nil, secrets.ErrVaultAddressRequired
	}
	vc := vaultapi.DefaultConfig() // honors VAULT_* env, then overridden below
	vc.Address = cfg.Address
	if cfg.CACert != "" {
		if err := vc.ConfigureTLS(&vaultapi.TLSConfig{CACertBytes: []byte(cfg.CACert)}); err != nil {
			return nil, fmt.Errorf("configuring vault CA: %w", err)
		}
	}
	client, err := vaultapi.NewClient(vc)
	if err != nil {
		return nil, fmt.Errorf("building vault client: %w", err)
	}
	client.SetToken(cfg.Token)
	if cfg.Namespace != "" {
		client.SetNamespace(cfg.Namespace)
	}
	mount := cfg.Mount
	if strings.TrimSpace(mount) == "" {
		mount = secrets.DefaultVaultMount
	}
	return &APIClient{kv: client.KVv2(mount), api: client}, nil
}

func (c *APIClient) ReadItem(ctx context.Context, path string) (map[string][]byte, int, error) {
	sec, err := c.kv.Get(ctx, path)
	if err != nil {
		if isNotFound(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	data := make(map[string][]byte, len(sec.Data))
	for k, v := range sec.Data {
		// KV v2 stores JSON; suparship only ever writes strings (UI accepts
		// UTF-8 KEY=value). Anything else was written out-of-band — render it
		// via Sprint rather than dropping it silently.
		if s, ok := v.(string); ok {
			data[k] = []byte(s)
		} else {
			data[k] = fmt.Appendf(nil, "%v", v)
		}
	}
	version := 0
	if sec.VersionMetadata != nil {
		version = sec.VersionMetadata.Version
	}
	return data, version, nil
}

func (c *APIClient) WriteItem(ctx context.Context, path string, data map[string][]byte, cas int) error {
	payload := make(map[string]any, len(data))
	for k, v := range data {
		payload[k] = string(v)
	}
	_, err := c.kv.Put(ctx, path, payload, vaultapi.WithCheckAndSet(cas))
	if err != nil && isCASViolation(err) {
		return fmt.Errorf("%w: %s", secrets.ErrStaleVersion, path)
	}
	return err
}

func (c *APIClient) DeleteItem(ctx context.Context, path string) error {
	err := c.kv.DeleteMetadata(ctx, path)
	if err != nil && isNotFound(err) {
		return nil
	}
	return err
}

// Probe validates the address and token via token self-lookup — proving the
// server is reachable and the credential works, without touching any secret.
func (c *APIClient) Probe(ctx context.Context) error {
	if _, err := c.api.Auth().Token().LookupSelfWithContext(ctx); err != nil {
		return fmt.Errorf("vault token lookup failed: %w", err)
	}
	return nil
}

// isNotFound reports whether err is KV v2's absent-secret error.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	if strings.Contains(err.Error(), vaultapi.ErrSecretNotFound.Error()) {
		return true
	}
	var respErr *vaultapi.ResponseError
	return errors.As(err, &respErr) && respErr.StatusCode == 404
}

// isCASViolation reports whether err is a check-and-set precondition failure.
// The API surfaces it only as a 400 with a fixed message — there is no typed
// error to match on.
func isCASViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "check-and-set parameter did not match")
}
