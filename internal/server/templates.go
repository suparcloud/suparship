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
	Name           string                       `json:"name"`
	Version        string                       `json:"version"`
	Title          string                       `json:"title"`
	Description    string                       `json:"description,omitempty"`
	Category       string                       `json:"category"`
	Engine         string                       `json:"engine"`
	Components     []TemplateComponentDTO       `json:"components"`
	Inputs         []InputDTO                   `json:"inputs"`
	AdvancedInputs []InputDTO                   `json:"advancedInputs"`
	SecretInputs   []SecretInputDTO             `json:"secretInputs"`
	Presets        []PresetDTO                  `json:"presets"`
}

// TemplateComponentDTO mirrors tpl.TemplateComponent for the wire,
// with capabilities resolved (no pointers, type-based defaults
// already filled in) so the UI can drive form rendering directly.
type TemplateComponentDTO struct {
	Name               string                  `json:"name"`
	Type               string                  `json:"type"`
	Required           bool                    `json:"required"`
	DefaultEnabled     bool                    `json:"defaultEnabled"`
	PreviewEnabled     bool                    `json:"previewEnabled"`
	Exposed            bool                    `json:"exposed"`
	Produces           []string                `json:"produces,omitempty"`
	OptionallyProduces []string                `json:"optionallyProduces,omitempty"`
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
// Resolution policy: built-ins take precedence on name collisions — that
// way an operator who accidentally imports a chart named "web-service"
// can't shadow the platform's golden path.
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
	mux.HandleFunc("DELETE /api/v1/templates/{name}", th.adminOrAuth()(th.handleDelete))
}

// handleDelete removes a template's cluster ConfigMap. Returns 204 on
// success, 404 when the template isn't cluster-stored (built-ins shipped
// via --templates-dir live on disk and can't be deleted from the API),
// and 409 when the name shadows a built-in (deletion would re-expose the
// built-in, which is confusing — operator should rename the built-in
// instead).
//
// Note: for templates synced from an external repo, deletion succeeds
// but the next sync tick re-creates the ConfigMap. The UI surfaces a
// warning before calling this; callers running scripts should be aware.
func (th *templateHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "template name required"})
		return
	}
	for _, t := range th.builtin {
		if t.Metadata.Name == name {
			writeJSON(w, http.StatusConflict, errorResponse{
				Error: "template " + name + " is built-in and shipped with the binary; cluster ConfigMaps with the same name are shadowed and can't be deleted via this endpoint",
			})
			return
		}
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
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "template " + name + " not found in cluster"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resolve merges built-ins with the current cluster list. Cluster fetch
// errors are logged but not surfaced — the gallery should still render the
// built-ins when the API server is misbehaving rather than 500ing entirely.
func (th *templateHandler) resolve(ctx context.Context) ([]*tpl.Template, map[string]*tpl.Template) {
	seen := make(map[string]bool, len(th.builtin))
	merged := make([]*tpl.Template, 0, len(th.builtin))
	for _, t := range th.builtin {
		merged = append(merged, t)
		seen[t.Metadata.Name] = true
	}
	if th.clusterLoader != nil {
		cluster, err := th.clusterLoader(ctx)
		if err != nil && th.logger != nil {
			th.logger.Warn("template list: cluster fetch failed; serving built-ins only", "err", err)
		}
		for _, t := range cluster {
			if seen[t.Metadata.Name] {
				continue
			}
			merged = append(merged, t)
			seen[t.Metadata.Name] = true
		}
	}
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Metadata.Name < merged[j].Metadata.Name
	})
	byName := make(map[string]*tpl.Template, len(merged))
	for _, t := range merged {
		byName[t.Metadata.Name] = t
	}
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
