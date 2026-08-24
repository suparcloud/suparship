package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/tpl"
	"github.com/suparcloud/suparship/internal/tpl/credstore"
	"github.com/suparcloud/suparship/internal/tpl/registrysync"
)

// templateRegistryHandler serves the template registry API.
//
// engine is optional — when nil the /sync endpoints return 503. The other
// endpoints continue to work because they only need the store. authMiddleware
// composes auth + org_admin enforcement; required for the write/sync paths,
// and used uniformly so a future tightening of read access only touches one
// line.
type templateRegistryHandler struct {
	store *tpl.RegistryStore
	auth  *authHandler
	// engine is the external-repo sync driver. Nil → /sync routes 503.
	engine *registrysync.Engine
	// credStore writes SealedSecret-backed credentials. Nil → /credentials
	// and /test-connection routes 503; operators fall back to hand-managed
	// Secrets via existingSecret.
	credStore *credstore.Store
	// kubeClient reads stored credential Secrets to support test-connection
	// against an already-saved source. Nil disables that fallback; tests
	// still work as long as the request body carries plaintext creds.
	kubeClient     kubernetes.Interface
	authMiddleware func(http.HandlerFunc) http.HandlerFunc
	logger         *slog.Logger
}

func (h *templateRegistryHandler) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/templates/registry", h.auth.requireAuth(h.handleGetRegistry))
	mux.HandleFunc("PUT /api/v1/templates/registry", h.adminOrAuth()(h.handleUpdateRegistry))
	mux.HandleFunc("GET /api/v1/templates/sources", h.auth.requireAuth(h.handleListSources))
	mux.HandleFunc("POST /api/v1/templates/registry/sync", h.adminOrAuth()(h.handleSyncAll))
	mux.HandleFunc("POST /api/v1/templates/registry/sources/{name}/sync", h.adminOrAuth()(h.handleSyncOne))
	mux.HandleFunc("POST /api/v1/templates/registry/sources/{name}/credentials", h.adminOrAuth()(h.handleSetCredentials))
	mux.HandleFunc("POST /api/v1/templates/registry/sources/{name}/test-connection", h.adminOrAuth()(h.handleTestConnection))
}

// adminOrAuth returns the org_admin middleware when wired, falling back to
// plain auth so the registry endpoints still work in environments that
// haven't installed the rbac handler (notably tests).
func (h *templateRegistryHandler) adminOrAuth() func(http.HandlerFunc) http.HandlerFunc {
	if h.authMiddleware != nil {
		return h.authMiddleware
	}
	return h.auth.requireAuth
}

