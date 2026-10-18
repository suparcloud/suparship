import { Outlet } from "react-router-dom";

import { ReadFreshness } from "./ReadFreshness";

import { Header } from "./Header";
import { Sidebar } from "./Sidebar";

export function AppShell() {
  return (
    <div className="flex h-screen flex-col">
      <Header />
      <div className="flex flex-1 overflow-hidden">
        <Sidebar />
        <main className="flex-1 overflow-y-auto p-6">
          <ReadFreshness />
          <Outlet />
        </main>
      </div>
    </div>
  );
}
