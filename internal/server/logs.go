package server

import (
	"net/http"
	"strconv"

	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/runtime"
)

// --- Logs DTOs ---

// LogsResponse is the JSON body returned by the logs endpoint.
type LogsResponse struct {
	Project   string `json:"project"`
	Service   string `json:"service"`
	Pod       string `json:"pod"`
	Container string `json:"container"`
	Logs      string `json:"logs"`
}

// --- Handler ---

// logsHandler serves container log output for a service.
type logsHandler struct {
	projectStore project.Store
	logsProvider runtime.LogsProvider
}

func newLogsHandler(store project.Store, lp runtime.LogsProvider) *logsHandler {
	return &logsHandler{projectStore: store, logsProvider: lp}
}

// parseLogsQuery extracts and validates query parameters for the logs
// endpoint. It returns a populated LogsRequest and the resolved namespace,
// or writes an error response and returns ok=false.
func (lh *logsHandler) parseLogsQuery(w http.ResponseWriter, r *http.Request) (
	projectName, serviceName string,
	req runtime.LogsRequest,
	ok bool,
) {
	projectName = r.PathValue("project")
	serviceName = r.PathValue("service")

	env := r.URL.Query().Get("environment")
	if env == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "query parameter 'environment' is required"})
		return "", "", runtime.LogsRequest{}, false
	}

	proj, err := lh.projectStore.Get(r.Context(), projectName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "project \"" + projectName + "\" not found",
		})
		return "", "", runtime.LogsRequest{}, false
	}

	var envFound bool
	for _, e := range proj.Spec.Environments {
		if e.Name == env {
			envFound = true
			break
		}
	}
	if !envFound {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Error: "environment \"" + env + "\" not found in project \"" + projectName + "\"",
		})
		return "", "", runtime.LogsRequest{}, false
	}

	var svcFound bool
	for _, svc := range proj.Spec.Services {
		if svc.Name == serviceName {
			svcFound = true
			break
		}
	}
	if !svcFound {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "service \"" + serviceName + "\" not found in project \"" + projectName + "\"",
		})
		return "", "", runtime.LogsRequest{}, false
	}

	namespace := runtime.Namespace(projectName, env)

	req = runtime.LogsRequest{
		Namespace: namespace,
		Pod:       r.URL.Query().Get("pod"),
		Container: r.URL.Query().Get("container"),
	}

	if tl := r.URL.Query().Get("tailLines"); tl != "" {
		n, err := strconv.ParseInt(tl, 10, 64)
		if err != nil || n < 0 {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "tailLines must be a non-negative integer"})
			return "", "", runtime.LogsRequest{}, false
		}
		req.TailLines = &n
	}

	return projectName, serviceName, req, true
}

func (lh *logsHandler) handleGetLogs(w http.ResponseWriter, r *http.Request) {
	projectName, serviceName, req, ok := lh.parseLogsQuery(w, r)
	if !ok {
		return
	}

	// If no pod is specified, auto-select the first matching pod.
	if req.Pod == "" {
		pods, err := lh.logsProvider.ListPods(r.Context(), req.Namespace, serviceName)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list pods"})
			return
		}
		if len(pods) == 0 {
			writeJSON(w, http.StatusNotFound, errorResponse{
				Error: "no pods found for service \"" + serviceName + "\" in namespace \"" + req.Namespace + "\"",
			})
			return
		}
		req.Pod = pods[0].Name

		if req.Container == "" && len(pods[0].Containers) == 1 {
			req.Container = pods[0].Containers[0]
		}
	}

	result, err := lh.logsProvider.GetLogs(r.Context(), req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{
			Error: "failed to read logs: " + err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, LogsResponse{
		Project:   projectName,
		Service:   serviceName,
		Pod:       result.Pod,
		Container: result.Container,
		Logs:      result.Logs,
	})
}
