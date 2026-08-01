# suparship — Pre-Launch Blog Series Plan

_Prepared: July 2026 · Goal: build hype, credibility, and a warm audience ahead of launch · Primary audience: SRE / platform engineers_

---

## 1. Strategy at a glance

**Objective:** By launch day, have an SRE/platform-engineer audience that (a) trusts the project's technical judgment, (b) understands the category gap suparship fills, and (c) is on the waitlist / watching the repo.

**Core narrative arc** (derived from the competitive analysis):

> The mid-market platform team is stuck between building an internal platform they can't staff and renting a PaaS they can't control. suparship is the missing middle — a productized, GitOps-native golden-path layer on the CNCF tools you already trust.

**Content principles for this audience:**

- **Earn trust with substance, not hype.** SREs smell marketing instantly. Lead with architecture, trade-offs, and honest limitations. Show real YAML, real commits, real failure modes.
- **Teach the category, sell nothing (yet).** Most posts should be genuinely useful even to someone who never adopts suparship.
- **Use competitors as teaching devices**, never as cheap shots. "Why we build on Kargo" beats "why X sucks."
- **Every post ends in one soft CTA** — waitlist, GitHub star, or "watch the build in public" — never a hard sell.

**Cadence:** One post per week for ~8–10 weeks pre-launch, escalating intensity in the final three weeks. Mix long-form technical (owned blog) with short amplification (dev.to, Hashnode, LinkedIn, r/kubernetes, r/devops, Platform Engineering Slack/Discord, Hacker News for the 1–2 strongest).

---

## 2. The three phases

| Phase | Weeks | Theme | Job to be done |
|---|---|---|---|
| **1 — Problem & credibility** | 1–3 | Name the pain, prove we understand the domain | Attract the audience; establish technical authority before we ever pitch |
| **2 — Approach & architecture** | 4–7 | Show how we solve it, transparently | Convert "interesting" into "these people know what they're doing" |
| **3 — Reveal & momentum** | 8–10 | Introduce suparship, drive waitlist/stars | Turn trust into signups and launch-day amplifiers |

---

## 3. The posts

### Phase 1 — Problem & credibility (weeks 1–3)

**Post 1 — "The missing middle: why mid-market platform teams are stuck between Backstage and Vercel"**
- **Angle:** The category-defining thesis. Two roads both dead-end: build an internal platform a 2–3 person team can't sustain, or rent a hosted PaaS you outgrow and can't control. Name the gap.
- **Why it works:** Frames the entire series; deeply relatable to the ICP; zero product pitch = high shareability.
- **Proof points:** Gartner 80%-by-2026 stat, mid-market team-size reality (2–3 vs 10–20 engineers), Vercel-cost-at-scale anecdote.
- **CTA:** "We're building something for this gap — follow along."
- **Distribution:** Owned blog + dev.to + r/platformengineering + LinkedIn. HN candidate.

**Post 2 — "We priced out 'just use a PaaS' at scale. Here's where it breaks."**
- **Angle:** Honest, numbers-driven teardown of hosted-PaaS economics and lock-in (per-seat + bandwidth surprises, integration rewrites to leave). Not an attack — a lifecycle story: PaaS is right early, wrong later.
- **Why it works:** Speaks directly to the warmest audience — PaaS refugees. Concrete numbers travel.
- **Proof points:** Documented Vercel $20→$286 case; Render's 2026 removal of per-seat; lock-in via Edge Config/KV/private services.
- **CTA:** Waitlist ("building the self-hosted answer").

**Post 3 — "You don't need a platform team to have a platform (but you do need golden paths)"**
- **Angle:** Reframe platform engineering for small teams. The value isn't a big team — it's paved roads: standardized deploy, preview, promote. Introduce the `app → environment → preview → promote` mental model without naming the product yet.
- **Why it works:** Establishes the conceptual vocabulary the product later delivers; genuinely useful essay.
- **CTA:** Repo watch / newsletter.

### Phase 2 — Approach & architecture (weeks 4–7)

**Post 4 — "Why we built on ArgoCD and Kargo instead of reinventing CD"**
- **Angle:** Credibility centerpiece for SREs. Explain the CNCF-native bet: ArgoCD (97% prod adoption) for reconciliation, Kargo for promotion. Show the actual generated Warehouse/Stage/Application manifests. Argue productizing > rebuilding.
- **Why it works:** This is the trust-earner. SREs respect teams who stand on proven primitives and show their work.
- **CTA:** GitHub star.
- **Distribution:** HN + r/kubernetes + Argo/Kargo community channels + dev.to.

**Post 5 — "Secrets in a GitOps world: five scopes, an external vault, and never a value in Git"**
- **Angle:** Deep-dive on the five-level secret hierarchy (org → env-type → project → app → app-env), External Secrets Operator, `VaultWriter`, and `hard`/`soft` isolation for SOC2. Show why "secrets in sealed Git" is the wrong default.
- **Why it works:** Secrets are a universal pain and a real differentiator; depth here separates us from every Docker-based OSS PaaS.
- **CTA:** Waitlist.

