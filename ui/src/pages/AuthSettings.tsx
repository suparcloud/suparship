import { useEffect, useState } from "react";
import { toast } from "sonner";

import {
  createLocalUser,
  deleteLocalUser,
  fetchTeams,
  getAuthConfig,
  listLocalUsers,
  reinviteLocalUser,
  updateAuthConfig,
} from "../lib/settings";
import type { LocalUser, LocalUserInvite } from "../lib/settings";
import type { OIDCConfig, TeamInfo } from "../types";

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


// ── Local users ───────────────────────────────────────────────────────────────
//
// The basic-auth escape hatch for orgs without an IdP: an admin creates a
// user, optionally picks teams, and hands over a one-time invite link. The
// user sets their password on first use; the link dies on redemption.
// Re-inviting an existing user doubles as password reset.

const statusChip: Record<string, { label: string; cls: string }> = {
  active: { label: "active", cls: "bg-emerald-50 text-emerald-700" },
  invited: { label: "invited", cls: "bg-blue-50 text-blue-700" },
  invite_expired: { label: "invite expired", cls: "bg-amber-50 text-amber-700" },
};

function InviteLinkModal({
  invite,
  onClose,
}: {
  invite: LocalUserInvite;
  onClose: () => void;
}) {
  const link = `${window.location.origin}/invite/${invite.inviteToken}`;
  const [copied, setCopied] = useState(false);
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/30 p-4">
      <div className="w-full max-w-lg rounded-lg bg-white p-6 shadow-xl">
        <h3 className="text-lg font-semibold text-gray-900">
          Invite link for {invite.username}
        </h3>
        <p className="mt-1 text-sm text-gray-500">
          Share this link with the user. It can be used <strong>once</strong>,
          expires {new Date(invite.expiresAt).toLocaleDateString()}, and will
          not be shown again.
        </p>
        <div className="mt-4 flex items-center gap-2">
          <input
            readOnly
            value={link}
            onFocus={(e) => e.target.select()}
            className="block w-full rounded-md border border-gray-300 bg-gray-50 px-3 py-2 font-mono text-xs"
          />
          <button
            onClick={() => {
              void navigator.clipboard.writeText(link);
              setCopied(true);
            }}
            className="whitespace-nowrap rounded-md bg-gray-900 px-3 py-2 text-sm font-medium text-white hover:bg-gray-700"
          >
            {copied ? "Copied!" : "Copy"}
          </button>
        </div>
        <div className="mt-5 flex justify-end">
          <button
            onClick={onClose}
            className="rounded-md border border-gray-300 px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50"
          >
            Done
          </button>
        </div>
      </div>
    </div>
  );
}

