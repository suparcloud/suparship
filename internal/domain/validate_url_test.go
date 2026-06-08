package domain

import "testing"

func TestValidateAPIServerURL(t *testing.T) {
	good := []string{
		"https://10.0.0.1:6443",
		"https://staging-aks-01.privatelink.southcentralus.azmk8s.io:443",
	}
	for _, s := range good {
		if err := ValidateAPIServerURL(s); err != nil {
			t.Errorf("ValidateAPIServerURL(%q) = %v, want nil", s, err)
		}
	}

	bad := []string{
		"",                          // empty
		" https://10.0.0.1:6443",    // leading space (the incident)
		"https://10.0.0.1:6443 ",    // trailing space
		"http://10.0.0.1:6443",      // not https
		"10.0.0.1:6443",             // no scheme → no host parsed
		"https://",                  // no host
	}
	for _, s := range bad {
		if err := ValidateAPIServerURL(s); err == nil {
			t.Errorf("ValidateAPIServerURL(%q) = nil, want error", s)
		}
	}
}

func TestValidateImageRepository(t *testing.T) {
	good := []string{
		"",                                // empty allowed (template ships its own)
		"ghcr.io/acme/web",
		"registry.example.com:5000/team/web",
		"docker.io/library/nginx",
	}
	for _, s := range good {
		if err := ValidateImageRepository(s); err != nil {
			t.Errorf("ValidateImageRepository(%q) = %v, want nil", s, err)
		}
	}

	bad := []string{
		" ghcr.io/acme/web",            // leading space
		"https://ghcr.io/acme/web",     // scheme
		"ghcr.io/acme/web:1.2.3",       // tag belongs in image_tag
		"ghcr.io/acme/web@sha256:abcd", // digest
		"ghcr.io/acme web",             // space
	}
	for _, s := range bad {
		if err := ValidateImageRepository(s); err == nil {
			t.Errorf("ValidateImageRepository(%q) = nil, want error", s)
		}
	}
}

func TestValidateConnectEndpoint(t *testing.T) {
	good := []string{
		"", // empty allowed (falls back to org/built-in default)
		"http://onepassword-connect.onepassword-connect.svc.cluster.local:8080",
		"https://connect.example.com",
	}
	for _, s := range good {
		if err := ValidateConnectEndpoint(s); err != nil {
			t.Errorf("ValidateConnectEndpoint(%q) = %v, want nil", s, err)
		}
	}

	bad := []string{
		" http://connect:8080",  // leading space
		"ftp://connect:8080",    // wrong scheme
		"connect:8080",          // no scheme → no host
		"http://",               // no host
	}
	for _, s := range bad {
		if err := ValidateConnectEndpoint(s); err == nil {
			t.Errorf("ValidateConnectEndpoint(%q) = nil, want error", s)
		}
	}
}
