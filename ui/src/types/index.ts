export interface MetaInfo {
  app: string;
  version: string;
  commit: string;
  buildDate: string;
}

export interface Project {
  name: string;
}

export interface Service {
  name: string;
  project: string;
}

export interface PreviewEnvironment {
  name: string;
  status: string;
  url?: string;
}
