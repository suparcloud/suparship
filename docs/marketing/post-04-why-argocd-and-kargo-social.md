# Post 4 — "Why We Built on Argo CD and Kargo Instead of Reinventing CD" · Social copy

_Blog post: https://rtnpro.com/post/2026-07-31-why-argocd-and-kargo/ · Published 2026-07-31 · Phase 2, week 4_

**Positioning constraint:** suparship is STILL NOT named. Post 4 opens Phase 2 (approach & architecture); the reveal remains Post 8. The blog post anonymizes its own labels to `platform.example/` — the social copy matches: no product name, no repo link, no GitHub-star CTA. (The original series plan pencilled a star CTA here; the "stay unnamed, anonymize" decision overrides it. When the product is named, the CTA swaps to a star ask.)

**CTA choice:** invite argument, not signups — same as Posts 1 and 3. The ask is the blog's closing question: *if you've wired Argo CD and Kargo by hand, where did schema drift bite you?* Only someone who has actually done it can answer, which filters for the ICP and seeds a thread of war stories that is itself credibility.

---

## LinkedIn

Paste verbatim. Plain text on purpose — see "Formatting" below.

> The fastest way to kill an internal platform: decide to reinvent continuous delivery.
>
> When you build a platform layer, a voice says own the whole pipeline. Your own reconciler. Your own promotion engine. Your own state, where you control it.
>
> It's a trap — and it's the one that's killed more internal platforms than any budget.
>
> Reconciliation isn't a weekend problem. It's drift detection, sync ordering, health checks across dozens of resource kinds, pruning, retries, and a decade of edge cases that only surface in someone else's cluster at 3am.
>
> Promotion isn't a weekend problem either. It's provenance, immutability, and the guarantee that what you tested is byte-for-byte what you shipped.
>
> Rebuild those and you've signed up to re-hit every bug the ecosystem already fixed. Forever.
>
> And here's the part that should settle it: a better reconciler is not your competitive edge. Nobody adopts your platform because its home-grown CD engine is marginally different from Argo CD's.
>
> So don't reinvent continuous delivery. Productize the primitives that already won.
>
> Argo CD reconciles — 97% of teams run it in production (CNCF's 2025 survey), with an NPS of 79.
>
> Kargo promotes — built by the Argo CD creators, for exactly the gap Argo CD leaves open: deciding when one environment's state should become another's.
>
> The whole job of the layer is to generate the right Warehouse, Stage, and Application from one app definition, then get out of the way.
>
> The payoff is the exit. Everything is a plain, labeled CRD reconciled from a Git repo you own. A promotion is just a Git commit. If the layer vanished tomorrow, Argo CD keeps reconciling the same repo without it.
>
> Leaving is a Tuesday afternoon.
>
> You can't be trapped in a control plane that was never built.
>
> The honest cost: you inherit their schema drift. Kargo's promotion API changed shape between v0.9 and v1 — standing on primitives means their breaking changes become your regression suite. Cheaper than reinventing CD. Not free.
>
> I wrote up the whole thing, with the actual generated manifests — not a block diagram.
>
> Link in the comments.
>
> And a real question: if you've wired Argo CD and Kargo together by hand, where did the schema drift bite you? That scar list is the best documentation this ecosystem doesn't have.
>
> #PlatformEngineering #Kubernetes #GitOps #ArgoCD #SRE

### Formatting

Same rules as every post in this series — they matter every time:

- **No Unicode bold/italics** (𝗕𝗼𝗹𝗱). Screen readers announce them character by character, LinkedIn search doesn't normalize them to ASCII, and they read as growth-hacked to an audience that smells marketing instantly.
- **No markdown** — `**bold**` renders as literal asterisks in the composer.
- The only emphasis lever is the line break. Short paragraphs, blank line between each, load-bearing sentence standing alone ("Leaving is a Tuesday afternoon." / "You can't be trapped in a control plane that was never built.").

### Mechanics

- **Link in the first comment**, not the body — LinkedIn suppresses reach on posts with outbound links. Post the comment immediately after publishing.
- **Push before you post.** Post 4 is still a local-only commit; the blog URL 404s until `master` is pushed. "Link in comments" needs a live link.
- The fold sits ~140 chars in. Line 1 ("The fastest way to kill an internal platform: decide to reinvent continuous delivery.") is the whole hook — an arguable claim above the fold. The stats (97%, NPS) sit well below it; a number is a weaker cold open than a provocation.
- ~2,350 chars against the 3,000 limit.

---

## X — single post

> When you build a platform, a voice says: own the whole CD pipeline. Your own reconciler, your own promotion engine.
>
> It's a trap. A better reconciler isn't your edge.
>
> Productize Argo CD + Kargo instead of reinventing CD:
>
> https://rtnpro.com/post/2026-07-31-why-argocd-and-kargo/

---

## X — thread

The argument has clean beats (the trap / why / the bet / the exit / the cost), which a thread carries better than one post.

**1/**
> The fastest way to kill an internal platform:
>
> decide to reinvent continuous delivery.
>
> Your own reconciler. Your own promotion engine. Your own state.
>
> It's the trap that's killed more platforms than any budget.

**2/**
> Reconciliation isn't a weekend problem. Drift detection, sync ordering, health checks, pruning, retries — a decade of edge cases that only surface in someone else's cluster at 3am.
>
> Rebuild it and you re-hit every bug the ecosystem already fixed.

**3/**
> The part that should settle it:
>
> a better reconciler is not your competitive edge.
>
> Nobody adopts your platform because its home-grown CD engine differs marginally from Argo CD's.
>
> Don't reinvent CD. Productize the primitives that already won.

**4/**
> Two jobs, two tools.
>
> Argo CD reconciles — 97% of teams run it in production (CNCF 2025 survey).
>
> Kargo promotes — built by the Argo CD creators, for the gap Argo CD leaves open: when one env's state should become another's.

**5/**
> The payoff is the exit.
>
> Everything is a plain labeled CRD, reconciled from a Git repo you own. A promotion is just a Git commit.
>
> If the layer vanished tomorrow, Argo CD keeps reconciling the same repo.
>
> You can't be trapped in a control plane that was never built.

**6/**
> Honest cost: you inherit their schema drift.
>
> Kargo's promotion API changed shape between v0.9 and v1. Standing on primitives means their breaking changes are yours.
>
> Cheaper than reinventing CD. Not free.

**7/**
> Full writeup — with the actual generated manifests, not a block diagram:
>
> https://rtnpro.com/post/2026-07-31-why-argocd-and-kargo/
>
> If you've wired Argo CD + Kargo by hand: where did schema drift bite you?
>
> #PlatformEngineering #Kubernetes #GitOps

**Mechanics:** every post is under 280 characters (a URL counts as 23). Hashtags on the final post only — mid-thread hashtags read as spam to this audience.

---

## Watch-outs

- **Argo CD and Kargo are the foundation, not the villain.** The "don't reinvent CD" framing is a compliment to them — the whole point is standing ON them. If an Argo/Kargo maintainer shows up in the thread, the line to hold is "we productize your work, we don't compete with it." Never let it read as a knock on either project.
- **The stats trace to primary sources — don't inflate them in replies.**
  - Argo CD 97% in production / NPS 79 / 37% of users are platform engineers → [CNCF 2025 Argo CD End User Survey](https://www.cncf.io/announcements/2025/07/24/cncf-end-user-survey-finds-argo-cd-as-majority-adopted-gitops-solution-for-kubernetes/) (up from 93% in 2023). CNCF's own survey, safe to defend directly.
  - Kargo by the Argo CD creators at Akuity; 3.5M+ downloads, ~3,100 stars (+50% YoY) → [Akuity](https://akuity.io/blog/kargo-gitops-promotion-layer).
- **The manifests use CURRENT Kargo field names**, verified against the [Kargo v1alpha1 docs](https://docs.kargo.io/user-guide/how-to-guides/working-with-warehouses) at publish time (`allowTagsRegexes`, `constraint`, `imageSelectionStrategy`; `yaml-update` step; `sources.direct`/`stages`). If a reader says "the field is X now," that's the schema-drift the post is literally about — thank them, note the version, move on. Do not claim the YAML is frozen-correct forever.
- **Don't over-claim the label story.** The universal label across Warehouse + Stage + Application is `platform.example/project` (anonymized). `app.kubernetes.io/managed-by` is on the Kargo CRs and the rendered workloads, NOT on the Argo CD Applications. If someone inspects and points this out, they're right — the exit story rests on "plain labeled CRDs in a Git repo you own," which holds regardless of which label you select on.
- **"So what are you building?" is coming.** Post 4 shows the architecture in detail, which invites it. Hold the line for four more weeks: "a golden-path layer on the CNCF stack, still pre-launch, writing it up as I go." No name, no repo link.

---

_Companion docs: `blog-series-plan.md`, `competitive-analysis.md`, `post-01-the-missing-middle-social.md`, `post-03-platform-without-a-platform-team-social.md`._