function LocalUsersSection() {
  const [users, setUsers] = useState<LocalUser[]>([]);
  const [teams, setTeams] = useState<TeamInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const [showAdd, setShowAdd] = useState(false);
  const [newUsername, setNewUsername] = useState("");
  const [newTeams, setNewTeams] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [invite, setInvite] = useState<LocalUserInvite | null>(null);

  const reload = () => {
    listLocalUsers()
      .then(setUsers)
      .catch(() => toast.error("Failed to load local users"))
      .finally(() => setLoading(false));
  };

  useEffect(() => {
    reload();
    fetchTeams()
      .then((r) => setTeams(r.teams))
      .catch(() => {
        /* team picker degrades to none */
      });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const handleCreate = async () => {
    setBusy(true);
    try {
      const inv = await createLocalUser(newUsername.trim(), newTeams);
      setInvite(inv);
      setShowAdd(false);
      setNewUsername("");
      setNewTeams([]);
      reload();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to create user");
    } finally {
      setBusy(false);
    }
  };

  const handleReinvite = async (username: string) => {
    try {
      setInvite(await reinviteLocalUser(username));
      reload();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to issue invite");
    }
  };

  const handleDelete = async (username: string) => {
    if (!window.confirm(`Delete user ${username}? They will no longer be able to sign in.`)) return;
    try {
      await deleteLocalUser(username);
      reload();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to delete user");
    }
  };

  return (
    <div className="max-w-xl rounded-lg border border-gray-200 bg-white">
      <div className="flex items-center justify-between border-b border-gray-100 px-6 py-4">
        <div>
          <h2 className="text-sm font-medium text-gray-900">Local users</h2>
          <p className="mt-0.5 text-xs text-gray-500">
            Password accounts provisioned via one-time invite links — for teams
            without an identity provider. Re-inviting resets the password.
          </p>
        </div>
        <button
          onClick={() => setShowAdd(true)}
          className="rounded-md bg-gray-900 px-3 py-1.5 text-sm font-medium text-white hover:bg-gray-700"
        >
          Add user
        </button>
      </div>

      <div className="px-6 py-4">
        {loading ? (
          <div className="h-16 animate-pulse rounded bg-gray-100" />
        ) : users.length === 0 ? (
          <p className="py-4 text-center text-sm text-gray-400">
            No local users yet. SSO users sign in via your IdP; add a local
            user to invite someone outside it.
          </p>
        ) : (
          <ul className="divide-y divide-gray-100">
            {users.map((u) => {
              const chip = statusChip[u.status] ?? { label: u.status, cls: "bg-gray-100 text-gray-600" };
              return (
                <li key={u.username} className="flex items-center justify-between py-2.5">
                  <div className="flex items-center gap-2">
                    <span className="text-sm font-medium text-gray-900">{u.username}</span>
                    <span className={`rounded-full px-2 py-0.5 text-[11px] font-medium ${chip.cls}`}>
                      {chip.label}
                    </span>
                  </div>
                  <div className="flex items-center gap-3 text-xs">
                    <button
                      onClick={() => void handleReinvite(u.username)}
                      title="Generate a fresh one-time link (also resets the password)"
                      className="font-medium text-gray-500 hover:text-gray-900"
                    >
                      Re-invite
                    </button>
                    <button
                      onClick={() => void handleDelete(u.username)}
                      className="font-medium text-red-400 hover:text-red-600"
                    >
                      Delete
                    </button>
                  </div>
                </li>
              );
            })}
          </ul>
        )}
      </div>

      {showAdd && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/30 p-4">
          <div className="w-full max-w-md rounded-lg bg-white p-6 shadow-xl">
            <h3 className="text-lg font-semibold text-gray-900">Add local user</h3>
            <div className="mt-4 space-y-4">
              <Field label="Username" help="Used to sign in and matched against team membership.">
                <input
                  className={inputClass}
                  value={newUsername}
                  onChange={(e) => setNewUsername(e.target.value)}
                  placeholder="jane@example.com"
                />
              </Field>
              {teams.length > 0 && (
                <Field label="Teams (optional)" help="Gives the user a role on first sign-in.">
                  <div className="space-y-1.5">
                    {teams.map((t) => (
                      <label key={t.name} className="flex items-center gap-2 text-sm text-gray-700">
                        <input
                          type="checkbox"
                          className="h-4 w-4 rounded border-gray-300"
                          checked={newTeams.includes(t.name)}
                          onChange={(e) =>
                            setNewTeams((prev) =>
                              e.target.checked ? [...prev, t.name] : prev.filter((n) => n !== t.name),
                            )
                          }
                        />
                        {t.displayName || t.name}
                      </label>
                    ))}
                  </div>
                </Field>
              )}
            </div>
            <div className="mt-5 flex justify-end gap-2">
              <button
                onClick={() => setShowAdd(false)}
                className="rounded-md border border-gray-300 px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50"
              >
                Cancel
              </button>
              <button
                onClick={() => void handleCreate()}
                disabled={busy || !newUsername.trim()}
                className="rounded-md bg-gray-900 px-4 py-2 text-sm font-medium text-white hover:bg-gray-700 disabled:opacity-50"
              >
                {busy ? "Creating..." : "Create & get invite link"}
              </button>
            </div>
          </div>
        </div>
      )}

      {invite && <InviteLinkModal invite={invite} onClose={() => setInvite(null)} />}
    </div>
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

      <LocalUsersSection />

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
              help="suparship's callback URL, registered with your IdP"
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
