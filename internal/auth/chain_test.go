package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type staticAuth struct {
	username, password string
	err                error // returned for any non-matching attempt
}

func (a staticAuth) Authenticate(_ context.Context, username, password string) (*Credentials, error) {
	if a.err == nil && username == a.username && password == a.password {
		return &Credentials{Username: username}, nil
	}
	if a.err != nil {
		return nil, a.err
	}
	return nil, ErrInvalidCredentials
}

func TestChainFallsThrough(t *testing.T) {
	admin := staticAuth{username: "admin", password: "root-pass"}
	local := staticAuth{username: "jane", password: "jane-pass"}
	chain := Chain{admin, local}

	if creds, err := chain.Authenticate(context.Background(), "admin", "root-pass"); err != nil || creds.Username != "admin" {
		t.Fatalf("admin login through chain: %v, %v", creds, err)
	}
	if creds, err := chain.Authenticate(context.Background(), "jane", "jane-pass"); err != nil || creds.Username != "jane" {
		t.Fatalf("local login through chain: %v, %v", creds, err)
	}
	if _, err := chain.Authenticate(context.Background(), "ghost", "nope"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown user: got %v", err)
	}
}

func TestChainNotConfiguredSemantics(t *testing.T) {
	// Admin secret missing but local users exist: local login works, and an
	// unknown user is invalid credentials (NOT "not configured").
	chain := Chain{staticAuth{err: ErrNotConfigured}, staticAuth{username: "jane", password: "jane-pass"}}
	if _, err := chain.Authenticate(context.Background(), "jane", "jane-pass"); err != nil {
		t.Fatalf("local login with unconfigured admin: %v", err)
	}
	if _, err := chain.Authenticate(context.Background(), "ghost", "nope"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown user with unconfigured admin: got %v", err)
	}

	// EVERY link unconfigured → the state is preserved for the 503 mapping.
	allOff := Chain{staticAuth{err: ErrNotConfigured}, staticAuth{err: ErrNotConfigured}}
	if _, err := allOff.Authenticate(context.Background(), "x", "y"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("all links unconfigured: got %v", err)
	}
}

func TestChainAbortsOnUnexpectedError(t *testing.T) {
	boom := fmt.Errorf("backend down")
	chain := Chain{staticAuth{err: boom}, staticAuth{username: "jane", password: "jane-pass"}}
	if _, err := chain.Authenticate(context.Background(), "jane", "jane-pass"); !errors.Is(err, boom) {
		t.Fatalf("unexpected error must abort the chain, got %v", err)
	}
}