// handleGetRegistry returns the full template registry.
func (h *templateRegistryHandler) handleGetRegistry(w http.ResponseWriter, r *http.Request) {
	reg, err := h.store.Get(r.Context())
	if err != nil {
		if errors.Is(err, tpl.ErrRegistryNotFound) {
			writeJSON(w, http.StatusOK, templateRegistryResponse{
				Configured: false,
				Registry:   &tpl.TemplateRegistry{BuiltIn: []string{}, Sources: []tpl.TemplateSource{}},
			})
			return
		}
		h.logger.Error("get template registry", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to read template registry"})
		return
	}

	writeJSON(w, http.StatusOK, templateRegistryResponse{Configured: true, Registry: reg})
}

// handleUpdateRegistry saves the template registry configuration
// (built-in list and external repos). This does not trigger a sync — it only
// persists the desired state.
func (h *templateRegistryHandler) handleUpdateRegistry(w http.ResponseWriter, r *http.Request) {
	var reg tpl.TemplateRegistry
	if err := json.NewDecoder(r.Body).Decode(&reg); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid JSON body"})
		return
	}

	if reg.BuiltIn == nil {
		reg.BuiltIn = []string{}
	}
	if reg.Sources == nil {
		reg.Sources = []tpl.TemplateSource{}
	}

	// Identify UI-managed credentials about to be orphaned by this update —
	// sources that were present and pointing at our deterministic secret
	// name, and that are not in the new External[] list. Hand-wired
	// existingSecret values are left alone (operator may share them
	// across sources, or manage them via ESO/SealedSecret-by-hand).
	orphans := orphanedManagedCreds(h.previousExternal(r.Context()), reg.External)

	if err := h.store.Save(r.Context(), &reg); err != nil {
		h.logger.Error("save template registry", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save template registry"})
		return
	}

	// Sweep orphans after the save so a deletion failure can't leave the
	// registry pointing at a Secret that no longer exists.
	if h.credStore != nil {
		for _, name := range orphans {
			if err := h.credStore.Delete(r.Context(), name); err != nil {
				h.logger.Warn("orphan cleanup: delete sealed credentials",
					"source", name, "error", err)
			}
		}
	}

	writeJSON(w, http.StatusOK, templateRegistryResponse{Configured: true, Registry: &reg})
}

// previousExternal returns the registry's current External[] for diffing
// against an incoming update. Returns nil when the registry doesn't yet
// exist (first save) or read fails — both safely degrade to "no orphans
// to clean up", which is the right outcome.
func (h *templateRegistryHandler) previousExternal(ctx context.Context) []tpl.ExternalTemplateRepo {
	prev, err := h.store.Get(ctx)
	if err != nil || prev == nil {
		return nil
	}
	return prev.External
}

// orphanedManagedCreds returns the names of sources that disappear from
// before→after AND were UI-managed (existingSecret matches the
// deterministic credstore.SecretNameFor name). Renames count as an
// orphan + a fresh entry, which is what we want — the renamed source's
// new name will get its own credentials on the next /credentials call.
func orphanedManagedCreds(before, after []tpl.ExternalTemplateRepo) []string {
	if len(before) == 0 {
		return nil
	}
	keep := make(map[string]struct{}, len(after))
	for _, r := range after {
		keep[r.Name] = struct{}{}
	}
	var orphans []string
	for _, r := range before {
		if _, kept := keep[r.Name]; kept {
			continue
		}
		if r.ExistingSecret != "" && r.ExistingSecret == credstore.SecretNameFor(r.Name) {
			orphans = append(orphans, r.Name)
		}
	}
	return orphans
}

// handleListSources returns just the resolved source list (lighter than full registry).
func (h *templateRegistryHandler) handleListSources(w http.ResponseWriter, r *http.Request) {
	reg, err := h.store.Get(r.Context())
	if err != nil {
		if errors.Is(err, tpl.ErrRegistryNotFound) {
			writeJSON(w, http.StatusOK, templateSourcesResponse{Sources: []tpl.TemplateSource{}})
			return
		}
		h.logger.Error("list template sources", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to read template sources"})
		return
	}

	writeJSON(w, http.StatusOK, templateSourcesResponse{Sources: reg.Sources})
}

// templateRegistryResponse wraps the registry for the API.
type templateRegistryResponse struct {
	Configured bool                  `json:"configured"`
	Registry   *tpl.TemplateRegistry `json:"registry"`
}

// templateSourcesResponse is the lighter sources-only response.
type templateSourcesResponse struct {
	Sources []tpl.TemplateSource `json:"sources"`
}

// syncResultDTO is the JSON-friendly view of a single source's sync outcome.
// We expose Templates + Error explicitly so the UI can show "3 imported, 1
// failed" without needing to inspect an opaque Go error.
type syncResultDTO struct {
	SourceName string    `json:"sourceName"`
	Templates  []string  `json:"templates"`
	SyncedAt   time.Time `json:"syncedAt"`
	Error      string    `json:"error,omitempty"`
}

// syncResponse wraps the per-source results returned from the sync
// endpoints. Always returns 200 even when individual sources failed —
// partial success is a normal outcome and the caller inspects each entry.
type syncResponse struct {
	Results []syncResultDTO `json:"results"`
}

// handleSyncAll triggers a sync across every ExternalTemplateRepo in the
// registry. Updates the registry's Sources list with the freshly imported
// templates and returns per-source results inline.
func (h *templateRegistryHandler) handleSyncAll(w http.ResponseWriter, r *http.Request) {
	if h.engine == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "registry sync engine not configured"})
		return
	}
	reg, err := h.store.Get(r.Context())
	if err != nil {
		if errors.Is(err, tpl.ErrRegistryNotFound) {
			// No registry → nothing to sync. Respond 200 with an empty
			// results list so callers don't need to special-case "no
			// registry yet".
			writeJSON(w, http.StatusOK, syncResponse{Results: []syncResultDTO{}})
			return
		}
		h.logger.Error("sync: get registry", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to read template registry"})
		return
	}

	results := h.engine.SyncAll(r.Context(), reg)
	for i, repo := range reg.External {
		if i < len(results) {
			registrysync.ApplyResult(reg, repo, results[i])
		}
	}
	if err := h.store.Save(r.Context(), reg); err != nil {
		h.logger.Error("sync: save registry", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to persist sync state"})
		return
	}

	writeJSON(w, http.StatusOK, syncResponse{Results: toSyncDTOs(results)})
}

// handleSyncOne syncs a single named source. The {name} path parameter must
// match an ExternalTemplateRepo.Name in the registry.
func (h *templateRegistryHandler) handleSyncOne(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "source name required"})
		return
	}
	if h.engine == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "registry sync engine not configured"})
		return
	}
	reg, err := h.store.Get(r.Context())
	if err != nil {
		h.logger.Error("sync: get registry", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to read template registry"})
		return
	}
	var target *tpl.ExternalTemplateRepo
	for i := range reg.External {
		if reg.External[i].Name == name {
			target = &reg.External[i]
			break
		}
	}
	if target == nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "source not found: " + name})
		return
	}

	result := h.engine.SyncOne(r.Context(), *target, reg)
	registrysync.ApplyResult(reg, *target, result)
	if err := h.store.Save(r.Context(), reg); err != nil {
		h.logger.Error("sync: save registry", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to persist sync state"})
		return
	}
	writeJSON(w, http.StatusOK, syncResponse{Results: toSyncDTOs([]registrysync.SyncResult{result})})
}

