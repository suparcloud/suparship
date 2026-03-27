import { BrowserRouter, Route, Routes } from "react-router-dom";

import { AppShell } from "./components/AppShell";
import { AuthGuard } from "./components/AuthGuard";
import { AuthProvider } from "./lib/AuthContext";
import { AuthSettings } from "./pages/AuthSettings";
import { Dashboard } from "./pages/Dashboard";
import { Login } from "./pages/Login";
import { Onboarding } from "./pages/Onboarding";
import { Previews } from "./pages/Previews";
import { ServiceDetail } from "./pages/ServiceDetail";

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
                path="/projects/:project/services/:service"
                element={<ServiceDetail />}
              />
              <Route path="/previews" element={<Previews />} />
              <Route path="/settings/auth" element={<AuthSettings />} />
            </Route>
          </Route>
        </Routes>
      </AuthProvider>
    </BrowserRouter>
  );
}
