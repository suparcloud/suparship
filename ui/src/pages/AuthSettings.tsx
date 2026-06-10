import { useEffect, useState } from "react";
import { toast } from "sonner";

import { getAuthConfig, updateAuthConfig } from "../lib/settings";
import type { OIDCConfig } from "../types";

const inputClass =
  "block w-full rounded-md border border-gray-300 px-3 py-2 text-sm shadow-sm focus:border-blue-500 focus:ring-1 focus:ring-blue-500";

function Field({
  label,
  children,
  help,
}: {
  label: string;
  children: React.ReactNode;
  help?: string;
}) {
  return (
    <label className="block">
      <span className="text-sm font-medium text-gray-700">{label}</span>
      <div className="mt-1">{children}</div>
      {help && <p className="mt-1 text-xs text-gray-400">{help}</p>}
    </label>
  );
}

export function AuthSettings() {
  const [cfg, setCfg] = useState<OIDCConfig | null>(null);
  const [secret, setSecret] = useState(""); // write-only; never populated from server
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    async function load() {
      try {
        const res = await getAuthConfig();
        if (!cancelled) setCfg(res.oidc);
      } catch (err) {
        if (!cancelled)
          setError(err instanceof Error ? err.message : "Failed to load");
      } finally {
        if (!cancelled) setLoading(false);
      }
    }
    load();
    return () => {
      cancelled = true;
    };
  }, []);

  const update = (partial: Partial<OIDCConfig>) =>
    setCfg((prev) => (prev ? { ...prev, ...partial } : prev));

  const handleSave = async () => {
    if (!cfg) return;
    setSaving(true);
    try {
      const res = await updateAuthConfig({
        enabled: cfg.enabled,
        issuerURL: cfg.issuerURL,
        clientID: cfg.clientID,
        clientSecret: secret || undefined,
        redirectURL: cfg.redirectURL,
        scopes: cfg.scopes,
        usernameClaim: cfg.usernameClaim,
        groupsClaim: cfg.groupsClaim,
      });
      setCfg(res.oidc);
      setSecret("");
      toast.success("Authentication settings saved");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Save failed");
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return (
      <div className="space-y-4">
        <div className="h-8 w-48 animate-pulse rounded bg-gray-100" />
        <div className="h-64 animate-pulse rounded-lg bg-gray-50" />
      </div>
    );
  }

  if (error || !cfg) {
    return (
      <div className="rounded-lg border border-red-200 bg-red-50 p-4">
        <p className="text-sm text-red-700">
          Failed to load auth config: {error}
        </p>
      </div>
    );
  }

  const missingRequired =
    cfg.enabled && (!cfg.issuerURL || !cfg.clientID || !cfg.redirectURL);

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-semibold text-gray-900">
          Authentication Settings
        </h1>
        <p className="mt-1 text-sm text-gray-500">
          Configure OpenID Connect (OIDC) single sign-on. The local admin
          credential always remains as break-glass access.
        </p>
      </div>

      <div className="max-w-xl space-y-6 rounded-lg border border-gray-200 bg-white p-6">
        <div className="flex items-center gap-3">
          <label className="relative inline-flex cursor-pointer items-center">
            <input
              type="checkbox"
              className="peer sr-only"
              checked={cfg.enabled}
              onChange={(e) => update({ enabled: e.target.checked })}
            />
            <div className="peer h-6 w-11 rounded-full bg-gray-200 after:absolute after:left-[2px] after:top-[2px] after:h-5 after:w-5 after:rounded-full after:border after:border-gray-300 after:bg-white after:transition-all after:content-[''] peer-checked:bg-blue-600 peer-checked:after:translate-x-full peer-checked:after:border-white" />
          </label>
          <span className="text-sm font-medium text-gray-700">
            {cfg.enabled ? "SSO enabled" : "SSO disabled"}
          </span>
        </div>

        {cfg.enabled && (
          <>
            <Field
              label="Issuer URL"
              help="OIDC issuer; discovery happens at issuer/.well-known/openid-configuration"
            >
              <input
                className={inputClass}
                value={cfg.issuerURL}
                onChange={(e) => update({ issuerURL: e.target.value })}
                placeholder="https://accounts.google.com"
              />
            </Field>

            <Field label="Client ID">
              <input
                className={inputClass}
                value={cfg.clientID}
                onChange={(e) => update({ clientID: e.target.value })}
                placeholder="suparship"
              />
            </Field>

            <Field
              label="Client Secret"
              help={
                cfg.clientSecretSet
                  ? "A client secret is configured. Leave blank to keep it; enter a new value to replace it."
                  : "Stored in a Kubernetes Secret, never in config."
              }
            >
              <input
                type="password"
                className={inputClass}
                value={secret}
                onChange={(e) => setSecret(e.target.value)}
                placeholder={cfg.clientSecretSet ? "configured ✓" : "enter client secret"}
                autoComplete="new-password"
              />
            </Field>

            <Field
              label="Redirect URL"
              help="suparShip's callback URL, registered with your IdP"
            >
              <input
                className={inputClass}
                value={cfg.redirectURL}
                onChange={(e) => update({ redirectURL: e.target.value })}
                placeholder="https://suparship.example.com/api/v1/auth/oidc/callback"
              />
            </Field>

            <Field
              label="Scopes"
              help="Space- or comma-separated OIDC scopes requested at login"
            >
              <input
                className={inputClass}
                value={cfg.scopes.join(" ")}
                onChange={(e) =>
                  update({
                    scopes: e.target.value
                      .split(/[\s,]+/)
                      .map((s) => s.trim())
                      .filter(Boolean),
                  })
                }
                placeholder="openid profile email groups"
              />
            </Field>

            <div className="grid grid-cols-2 gap-4">
              <Field
                label="Username Claim"
                help="ID-token claim used as the username"
              >
                <input
                  className={inputClass}
                  value={cfg.usernameClaim}
                  onChange={(e) => update({ usernameClaim: e.target.value })}
                  placeholder="email"
                />
              </Field>
              <Field
                label="Groups Claim"
                help="Claim matched against role-binding groups"
              >
                <input
                  className={inputClass}
                  value={cfg.groupsClaim}
                  onChange={(e) => update({ groupsClaim: e.target.value })}
                  placeholder="groups"
                />
              </Field>
            </div>
          </>
        )}

        <div className="pt-2">
          <button
            onClick={handleSave}
            disabled={saving || missingRequired}
            className="rounded-md bg-blue-600 px-4 py-2 text-sm font-medium text-white shadow-sm hover:bg-blue-700 disabled:opacity-50"
          >
            {saving ? "Saving..." : "Save"}
          </button>
          {missingRequired && (
            <p className="mt-2 text-xs text-amber-600">
              Issuer URL, Client ID, and Redirect URL are required to enable SSO.
            </p>
          )}
        </div>
      </div>
    </div>
  );
}
