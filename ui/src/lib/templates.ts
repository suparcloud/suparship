import { api } from "./api";
import type {
  TemplateDetail,
  TemplateImportPreview,
  TemplateImportResult,
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
