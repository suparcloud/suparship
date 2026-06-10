package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/secrets"
)

// org_rbac_handler.go — write handlers for Teams, RoleBindings, and OIDC SSO
// config. All mutate the org ConfigMap via OrgStore.GetOrg → mutate →
// SaveOrg (which runs Org.Validate() + an optimistic CAS). Reads live in org.go.
//
// Routes are registered org_admin-only in rbac.go.

const (
	// oidcSecretName is the default Secret holding the OIDC client secret.
	oidcSecretName = "suparship-oidc"
	// oidcSecretKey is the default key within that Secret.
	oidcSecretKey = "client-secret"
)

// saveOrgStatus maps a SaveOrg error to an HTTP status: a concurrent-write
// conflict is 409, a validation failure is 422, anything else 500. Returns 0
// when err is nil.
func saveOrgStatus(err error) (int, string) {
	switch {
	case err == nil:
		return 0, ""
	case apierrors.IsConflict(err):
		return http.StatusConflict, "the org config changed concurrently; reload and retry"
	default:
		// SaveOrg runs Org.Validate() before writing; surface its message.
		return http.StatusUnprocessableEntity, err.Error()
	}
}

// --- Teams ---

type upsertTeamRequest struct {
	Name        string   `json:"name,omitempty"`
	DisplayName string   `json:"displayName,omitempty"`
	Members     []string `json:"members,omitempty"`
}

func (rh *rbacHandler) handleCreateTeam(w http.ResponseWriter, r *http.Request) {
	var req upsertTeamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "name is required"})
		return
	}

	org, err := rh.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}
	for _, t := range org.Teams {
		if t.Name == req.Name {
			writeJSON(w, http.StatusConflict, errorResponse{Error: "team already exists"})
			return
		}
	}

	team := rbac.Team{Name: req.Name, DisplayName: req.DisplayName, Members: normMembers(req.Members)}
	org.Teams = append(org.Teams, team)

	if err := rh.orgStore.SaveOrg(r.Context(), org); err != nil {
		status, msg := saveOrgStatus(err)
		writeJSON(w, status, errorResponse{Error: msg})
		return
	}
	writeJSON(w, http.StatusCreated, teamToDTO(team))
}

func (rh *rbacHandler) handleUpdateTeam(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("team")
	var req upsertTeamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	org, err := rh.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}
	idx := -1
	for i, t := range org.Teams {
		if t.Name == name {
			idx = i
			break
		}
	}
	if idx == -1 {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "team \"" + name + "\" not found"})
		return
	}

	// Name is immutable (it's referenced by role bindings); only displayName +
	// members are editable.
	org.Teams[idx].DisplayName = req.DisplayName
	org.Teams[idx].Members = normMembers(req.Members)

	if err := rh.orgStore.SaveOrg(r.Context(), org); err != nil {
		status, msg := saveOrgStatus(err)
		writeJSON(w, status, errorResponse{Error: msg})
		return
	}
	writeJSON(w, http.StatusOK, teamToDTO(org.Teams[idx]))
}

func (rh *rbacHandler) handleDeleteTeam(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("team")

	org, err := rh.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}
	idx := -1
	for i, t := range org.Teams {
		if t.Name == name {
			idx = i
			break
		}
	}
	if idx == -1 {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "team \"" + name + "\" not found"})
		return
	}

	// Pre-check so we return a clear message rather than relying on the generic
	// Validate() error (which SaveOrg would also raise).
	for _, rb := range org.RoleBindings {
		if rb.Team == name {
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{
				Error: fmt.Sprintf("team %q is still referenced by a role binding on project %q; remove the binding first", name, rb.Project),
			})
			return
		}
	}

	org.Teams = append(org.Teams[:idx], org.Teams[idx+1:]...)
	if err := rh.orgStore.SaveOrg(r.Context(), org); err != nil {
		status, msg := saveOrgStatus(err)
		writeJSON(w, status, errorResponse{Error: msg})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Role bindings ---

// RoleBindingsResponse is the JSON body for GET /api/v1/role-bindings.
type RoleBindingsResponse struct {
	RoleBindings []RoleBindingDTO `json:"roleBindings"`
}

func (rh *rbacHandler) handleListRoleBindings(w http.ResponseWriter, r *http.Request) {
	org, err := rh.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org config"})
		return
	}
	dtos := make([]RoleBindingDTO, len(org.RoleBindings))
	for i, rb := range org.RoleBindings {
		dtos[i] = roleBindingToDTO(rb)
	}
	writeJSON(w, http.StatusOK, RoleBindingsResponse{RoleBindings: dtos})
}

