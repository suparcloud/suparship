package rbac

import (
	"context"
	"errors"
	"testing"
)

type stubProvider struct {
	org *Org
	err error
}

func (s stubProvider) GetOrg(context.Context) (*Org, error) { return s.org, s.err }

func TestOrgAuthorizerAllowsAndDenies(t *testing.T) {
	a := NewOrgAuthorizer(stubProvider{org: NewDefaultOrg("acme", "Acme", "admin")})
	ctx := context.Background()

	// admin holds org_admin on "*".
	if ok, err := a.Authorize(ctx, Identity{Username: "admin"}, "*", RoleOrgAdmin); err != nil || !ok {
		t.Errorf("admin org_admin: got (allowed=%v, err=%v), want (true, nil)", ok, err)
	}
	// A higher bar than the admin's role on a project they have no binding for.
	if ok, err := a.Authorize(ctx, Identity{Username: "nobody"}, "api", RoleViewer); err != nil || ok {
		t.Errorf("unknown user: got (allowed=%v, err=%v), want (false, nil)", ok, err)
	}
}

func TestOrgAuthorizerPropagatesLoadError(t *testing.T) {
	a := NewOrgAuthorizer(stubProvider{err: errors.New("boom")})
	ok, err := a.Authorize(context.Background(), Identity{Username: "admin"}, "*", RoleOrgAdmin)
	if err == nil {
		t.Error("expected the org-load error to propagate")
	}
	if ok {
		t.Error("expected allowed=false when the org cannot be loaded")
	}
}
