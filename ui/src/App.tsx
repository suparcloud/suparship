import { BrowserRouter, Route, Routes } from "react-router-dom";

import { AppShell } from "./components/AppShell";
import { AuthGuard } from "./components/AuthGuard";
import { AuthProvider } from "./lib/AuthContext";
import { AuthSettings } from "./pages/AuthSettings";
import { Dashboard } from "./pages/Dashboard";
import { Login } from "./pages/Login";
import { Onboarding } from "./pages/Onboarding";
import { OrgSettings } from "./pages/OrgSettings";
import { Previews } from "./pages/Previews";
import { NewService } from "./pages/NewService";
import { ServiceDetail } from "./pages/ServiceDetail";
import { TeamSettings } from "./pages/TeamSettings";
import { TemplateDetail } from "./pages/TemplateDetail";
import { Templates } from "./pages/Templates";

export function App() {
  return (
    <BrowserRouter>
      <AuthProvider>
        <Routes>
          <Route path="/login" element={<Login />} />

          <Route element={<AuthGuard />}>
            <Route path="/onboarding" element={<Onboarding />} />
            <Route element={<AppShell />}>
              <Route index element={<Dashboard />} />
              <Route
                path="/projects/:project/services/new"
                element={<NewService />}
              />
              <Route
                path="/projects/:project/services/:service"
                element={<ServiceDetail />}
              />
              <Route path="/templates" element={<Templates />} />
              <Route path="/templates/:name" element={<TemplateDetail />} />
              <Route path="/previews" element={<Previews />} />
              <Route path="/settings/org" element={<OrgSettings />} />
              <Route path="/settings/teams" element={<TeamSettings />} />
              <Route path="/settings/auth" element={<AuthSettings />} />
            </Route>
          </Route>
        </Routes>
      </AuthProvider>
    </BrowserRouter>
  );
}
