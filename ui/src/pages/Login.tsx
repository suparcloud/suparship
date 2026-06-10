import { type FormEvent, useEffect, useState } from "react";
import { Navigate, useNavigate, useSearchParams } from "react-router-dom";

import { useAuth } from "../lib/AuthContext";
import { api, ApiError } from "../lib/api";

interface AuthProviders {
  oidc: { enabled: boolean; loginURL?: string };
}

// Friendly messages for the ?error= codes the OIDC callback redirects with.
const ssoErrors: Record<string, string> = {
  sso_unavailable: "Single sign-on is not configured.",
  sso_init: "Could not start single sign-on. Check the OIDC configuration.",
  sso_state: "Single sign-on session expired or was tampered with. Try again.",
  sso_denied: "Single sign-on was denied by the identity provider.",
  sso_exchange: "Single sign-on failed during token exchange.",
  sso_no_id_token: "The identity provider returned no ID token.",
  sso_verify: "Could not verify the single sign-on identity.",
  sso_nonce: "Single sign-on verification failed (nonce mismatch). Try again.",
  sso_claims: "Could not read identity claims from the provider.",
  sso_session: "Could not create a session after single sign-on.",
};

export function Login() {
  const { user, login } = useAuth();
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();

  const [username, setUsername] = useState(
    import.meta.env.DEV ? "admin@local" : "",
  );
  const [password, setPassword] = useState("");
  const [error, setError] = useState(() => {
    const code = searchParams.get("error");
    return code ? (ssoErrors[code] ?? "Single sign-on failed.") : "";
  });
  const [submitting, setSubmitting] = useState(false);
  const [oidc, setOidc] = useState<AuthProviders["oidc"] | null>(null);

  useEffect(() => {
    let cancelled = false;
    api
      .get<AuthProviders>("/auth/providers")
      .then((res) => {
        if (!cancelled) setOidc(res.oidc);
      })
      .catch(() => {
        /* providers endpoint optional; ignore */
      });
    return () => {
      cancelled = true;
    };
  }, []);

  if (user) {
    return <Navigate to="/" replace />;
  }

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError("");
    setSubmitting(true);

    try {
      await login(username, password);
      navigate("/");
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.status === 401) {
          setError("Invalid username or password.");
        } else if (err.status === 503) {
          setError(
            "Admin credentials not configured. Run: suparship admin bootstrap",
          );
        } else {
          setError(err.message || "An unexpected error occurred.");
        }
      } else {
        setError("Could not reach the server.");
      }
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="flex min-h-screen items-center justify-center bg-gray-50">
      <div className="w-full max-w-sm rounded-lg border border-gray-200 bg-white p-8 shadow-sm">
        <h1 className="text-center text-2xl font-semibold tracking-tight">
          suparShip
        </h1>
        <p className="mt-1 text-center text-sm text-gray-500">
          Sign in to your account
        </p>

        {import.meta.env.DEV && (
          <div className="mt-6 rounded-lg border border-blue-200 bg-blue-50 px-4 py-3">
            <p className="text-xs font-semibold uppercase tracking-wide text-blue-600">
              Local dev mode
            </p>
            <div className="mt-1.5 space-y-0.5">
              <p className="text-xs text-blue-700">
                <span className="font-medium">Username</span>{" "}
                <span className="font-mono">admin@local</span>
              </p>
              <p className="text-xs text-blue-700">
                <span className="font-medium">Password</span>{" "}
                <span className="font-mono">admin123</span>
              </p>
            </div>
          </div>
        )}

        <form onSubmit={handleSubmit} className="mt-6 space-y-4">
          <div>
            <label
              htmlFor="username"
              className="block text-sm font-medium text-gray-700"
            >
              Username
            </label>
            <input
              id="username"
              type="text"
              autoComplete="username"
              required
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              className="mt-1 block w-full rounded-md border border-gray-300 px-3 py-2 text-sm shadow-sm transition-colors focus:border-gray-900 focus:outline-none focus:ring-1 focus:ring-gray-900"
            />
          </div>

          <div>
            <label
              htmlFor="password"
              className="block text-sm font-medium text-gray-700"
            >
              Password
            </label>
            <input
              id="password"
              type="password"
              autoComplete="current-password"
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              className="mt-1 block w-full rounded-md border border-gray-300 px-3 py-2 text-sm shadow-sm transition-colors focus:border-gray-900 focus:outline-none focus:ring-1 focus:ring-gray-900"
            />
          </div>

          {error && (
            <div className="rounded-md bg-red-50 px-3 py-2 text-sm text-red-700">
              {error}
            </div>
          )}

          <button
            type="submit"
            disabled={submitting}
            className="w-full rounded-md bg-gray-900 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-gray-900 focus:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50"
          >
            {submitting ? "Signing in..." : "Sign in"}
          </button>
        </form>

        {oidc?.enabled && oidc.loginURL && (
          <>
            <div className="my-6 flex items-center gap-3">
              <span className="h-px flex-1 bg-gray-200" />
              <span className="text-xs uppercase tracking-wide text-gray-400">
                or
              </span>
              <span className="h-px flex-1 bg-gray-200" />
            </div>
            <a
              href={oidc.loginURL}
              className="block w-full rounded-md border border-gray-300 px-4 py-2 text-center text-sm font-medium text-gray-700 transition-colors hover:bg-gray-50"
            >
              Sign in with SSO
            </a>
          </>
        )}
      </div>
    </div>
  );
}
