package server

import (
	"net/http"

	"github.com/suparcloud/suparship/internal/tpl"
)

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
	Name           string           `json:"name"`
	Version        string           `json:"version"`
	Title          string           `json:"title"`
	Description    string           `json:"description,omitempty"`
	Category       string           `json:"category"`
	Engine         string           `json:"engine"`
	Inputs         []InputDTO       `json:"inputs"`
	AdvancedInputs []InputDTO       `json:"advancedInputs"`
	SecretInputs   []SecretInputDTO `json:"secretInputs"`
	Presets        []PresetDTO      `json:"presets"`
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

// templateHandler serves template metadata endpoints.
type templateHandler struct {
	auth   *authHandler
	list   []TemplateSummaryDTO
	byName map[string]*tpl.Template
}

func newTemplateHandler(auth *authHandler, templates []*tpl.Template) *templateHandler {
	list := make([]TemplateSummaryDTO, len(templates))
	byName := make(map[string]*tpl.Template, len(templates))

	for i, t := range templates {
		list[i] = TemplateSummaryDTO{
			Name:        t.Metadata.Name,
			Version:     t.Metadata.Version,
			Title:       t.Spec.Title,
			Description: t.Spec.Description,
			Category:    t.Spec.Category,
			Engine:      t.Spec.Engine.Type,
		}
		byName[t.Metadata.Name] = t
	}

	return &templateHandler{auth: auth, list: list, byName: byName}
}

func (th *templateHandler) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/templates", th.auth.requireAuth(th.handleList))
	mux.HandleFunc("GET /api/v1/templates/{name}", th.auth.requireAuth(th.handleDetail))
}

func (th *templateHandler) handleList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, TemplatesResponse{Templates: th.list})
}

func (th *templateHandler) handleDetail(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	t, ok := th.byName[name]
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
		Inputs:         inputsToDTO(t.Spec.Inputs),
		AdvancedInputs: inputsToDTO(t.Spec.AdvancedInputs),
		SecretInputs:   secretInputsToDTO(t.Spec.SecretInputs),
		Presets:        presetsToDTO(t.Spec.Presets),
	}
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
