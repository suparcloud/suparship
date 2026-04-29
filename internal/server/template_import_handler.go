package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/kube"
	"github.com/suparcloud/suparship/internal/tpl"
	"github.com/suparcloud/suparship/internal/tpl/chartimport"
)

// maxImportUploadBytes caps the multipart payload at 2 MiB. Helm charts are
// almost always well under this; the cluster-side ConfigMap binaryData limit
// (1 MiB) is the real bottleneck, but we keep the request limit a touch
// looser so the user gets a useful error message rather than a truncated
// stream when the chart is too large.
const maxImportUploadBytes = 2 * 1024 * 1024

// templateImportHandler exposes the BYO-Helm-chart import flow:
//
//	POST /api/v1/templates/import/preview   — generate a template.yaml from
//	                                          an uploaded chart, no persistence
//	POST /api/v1/templates/import           — persist the template + chart
//	                                          bundle as a cluster ConfigMap
//
// Both endpoints accept multipart/form-data with a "chart" file (the .tgz).
// The save endpoint additionally accepts a "template" text field carrying
// the (potentially edited) template.yaml; when omitted, the auto-generated
// template is used.
//
// authMiddleware is expected to compose authentication + org_admin
// authorisation (typically auth.requireAuth(rbac.requireOrgAdmin(...))).
// Wiring it as a closure keeps this handler free of dependencies on the
// full rbacHandler.
type templateImportHandler struct {
	client         kubernetes.Interface
	authMiddleware func(http.HandlerFunc) http.HandlerFunc
	logger         *slog.Logger
}

func (h *templateImportHandler) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/templates/import/preview", h.authMiddleware(h.handlePreview))
	mux.HandleFunc("POST /api/v1/templates/import", h.authMiddleware(h.handleImport))
}

// templateImportPreviewResponse is the JSON body returned by the preview
// endpoint. The UI shows TemplateYAML in a code editor; Summary feeds the
// "what we detected" panel above it.
type templateImportPreviewResponse struct {
	TemplateYAML string                       `json:"templateYAML"`
	Summary      templateImportSummary        `json:"summary"`
	ChartFiles   []templateImportChartFileDTO `json:"chartFiles"`
}

// templateImportSummary collapses a few headline facts about the chart and
// the generated template so the UI can render a quick overview without
// re-parsing the YAML.
type templateImportSummary struct {
	ChartName      string `json:"chartName"`
	ChartVersion   string `json:"chartVersion"`
	AppVersion     string `json:"appVersion,omitempty"`
	Description    string `json:"description,omitempty"`
	HasSchema      bool   `json:"hasSchema"`
	InputCount     int    `json:"inputCount"`
	MappingCount   int    `json:"mappingCount"`
	ArchiveSize    int    `json:"archiveSize"`
}

// templateImportChartFileDTO surfaces just enough of the archive listing for
// the UI to render a "what's in the chart" tree without shipping the bytes.
type templateImportChartFileDTO struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// handlePreview parses an uploaded chart, generates a template.yaml from it,
// and returns both to the caller. Nothing is persisted.
func (h *templateImportHandler) handlePreview(w http.ResponseWriter, r *http.Request) {
	chartBytes, err := readChartUpload(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	arc, err := chartimport.ParseArchive(chartBytes)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "parse chart: " + err.Error()})
		return
	}
	tmpl, err := chartimport.ToTemplate(arc)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "generate template: " + err.Error()})
		return
	}
	yamlBytes, err := tpl.Marshal(tmpl)
	if err != nil {
		h.logger.Error("template import: marshal generated template", "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "marshal template: " + err.Error()})
		return
	}

	files, err := chartArchiveFileList(chartBytes)
	if err != nil {
		// File listing is a UI nicety — fall through with an empty list
		// rather than failing the whole preview.
		h.logger.Warn("template import: list archive files", "err", err)
	}

	writeJSON(w, http.StatusOK, templateImportPreviewResponse{
		TemplateYAML: string(yamlBytes),
		Summary: templateImportSummary{
			ChartName:    arc.Chart.Name,
			ChartVersion: arc.Chart.Version,
			AppVersion:   arc.Chart.AppVersion,
			Description:  arc.Chart.Description,
			HasSchema:    arc.Schema != nil,
			InputCount:   len(tmpl.Spec.Inputs),
			MappingCount: len(tmpl.Spec.Mappings),
			ArchiveSize:  len(chartBytes),
		},
		ChartFiles: files,
	})
}

