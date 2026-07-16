package server

import (
	"net/http"
	"strconv"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/runtime"
)

// AppLogsResponse is the JSON body returned by the app-oriented logs endpoint.
type AppLogsResponse struct {
	Project     string `json:"project"`
	App         string `json:"app"`
	Environment string `json:"environment"`
	Namespace   string `json:"namespace"`
	Component   string `json:"component,omitempty"`
	Pod         string `json:"pod"`
	Container   string `json:"container"`
	Logs        string `json:"logs"`
}

// handleGetAppLogs serves GET /api/v1/projects/{project}/apps/{app}/logs.
//
// Query parameters:
//   - environment (required): the logical environment name (staging, prod, or preview name)
//   - component   (optional): component/workload name; used as the pod selector label
//   - pod         (optional): exact pod name; auto-selects first matching pod when absent
//   - container   (optional): container name within the pod
//   - tailLines   (optional): non-negative integer limiting output lines
//
// Resolution order:
//  1. Verify the app exists via AppStore.
//  2. Resolve the AppEnvironment record to obtain the Kubernetes namespace.
//  3. List pods in that namespace, optionally filtered by component name.
//  4. Stream/return logs for the selected pod and container.
func (ah *appHandler) handleGetAppLogs(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")

	envName := r.URL.Query().Get("environment")
	if envName == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "query parameter 'environment' is required"})
		return
	}

	app, err := ah.appStore.GetApp(r.Context(), projectName, appName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "app \"" + appName + "\" not found in project \"" + projectName + "\"",
		})
		return
	}

	appEnv, err := ah.appStore.GetAppEnvironment(r.Context(), projectName, appName, envName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "environment \"" + envName + "\" not found for app \"" + appName + "\"",
		})
		return
	}

	namespace := appEnv.Namespace
	component := r.URL.Query().Get("component")

	// Logs must be streamed from the env's workload cluster (remote in a
	// hub-spoke install), not suparship's own tooling cluster — the pods only
	// exist where ArgoCD deployed them. A preview runs on its base env's cluster
	// and its own name (e.g. "pr-712") is not a configured org env, so resolve
	// the cluster from the base env — else routing falls back to the local
	// cluster and the preview's pods (on the base env's cluster) aren't found.
	routeEnv := envName
	if appEnv.EnvType == domain.AppEnvPreview && appEnv.BaseEnv != "" {
		routeEnv = appEnv.BaseEnv
	}
	logsProvider := ah.logsProvider
	if client, err := ah.workloadClusterClient(r.Context(), projectName, appName, routeEnv); err != nil {
		writeJSON(w, http.StatusBadGateway, errorResponse{
			Error: "workload cluster for environment \"" + envName + "\" is unreachable: " + err.Error(),
		})
		return
	} else if client != nil {
		logsProvider = runtime.NewK8sLogsProvider(client)
	}
	if logsProvider == nil {
		writeJSON(w, http.StatusNotImplemented, errorResponse{Error: "logs are not available in this mode"})
		return
	}

	req := runtime.LogsRequest{
		Namespace: namespace,
		Pod:       r.URL.Query().Get("pod"),
		Container: r.URL.Query().Get("container"),
	}

	if tl := r.URL.Query().Get("tailLines"); tl != "" {
		n, err := strconv.ParseInt(tl, 10, 64)
		if err != nil || n < 0 {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "tailLines must be a non-negative integer"})
			return
		}
		req.TailLines = &n
	}

	if req.Pod == "" {
		// Resolve the workload instance label(s) to select pods on. A composed
		// component's pods are labelled app.kubernetes.io/instance={app}-{component}
		// (not {app}), so the raw component name won't match — map it to the
		// component's release name. With no component, union every component's pods
		// (single-source → just {app}).
		var instances []string
		if component != "" {
			if inst := app.InstanceForComponent(component); inst != "" {
				instances = []string{inst}
			} else {
				instances = []string{component} // BYO/unknown component: try it verbatim
			}
		} else {
			for _, wi := range app.WorkloadInstances() {
				instances = append(instances, wi.Instance)
			}
			if len(instances) == 0 {
				instances = []string{appName}
			}
		}

		var pods []runtime.PodInfo
		seen := map[string]bool{}
		for _, inst := range instances {
			ps, err := logsProvider.ListPods(r.Context(), namespace, inst)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list pods"})
				return
			}
			for _, p := range ps {
				if seen[p.Name] {
					continue
				}
				seen[p.Name] = true
				pods = append(pods, p)
			}
		}
		if len(pods) == 0 {
			writeJSON(w, http.StatusNotFound, errorResponse{
				Error: "no pods found for app \"" + appName + "\" in namespace \"" + namespace + "\"",
			})
			return
		}
		req.Pod = pods[0].Name

		if req.Container == "" && len(pods[0].Containers) == 1 {
			req.Container = pods[0].Containers[0]
		}
	}

	result, err := logsProvider.GetLogs(r.Context(), req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{
			Error: "failed to read logs: " + err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, AppLogsResponse{
		Project:     projectName,
		App:         appName,
		Environment: envName,
		Namespace:   namespace,
		Component:   component,
		Pod:         result.Pod,
		Container:   result.Container,
		Logs:        result.Logs,
	})
}