// toSyncDTOs flattens engine results into JSON-friendly form. Errors are
// stringified at the boundary because the JSON encoder won't expose Go
// error types meaningfully.
func toSyncDTOs(results []registrysync.SyncResult) []syncResultDTO {
	out := make([]syncResultDTO, 0, len(results))
	for _, r := range results {
		dto := syncResultDTO{
			SourceName: r.SourceName,
			Templates:  r.Templates,
			SyncedAt:   r.SyncedAt,
		}
		if r.Err != nil {
			dto.Error = r.Err.Error()
		}
		if dto.Templates == nil {
			dto.Templates = []string{}
		}
		out = append(out, dto)
	}
	return out
}

// setCredentialsRequest carries plaintext creds the UI is submitting for
// a template source. Provider may override the source's stored value
// (handy when the operator is correcting a misclassified entry); empty
// means "use whatever the source already has, falling back to generic".
type setCredentialsRequest struct {
	Provider string `json:"provider,omitempty"`
	Token    string `json:"token,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// setCredentialsResponse echoes back the updated source plus the test
// result so the UI can show "credentials saved & verified" in one shot.
type setCredentialsResponse struct {
	Source           *tpl.ExternalTemplateRepo `json:"source"`
	SealedSecretName string                    `json:"sealedSecretName"`
	TestResult       *tplTestResult            `json:"testResult,omitempty"`
}

type tplTestRequest struct {
	Provider string `json:"provider,omitempty"`
	Token    string `json:"token,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

type tplTestResult struct {
	Success    bool   `json:"success"`
	Message    string `json:"message"`
	DurationMs int64  `json:"durationMs"`
}

// handleSetCredentials seals the submitted credentials into the management
// cluster as a SealedSecret, points the source at the resulting Secret,
// and persists the registry. The plaintext is verified against the live
// repo *before* sealing, so a stored credential is always known-good.
func (h *templateRegistryHandler) handleSetCredentials(w http.ResponseWriter, r *http.Request) {
	if h.credStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "template credential store not configured"})
		return
	}
	name := r.PathValue("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "source name required"})
		return
	}

	var req setCredentialsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid JSON body"})
		return
	}

	reg, err := h.store.Get(r.Context())
	if err != nil {
		if errors.Is(err, tpl.ErrRegistryNotFound) {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "template registry not configured"})
			return
		}
		h.logger.Error("set credentials: get registry", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to read template registry"})
		return
	}

	idx := -1
	for i := range reg.External {
		if reg.External[i].Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "source not found: " + name})
		return
	}
	source := &reg.External[idx]

	provider := req.Provider
	if provider == "" {
		provider = source.Provider
	}

	// Verify with the live repo first — never seal credentials that
	// don't actually authenticate. Mirrors the gitops test-connection
	// flow's intent, but inlined into the save path because skipping
	// verification on save is a footgun (a sealed-but-broken cred
	// produces opaque sync failures hours later).
	test := runGitTestConnection(r.Context(), source.RepoURL, basicAuthFor(provider, req.Token, req.Username, req.Password))
	if !test.Success {
		writeJSON(w, http.StatusBadRequest, setCredentialsResponse{
			Source:     source,
			TestResult: &test,
		})
		return
	}

	secretName, err := h.credStore.SealAndApply(r.Context(), name, provider, req.Token, req.Username, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, credstore.ErrSealedSecretsNotInstalled):
			writeJSON(w, http.StatusPreconditionFailed, errorResponse{Error: err.Error()})
		case errors.Is(err, credstore.ErrNoCredentials):
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		default:
			h.logger.Error("set credentials: seal", "source", name, "error", err)
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to seal credentials"})
		}
		return
	}

	source.Provider = provider
	source.ExistingSecret = secretName
	if err := h.store.Save(r.Context(), reg); err != nil {
		h.logger.Error("set credentials: save registry", "source", name, "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "credentials sealed but registry update failed"})
		return
	}

	writeJSON(w, http.StatusOK, setCredentialsResponse{
		Source:           source,
		SealedSecretName: secretName,
		TestResult:       &test,
	})
}

