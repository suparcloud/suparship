import { type FormEvent, useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";

import { acceptInvite, getInvite } from "../lib/auth";
import { ApiError } from "../lib/api";

// InviteAccept is the public landing page for one-time invite links
// (/invite/:token). A valid invite greets the user by name and asks them to
// choose a password; on success the server sets the session cookie, so the
// user lands in the product already signed in. The link is single-use: once
// redeemed (or expired) it shows the ask-your-admin state.
export function InviteAccept() {
  const { token = "" } = useParams();
  const navigate = useNavigate();

  const [checking, setChecking] = useState(true);
  const [username, setUsername] = useState<string | null>(null);
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    let cancelled = false;
    getInvite(token)
      .then((res) => {
        if (!cancelled) setUsername(res.valid && res.username ? res.username : null);
      })
      .catch(() => {
        if (!cancelled) setUsername(null);
      })
      .finally(() => {
        if (!cancelled) setChecking(false);
      });
    return () => {
      cancelled = true;
    };
  }, [token]);

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (password !== confirm) {
      setError("Passwords do not match.");
      return;
    }
    setError("");
    setSubmitting(true);
    try {
      await acceptInvite(token, password);
      // Full reload so AuthContext picks up the fresh session cookie.
      window.location.href = "/";
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.status === 410) {
          setUsername(null); // consumed while the form was open
        } else {
          setError(err.message || "Could not set your password.");
        }
      } else {
        setError("Could not reach the server.");
      }
      setSubmitting(false);
    }
  };

  return (
    <div className="flex min-h-screen items-center justify-center bg-gray-50">
      <div className="w-full max-w-sm rounded-lg border border-gray-200 bg-white p-8 shadow-sm">
        <h1 className="text-center text-2xl font-semibold tracking-tight">
          suparship
        </h1>

        {checking ? (
          <div className="mt-6 h-24 animate-pulse rounded bg-gray-100" />
        ) : username === null ? (
          <div className="mt-6 space-y-4 text-center">
            <p className="text-sm text-gray-700">
              This invite link is invalid, expired, or has already been used.
            </p>
            <p className="text-xs text-gray-500">
              Invite links work exactly once. Ask your administrator to send
              you a new one.
            </p>
            <button
              onClick={() => navigate("/login")}
              className="w-full rounded-md border border-gray-300 px-4 py-2 text-sm font-medium text-gray-700 transition-colors hover:bg-gray-50"
            >
              Go to sign in
            </button>
          </div>
        ) : (
          <>
            <p className="mt-1 text-center text-sm text-gray-500">
              Welcome, <span className="font-medium text-gray-900">{username}</span> —
              choose a password to finish setting up your account.
            </p>

            <form onSubmit={handleSubmit} className="mt-6 space-y-4">
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
                  autoComplete="new-password"
                  required
                  minLength={8}
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  className="mt-1 block w-full rounded-md border border-gray-300 px-3 py-2 text-sm shadow-sm transition-colors focus:border-gray-900 focus:outline-none focus:ring-1 focus:ring-gray-900"
                />
                <p className="mt-1 text-xs text-gray-400">At least 8 characters.</p>
              </div>

              <div>
                <label
                  htmlFor="confirm"
                  className="block text-sm font-medium text-gray-700"
                >
                  Confirm password
                </label>
                <input
                  id="confirm"
                  type="password"
                  autoComplete="new-password"
                  required
                  minLength={8}
                  value={confirm}
                  onChange={(e) => setConfirm(e.target.value)}
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
                {submitting ? "Setting password..." : "Set password & sign in"}
              </button>
            </form>
          </>
        )}
      </div>
    </div>
  );
}
