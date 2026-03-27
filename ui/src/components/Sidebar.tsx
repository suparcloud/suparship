import { NavLink } from "react-router-dom";

const navItems = [
  { to: "/", label: "Dashboard" },
  { to: "/previews", label: "Previews" },
  { to: "/settings/auth", label: "Settings" },
];

export function Sidebar() {
  return (
    <aside className="flex w-56 flex-col border-r border-gray-200 bg-white py-4">
      <nav className="flex flex-1 flex-col gap-1 px-3">
        {navItems.map((item) => (
          <NavLink
            key={item.to}
            to={item.to}
            end={item.to === "/"}
            className={({ isActive }) =>
              `rounded-md px-3 py-2 text-sm font-medium transition-colors ${
                isActive
                  ? "bg-gray-100 text-gray-900"
                  : "text-gray-600 hover:bg-gray-50 hover:text-gray-900"
              }`
            }
          >
            {item.label}
          </NavLink>
        ))}
      </nav>
    </aside>
  );
}
