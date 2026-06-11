import { NavLink } from "react-router-dom";

import { useAuth } from "../lib/AuthContext";

const mainNav = [
  { to: "/", label: "Dashboard" },
  { to: "/templates", label: "Templates" },
  { to: "/previews", label: "Previews" },
];

const settingsNav = [
  { to: "/settings/org", label: "Organization" },
  { to: "/settings/teams", label: "Teams" },
  { to: "/settings/clusters", label: "Clusters" },
  { to: "/settings/gitops", label: "GitOps" },
  { to: "/settings/registry", label: "Registry" },
  { to: "/settings/platform", label: "Platform" },
  { to: "/settings/auth", label: "Auth" },
];

function navLinkClass({ isActive }: { isActive: boolean }) {
  return `rounded-md px-3 py-2 text-sm font-medium transition-colors ${
    isActive
      ? "bg-gray-100 text-gray-900"
      : "text-gray-600 hover:bg-gray-50 hover:text-gray-900"
  }`;
}

export function Sidebar() {
  const { user } = useAuth();
  // Org-level Settings are an org_admin/platform area; the read APIs behind
  // them are org_admin-gated, so hide the section for non-admins (they'd only
  // hit 403s). Developers use environments/profiles via the app flows.
  const isOrgAdmin = user?.role === "org_admin";

  return (
    <aside className="flex w-56 flex-col border-r border-gray-200 bg-white py-4">
      <nav className="flex flex-1 flex-col gap-1 px-3">
        {mainNav.map((item) => (
          <NavLink
            key={item.to}
            to={item.to}
            end={item.to === "/"}
            className={navLinkClass}
          >
            {item.label}
          </NavLink>
        ))}

        {isOrgAdmin && (
          <>
            <div className="mb-1 mt-5 px-3 text-xs font-semibold uppercase tracking-wider text-gray-400">
              Settings
            </div>
            {settingsNav.map((item) => (
              <NavLink key={item.to} to={item.to} className={navLinkClass}>
                {item.label}
              </NavLink>
            ))}
          </>
        )}
      </nav>
    </aside>
  );
}
