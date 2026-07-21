import { api } from "./api";
import type {
  EffectiveValuesResponse,
  TemplateOverride,
  TemplateCredentialsRequest,
  TemplateCredentialsResponse,
  TemplateDetail,
  TemplateMetadataPatch,
  TemplateImportPreview,
  TemplateImportResult,
  TemplateRegistry,
  TemplateRegistryResponse,
  TemplateSyncResponse,
  TemplateTestConnectionResult,
  TemplateVersionsResponse,
  TemplatesResponse,
} from "../types";

export function fetchTemplates(): Promise<TemplatesResponse> {
  return api.get<TemplatesResponse>("/templates");
}

export function fetchTemplate(name: string): Promise<TemplateDetail> {
  return api.get<TemplateDetail>(`/templates/${encodeURIComponent(name)}`);
}

// updateTemplateMetadata edits an imported/BYO template's metadata in place
// (title/category/description/passthrough). org_admin; 409 for synced templates.
export function updateTemplateMetadata(
  name: string,
  patch: TemplateMetadataPatch,
): Promise<TemplateDetail> {
  return api.patch<TemplateDetail>(
    `/templates/${encodeURIComponent(name)}`,
    patch,
  );
}

// fetchTemplateEffectiveValues returns the starting values document for a
// not-yet-created app from this template: chart defaults ⊕ the template's
// platform/env defaults ⊕ the saved org override (for the given env). Backs the
// create form's read-only effective-values preview.
export function fetchTemplateEffectiveValues(
  name: string,
  env?: string,
): Promise<EffectiveValuesResponse> {
  const q = env ? `?env=${encodeURIComponent(env)}` : "";
  return api.get<EffectiveValuesResponse>(
    `/templates/${encodeURIComponent(name)}/effective-values${q}`,
  );
}

// fetchTemplateOverride returns the saved org-level platform override for a
// template (PE/SRE-authored). Empty object when none is set.
export function fetchTemplateOverride(name: string): Promise<TemplateOverride> {
  return api.get<TemplateOverride>(
    `/templates/${encodeURIComponent(name)}/overrides`,
  );
}

// updateTemplateOverride replaces the org-level platform override (org_admin).
// Send an empty override to clear it.
export function updateTemplateOverride(
  name: string,
  override: TemplateOverride,
): Promise<TemplateOverride> {
  return api.put<TemplateOverride>(
    `/templates/${encodeURIComponent(name)}/overrides`,
    override,
  );
}

// previewTemplateEffectiveValues previews chart ⊕ template ⊕ the supplied
// (unsaved) org override for an env (and optional target cluster) — backs the PE
// editor's live preview.
export function previewTemplateEffectiveValues(
  name: string,
  env: string,
  cluster: string,
  override: TemplateOverride,
  // preview layers the template's preview defaults + the preview override
  // (sent as override.envValues.preview) on top of the base-env effective.
  preview = false,
): Promise<EffectiveValuesResponse> {
  const params = new URLSearchParams();
  if (env) params.set("env", env);
  if (cluster) params.set("cluster", cluster);
  if (preview) params.set("preview", "true");
  const q = params.toString();
  return api.post<EffectiveValuesResponse>(
    `/templates/${encodeURIComponent(name)}/effective-values${q ? `?${q}` : ""}`,
    override,
  );
}

// deleteTemplate removes a cluster-stored template. Built-in templates
// shipped with the binary return 409; templates synced from an external
// repo will be re-created on the next sync tick — the UI warns before
// calling.
export function deleteTemplate(name: string): Promise<void> {
  return api.del(`/templates/${encodeURIComponent(name)}`);
}

// listTemplateVersions returns every persisted version of a template,
// descending by SemVer. Empty array for built-in templates that don't
// have archives.
export function listTemplateVersions(
  name: string,
): Promise<TemplateVersionsResponse> {
  return api.get<TemplateVersionsResponse>(
    `/templates/${encodeURIComponent(name)}/versions`,
  );
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