func (rh *rbacHandler) handleCreateRoleBinding(w http.ResponseWriter, r *http.Request) {
	var req RoleBindingDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	if req.Project == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "project is required (use \"*\" for all projects)"})
		return
	}
	if (req.Team == "") == (req.Group == "") {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "set exactly one of team or group"})
		return
	}
	if !rbac.IsValidRole(rbac.Role(req.Role)) {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "unknown role \"" + req.Role + "\""})
		return
	}

	org, err := rh.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}
	for _, rb := range org.RoleBindings {
		if rb.Project == req.Project && rb.Team == req.Team && rb.Group == req.Group && string(rb.Role) == req.Role {
			writeJSON(w, http.StatusConflict, errorResponse{Error: "an identical role binding already exists"})
			return
		}
	}

	binding := rbac.RoleBinding{Project: req.Project, Team: req.Team, Group: req.Group, Role: rbac.Role(req.Role)}
	org.RoleBindings = append(org.RoleBindings, binding)
	if err := rh.orgStore.SaveOrg(r.Context(), org); err != nil {
		status, msg := saveOrgStatus(err)
		writeJSON(w, status, errorResponse{Error: msg})
		return
	}
	writeJSON(w, http.StatusCreated, roleBindingToDTO(binding))
}

func (rh *rbacHandler) handleDeleteRoleBinding(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	project, team, group, role := q.Get("project"), q.Get("team"), q.Get("group"), q.Get("role")
	if project == "" || role == "" || (team == "") == (group == "") {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Error: "project, role, and exactly one of team or group are required",
		})
		return
	}

	org, err := rh.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}
	idx := -1
	for i, rb := range org.RoleBindings {
		if rb.Project == project && rb.Team == team && rb.Group == group && string(rb.Role) == role {
			idx = i
			break
		}
	}
	if idx == -1 {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "role binding not found"})
		return
	}

	org.RoleBindings = append(org.RoleBindings[:idx], org.RoleBindings[idx+1:]...)
	if err := rh.orgStore.SaveOrg(r.Context(), org); err != nil {
		status, msg := saveOrgStatus(err)
		writeJSON(w, status, errorResponse{Error: msg})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- OIDC SSO config ---

// AuthConfigResponse is the JSON body for GET /api/v1/org/auth. It never
// includes the client secret value — only whether one is configured.
type AuthConfigResponse struct {
	OIDC OIDCConfigDTO `json:"oidc"`
}

// OIDCConfigDTO mirrors rbac.OIDCConfig for the API, minus the secret value.
type OIDCConfigDTO struct {
	Enabled         bool     `json:"enabled"`
	IssuerURL       string   `json:"issuerURL"`
	ClientID        string   `json:"clientID"`
	RedirectURL     string   `json:"redirectURL"`
	Scopes          []string `json:"scopes"`
	UsernameClaim   string   `json:"usernameClaim"`
	GroupsClaim     string   `json:"groupsClaim"`
	ClientSecretSet bool     `json:"clientSecretSet"`
}

// putAuthConfigRequest is the PUT body. ClientSecret is write-only — when
// empty an existing stored secret is kept.
type putAuthConfigRequest struct {
	Enabled       bool     `json:"enabled"`
	IssuerURL     string   `json:"issuerURL"`
	ClientID      string   `json:"clientID"`
	ClientSecret  string   `json:"clientSecret,omitempty"`
	RedirectURL   string   `json:"redirectURL"`
	Scopes        []string `json:"scopes,omitempty"`
	UsernameClaim string   `json:"usernameClaim,omitempty"`
	GroupsClaim   string   `json:"groupsClaim,omitempty"`
}

func (rh *rbacHandler) handleGetAuthConfig(w http.ResponseWriter, r *http.Request) {
	org, err := rh.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org config"})
		return
	}

	var dto OIDCConfigDTO
	if org.Auth.OIDC != nil {
		c := org.Auth.OIDC.Defaulted()
		dto = OIDCConfigDTO{
			Enabled:       c.Enabled,
			IssuerURL:     c.IssuerURL,
			ClientID:      c.ClientID,
			RedirectURL:   c.RedirectURL,
			Scopes:        c.Scopes,
			UsernameClaim: c.UsernameClaim,
			GroupsClaim:   c.GroupsClaim,
		}
		dto.ClientSecretSet = rh.oidcSecretExists(r.Context(), c.ClientSecretRef)
	} else {
		// Surface the conventional defaults so the form shows sensible values.
		def := rbac.OIDCConfig{}.Defaulted()
		dto = OIDCConfigDTO{Scopes: def.Scopes, UsernameClaim: def.UsernameClaim, GroupsClaim: def.GroupsClaim}
	}
	writeJSON(w, http.StatusOK, AuthConfigResponse{OIDC: dto})
}