// handleImport validates the (optionally edited) template.yaml, persists it
// to the cluster as a ConfigMap, and stores the chart bytes alongside it as
// binaryData["chart.tgz"] so the GitOps publisher can later untar the chart
// into the rendered repo without needing a local SUPARSHIP_TEMPLATES_DIR.
func (h *templateImportHandler) handleImport(w http.ResponseWriter, r *http.Request) {
	chartBytes, err := readChartUpload(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	arc, err := chartimport.ParseArchive(chartBytes)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "parse chart: " + err.Error()})
		return
	}

	// The "template" form field is optional: when present it carries the
	// operator-edited YAML; when absent we re-generate from the archive so
	// the API still works for non-interactive callers.
	var t *tpl.Template
	if raw := r.FormValue("template"); raw != "" {
		parsed, parseErr := tpl.Parse([]byte(raw))
		if parseErr != nil {
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "invalid template.yaml: " + parseErr.Error()})
			return
		}
		t = parsed
	} else {
		gen, genErr := chartimport.ToTemplate(arc)
		if genErr != nil {
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "generate template: " + genErr.Error()})
			return
		}
		t = gen
	}

	if err := kube.SaveTemplate(r.Context(), h.client, t, chartBytes); err != nil {
		h.logger.Error("template import: save template", "name", t.Metadata.Name, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "save template: " + err.Error()})
		return
	}

	h.logger.Info("template imported",
		"name", t.Metadata.Name,
		"chart", arc.Chart.Name,
		"version", arc.Chart.Version,
		"size", len(chartBytes),
	)
	writeJSON(w, http.StatusCreated, map[string]any{
		"name":    t.Metadata.Name,
		"version": t.Metadata.Version,
	})
}

// readChartUpload pulls the "chart" file out of a multipart request and
// returns its bytes. Enforces maxImportUploadBytes and returns user-facing
// errors that are safe to surface verbatim.
func readChartUpload(r *http.Request) ([]byte, error) {
	if err := r.ParseMultipartForm(maxImportUploadBytes); err != nil {
		return nil, fmt.Errorf("read multipart form: %w", err)
	}
	file, _, err := r.FormFile("chart")
	if err != nil {
		return nil, fmt.Errorf("missing 'chart' file in multipart form: %w", err)
	}
	defer file.Close()
	limited := io.LimitReader(file, maxImportUploadBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read chart upload: %w", err)
	}
	if len(data) > maxImportUploadBytes {
		return nil, fmt.Errorf("chart upload exceeds %d byte limit", maxImportUploadBytes)
	}
	return data, nil
}

// chartArchiveFileList does a lightweight second pass over the .tgz to build
// a path/size listing for the UI tree. Kept here (rather than in chartimport)
// because chartimport's ParseArchive is intentionally focused on metadata —
// the file tree is a UI concern.
func chartArchiveFileList(data []byte) ([]templateImportChartFileDTO, error) {
	out := []templateImportChartFileDTO{}
	if err := walkTGZ(data, func(name string, size int64) {
		out = append(out, templateImportChartFileDTO{Path: name, Size: size})
	}); err != nil {
		return out, err
	}
	return out, nil
}

// walkTGZ iterates regular file entries in a gzip-tarred archive, calling fn
// for each. Defined alongside the handler so the chart parsing stays a
// pure-function island in chartimport.
func walkTGZ(data []byte, fn func(name string, size int64)) error {
	rd, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer rd.Close()
	tr := tar.NewReader(rd)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeReg || hdr.Typeflag == tar.TypeRegA {
			fn(hdr.Name, hdr.Size)
		}
	}
	return nil
}
