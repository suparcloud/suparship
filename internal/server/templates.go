package server

import (
	"context"
	"log/slog"
	"net/http"
	"sort"

	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/kube"
	"github.com/suparcloud/suparship/internal/tpl"
)

// ClusterTemplateLoader returns templates persisted as cluster ConfigMaps.
// Wired by cmd/suparship/server.go to kube.LoadTemplates so freshly imported
// charts appear in the gallery without a server restart.
type ClusterTemplateLoader func(ctx context.Context) ([]*tpl.Template, error)

// --- Template API DTO types ---

// TemplateSummaryDTO is the short form returned by GET /api/v1/templates.
type TemplateSummaryDTO struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Category    string `json:"category"`
	Engine      string `json:"engine"`
}

// TemplatesResponse is the JSON body for GET /api/v1/templates.
type TemplatesResponse struct {
	Templates []TemplateSummaryDTO `json:"templates"`
}

// TemplateDetailDTO is the full form returned by GET /api/v1/templates/{name},
// including all inputs and presets needed for UI form generation.
type TemplateDetailDTO struct {
	Name           string                 `json:"name"`
	Version        string                 `json:"version"`
	Title          string                 `json:"title"`
	Description    string                 `json:"description,omitempty"`
	Category       string                 `json:"category"`
	Engine         string                 `json:"engine"`
	Components     []TemplateComponentDTO `json:"components"`
	Inputs         []InputDTO             `json:"inputs"`
	AdvancedInputs []InputDTO             `json:"advancedInputs"`
	SecretInputs   []SecretInputDTO       `json:"secretInputs"`
	Presets        []PresetDTO            `json:"presets"`
}

// TemplateComponentDTO mirrors tpl.TemplateComponent for the wire,
// with capabilities resolved (no pointers, type-based defaults
// already filled in) so the UI can drive form rendering directly.
type TemplateComponentDTO struct {
	Name               string                   `json:"name"`
	Type               string                   `json:"type"`
	Required           bool                     `json:"required"`
	DefaultEnabled     bool                     `json:"defaultEnabled"`
	PreviewEnabled     bool                     `json:"previewEnabled"`
	Exposed            bool                     `json:"exposed"`
	Produces           []string                 `json:"produces,omitempty"`
	OptionallyProduces []string                 `json:"optionallyProduces,omitempty"`
	Capabilities       tpl.ResolvedCapabilities `json:"capabilities"`
}