func (rh *rbacHandler) handlePutAuthConfig(w http.ResponseWriter, r *http.Request) {
	var req putAuthConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	org, err := rh.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}

	// Preserve any existing secret ref so a metadata-only PUT keeps the secret.
	var prevRef rbac.SecretKeyRef
	if org.Auth.OIDC != nil {
		prevRef = org.Auth.OIDC.ClientSecretRef
	}

	cfg := rbac.OIDCConfig{
		Enabled:         req.Enabled,
		IssuerURL:       req.IssuerURL,
		ClientID:        req.ClientID,
		RedirectURL:     req.RedirectURL,
		Scopes:          req.Scopes,
		UsernameClaim:   req.UsernameClaim,
		GroupsClaim:     req.GroupsClaim,
		ClientSecretRef: prevRef,
	}.Defaulted()

	if req.Enabled {
		if cfg.IssuerURL == "" || cfg.ClientID == "" || cfg.RedirectURL == "" {
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{
				Error: "issuerURL, clientID, and redirectURL are required when OIDC is enabled",
			})
			return
		}
	}

	// Write/replace the client secret when provided.
	if req.ClientSecret != "" {
		if rh.kubeClient == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorResponse{
				Error: "cannot store the client secret: no Kubernetes client configured",
			})
			return
		}
		ref := cfg.ClientSecretRef
		if ref.Name == "" {
			ref.Name = oidcSecretName
		}
		if ref.Key == "" {
			ref.Key = oidcSecretKey
		}
		if err := upsertSecretKey(r.Context(), rh.kubeClient, ref.Name, ref.Key, req.ClientSecret); err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to store client secret: " + err.Error()})
			return
		}
		cfg.ClientSecretRef = ref
	}

	org.Auth.OIDC = &cfg
	if err := rh.orgStore.SaveOrg(r.Context(), org); err != nil {
		status, msg := saveOrgStatus(err)
		writeJSON(w, status, errorResponse{Error: msg})
		return
	}

	dto := OIDCConfigDTO{
		Enabled:         cfg.Enabled,
		IssuerURL:       cfg.IssuerURL,
		ClientID:        cfg.ClientID,
		RedirectURL:     cfg.RedirectURL,
		Scopes:          cfg.Scopes,
		UsernameClaim:   cfg.UsernameClaim,
		GroupsClaim:     cfg.GroupsClaim,
		ClientSecretSet: rh.oidcSecretExists(r.Context(), cfg.ClientSecretRef),
	}
	writeJSON(w, http.StatusOK, AuthConfigResponse{OIDC: dto})
}

// oidcSecretExists reports whether the referenced Secret + key holds a value.
func (rh *rbacHandler) oidcSecretExists(ctx context.Context, ref rbac.SecretKeyRef) bool {
	if rh.kubeClient == nil || ref.Name == "" {
		return false
	}
	key := ref.Key
	if key == "" {
		key = oidcSecretKey
	}
	sec, err := rh.kubeClient.CoreV1().Secrets(secrets.SystemNamespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return false
	}
	return len(sec.Data[key]) > 0
}

// --- helpers ---

func normMembers(m []string) []string {
	if m == nil {
		return []string{}
	}
	return m
}

func teamToDTO(t rbac.Team) TeamDTO {
	return TeamDTO{Name: t.Name, DisplayName: t.DisplayName, Members: normMembers(t.Members)}
}

func roleBindingToDTO(rb rbac.RoleBinding) RoleBindingDTO {
	return RoleBindingDTO{Project: rb.Project, Team: rb.Team, Group: rb.Group, Role: string(rb.Role)}
}

// upsertSecretKey creates or updates an Opaque Secret in the suparship-system
// namespace, setting key=value. Mirrors the get→create/update+resourceVersion
// pattern in internal/secrets/connect_token_stash.go and internal/auth/k8s.go.
func upsertSecretKey(ctx context.Context, client kubernetes.Interface, name, key, value string) error {
	api := client.CoreV1().Secrets(secrets.SystemNamespace)
	existing, err := api.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, createErr := api.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: secrets.SystemNamespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "suparship",
					"app.kubernetes.io/component":  "oidc",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{key: []byte(value)},
		}, metav1.CreateOptions{})
		if createErr != nil {
			return fmt.Errorf("create secret %s: %w", name, createErr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get secret %s: %w", name, err)
	}
	if existing.Data == nil {
		existing.Data = map[string][]byte{}
	}
	existing.Data[key] = []byte(value)
	if _, err := api.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update secret %s: %w", name, err)
	}
	return nil
}
