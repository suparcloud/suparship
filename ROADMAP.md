# Suparship roadmap to 1.0

Planning baseline: 2026-10-18. These are target milestones, not shipped claims.
The recorded release is 0.1.0. Calendar dates and individual owners remain pending
capacity planning; versions are not equal-sized monthly increments.

## Product promise

A PaaS-like developer experience for complete applications on Kubernetes
infrastructure customers control, with open-source standards, reviewable
production changes, and tested recovery.

Initial audience: teams already using Kubernetes with a small platform/SRE team.
One organization per installation; project permissions do not imply isolation
between mutually untrusted tenants.

| Release | Outcome | Exit gate |
| --- | --- | --- |
| 0.2 | Fast UI reads and Git operations, durable task queue/workers, reliable verified releases | Read/Git latency, crash-recovery, current workflow, and upgrade gates pass |
| 0.3 | Durable PR workflows, approvals, rendered impact, and Argo CD diff | Protected changes cannot apply before approved merge; workflows recover after interruption |
| 0.4 | Repository-to-app deployment and automatic previews | External developer completes GitHub-to-preview workflow |
| 0.5 | Managed addons and storage bindings | Initial catalog can be provisioned, attached, protected, and recovered |
| 0.6 | Resource monitoring, custom metrics, logs, and traces | Diagnose seeded failures from the app view |
| 0.7 | Manual HTTP canaries | Advance and abort safely under approval policy |
| 0.8 | Proven multi-cluster and multi-cloud operation | Two-cloud deployment and traffic-failover rehearsal passes |
| 0.9 | Feature freeze and production qualification | Security, upgrade, restore, compatibility, and usability gates pass |
| 1.0 | Stable public release | Release-candidate and production-observation gates pass |

The [detailed release plan](docs/release-plan-1.0.md) defines required capabilities,
initial provider choices, dependencies, exclusions, and acceptance criteria.

## Scope rules

- All pillars are required for 1.0; broad provider parity is not.
- Every release includes documentation, migration notes, validation, and stable/beta labels.
- Security, recovery, accessibility, and operational reliability apply throughout.
- Ordinary UI reads use background-maintained projections; slow infrastructure
  mutations run as durable queued operations with recoverable backend workers.
- Git operations reuse scoped repository caches with incremental fetches and
  isolated task workspaces; routine work does not repeatedly clone full repositories.
- Git/Kubernetes retain their defined configuration authority; derived read data
  is rebuildable, while authoritative task/workflow records require backup.
- External pilots begin at 0.3/0.4, not after the entire roadmap is built.
- New release blockers require an explicit scope or schedule tradeoff.
- Patch releases address defects; milestone completion requires passing evidence.

## Baseline and history

Existing foundations include app/component modeling, chart deployment, GitOps,
previews, secrets/configuration, RBAC, and multi-cluster targeting. The changelog
records verified-release and optional validation work as unreleased. Implementation
alone is not proof of production readiness.

[ROADMAP-pre-1.0.md](ROADMAP-pre-1.0.md) preserves historical design context only.
Its pending-feature lists, estimates, phase numbering, and release-train sketch
are superseded. Hierarchical variables and resolved configuration, for example,
are already described in the README and changelog.