// InputDTO represents a template input for form generation.
type InputDTO struct {
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Required    bool     `json:"required"`
	Default     any      `json:"default,omitempty"`
	Options     []string `json:"options,omitempty"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
	Pattern     string   `json:"pattern,omitempty"`
}

// SecretInputDTO represents a secret-ref input.
type SecretInputDTO struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	SecretRef   string `json:"secretRef"`
}

// PresetDTO represents a named preset for a template.
type PresetDTO struct {
	Name        string         `json:"name"`
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Values      map[string]any `json:"values"`
}

// --- Handler ---

// templateHandler serves template metadata endpoints. The handler holds the
// disk/built-in templates loaded at startup and (optionally) a live loader
// for cluster-stored templates so the BYO-chart import flow surfaces in the
// gallery without a server restart.
//
// Resolution policy: cluster/externally-synced templates OVERRIDE built-ins on
// name collisions — an operator can update or replace a shipped golden path
// (e.g. voiceai-agent) from their templates repo. The built-in is the fallback
// used only when no cluster copy exists.
type templateHandler struct {
	auth          *authHandler
	builtin       []*tpl.Template
	clusterLoader ClusterTemplateLoader
	// kubeClient lets DELETE /templates/{name} drop the cluster ConfigMap
	// for imported / externally synced templates. Nil disables the route.
	kubeClient kubernetes.Interface
	// authMiddleware composes auth + org_admin enforcement for the
	// destructive route. Nil falls back to plain auth so test harnesses
	// without an OrgStore keep working.
	authMiddleware func(http.HandlerFunc) http.HandlerFunc
	logger         *slog.Logger
}

func newTemplateHandler(auth *authHandler, builtin []*tpl.Template, clusterLoader ClusterTemplateLoader, logger *slog.Logger) *templateHandler {
	return &templateHandler{
		auth:          auth,
		builtin:       builtin,
		clusterLoader: clusterLoader,
		logger:        logger,
	}
}

// adminOrAuth returns the org_admin middleware when wired, falling back
// to plain auth so test harnesses without an OrgStore keep working.
// Mirrors templateRegistryHandler.adminOrAuth.
func (th *templateHandler) adminOrAuth() func(http.HandlerFunc) http.HandlerFunc {
	if th.authMiddleware != nil {
		return th.authMiddleware
	}
	return th.auth.requireAuth
}

func (th *templateHandler) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/templates", th.auth.requireAuth(th.handleList))
	mux.HandleFunc("GET /api/v1/templates/{name}", th.auth.requireAuth(th.handleDetail))
	mux.HandleFunc("GET /api/v1/templates/{name}/versions", th.auth.requireAuth(th.handleListVersions))
	mux.HandleFunc("DELETE /api/v1/templates/{name}", th.adminOrAuth()(th.handleDelete))
}

// TemplateVersionDTO mirrors kube.TemplateVersion on the wire so the UI
// can render an upgrade picker.
type TemplateVersionDTO struct {
	Version   string `json:"version"`
	CreatedAt string `json:"createdAt,omitempty"`
}

// TemplateVersionsResponse wraps the list for GET .../versions.
type TemplateVersionsResponse struct {
	Versions []TemplateVersionDTO `json:"versions"`
}

// handleListVersions returns every persisted version of a template
// (per-version archive ConfigMaps). Built-in templates loaded from disk
// don't have archives — they return an empty list so the UI can flag
// them as "not version-managed" rather than 404.
//
// Sorted descending by SemVer when the strings parse cleanly; falls
// back to the lex order when not (so non-SemVer tags don't crash the
// endpoint, just appear in surprising order).
func (th *templateHandler) handleListVersions(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "template name required"})
		return
	}
	if th.kubeClient == nil {
		writeJSON(w, http.StatusOK, TemplateVersionsResponse{Versions: []TemplateVersionDTO{}})
		return
	}
	versions, err := kube.ListTemplateVersions(r.Context(), th.kubeClient, name)
	if err != nil {
		if th.logger != nil {
			th.logger.Error("list template versions", "name", name, "err", err)
		}
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list template versions"})
		return
	}
	sort.Slice(versions, func(i, j int) bool {
		return semverGreater(versions[i].Version, versions[j].Version)
	})
	dto := make([]TemplateVersionDTO, 0, len(versions))
	for _, v := range versions {
		dto = append(dto, TemplateVersionDTO{Version: v.Version, CreatedAt: v.CreatedAt})
	}
	writeJSON(w, http.StatusOK, TemplateVersionsResponse{Versions: dto})
}

// semverGreater returns true if a > b under SemVer ordering. Falls back
// to string comparison when either side doesn't parse — keeps the
// endpoint stable in the face of non-SemVer tags an operator might
// experiment with.
func semverGreater(a, b string) bool {
	pa, okA := parseSemVer(a)
	pb, okB := parseSemVer(b)
	if !okA || !okB {
		return a > b
	}
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

// parseSemVer returns [major, minor, patch] from "X.Y.Z" (ignoring
// any pre-release / build suffix). Returns ok=false when the input
// can't be parsed cleanly.
func parseSemVer(s string) ([3]int, bool) {
	// Strip pre-release / build (everything after first '-' or '+').
	for i, c := range s {
		if c == '-' || c == '+' {
			s = s[:i]
			break
		}
	}
	parts := [3]int{}
	idx := 0
	cur := 0
	hasDigit := false
	for _, c := range s {
		if c == '.' {
			if !hasDigit || idx >= 2 {
				return [3]int{}, false
			}
			parts[idx] = cur
			idx++
			cur = 0
			hasDigit = false
			continue
		}
		if c < '0' || c > '9' {
			return [3]int{}, false
		}
		cur = cur*10 + int(c-'0')
		hasDigit = true
	}
	if !hasDigit || idx != 2 {
		return [3]int{}, false
	}
	parts[2] = cur
	return parts, true
}

// handleDelete removes a template's cluster ConfigMap (the synced/imported copy).
// When the name also has a built-in, that built-in reappears as the fallback
// after deletion — so deleting an override is allowed and returns 204. Returns
// 409 when the name is a built-in default with no cluster copy to delete, and
// 404 when the name is unknown entirely.
//
// Note: for templates synced from an external repo, deletion succeeds but the
// next sync tick re-creates the ConfigMap. The UI warns before calling this.
func (th *templateHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "template name required"})
		return
	}
	if th.kubeClient == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "cluster client not configured"})
		return
	}
	deleted, err := kube.DeleteTemplate(r.Context(), th.kubeClient, name)
	if err != nil {
		if th.logger != nil {
			th.logger.Error("delete template", "name", name, "err", err)
		}
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to delete template"})
		return
	}
	if !deleted {
		// Nothing in the cluster to delete: distinguish a built-in default
		// (ships with the platform; nothing to remove) from an unknown name.
		for _, t := range th.builtin {
			if t.Metadata.Name == name {
				writeJSON(w, http.StatusConflict, errorResponse{
					Error: "template " + name + " is a built-in default; there is no synced/cluster copy to delete",
				})
				return
			}
		}
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "template " + name + " not found in cluster"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resolveTemplates merges built-ins with the live cluster set, with cluster /
// externally-synced templates OVERRIDING built-ins of the same name (the
// built-in is the fallback). Cluster fetch errors are logged, not surfaced —
// callers still get the built-ins. Shared by the gallery, app-creation lookups,
// and the publish adapter so all resolve identically.
func resolveTemplates(ctx context.Context, builtin []*tpl.Template, clusterLoader ClusterTemplateLoader, logger *slog.Logger) map[string]*tpl.Template {
	byName := make(map[string]*tpl.Template, len(builtin))
	for _, t := range builtin {
		byName[t.Metadata.Name] = t
	}
	if clusterLoader != nil {
		cluster, err := clusterLoader(ctx)
		if err != nil && logger != nil {
			logger.Warn("templates: cluster fetch failed; serving built-ins only", "err", err)
		}
		for _, t := range cluster {
			byName[t.Metadata.Name] = t // cluster overrides built-in
		}
	}
	return byName
}

// resolve returns the gallery's merged template list (sorted) + name index.
func (th *templateHandler) resolve(ctx context.Context) ([]*tpl.Template, map[string]*tpl.Template) {
	byName := resolveTemplates(ctx, th.builtin, th.clusterLoader, th.logger)
	merged := make([]*tpl.Template, 0, len(byName))
	for _, t := range byName {
		merged = append(merged, t)
	}
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Metadata.Name < merged[j].Metadata.Name
	})
	return merged, byName
}

func (th *templateHandler) handleList(w http.ResponseWriter, r *http.Request) {
	merged, _ := th.resolve(r.Context())
	list := make([]TemplateSummaryDTO, len(merged))
	for i, t := range merged {
		list[i] = TemplateSummaryDTO{
			Name:        t.Metadata.Name,
			Version:     t.Metadata.Version,
			Title:       t.Spec.Title,
			Description: t.Spec.Description,
			Category:    t.Spec.Category,
			Engine:      t.Spec.Engine.Type,
		}
	}
	writeJSON(w, http.StatusOK, TemplatesResponse{Templates: list})
}

func (th *templateHandler) handleDetail(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	_, byName := th.resolve(r.Context())
	t, ok := byName[name]
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "template not found"})
		return
	}
	writeJSON(w, http.StatusOK, templateToDetail(t))
}

// --- Conversion helpers ---

func templateToDetail(t *tpl.Template) TemplateDetailDTO {
	return TemplateDetailDTO{
		Name:           t.Metadata.Name,
		Version:        t.Metadata.Version,
		Title:          t.Spec.Title,
		Description:    t.Spec.Description,
		Category:       t.Spec.Category,
		Engine:         t.Spec.Engine.Type,
		Components:     componentsToTemplateDTO(t.Spec.Components),
		Inputs:         inputsToDTO(t.Spec.Inputs),
		AdvancedInputs: inputsToDTO(t.Spec.AdvancedInputs),
		SecretInputs:   secretInputsToDTO(t.Spec.SecretInputs),
		Presets:        presetsToDTO(t.Spec.Presets),
	}
}

func componentsToTemplateDTO(components []tpl.TemplateComponent) []TemplateComponentDTO {
	if len(components) == 0 {
		return []TemplateComponentDTO{}
	}
	out := make([]TemplateComponentDTO, len(components))
	for i, c := range components {
		out[i] = TemplateComponentDTO{
			Name:               c.Name,
			Type:               string(c.Type),
			Required:           c.Required,
			DefaultEnabled:     c.IsDefaultEnabled(),
			PreviewEnabled:     c.PreviewEnabled,
			Exposed:            c.Exposed,
			Produces:           c.Produces,
			OptionallyProduces: c.OptionallyProduces,
			Capabilities:       c.ResolvedCapabilities(),
		}
	}
	return out
}

func inputsToDTO(inputs []tpl.Input) []InputDTO {
	if len(inputs) == 0 {
		return []InputDTO{}
	}
	dtos := make([]InputDTO, len(inputs))
	for i, inp := range inputs {
		opts := inp.Options
		if opts == nil {
			opts = []string{}
		}
		dtos[i] = InputDTO{
			Name:        inp.Name,
			Title:       inp.Title,
			Type:        string(inp.Type),
			Description: inp.Description,
			Required:    inp.Required,
			Default:     inp.Default,
			Options:     opts,
			Min:         inp.Min,
			Max:         inp.Max,
			Pattern:     inp.Pattern,
		}
	}
	return dtos
}

func secretInputsToDTO(inputs []tpl.SecretInput) []SecretInputDTO {
	if len(inputs) == 0 {
		return []SecretInputDTO{}
	}
	dtos := make([]SecretInputDTO, len(inputs))
	for i, si := range inputs {
		dtos[i] = SecretInputDTO{
			Name:        si.Name,
			Title:       si.Title,
			Description: si.Description,
			SecretRef:   si.SecretRef,
		}
	}
	return dtos
}

func presetsToDTO(presets []tpl.Preset) []PresetDTO {
	if len(presets) == 0 {
		return []PresetDTO{}
	}
	dtos := make([]PresetDTO, len(presets))
	for i, p := range presets {
		vals := p.Values
		if vals == nil {
			vals = map[string]any{}
		}
		dtos[i] = PresetDTO{
			Name:        p.Name,
			Title:       p.Title,
			Description: p.Description,
			Values:      vals,
		}
	}
	return dtos
}
