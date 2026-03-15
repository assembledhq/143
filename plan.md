# Landing Page Content Plan for 143

## Current State
- Landing page exists at `frontend/src/app/(landing)/` with Hero, How It Works, Story, and CTA sections
- **No footer** exists anywhere on the landing page
- **No legal or informational pages** exist
- Project is **open source** under **MIT License** by Assembled
- Tech stack: Next.js 16 (App Router), Tailwind CSS v4, shadcn/ui
- The project is a self-hosted AI agent tool, **not a SaaS**

---

## Pages to Create (Priority Order)

### 1. Footer Component (Create First)
**Why:** Every page below needs to be discoverable. A footer is the standard place for legal/informational links on landing pages.

**Contents:**
- Project name & tagline ("143 — AI agents that fix and improve production systems")
- GitHub repo link
- Links to: Privacy Policy, Terms of Use, License, Security
- "Built by Assembled" attribution
- Copyright notice

**Location:** `frontend/src/components/landing/footer.tsx`
Add to landing page below the CTA section.

---

### 2. Privacy Policy (`/privacy`)
**Why:** Required if the website collects _any_ data — even basic analytics, cookies for auth sessions, or sign-up email addresses. The middleware already sets a `session_token` cookie, so this is needed.

**Should cover:**
- What data the **website** (143.dev) collects (account info, session cookies, usage analytics if any)
- That the **self-hosted software** itself doesn't phone home or send data to Assembled
- No selling of data to third parties
- Cookie usage (session_token for auth)
- Contact info for privacy questions
- Open-source transparency: users can inspect all data handling in the source code

**Tone:** Plain language, short, no legalese walls. Appropriate for an open-source project.

**Location:** `frontend/src/app/(landing)/privacy/page.tsx`

---

### 3. Terms of Use (`/terms`)
**Why:** Covers use of the **website** (143.dev). The MIT license covers the software itself, but the website needs its own terms for account creation, acceptable use, etc.

**Should cover:**
- Website usage terms (not software license — that's MIT)
- Account responsibilities
- Acceptable use policy
- Disclaimer of warranties (mirrors MIT license language)
- Limitation of liability
- Reference to MIT License for the software
- Right to modify terms

**Tone:** Concise, plain language. Not a 20-page corporate document.

**Location:** `frontend/src/app/(landing)/terms/page.tsx`

---

### 4. Security Policy (`/security`)
**Why:** Critical for a tool that handles production systems, repos, and issue trackers. Builds trust and provides responsible disclosure guidance.

**Should cover:**
- How to report security vulnerabilities (responsible disclosure)
- Supported versions
- Security design principles (sandboxed containers, etc.)
- Link to GitHub security advisories
- PGP key or security contact email (placeholder)

**Location:** `frontend/src/app/(landing)/security/page.tsx`

---

## Pages NOT Needed (and Why)

| Page | Why Skip |
|------|----------|
| **About Page** | The landing page already tells the story. The README and GitHub repo serve as the "about." Not needed for an open-source project with a clear landing page. |
| **Cookie Policy (separate)** | Overkill — cookie info fits within the Privacy Policy since there's only a session cookie. |
| **SLA / Service Agreement** | Not a SaaS. Users self-host. No uptime guarantees to make. |
| **Pricing Page** | Open source, no paid tiers. |
| **Refund Policy** | Nothing to refund. |
| **GDPR/CCPA dedicated pages** | Can be addressed within Privacy Policy given the minimal data collection. |
| **Contributing Page (on website)** | Already covered by GitHub repo conventions (CONTRIBUTING.md can live in repo, not on the website). |
| **License Page** | MIT License is in the repo. Footer can link directly to the LICENSE file on GitHub. No need for a dedicated page. |

---

## Implementation Approach

All new pages will:
- Use the same `isDark` theme detection pattern as the landing page
- Match the existing minimalist design (Geist font, dark/light mode, clean spacing)
- Be static content (no API calls needed)
- Live under the `(landing)` route group
- Share a consistent layout with a back-to-home link and the new footer

### File Structure After Implementation
```
frontend/src/app/(landing)/
├── page.tsx                    (existing landing page)
├── layout.tsx                  (update to include footer)
├── privacy/page.tsx            (new)
├── terms/page.tsx              (new)
├── security/page.tsx           (new)
frontend/src/components/landing/
├── footer.tsx                  (new)
├── legal-page-layout.tsx       (new - shared layout for legal pages)
```

### Order of Work
1. Create footer component
2. Create shared legal page layout component
3. Create Privacy Policy page
4. Create Terms of Use page
5. Create Security Policy page
6. Add footer to landing page layout
