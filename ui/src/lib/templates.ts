import { api } from "./api";
import type { TemplatesResponse, TemplateDetail } from "../types";

export function fetchTemplates(): Promise<TemplatesResponse> {
  return api.get<TemplatesResponse>("/templates");
}

export function fetchTemplate(name: string): Promise<TemplateDetail> {
  return api.get<TemplateDetail>(`/templates/${encodeURIComponent(name)}`);
}