// handleTestConnection runs `git ls-remote` against the source's repoURL.
// When the request body carries plaintext creds those are used; otherwise
// the handler falls back to credentials already stored under the source's
// existingSecret so the operator can verify a saved configuration without
// re-pasting the PAT.
func (h *templateRegistryHandler) handleTestConnection(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "source name required"})
		return
	}

	var req tplTestRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid JSON body"})
			return
		}
	}

	reg, err := h.store.Get(r.Context())
	if err != nil {
		if errors.Is(err, tpl.ErrRegistryNotFound) {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "template registry not configured"})
			return
		}
		h.logger.Error("test connection: get registry", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to read template registry"})
		return
	}

	var source *tpl.ExternalTemplateRepo
	for i := range reg.External {
		if reg.External[i].Name == name {
			source = &reg.External[i]
			break
		}
	}
	if source == nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "source not found: " + name})
		return
	}

	provider := req.Provider
	if provider == "" {
		provider = source.Provider
	}

	user, pass := req.Username, req.Password
	if req.Token != "" {
		user, pass = "x-access-token", req.Token
	}
	if user == "" && pass == "" && source.ExistingSecret != "" && h.kubeClient != nil {
		// Fall back to whatever's already stored for this source so the UI
		// can verify a saved configuration without forcing a re-paste.
		u, p, lookupErr := h.lookupStoredAuth(r.Context(), source.ExistingSecret)
		if lookupErr != nil {
			h.logger.Warn("test connection: read stored auth", "source", name, "error", lookupErr)
		}
		user, pass = u, p
	}

	res := runGitTestConnection(r.Context(), source.RepoURL, basicAuthCreds{user: user, pass: pass})
	// 200 even on failure: the call itself succeeded; the body's `success`
	// field reports the auth outcome. Same shape as gitops test-connection.
	_ = provider // currently informational; reserved for provider-specific tweaks (e.g. SSH later)
	writeJSON(w, http.StatusOK, res)
}

// basicAuthCreds is the shape `git clone`-via-HTTPS needs: a username and
// a password (or PAT). For token-based providers the username is the
// "x-access-token" magic value; for username/password providers it's the
// real username.
type basicAuthCreds struct {
	user string
	pass string
}

// basicAuthFor turns a (provider, token, username, password) tuple into
// the basic-auth creds `git` actually needs. Mirrors credstore's
// provider→data-key mapping; kept here so the handler doesn't reach into
// credstore internals.
func basicAuthFor(provider, token, username, password string) basicAuthCreds {
	switch provider {
	case "github", "gitlab", "gitea":
		if token != "" {
			return basicAuthCreds{user: "x-access-token", pass: token}
		}
	}
	return basicAuthCreds{user: username, pass: password}
}

// lookupStoredAuth reads the (potentially sealed-secrets-derived) Secret
// for a source and returns basic-auth creds. Mirrors registrysync's
// readAuth precedence: data["token"] wins over data["username"]+["password"].
func (h *templateRegistryHandler) lookupStoredAuth(ctx context.Context, secretName string) (string, string, error) {
	sec, err := h.kubeClient.CoreV1().Secrets(credstore.SystemNamespace).Get(ctx, secretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	if tok := string(sec.Data["token"]); tok != "" {
		return "x-access-token", tok, nil
	}
	return string(sec.Data["username"]), string(sec.Data["password"]), nil
}

// runGitTestConnection runs `git ls-remote --exit-code` against repoURL
// with credentials embedded. Returns a structured result with timing so
// the UI can show "verified in 240ms". GIT_TERMINAL_PROMPT=0 prevents
// hangs on bad creds.
func runGitTestConnection(ctx context.Context, repoURL string, creds basicAuthCreds) tplTestResult {
	if repoURL == "" {
		return tplTestResult{Success: false, Message: "source has no repoURL configured"}
	}
	url := repoURL
	if creds.user != "" && creds.pass != "" {
		url = injectCredentials(url, creds.user, creds.pass)
	}
	start := time.Now()
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--exit-code", url)
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	elapsed := time.Since(start)

	res := tplTestResult{DurationMs: elapsed.Milliseconds()}
	if err != nil {
		res.Success = false
		msg := strings.TrimSpace(string(output))
		if msg == "" {
			msg = err.Error()
		}
		res.Message = sanitizeGitError(msg)
		return res
	}
	res.Success = true
	res.Message = "connection successful"
	return res
}
