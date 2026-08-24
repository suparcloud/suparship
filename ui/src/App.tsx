import { lazy, Suspense } from "react";
import { BrowserRouter, Route, Routes } from "react-router-dom";
import { Toaster } from "sonner";

import { AppShell } from "./components/AppShell";
import { AuthGuard } from "./components/AuthGuard";
import { AuthProvider } from "./lib/AuthContext";

// Pages are lazy-loaded so the first paint ships only the route the user landed
// on, not every page (AppDetail, all settings pages, NewService, …) in one
// ~800 KB chunk. Each import() becomes its own chunk fetched on demand. Named
// exports are mapped to a default for React.lazy.
const named = <M, K extends keyof M>(p: Promise<M>, key: K) =>
  p.then((m) => ({ default: m[key] as unknown as React.ComponentType }));

const AppDetail = lazy(() => named(import("./pages/AppDetail"), "AppDetail"));
const AuthSettings = lazy(() => named(import("./pages/AuthSettings"), "AuthSettings"));
const InviteAccept = lazy(() => named(import("./pages/InviteAccept"), "InviteAccept"));
const ClusterSettings = lazy(() => named(import("./pages/ClusterSettings"), "ClusterSettings"));
const Dashboard = lazy(() => named(import("./pages/Dashboard"), "Dashboard"));
const GitOpsSettings = lazy(() => named(import("./pages/GitOpsSettings"), "GitOpsSettings"));
const PlatformSettings = lazy(() => named(import("./pages/PlatformSettings"), "PlatformSettings"));
const RegistrySettings = lazy(() => named(import("./pages/RegistrySettings"), "RegistrySettings"));
const Login = lazy(() => named(import("./pages/Login"), "Login"));
const Onboarding = lazy(() => named(import("./pages/Onboarding"), "Onboarding"));
const OrgSettings = lazy(() => named(import("./pages/OrgSettings"), "OrgSettings"));
const Previews = lazy(() => named(import("./pages/Previews"), "Previews"));
const ProjectDetail = lazy(() => named(import("./pages/ProjectDetail"), "ProjectDetail"));
const StackDetail = lazy(() => named(import("./pages/StackDetail"), "StackDetail"));
const ProjectSettings = lazy(() => named(import("./pages/ProjectSettings"), "ProjectSettings"));
const NewService = lazy(() => named(import("./pages/NewService"), "NewService"));
const ServiceDetail = lazy(() => named(import("./pages/ServiceDetail"), "ServiceDetail"));
const TeamSettings = lazy(() => named(import("./pages/TeamSettings"), "TeamSettings"));
const TemplateDetail = lazy(() => named(import("./pages/TemplateDetail"), "TemplateDetail"));
const TemplateImport = lazy(() => named(import("./pages/TemplateImport"), "TemplateImport"));
const TemplateSources = lazy(() => named(import("./pages/TemplateSources"), "TemplateSources"));
const Templates = lazy(() => named(import("./pages/Templates"), "Templates"));

function RouteFallback() {
  return (
    <div className="flex h-64 items-center justify-center">
      <div className="h-6 w-6 animate-spin rounded-full border-2 border-gray-300 border-t-gray-600" />
    </div>
  );
}

export function App() {
  return (
    <BrowserRouter>
      <AuthProvider>
        <Toaster position="top-right" richColors closeButton />
        <Suspense fallback={<RouteFallback />}>
          <Routes>
            <Route path="/login" element={<Login />} />
            <Route path="/invite/:token" element={<InviteAccept />} />

            <Route element={<AuthGuard />}>
              <Route path="/onboarding" element={<Onboarding />} />
              <Route element={<AppShell />}>
                <Route index element={<Dashboard />} />
                <Route
                  path="/projects/:project"
                  element={<ProjectDetail />}
                />
                <Route
                  path="/projects/:project/settings"
                  element={<ProjectSettings />}
                />
                <Route
                  path="/projects/:project/stacks/:stack"
                  element={<StackDetail />}
                />
                <Route
                  path="/projects/:project/apps/:app"
                  element={<AppDetail />}
                />
                {/* Primary app creation route */}
                <Route
                  path="/projects/:project/apps/new"
                  element={<NewService />}
                />
                {/* Legacy route — kept for backward compatibility */}
                <Route
                  path="/projects/:project/services/new"
                  element={<NewService />}
                />
                {/* Legacy route preserved for backward compatibility */}
                <Route
                  path="/projects/:project/services/:service"
                  element={<ServiceDetail />}
                />
                <Route path="/templates" element={<Templates />} />
                <Route path="/templates/import" element={<TemplateImport />} />
                <Route path="/templates/sources" element={<TemplateSources />} />
                <Route path="/templates/:name" element={<TemplateDetail />} />
                <Route path="/previews" element={<Previews />} />
                <Route path="/settings/org" element={<OrgSettings />} />
                <Route path="/settings/teams" element={<TeamSettings />} />
                <Route path="/settings/auth" element={<AuthSettings />} />
                <Route path="/settings/clusters" element={<ClusterSettings />} />
                <Route path="/settings/gitops" element={<GitOpsSettings />} />
                <Route path="/settings/registry" element={<RegistrySettings />} />
                <Route path="/settings/platform" element={<PlatformSettings />} />
              </Route>
            </Route>
          </Routes>
        </Suspense>
      </AuthProvider>
    </BrowserRouter>
  );
}
