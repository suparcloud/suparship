import { useEffect, useState } from "react";
import { toast } from "sonner";

import {
  fetchTeams,
  fetchProjects,
  listRoleBindings,
  createTeam,
  updateTeam,
  deleteTeam,
  createRoleBinding,
  deleteRoleBinding,
} from "../lib/settings";
import type { TeamInfo, RoleBinding, Project } from "../types";

const inputClass =
  "block w-full rounded-md border border-gray-300 px-3 py-2 text-sm shadow-sm focus:border-blue-500 focus:ring-1 focus:ring-blue-500";

const ROLES = ["org_admin", "project_admin", "developer", "viewer"] as const;

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

function Modal({
  title,
  onClose,
  children,
}: {
  title: string;
  onClose: () => void;
  children: React.ReactNode;
}) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
      <div className="w-full max-w-md rounded-lg bg-white p-6 shadow-xl">
        <div className="mb-4 flex items-center justify-between">
          <h2 className="text-lg font-semibold text-gray-900">{title}</h2>
          <button
            onClick={onClose}
            className="text-gray-400 hover:text-gray-600"
            aria-label="Close"
          >
            ✕
          </button>
        </div>
        {children}
      </div>
    </div>
  );
}

export function TeamSettings() {
  const [teams, setTeams] = useState<TeamInfo[]>([]);
  const [bindings, setBindings] = useState<RoleBinding[]>([]);
  const [projects, setProjects] = useState<Project[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // Team modal state. editingTeam===null + open => create; set => edit.
  const [teamModalOpen, setTeamModalOpen] = useState(false);
  const [editingTeam, setEditingTeam] = useState<TeamInfo | null>(null);
  const [teamForm, setTeamForm] = useState({ name: "", displayName: "", members: "" });
  const [savingTeam, setSavingTeam] = useState(false);

  // Binding modal state.
  const [bindModalOpen, setBindModalOpen] = useState(false);
  const [bindForm, setBindForm] = useState<{
    project: string;
    subjectType: "team" | "group";
    team: string;
    group: string;
    role: string;
  }>({ project: "*", subjectType: "team", team: "", group: "", role: "developer" });
  const [savingBind, setSavingBind] = useState(false);

  async function reload() {
    const [t, b, p] = await Promise.all([
      fetchTeams(),
      listRoleBindings(),
      fetchProjects().catch(() => ({ projects: [] as Project[] })),
    ]);
    setTeams(t.teams);
    setBindings(b.roleBindings);
    setProjects(p.projects);
  }

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const [t, b, p] = await Promise.all([
          fetchTeams(),
          listRoleBindings(),
          fetchProjects().catch(() => ({ projects: [] as Project[] })),
        ]);
        if (cancelled) return;
        setTeams(t.teams);
        setBindings(b.roleBindings);
        setProjects(p.projects);
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : "Failed to load");
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  // --- Team actions ---

  const openCreateTeam = () => {
    setEditingTeam(null);
    setTeamForm({ name: "", displayName: "", members: "" });
    setTeamModalOpen(true);
  };

  const openEditTeam = (team: TeamInfo) => {
    setEditingTeam(team);
    setTeamForm({
      name: team.name,
      displayName: team.displayName ?? "",
      members: team.members.join(", "),
    });
    setTeamModalOpen(true);
  };

  const saveTeam = async () => {
    const members = teamForm.members
      .split(",")
      .map((m) => m.trim())
      .filter(Boolean);
    if (!editingTeam && !teamForm.name.trim()) {
      toast.error("Team name is required");
      return;
    }
    setSavingTeam(true);
    try {
      if (editingTeam) {
        await updateTeam(editingTeam.name, { displayName: teamForm.displayName, members });
        toast.success(`Team "${editingTeam.name}" updated`);
      } else {
        await createTeam({ name: teamForm.name.trim(), displayName: teamForm.displayName, members });
        toast.success(`Team "${teamForm.name.trim()}" created`);
      }
      setTeamModalOpen(false);
      await reload();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Save failed");
    } finally {
      setSavingTeam(false);
    }
  };

  const removeTeam = async (team: TeamInfo) => {
    if (!confirm(`Delete team "${team.name}"? This cannot be undone.`)) return;
    try {
      await deleteTeam(team.name);
      toast.success(`Team "${team.name}" deleted`);
      await reload();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Delete failed");
    }
  };

  // --- Role binding actions ---

  const saveBinding = async () => {
    const rb: RoleBinding = { project: bindForm.project.trim() || "*", role: bindForm.role };
    if (bindForm.subjectType === "team") {
      if (!bindForm.team) {
        toast.error("Select a team");
        return;
      }
      rb.team = bindForm.team;
    } else {
      if (!bindForm.group.trim()) {
        toast.error("Enter a group name");
        return;
      }
      rb.group = bindForm.group.trim();
    }
    setSavingBind(true);
    try {
      await createRoleBinding(rb);
      toast.success("Role binding added");
      setBindModalOpen(false);
      setBindForm({ project: "*", subjectType: "team", team: "", group: "", role: "developer" });
      await reload();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Add failed");
    } finally {
      setSavingBind(false);
    }
  };

  const removeBinding = async (rb: RoleBinding) => {
    const subject = rb.team ? `team ${rb.team}` : `group ${rb.group}`;
    if (!confirm(`Remove ${rb.role} for ${subject} on ${rb.project}?`)) return;
    try {
      await deleteRoleBinding(rb);
      toast.success("Role binding removed");
      await reload();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Remove failed");
    }
  };

  if (loading) {
    return (
      <div className="space-y-4">
        <div className="h-8 w-48 animate-pulse rounded bg-gray-100" />
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {[1, 2, 3].map((n) => (
            <div key={n} className="h-40 animate-pulse rounded-lg bg-gray-50" />
          ))}
        </div>
      </div>
    );
  }

  if (error) {
    return (
      <div className="rounded-lg border border-red-200 bg-red-50 p-4">
        <p className="text-sm text-red-700">Failed to load: {error}</p>
      </div>
    );
  }

  return (
    <div className="space-y-10">
      {/* Teams */}
      <section className="space-y-4">
        <div className="flex items-center justify-between">
          <div>
            <h1 className="text-2xl font-semibold text-gray-900">Teams</h1>
            <p className="mt-1 text-sm text-gray-500">
              Named groups of users. Grant access by binding a team to a role below.
            </p>
          </div>
          <button
            onClick={openCreateTeam}
            className="rounded-md bg-blue-600 px-4 py-2 text-sm font-medium text-white shadow-sm hover:bg-blue-700"
          >
            New team
          </button>
        </div>

        {teams.length === 0 ? (
          <div className="rounded-lg border border-gray-200 bg-white px-6 py-12 text-center">
            <p className="text-sm text-gray-400">No teams configured yet.</p>
          </div>
        ) : (
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {teams.map((team) => (
              <div key={team.name} className="rounded-lg border border-gray-200 bg-white">
                <div className="flex items-start justify-between border-b border-gray-100 px-5 py-4">
                  <div>
                    <h3 className="text-sm font-semibold text-gray-900">
                      {team.displayName || team.name}
                    </h3>
                    {team.displayName && team.displayName !== team.name && (
                      <p className="mt-0.5 text-xs text-gray-400">{team.name}</p>
                    )}
                  </div>
                  <div className="flex gap-2 text-xs">
                    <button
                      onClick={() => openEditTeam(team)}
                      className="text-blue-600 hover:text-blue-800"
                    >
                      Edit
                    </button>
                    <button
                      onClick={() => removeTeam(team)}
                      className="text-red-500 hover:text-red-700"
                    >
                      Delete
                    </button>
                  </div>
                </div>
                <div className="px-5 py-4">
                  <p className="mb-2 text-xs font-medium uppercase tracking-wider text-gray-500">
                    Members ({team.members.length})
                  </p>
                  {team.members.length === 0 ? (
                    <p className="text-sm italic text-gray-400">No members</p>
                  ) : (
                    <div className="flex flex-wrap gap-1.5">
                      {team.members.map((m) => (
                        <span
                          key={m}
                          className="inline-block rounded-full bg-gray-100 px-2.5 py-0.5 text-xs font-medium text-gray-700"
                        >
                          {m}
                        </span>
                      ))}
                    </div>
                  )}
                </div>
              </div>
            ))}
          </div>
        )}
      </section>

      {/* Role bindings */}
      <section className="space-y-4">
        <div className="flex items-center justify-between">
          <div>
            <h2 className="text-xl font-semibold text-gray-900">Role Bindings</h2>
            <p className="mt-1 text-sm text-gray-500">
              Grant a role on a project to a team (local users) or an IdP group (SSO).
              Project <code className="rounded bg-gray-100 px-1">*</code> means all projects.
            </p>
          </div>
          <button
            onClick={() => setBindModalOpen(true)}
            className="rounded-md bg-blue-600 px-4 py-2 text-sm font-medium text-white shadow-sm hover:bg-blue-700"
          >
            Add binding
          </button>
        </div>

        {bindings.length === 0 ? (
          <div className="rounded-lg border border-gray-200 bg-white px-6 py-12 text-center">
            <p className="text-sm text-gray-400">No role bindings yet.</p>
          </div>
        ) : (
          <div className="overflow-hidden rounded-lg border border-gray-200 bg-white">
            <table className="min-w-full divide-y divide-gray-100 text-sm">
              <thead className="bg-gray-50 text-left text-xs font-medium uppercase tracking-wider text-gray-500">
                <tr>
                  <th className="px-5 py-2.5">Project</th>
                  <th className="px-5 py-2.5">Subject</th>
                  <th className="px-5 py-2.5">Role</th>
                  <th className="px-5 py-2.5" />
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100">
                {bindings.map((rb, i) => (
                  <tr
                    key={`${rb.project}|${rb.team ?? rb.group}|${rb.role}|${i}`}
                    className="hover:bg-gray-50"
                  >
                    <td className="px-5 py-3 font-mono text-xs text-gray-700">{rb.project}</td>
                    <td className="px-5 py-3">
                      <span className="text-gray-900">{rb.team ?? rb.group}</span>
                      <span className="ml-2 rounded bg-gray-100 px-1.5 py-0.5 text-xs text-gray-500">
                        {rb.team ? "team" : "group"}
                      </span>
                    </td>
                    <td className="px-5 py-3 text-gray-700">{rb.role}</td>
                    <td className="px-5 py-3 text-right">
                      <button
                        onClick={() => removeBinding(rb)}
                        className="text-xs text-red-500 hover:text-red-700"
                      >
                        Remove
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      {/* Team modal */}
      {teamModalOpen && (
        <Modal
          title={editingTeam ? `Edit team "${editingTeam.name}"` : "New team"}
          onClose={() => setTeamModalOpen(false)}
        >
          <div className="space-y-4">
            {!editingTeam && (
              <Field label="Name" help="Unique identifier; cannot be changed later">
                <input
                  className={inputClass}
                  value={teamForm.name}
                  onChange={(e) => setTeamForm((f) => ({ ...f, name: e.target.value }))}
                  placeholder="platform"
                />
              </Field>
            )}
            <Field label="Display Name">
              <input
                className={inputClass}
                value={teamForm.displayName}
                onChange={(e) => setTeamForm((f) => ({ ...f, displayName: e.target.value }))}
                placeholder="Platform"
              />
            </Field>
            <Field label="Members" help="Comma-separated usernames">
              <input
                className={inputClass}
                value={teamForm.members}
                onChange={(e) => setTeamForm((f) => ({ ...f, members: e.target.value }))}
                placeholder="alice, bob"
              />
            </Field>
            <div className="flex justify-end gap-2 pt-2">
              <button
                onClick={() => setTeamModalOpen(false)}
                className="rounded-md border border-gray-300 px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50"
              >
                Cancel
              </button>
              <button
                onClick={saveTeam}
                disabled={savingTeam}
                className="rounded-md bg-blue-600 px-4 py-2 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50"
              >
                {savingTeam ? "Saving..." : "Save"}
              </button>
            </div>
          </div>
        </Modal>
      )}

      {/* Binding modal */}
      {bindModalOpen && (
        <Modal title="Add role binding" onClose={() => setBindModalOpen(false)}>
          <div className="space-y-4">
            <Field label="Project" help='Use "*" for all projects'>
              <input
                className={inputClass}
                list="project-options"
                value={bindForm.project}
                onChange={(e) => setBindForm((f) => ({ ...f, project: e.target.value }))}
                placeholder="*"
              />
              <datalist id="project-options">
                <option value="*" />
                {projects.map((p) => (
                  <option key={p.name} value={p.name} />
                ))}
              </datalist>
            </Field>

            <Field label="Subject">
              <div className="flex gap-4 text-sm">
                <label className="flex items-center gap-1.5">
                  <input
                    type="radio"
                    checked={bindForm.subjectType === "team"}
                    onChange={() => setBindForm((f) => ({ ...f, subjectType: "team" }))}
                  />
                  Team
                </label>
                <label className="flex items-center gap-1.5">
                  <input
                    type="radio"
                    checked={bindForm.subjectType === "group"}
                    onChange={() => setBindForm((f) => ({ ...f, subjectType: "group" }))}
                  />
                  IdP group (SSO)
                </label>
              </div>
            </Field>

            {bindForm.subjectType === "team" ? (
              <Field label="Team">
                <select
                  className={inputClass}
                  value={bindForm.team}
                  onChange={(e) => setBindForm((f) => ({ ...f, team: e.target.value }))}
                >
                  <option value="">Select a team…</option>
                  {teams.map((t) => (
                    <option key={t.name} value={t.name}>
                      {t.displayName || t.name}
                    </option>
                  ))}
                </select>
              </Field>
            ) : (
              <Field label="Group" help="Value of the IdP group claim">
                <input
                  className={inputClass}
                  value={bindForm.group}
                  onChange={(e) => setBindForm((f) => ({ ...f, group: e.target.value }))}
                  placeholder="platform-engineers"
                />
              </Field>
            )}

            <Field label="Role">
              <select
                className={inputClass}
                value={bindForm.role}
                onChange={(e) => setBindForm((f) => ({ ...f, role: e.target.value }))}
              >
                {ROLES.map((r) => (
                  <option key={r} value={r}>
                    {r}
                  </option>
                ))}
              </select>
            </Field>

            <div className="flex justify-end gap-2 pt-2">
              <button
                onClick={() => setBindModalOpen(false)}
                className="rounded-md border border-gray-300 px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50"
              >
                Cancel
              </button>
              <button
                onClick={saveBinding}
                disabled={savingBind}
                className="rounded-md bg-blue-600 px-4 py-2 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50"
              >
                {savingBind ? "Adding..." : "Add"}
              </button>
            </div>
          </div>
        </Modal>
      )}
    </div>
  );
}
