# Post 1 — "The Missing Middle" · Social copy

_Blog post: https://rtnpro.com/post/2026-07-09-the-missing-middle/ · Published 2026-07-09 · Phase 1, week 1_

**Positioning constraint:** suparship is NOT named. Post 1 is pure thesis; the product reveal is Post 8. A premature "I'm building this" undercuts the seven weeks of trust-building the series depends on.

**CTA choice:** both drafts invite argument ("tell me where I've got it wrong") rather than asking for a signup. That reliably outperforms a waitlist ask with SREs, and it seeds the comment thread we want if the post reaches r/kubernetes or HN.

---

## LinkedIn

Paste verbatim. This is plain text on purpose — see "Formatting" below.

> At the tech giants, a platform team is 10–20 engineers.
>
> At mid-market companies, it's 2–3. Same job description, a fifth of the people.
>
> Gartner says 80% of large software orgs will have platform engineering teams by 2026, up from 45% in 2022. The projection never mentions how big those teams are.
>
> So the mid-market team gets asked to pick between two roads. Both dead-end.
>
> Road one: build it.
>
> Backstage, ArgoCD, Helm, a scaffolding script, preview environments, a promotion flow. Every decision is locally correct. Eighteen months later the only documentation lives in one engineer's head, and that engineer has a competing offer.
>
> The failure mode isn't that you can't build a platform. It's that you can't staff one.
>
> Road two: rent a PaaS.
>
> The DX is genuinely excellent, and early on it's the right call. Then the bill bends. A documented Vercel case went from $20/mo to $286. And leaving isn't a migration, it's a rewrite, because you didn't just deploy on the platform. You built on it.
>
> A hosted PaaS is the right answer early and the wrong answer later.
>
> Nothing in the product tells you when you crossed over. You find out from a finance meeting.
>
> The gap between those two roads is where most of us actually work. I wrote about what lives there, and why the missing middle isn't missing technology. It's missing a product.
>
> Link in comments. Tell me where I've got it wrong.
>
> #PlatformEngineering #Kubernetes #DevOps #SRE #GitOps

### Formatting

**Do not use Unicode bold or italics** (𝗕𝗼𝗹𝗱, 𝘐𝘵𝘢𝘭𝘪𝘤). LinkedIn has no native rich text; those glyphs are math-alphanumeric codepoints, not styled letters. Three costs:

- **Accessibility.** Screen readers announce them character by character, or as "mathematical sans-serif bold capital B." Our audience includes blind engineers.
- **Search.** LinkedIn doesn't normalize those codepoints to ASCII, so a bolded "Platform Engineering" is invisible to anyone searching the phrase.
- **Signal.** Unicode-bolded posts read as growth-hacked. `blog-series-plan.md` says "SREs smell marketing instantly" — the formatting would undercut the argument before anyone reads it.

Also **no markdown**: `**bold**` and `*italic*` render as literal asterisks in the composer.

The only formatting lever LinkedIn gives you is the line break, so emphasis is structural: short paragraphs, a blank line between each, and the load-bearing sentence standing alone.

### Mechanics

- **Link in the first comment**, not the body. LinkedIn suppresses reach on posts with outbound links. Post the comment immediately after publishing.
- LinkedIn truncates at roughly 140 characters behind a "see more" fold. The two-line team-size hook is deliberately above it; the Gartner stat is deliberately below, because a statistic is a weaker cold open than the contrast.

---

## X — single post

> At tech giants, a platform team is 10–20 engineers.
>
> At mid-market companies, it's 2–3. Same job description.
>
> So you either build a platform you can't staff, or rent one you can't control.
>
> On the missing middle, and why it isn't missing technology:
>
> https://rtnpro.com/post/2026-07-09-the-missing-middle/
>
> #PlatformEngineering #Kubernetes

---

## X — thread

Suits the argument better; it has two distinct halves (build / rent).

**1/**
> Gartner: 80% of large software orgs will have platform engineering teams by 2026.
>
> The projection doesn't say how big those teams are.
>
> Giants: 10–20 engineers. Mid-market: 2–3.
>
> Same job description. A fifth of the people.

**2/**
> Road one — build it.
>
> Backstage. ArgoCD. Helm. A scaffolding script. Previews. A promotion flow.
>
> Every decision locally correct. 18 months later the only documentation is in one engineer's head, and he's interviewing.
>
> You can build a platform. You can't staff one.

**3/**
> Road two — rent it.
>
> The DX is great. Early on it's the right call.
>
> Then a $20/mo Vercel plan becomes $286. And leaving isn't a migration, it's a rewrite — you didn't deploy on it, you built on it.

**4/**
> A hosted PaaS is the right answer early and the wrong answer later.
>
> Nothing in the product tells you when you crossed over.
>
> You find out from a finance meeting.

**5/**
> Between "build an IDP you can't sustain" and "rent a PaaS you can't control" is where most of us actually work.
>
> The missing middle isn't missing technology. ArgoCD, Kargo, ESO, Gateway API all exist.
>
> It's missing a product.
>
> https://rtnpro.com/post/2026-07-09-the-missing-middle/
>
> #PlatformEngineering #Kubernetes #GitOps

**Mechanics:** every post is under 280 characters. Hashtags on the final post only — mid-thread hashtags read as spam to this audience.

---

## Watch-outs

- The blog post frames the mid-market org as "thirty to a hundred and fifty engineers," while this copy leans on the 2–3 vs 10–20 **platform team** contrast. Both are consistent, but if a commenter conflates them, the distinction to draw is platform-team size, not company size.
- Proof points trace to `competitive-analysis.md`: the Gartner 80%-by-2026 figure, the 2–3 vs 10–20 team-size contrast, and the documented Vercel $20→$286 case. Do not inflate these numbers in follow-up replies.

---

_Companion docs: `blog-series-plan.md`, `competitive-analysis.md`._
