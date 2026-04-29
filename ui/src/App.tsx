import { BrowserRouter, Route, Routes } from "react-router-dom";
import { Toaster } from "sonner";

import { AppShell } from "./components/AppShell";
import { AuthGuard } from "./components/AuthGuard";
import { AuthProvider } from "./lib/AuthContext";
import { AppDetail } from "./pages/AppDetail";
import { AuthSettings } from "./pages/AuthSettings";
import { ClusterSettings } from "./pages/ClusterSettings";
import { Dashboard } from "./pages/Dashboard";
import { GitOpsSettings } from "./pages/GitOpsSettings";
import { PlatformSettings } from "./pages/PlatformSettings";
import { RegistrySettings } from "./pages/RegistrySettings";
import { Login } from "./pages/Login";
import { Onboarding } from "./pages/Onboarding";
import { OrgSettings } from "./pages/OrgSettings";
import { Previews } from "./pages/Previews";
import { ProjectDetail } from "./pages/ProjectDetail";
import { ProjectSettings } from "./pages/ProjectSettings";
import { NewService } from "./pages/NewService";
import { ServiceDetail } from "./pages/ServiceDetail";
import { TeamSettings } from "./pages/TeamSettings";
import { TemplateDetail } from "./pages/TemplateDetail";
import { TemplateImport } from "./pages/TemplateImport";
import { Templates } from "./pages/Templates";

export function App() {
  return (
    <BrowserRouter>
      <AuthProvider>
        <Toaster position="top-right" richColors closeButton />
        <Routes>
          <Route path="/login" element={<Login />} />

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
      </AuthProvider>
    </BrowserRouter>
  );
}
