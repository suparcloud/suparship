# suparShip

**Vercel-like developer experience for Kubernetes teams.**  
Open source. GitOps-native. Built on CNCF projects.

---

## What is suparShip?

**suparShip** is an open-source platform runtime from **suparCloud** that lets small SRE / DevOps teams provide a **simple, self-service PaaS-like experience** to developers — without building or maintaining a full platform team.

It standardizes the *golden paths* for:
- deploying applications
- creating preview environments
- promoting safely to production

…while using **proven open-source tools** like ArgoCD and Kargo under the hood.

> If you can run Kubernetes, you can run a great platform.

---

## Why suparShip?

Most teams want:
- preview environments for every change
- consistent deploy workflows
- safe promotions to production

But they:
- don’t have time to build an internal platform
- don’t want vendor lock-in
- eventually outgrow hosted PaaS solutions

**suparShip sits in the middle**:
- simple like a PaaS
- flexible like Kubernetes
- transparent like GitOps

---

## Key concepts

suparShip is built around a small set of primitives:

- **Environment** – `staging`, `prod`, `preview`
- **Project** – logical grouping (team / product)
- **Service** – a deployable workload
- **Template** – a golden path for deploying a service

Developers think in:
> *service → environment → preview → promote*

SREs define:
> *defaults, templates, and guardrails*

---

## Quickstart (no cloud, no paid services)

### Prerequisites

- `kubectl`
- `k3d`
- (optional) `docker` – required only for local build mode

### Install suparShip

```bash
curl -fsSL https://suparcloud.io/install/suparship.sh | sh