**Post 6 — "Preview environments that actually promote to prod: previews, release trains, and weighted canaries on Gateway API"**
- **Angle:** Progressive delivery for the rest of us. Ephemeral previews per change; stable+canary release trains in one namespace; weighted `HTTPRoute` traffic splitting; auto vs. manual promotion gates. Real config walkthrough.
- **Why it works:** These are "enterprise PaaS" capabilities shown as approachable and open-source.
- **CTA:** Repo watch.

**Post 7 — "No lock-in, by design: managing your apps with plain GitOps after us"**
- **Angle:** The anti-lock-in proof. Resources tracked by `app.kubernetes.io/managed-by` label, not name prefixes; deterministic commits; you can walk away and keep running. Contrast honestly with managed control planes (Qovery/Northflank) and PaaS integration lock-in.
- **Why it works:** Lock-in is the shared villain; "you can leave" is a counterintuitive, trust-building flex.
- **CTA:** Waitlist. HN candidate.

### Phase 3 — Reveal & momentum (weeks 8–10)

**Post 8 — "Introducing suparship: a Vercel-like experience for teams who live on Kubernetes"**
- **Angle:** The reveal. Tie the whole series together — name the product, show the app→env→preview→promote flow end to end, position squarely in the top-right quadrant (open + complete). Include the "if you can run Kubernetes, you can run a great platform" line.
- **Why it works:** Payoff for the audience built over 7 weeks; everything now has a name.
- **CTA:** Waitlist + star + "try it locally in ~1 min" (fake/local mode).
- **Distribution:** Everywhere, coordinated. HN launch-style post.

**Post 9 — "From `git push` to a running voice-AI agent: a real self-service workload"**
- **Angle:** Concrete, impressive demo. Use the VoiceAI/LiveKit worker templates to show a non-trivial AI workload deployed from a form, no raw YAML — riding the AI-workloads-need-self-service trend.
- **Why it works:** Proof the abstraction handles real, modern workloads; differentiates from toy "deploy a hello-world" demos.
- **CTA:** Try local mode / join waitlist.

**Post 10 — "Building suparship in the open: our roadmap, and what we're asking for"**
- **Angle:** Community-building close. Share the roadmap (Kargo pipeline, addons UI, stacks), invite contributors, be honest about what's beta and what's next. Convert watchers into participants.
- **Why it works:** Turns audience into a community and launch-day amplifiers; open-honesty compounds trust.
- **CTA:** Contribute / star / waitlist.

---

## 4. Cross-cutting distribution & mechanics

- **Owned blog is canonical**; syndicate to dev.to and Hashnode with canonical links.
- **Community seeding:** r/kubernetes, r/devops, r/platformengineering, Platform Engineering Slack, CNCF/Argo Slack — share as genuine discussion, not drops.
- **Hacker News:** aim 1–2 strongest posts (Posts 4, 7, or 8) at HN; time for US morning; have the team ready to answer in-thread.
- **LinkedIn/X:** every post gets a thread distilling the one key insight.
- **Waitlist infrastructure:** stand up before Post 1; capture email + "current stack" + "biggest platform pain" for later segmentation.
- **Repo readiness:** a clean README, the ~1-min local/fake mode, and a "watch/star" ask must be live before Post 4 (the first big technical traffic spike).

## 5. Measurement

- **Leading:** post reads, time-on-page, HN/Reddit rank & comments, repo stars/watchers week-over-week, waitlist signups per post.
- **Qualitative:** are SREs engaging on the *architecture* posts (Posts 4–7)? That's the trust signal that predicts launch conversion.
- **Launch target:** define a concrete waitlist + GitHub-star number for launch day; Posts 1–3 calibrate the achievable range.

## 6. Suggested editorial calendar

| Week | Post | Primary channel focus |
|---|---|---|
| 1 | 1 — The missing middle | Blog, dev.to, LinkedIn, r/platformengineering |
| 2 | 2 — PaaS at scale breaks | Blog, dev.to, PaaS-refugee communities |
| 3 | 3 — Platform without a platform team | Blog, LinkedIn |
| 4 | 4 — Why ArgoCD + Kargo | **HN**, r/kubernetes, Argo Slack |
| 5 | 5 — Secrets in GitOps | Blog, r/devops |
| 6 | 6 — Previews → prod / canaries | Blog, r/kubernetes |
| 7 | 7 — No lock-in by design | **HN candidate**, LinkedIn |
| 8 | 8 — Introducing suparship | **Everywhere + HN launch** |
| 9 | 9 — Voice-AI self-service demo | Blog, AI/ML + K8s communities |
| 10 | 10 — Building in the open | Blog, CNCF/community channels |

---

_Companion doc: `competitive-analysis.md`._
