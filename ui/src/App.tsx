import { BrowserRouter, Route, Routes } from "react-router-dom";

import { AppShell } from "./components/AppShell";
import { AuthSettings } from "./pages/AuthSettings";
import { Dashboard } from "./pages/Dashboard";
import { Login } from "./pages/Login";
import { Onboarding } from "./pages/Onboarding";
import { Previews } from "./pages/Previews";
import { ServiceDetail } from "./pages/ServiceDetail";

export function App() {
  return (
    <BrowserRouter>
      <Routes>
        {/* Public routes (no app shell) */}
        <Route path="/login" element={<Login />} />
        <Route path="/onboarding" element={<Onboarding />} />

        {/* Authenticated routes (with app shell) */}
        <Route element={<AppShell />}>
          <Route index element={<Dashboard />} />
          <Route
            path="/projects/:project/services/:service"
            element={<ServiceDetail />}
          />
          <Route path="/previews" element={<Previews />} />
          <Route path="/settings/auth" element={<AuthSettings />} />
        </Route>
      </Routes>
    </BrowserRouter>
  );
}
