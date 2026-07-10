import { useNavigate } from "react-router-dom";

import { useAuth } from "../lib/AuthContext";

export function Header() {
  const { user, logout } = useAuth();
  const navigate = useNavigate();

  const handleLogout = async () => {
    await logout();
    navigate("/login");
  };

  return (
    <header className="flex h-14 items-center justify-between border-b border-gray-200 bg-white px-6">
      <span className="text-lg font-semibold tracking-tight">suparship</span>
      {user && (
        <div className="flex items-center gap-3">
          <span className="text-sm text-gray-500">{user.username}</span>
          <button
            onClick={handleLogout}
            className="rounded-md px-3 py-1.5 text-sm text-gray-600 transition-colors hover:bg-gray-100 hover:text-gray-900"
          >
            Sign out
          </button>
        </div>
      )}
    </header>
  );
}
