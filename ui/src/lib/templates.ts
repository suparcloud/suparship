import { api } from "./api";
import type {
  TemplateCredentialsRequest,
  TemplateCredentialsResponse,
  TemplateDetail,
  TemplateImportPreview,
  TemplateImportResult,
  TemplateRegistry,
  TemplateRegistryResponse,
  TemplateSyncResponse,
  TemplateTestConnectionResult,
  TemplatesResponse,
} from "../types";

export function fetchTemplates(): Promise<TemplatesResponse> {
  return api.get<TemplatesResponse>("/templates");
}

export function fetchTemplate(name: string): Promise<TemplateDetail> {
  return api.get<TemplateDetail>(`/templates/${encodeURIComponent(name)}`);
}

// importTemplatePreview uploads a Helm chart .tgz and gets back a generated
// template.yaml plus a summary of what was detected — no persistence. Used
// to drive the review step in the BYO-chart wizard before the operator
// commits the import.
export function importTemplatePreview(
  file: File,
): Promise<TemplateImportPreview> {
  const fd = new FormData();
  fd.append("chart", file);
  return api.postFormData<TemplateImportPreview>(
    "/templates/import/preview",
    fd,
  );
}

// importTemplate persists the template + chart bundle as a cluster
// ConfigMap. When templateYAML is omitted, the server regenerates from the
// archive — useful for non-interactive (CLI) callers.
export function importTemplate(
  file: File,
  templateYAML?: string,
): Promise<TemplateImportResult> {
  const fd = new FormData();
  fd.append("chart", file);
  if (templateYAML !== undefined) {
    fd.append("template", templateYAML);
  }
  return api.postFormData<TemplateImportResult>("/templates/import", fd);
}

// fetchTemplateRegistry returns the configured external sources + the
// resolved sync state. response.configured is false when no registry
// ConfigMap exists yet; the embedded registry is still safe to render
// (empty arrays).
export function fetchTemplateRegistry(): Promise<TemplateRegistryResponse> {
  return api.get<TemplateRegistryResponse>("/templates/registry");
}

// updateTemplateRegistry replaces the registry document. The caller is
// responsible for preserving fields it doesn't know about (e.g. Sources)
// — this UI sends back what it loaded, mutated.
export function updateTemplateRegistry(
  registry: TemplateRegistry,
): Promise<TemplateRegistryResponse> {
  return api.put<TemplateRegistryResponse>("/templates/registry", registry);
}

// syncAllSources triggers a sync across every configured external repo
// and returns per-source results inline so the UI can show partial-success
// surfaces ("3 imported, 1 failed") without a follow-up call.
export function syncAllSources(): Promise<TemplateSyncResponse> {
  return api.post<TemplateSyncResponse>("/templates/registry/sync");
}

// syncSource triggers a sync on one named external repo. The {name} must
// match an ExternalTemplateRepo.name in the registry.
export function syncSource(name: string): Promise<TemplateSyncResponse> {
  return api.post<TemplateSyncResponse>(
    `/templates/registry/sources/${encodeURIComponent(name)}/sync`,
  );
}

// setSourceCredentials seals the supplied credentials into a managed
// SealedSecret in suparship-system, points the source at it, and verifies
// against the live repo before sealing — the response carries the test
// result so the UI can show "saved & verified" in one round-trip.
export function setSourceCredentials(
  name: string,
  req: TemplateCredentialsRequest,
): Promise<TemplateCredentialsResponse> {
  return api.post<TemplateCredentialsResponse>(
    `/templates/registry/sources/${encodeURIComponent(name)}/credentials`,
    req,
  );
}

// testSourceConnection runs git ls-remote against the source's repoURL.
// When req carries plaintext creds those are used; otherwise the server
// falls back to credentials already stored under existingSecret. Returns
// 200 even on auth failure — `success` reports the auth outcome.
export function testSourceConnection(
  name: string,
  req: TemplateCredentialsRequest = {},
): Promise<TemplateTestConnectionResult> {
  return api.post<TemplateTestConnectionResult>(
    `/templates/registry/sources/${encodeURIComponent(name)}/test-connection`,
    req,
  );
}
