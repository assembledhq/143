# How AI-Generated Content Is Marked: A Research Primer

*A technical study of the mechanisms behind AI content provenance and watermarking — what
Anthropic's Claude appears to do, how the underlying techniques work, and how to study them
rigorously.*

Prompted by the Claude support article
[*How Claude marks AI-generated content*](https://support.claude.com/en/articles/16266773-how-claude-marks-ai-generated-content).

---

## 0. Scope, and one honest caveat

This document is a research primer on **content provenance and watermarking for generative AI**.
It explains the publicly documented science: the C2PA provenance standard, the family of
statistical text-watermarking algorithms, how detection works as a hypothesis test, the known
attacks and impossibility results, and — importantly — a hands-on program for studying all of
this on **open models you control**.

There is one thing this document deliberately does **not** do: it does not try to reverse-engineer,
characterize, strip, or forge the *specific secret watermark that Anthropic deploys in Claude*.
That is out of scope for two independent reasons, and it's worth stating both because the second
one is the more useful insight:

1. **It's an attack on a provenance/safety mechanism.** The point of a provenance mark is to let
   people tell whether content came from an AI. Learning to remove it (laundering AI content to
   look human) or — worse — to *forge* it (making human-written text falsely test as
   Claude-generated, which can frame a real person) defeats that purpose and can harm third
   parties. That's not a research contribution; it's an exploit.

2. **It wouldn't work anyway from the outside.** As Section 6 shows in detail, you *cannot* recover
   a keyed generative watermark by generating a large corpus and hunting for patterns. What that
   procedure actually surfaces is **stylometry** — the model's stylistic fingerprint — which is a
   different layer entirely and is present with or without a watermark. Confusing the two is the
   single most common mistake in this area.

So the genuinely research-productive path — the one that lets you "understand this as much as
possible" — is to reproduce the *published* schemes on *open* models where you hold the key and can
toggle the watermark on and off. That's what Section 7 is for, and it's where the real learning is.

A sourcing note: the primary Claude support article was not directly reachable from this
environment (blocked by an egress proxy), so the description of Claude's specific rollout below is
reconstructed from Anthropic's Transparency Hub and secondary reporting, and is flagged as such.
Treat exact deployment specifics (dates, which scheme, parameters) as *reported* rather than
*confirmed from the primary source*; treat the underlying mechanisms (C2PA, watermarking math) as
well-established public knowledge.

---

## 1. What the article describes: Claude's two mechanisms

Per Anthropic's support documentation and reporting around it, Claude marks AI-generated content
using **two independent mechanisms**, chosen because they cover two different content types with
two different threat models:

| Mechanism | Applies to | What it is | What it proves | How it's defeated |
|---|---|---|---|---|
| **C2PA Content Credentials** | Files: `.svg`, `.png`, `.jpg` generated/processed by Claude | Cryptographically signed provenance metadata attached to the file | (a) the file passed through Claude; (b) it hasn't been altered since signing | Stripped by screenshots, re-encoding, format conversion, or metadata-removing tools |
| **Generative text watermark** | Text output from models launched on/after ~Aug 2, 2026 (as reported) | A low-amplitude statistical signal woven into token selection during generation | Statistical evidence, given the key, that the text was produced by the watermarking model | Degraded by heavy paraphrasing, translation, or re-generation; weak on short/low-entropy text |

The design logic is that **files and text fail differently**. A PNG is a container that can carry a
signed metadata block, so a cryptographic approach (C2PA) works and additionally gives you
tamper-evidence. Plain text has nowhere to *attach* metadata — copy-paste strips everything — so the
mark has to live *in the token choices themselves*, which is what a generative watermark does.

The "why" behind this is well-grounded in Anthropic's public commitments, even where the technical
specifics aren't: Anthropic's [Transparency Hub](https://www.anthropic.com/transparency) documents
voluntary commitments to content authentication under the **AI Seoul Summit Frontier AI Safety
Commitments** and the **2024 Munich AI Elections Accord** (mitigating deceptive AI election content
via provenance/watermarking). Watermarking is the technical discharge of those commitments.

The rest of this document unpacks each mechanism.

---

## 2. Mechanism 1 — C2PA Content Credentials (for files)

[C2PA](https://c2pa.org/) (Coalition for Content Provenance and Authenticity) is the industry
provenance standard, also surfaced to end users under the **Content Credentials** brand. It's the
same standard used by Adobe, camera manufacturers, OpenAI (DALL·E/ChatGPT images), Google, and
others. The current spec is [v2.x](https://spec.c2pa.org/).

### 2.1 The data model: manifests, assertions, claims, signatures

A Content Credential is a **C2PA Manifest** bound to an asset. Its structure, from the inside out:

- **Assertions** — atomic statements about the asset. Examples:
  - *Actions* (`c2pa.actions`): "created," "opened," "color-adjusted," "resized," etc.
  - *Ingredients*: references to prior assets that went into this one (provenance chains).
  - *Creation/authorship info*, thumbnails, and — the key one for AI — a **`digitalSourceType`**
    drawn from the IPTC vocabulary, e.g. `trainedAlgorithmicMedia` to mark "generated by a trained
    AI model."
  - *Hash assertions* binding the manifest to the actual bytes of the asset (see hard bindings).
- **Assertion store** — the collection of all assertions for the manifest.
- **Claim** — a single object that references the set of assertions *by cryptographic hash*, and
  records how they were hashed and which credential will sign them. The claim is the thing that
  gets signed.
- **Claim signature** — a digital signature over the claim, produced with the **private key** of an
  **X.509 certificate** belonging to the signing tool/service. C2PA uses
  [COSE](https://datatracker.ietf.org/doc/html/rfc8152) (CBOR Object Signing and Encryption) and
  recommends **SHA-256** for hashing.

Because the claim commits to the assertion hashes and the signature commits to the claim, the whole
structure is a tamper-evident chain: change any covered assertion or the covered asset bytes, and
the hashes no longer match / the signature no longer verifies.

### 2.2 Hard bindings vs soft bindings

This distinction is the heart of C2PA's robustness story:

- **Hard binding** — a cryptographic hash *of the asset's own bytes* (e.g. a `data hash` or box
  hash over image data), stored in an assertion and covered by the signature. This is what lets a
  verifier answer *"has this file been modified since it was signed?"* Flip a single pixel's byte
  and the hard binding breaks. Hard bindings are **exact** and **fragile** — any re-encoding
  changes the bytes and breaks them.

- **Soft binding** — a *content-derived* identifier that survives format changes: a **perceptual
  fingerprint** (a hash of perceptual features, robust to re-encoding) and/or an **invisible
  watermark** embedded in the pixels. A soft binding doesn't verify integrity; instead it lets you
  **re-discover the manifest** from a provenance store/cloud even after the metadata was stripped.

The combination — manifest + fingerprint + watermark — is marketed as **Durable Content
Credentials**: even if someone screenshots or re-saves the image and drops the metadata, the soft
binding can look the manifest back up. Note the crucial nuance: the C2PA *file* mechanism and an
*invisible image watermark* are **different layers** that C2PA composes. The signed metadata is not
itself a watermark; the watermark is one optional soft-binding aid to survive metadata loss.

### 2.3 Trust model

Anyone can create and sign a manifest — a signature alone only proves "some key signed this." Trust
comes from the **signer's certificate chaining to a recognized trust anchor** (a C2PA/CAI trust
list). A validator checks the signing certificate against known trust lists; a manifest whose chain
resolves to Anthropic's certificate is what tells you *Anthropic's tooling* produced or processed
the asset. This is why "signed by whom" matters more than "is it signed."

### 2.4 Honest limitations (the same ones the article notes)

- **Trivially stripped** by anything that doesn't preserve metadata: screenshots, many social
  platforms, format conversion, editing in non-C2PA tools. Hard bindings break on *any* re-encode.
- **Absence proves nothing.** No credential ≠ "not AI." It might just have been stripped.
- **Soft bindings mitigate but don't guarantee** recovery; watermarks in images can be attacked.
- It answers *"did this pass through a C2PA-enabled tool, unmodified since?"* — not *"is this true /
  is this real."* Provenance ≠ veracity.

---

## 3. Mechanism 2 — Generative text watermarking

This is the mechanism most people find mysterious, so it gets the most depth. The key mental model:
**a generative text watermark is not hidden characters, invisible Unicode, or a signature appended
to the text. It is a deliberate, keyed, low-amplitude bias in *which tokens the model samples*,
arranged so that a detector holding the secret key can later run a statistical test and say "this
token sequence is biased in the specific way my key predicts."**

### 3.1 First, what it is *not*: post-hoc detectors

Before the real mechanism, clear away the confusable neighbor. **Post-hoc detectors** try to guess
"is this AI?" *without* any mark having been embedded:

- **Classifier-based** (GPTZero, the now-withdrawn OpenAI classifier): train a model on human vs AI
  text.
- **Zero-shot statistical** (DetectGPT): AI text tends to sit near local maxima of a model's
  log-probability curvature; perturb-and-compare to detect it.
- **Perplexity/burstiness** heuristics: AI text is often "smoother" (lower perplexity variance).

These are **not watermarks**. They're unreliable (notoriously high false-positive rates — they've
flagged human writing, including non-native English and the U.S. Constitution), and they degrade
fast under light editing. Anthropic's mechanism is *not* this. But this category matters here
because **it is exactly what "generate a corpus and look for patterns" actually measures** — see
Section 6.

### 3.2 The real mechanism: generative (keyed) watermarks

A generative watermark modifies the model's **decoding/sampling** step. The general recipe shared
by every scheme in this family:

1. At generation step *t*, take the LLM's next-token distribution `p_t(·)` over the vocabulary.
2. Derive a **pseudo-random seed** from a **secret watermark key** combined with a **sliding window
   of the previous *h* tokens**: `seed_t = PRF(key, x_{t-h..t-1})`.
3. Use `seed_t` to compute per-token pseudo-random values, and **perturb the sampling** using those
   values so the chosen token is subtly correlated with the key.
4. Emit the token. The text reads normally; the correlation is invisible to a reader but
   measurable by someone who can regenerate the same pseudo-random values with the key.

Detection reverses steps 2–3: for candidate text, recompute the per-position pseudo-random values
with the key and test whether the actual tokens are correlated with them more than chance allows.

The schemes differ in *how* step 3 perturbs sampling. Three canonical designs:

#### (a) Green-list / red-list — Kirchenbauer et al., 2023

The foundational academic scheme ("[A Watermark for Large Language
Models](https://arxiv.org/abs/2301.10226)").

- Use `seed_t` to pseudo-randomly split the vocabulary into a **green list** (fraction γ, e.g. 0.5)
  and a **red list**.
- Add a fixed bias **δ** to the logits of green-list tokens before sampling. The model now *prefers*
  green tokens wherever it's not too confident about something else.
- **Detection**: recompute the green list at each position with the key; count how many emitted
  tokens are green. Human text has ≈ γ fraction green *by chance*. Watermarked text has more. The
  test statistic is a **z-score**:

  ```
  z = (|green tokens| − γ·T) / sqrt(T·γ·(1−γ))
  ```

  where *T* is the number of scored tokens. Large *z* ⇒ reject "human" null ⇒ watermarked.

- **Knobs**: δ trades **strength vs quality** (bigger δ = easier detection but more distortion of
  the text); γ sets the green fraction; *h* (context width) trades robustness vs repetition
  artifacts.

Pseudocode sketch (illustrative, on an open model — not Claude):

```python
def watermarked_logits(logits, prev_tokens, key, gamma=0.5, delta=2.0):
    seed = prf(key, prev_tokens[-H:])          # secret-key-seeded PRNG
    green = pseudo_random_subset(vocab, gamma, seed)
    logits[green] += delta                      # nudge green tokens up
    return logits                               # then sample as usual
```

#### (b) Distortion-free / exponential-minimum sampling — Aaronson (OpenAI), Kuditipudi et al.

A cleverer design that leaves the model's output distribution **unchanged in expectation**
("distortion-free"), so quality is provably preserved on average:

- Use the key to generate a pseudo-random value `r_i ∈ (0,1)` for each vocabulary token at this
  step.
- Instead of sampling from `p_t`, choose the token maximizing `r_i^{1/p_i}` (the **Gumbel / exponential**
  trick). Marginally, this still samples from `p_t` — but *which* token wins is now a deterministic
  function of the key and `p_t`.
- **Detection**: an alignment test between the emitted tokens and the key's `r` stream — watermarked
  text shows suspiciously strong alignment. Kuditipudi et al.
  ("[Robust Distortion-Free Watermarks](https://arxiv.org/abs/2307.15593)") add resampling and
  alignment via edit distance for robustness to edits.
- **Why it matters**: "distortion-free" means you don't pay a quality tax in expectation, which is a
  big deal for production use.

#### (c) Tournament sampling — SynthID-Text (Google DeepMind, Nature 2024)

The first generative text watermark deployed **at scale** (inside Gemini), published in Nature
("[Scalable watermarking for identifying large language model
outputs](https://www.nature.com/articles/s41586-024-08025-4)") and **open-sourced**
([github.com/google-deepmind/synthid-text](https://github.com/google-deepmind/synthid-text)). This
is the most relevant reference point for understanding a real, production-grade deployment.

Mechanism — **Tournament sampling**:

- For position *t*, use `PRF(key, context)` to assign, for each of *m* "layers," a pseudo-random
  **g-value** `g_ℓ(token) ∈ {0,1}` (or in [0,1]) to every vocabulary token.
- Draw several candidate tokens from the model's distribution `p_t`, then run a **multi-round
  single-elimination tournament**: at each layer, the surviving candidate with the higher g-value
  advances. The final winner is emitted.
- Net effect: tokens with high g-values are preferentially selected, so the emitted text carries a
  measurable bias toward high-g tokens **at positions determined by the key**.
- **Two configuration regimes**:
  - *Non-distortionary*: preserves text quality very closely (used where quality is paramount).
  - *Distortionary*: stronger, more detectable signal at a small quality cost.
- **Detection** (cheap, no LLM needed):
  - **Weighted Mean detector** — average the g-values of the emitted tokens; watermarked text scores
    above the ~0.5 chance baseline. No training required.
  - **Bayesian detector** — a small trained model over the g-value features; more powerful,
    especially on shorter or partially-edited text.
- **Validated at scale**: DeepMind reported comparing feedback across ~20 million Gemini responses
  and found no significant difference in thumbs-up/down between watermarked and non-watermarked
  outputs — i.e., users couldn't tell.

If you want to understand what a system like Claude's *plausibly* resembles, SynthID-Text is the
best-documented public analogue: keyed, context-seeded, sampling-level, detectable without the
model, and quality-preserving. (This is an analogy for study, not a claim about Claude's internals.)

### 3.3 The fundamental constraint: entropy

Every generative watermark can only "hide" signal where the model has **freedom to choose**. If the
next token is essentially forced — a closing bracket in code, a quoted string, the second half of a
common phrase, a deterministic factual answer — there's no room to bias the choice without breaking
the output. Consequences:

- **High-entropy text** (creative prose, open-ended answers) watermarks strongly.
- **Low-entropy text** (code, math, quotations, terse factual replies) watermarks weakly or not at
  all.
- This is not an implementation gap; it's information-theoretic. It's also why short outputs are
  hard to mark: fewer tokens × low per-token signal = not enough statistical power (next section).

---

## 4. Detection as a statistical hypothesis test

Every generative-watermark detector is the same statistical object, and understanding it dissolves
most of the mystique.

- **Null hypothesis H₀**: "this text was *not* produced by the watermarking model" (equivalently,
  the tokens are independent of the key). Under H₀ the test statistic — green-token count, mean
  g-value, alignment score — has a known distribution centered on chance.
- **The watermark shifts the statistic into the tail.** Detection picks a **threshold** for a target
  **false-positive rate (FPR)**. Production systems run at very low FPR (e.g. p < 10⁻⁶ or lower)
  because at scale even a tiny FPR means many false accusations.
- **Statistical power grows with length.** More scored tokens = more evidence = you can detect a
  weaker per-token signal. This is the length/strength/quality trade-off in one sentence: for a
  fixed quality budget (small per-token perturbation), you need enough tokens to accumulate
  significance.
- **Two error rates that trade off**:
  - *False positive*: human text flagged as watermarked. Kept extremely low by design (forging/
    spoofing risk makes this the dangerous direction).
  - *False negative*: watermarked text missed. Higher, and it's where paraphrase/editing attacks
    live.

A useful way to hold it: **the watermark is a probabilistic signal, not a proof.** A positive result
is "this text is statistically very unlikely to be unwatermarked," calibrated to a chosen FPR — not
a cryptographic certainty about a specific author.

---

## 5. Robustness, attacks, and impossibility results

A mature understanding of watermarking is mostly an understanding of its **limits**. These are
published, and they define the real-world threat model. (Listed to explain *why the mechanism is
bounded* — not as operational instructions against any deployed system.)

- **Paraphrasing / translation / re-generation** is the dominant robustness ceiling. Rewriting the
  text with another model re-samples the tokens and destroys the keyed choices. Round-tripping
  through translation does the same. This is why no one claims watermarks are removal-proof.
- **Editing, truncation, token insertion/deletion** desynchronize the context windows that seed the
  pseudo-randomness, weakening detection roughly in proportion to how much is changed.
- **Mixing / dilution**: interleaving watermarked and human text lowers the density of marked
  tokens below the detection threshold.
- **Spoofing / forging** — the *harmful* direction: crafting text that tests **positive** without
  being model-generated, to falsely attribute authorship. This is why detection keys are guarded
  and why low FPR matters: a forged positive can implicate a real person. Distortion-free and
  tournament schemes have some structural resistance, but spoofing remains an active risk class.
- **Black-box watermark discovery** (e.g. ETH SRI Lab's
  ["Black-Box Detection of Language Model Watermarks"](https://arxiv.org/abs/2405.20777)): with many
  controlled queries you can sometimes infer the *presence and rough type/parameters* of a watermark
  — but note the ceiling: this detects *that a scheme is present and what family it's in*, using
  large query budgets and distributional comparisons; it does **not** hand you the secret key, and
  its natural downstream use (evasion/spoofing) is exactly the attack surface above. This is the
  closest published analogue to "reverse-engineer from outputs," and its limits are precisely why
  Section 6 says the naive corpus-scanning version doesn't work.

### Impossibility results (the theory backstop)

- **Zhang et al., 2023 — "[Watermarks in the Sand: Impossibility of Strong Watermarking for
  Generative Models](https://arxiv.org/abs/2311.04378)."** Under a quality-preserving random-walk
  paraphraser, *strong* (fully robust) watermarking is impossible. Corollary: every deployed
  watermark is a **weak** watermark — it raises the cost and skill needed to launder content, it
  doesn't make laundering impossible.
- **Sadasivan et al. — "[Can AI-Generated Text Be Reliably Detected?](https://arxiv.org/abs/2303.11156)"**
  Paraphrase attacks and theoretical bounds on the best achievable detector as human/AI
  distributions converge.

The honest synthesis: **watermarking is defense-in-depth, not DRM.** It reliably catches casual and
at-scale unmodified use, deters, and supports provenance workflows; it does not survive a determined
adversary with a paraphraser. Anthropic's own framing (strippable metadata, probabilistic text mark)
is consistent with this.

---

## 6. The methodological reality check: why "generate a corpus and find the pattern" measures *style*, not the watermark

This section is the direct answer to the instinct behind the original request — *"generate a lot of
content and look for the patterns that reveal the watermark."* It's a natural instinct, and it's
worth understanding exactly why it fails, because the reason is the same reason the whole scheme is
designed the way it is.

**1. A keyed watermark is not identifiable from outputs alone.** A generative watermark is defined as
a *bias relative to the model's own next-token distribution* `p_t`, keyed by a *secret*. To measure
that bias at position *t* you would need to compare the token that was actually emitted against the
distribution `p_t` the model *would have* sampled from **without** the watermark — for the *same
context*. From finished text you have neither `p_t` (you don't have the un-watermarked weights and
matched decoding) nor the key. The per-token perturbation is small and is entangled with the model's
ordinary stochasticity. It is mathematically **not separable** by inspecting outputs. This is the
same identifiability wall that makes the scheme secure.

**2. What you *would* find is stylometry, not the watermark.** Generate a million words and you'll
absolutely find patterns: favored words and phrases, sentence-length rhythms, punctuation habits
(em-dashes, tricolons), function-word ratios, low perplexity variance, structural tics. These are
the model's **style fingerprint**. They are:

- present **with or without** any watermark (they come from training, RLHF, and decoding defaults);
- what post-hoc "AI detectors" (Section 3.1) actually key on;
- **not** what makes text test positive under a provenance detector.

Mistaking "this reads like AI / this has consistent stylistic patterns" for "I found the watermark"
is the central error in this space. Stylometry is a real and interesting field — but it's a
*different layer* from a keyed cryptographic-style watermark, and studying one tells you almost
nothing about the other.

**3. Keyed randomization is engineered to look like noise without the key.** In a SynthID-style
scheme, the g-values are pseudo-random per `(key, context)`. Across a corpus generated under one
unknown key, the biased positions are scattered pseudo-randomly and, without the key to line them
up, they average toward uniform. You cannot regress the key out of samples; that's the design goal,
not an accident.

**4. To actually characterize a generative watermark you need three things** — none of which a pile
of Claude outputs gives you:

- the **detection key**,
- a matched **un-watermarked baseline** from the *same* model weights and decoder, and
- **controlled prompts** so you can compare like with like.

Which is exactly why legitimate watermarking research is done on **open models where the researcher
sets the key and owns both the watermarked and baseline decoders.** That's the whole methodology of
the field, and it's the subject of the next section.

So: the corpus-scanning approach isn't just ethically the wrong target (Section 0) — it's
scientifically a dead end for recovering a watermark, and a detour into stylometry if pursued. The
productive redirection is to stop trying to read the secret off the outside and instead *rebuild the
mechanism yourself where you can see all of it.*

---

## 7. A legitimate hands-on research program

Here's how to "understand this as much as possible" with real experiments — the version that
actually teaches you the mechanisms, all on systems you control or that ship reference
implementations.

### 7.1 Reproduce the published text-watermark schemes on open models

Run these on open-weight models (GPT-2, Llama, Gemma, Mistral) where you set the key and can toggle
the watermark:

- **SynthID-Text** — [github.com/google-deepmind/synthid-text](https://github.com/google-deepmind/synthid-text).
  Generate watermarked and unwatermarked text from the same model; run the Weighted-Mean and
  Bayesian detectors; reproduce the Nature paper's detectability/quality curves; flip between
  distortionary and non-distortionary modes and watch the trade-off.
- **Green/red list** — the Kirchenbauer et al.
  [`lm-watermarking`](https://github.com/jwkirchenbauer/lm-watermarking) repo. Visualize the
  green-token fraction and z-score as you sweep δ, γ, and context width *h*.
- **Robust distortion-free** — Kuditipudi et al.'s reference implementation of the exponential/Gumbel
  scheme.
- **MarkLLM** — [an open toolkit](https://github.com/THU-BPM/MarkLLM) with unified implementations
  and *visualizers* of many schemes side by side; the fastest way to build intuition across the
  family.

### 7.2 Controlled experiments that build real understanding

For each scheme, measure the relationships that define it:

1. **Strength vs quality vs detectability.** Sweep the strength knob (δ / distortion). Plot text
   quality (perplexity, or a small human/A-B eval) against detection AUROC. You'll rediscover the
   central trade-off empirically.
2. **Length vs power.** Truncate outputs to 25/50/100/200/500 tokens; plot detection AUROC vs
   length. You'll see statistical power accumulate — and see why short text can't be marked
   reliably.
3. **Entropy dependence.** Compare watermark power on creative prose vs code vs terse factual
   answers. Quantify the entropy ceiling from Section 3.3.
4. **Robustness curves.** Apply controlled paraphrasing, random token edits at rate ρ, truncation,
   and temperature changes; plot detection AUROC vs attack strength. This reproduces the Section 5
   robustness story as data.
5. **False-positive calibration.** Run the detector over a large *human* corpus; measure empirical
   FPR vs threshold, and confirm it matches the theoretical null. This is the most important
   experiment for understanding why deployments run at extreme thresholds.

### 7.3 Study C2PA hands-on

- Install the open-source **[c2patool](https://github.com/contentauth/c2patool)** / Content
  Authenticity Initiative (CAI) SDK.
- **Create and sign** a manifest on an image; **inspect** it; **verify** it. Then **flip one byte**
  of the image and watch the hard binding fail — that's tamper-evidence made concrete.
- **Re-encode** the image (PNG→JPG, resize) and watch the metadata (and hard binding) disappear —
  that's the strippability limitation made concrete.
- Experiment with **soft bindings / fingerprints** to see how durable credentials re-discover a
  stripped manifest.
- Inspect a **real Claude-generated** `.png`/`.svg`'s manifest with `c2patool` or the
  [Content Credentials verifier](https://contentcredentials.org/verify) to see the actual assertions
  Anthropic writes (signer identity, `digitalSourceType`, actions). This is legitimate *inspection*
  of provenance you were given — the intended use — as opposed to reverse-engineering a secret.

The line to keep: **inspecting the credential you were handed, and reproducing published schemes on
models you own, is the whole legitimate research program. Trying to extract or defeat Claude's
specific secret text mark is neither necessary for understanding nor productive — it's the one move
that's both an attack and a scientific dead end.**

---

## 8. Open research questions (where the field actually is)

If you want frontier problems rather than settled ones:

- **Robustness under paraphrase** — can any scheme meaningfully survive a strong paraphraser without
  destroying quality, given the Zhang et al. impossibility? (Mostly "no" today; semantic-level
  watermarks are the hope.)
- **Semantic watermarks** — marking *meaning* rather than token choices, to survive rewording.
- **Spoofing resistance** — provably preventing forged positives; this is the safety-critical
  direction.
- **Multi-party / cross-model provenance** — reconciling watermarks with C2PA soft bindings when
  content is edited across tools (see ["Authenticated Contradictions from Desynchronized Provenance
  and Watermarking"](https://arxiv.org/abs/2603.02378)).
- **Low-entropy content** — provenance for code and short factual text, where token-level marks
  can't get purchase.
- **Standardization & interop** — will text watermarks converge on a shared detector/registry the
  way C2PA standardized file provenance?

---

## 9. Annotated sources

**Anthropic / Claude**
- Claude support: *How Claude marks AI-generated content* — https://support.claude.com/en/articles/16266773-how-claude-marks-ai-generated-content *(primary article; not directly reachable from this environment)*
- Anthropic Transparency Hub — https://www.anthropic.com/transparency *(voluntary commitments: Seoul Frontier AI Safety Commitments; Munich AI Elections Accord)*

**C2PA / Content Credentials**
- C2PA Specification & Explainer (v2.4) — https://spec.c2pa.org/
- C2PA FAQ — https://c2pa.org/faqs/
- Content Authenticity Initiative open-source tools — https://opensource.contentauthenticity.org/
- `c2patool` — https://github.com/contentauth/c2patool

**Text watermarking — foundational and deployed**
- Kirchenbauer et al., *A Watermark for Large Language Models* (green/red list) — https://arxiv.org/abs/2301.10226
- Kuditipudi et al., *Robust Distortion-Free Watermarks for Language Models* — https://arxiv.org/abs/2307.15593
- Dathathri et al. (DeepMind), *Scalable watermarking for identifying LLM outputs* (SynthID-Text, Nature 2024) — https://www.nature.com/articles/s41586-024-08025-4
- SynthID-Text open source — https://github.com/google-deepmind/synthid-text
- DeepMind blog, *Watermarking AI-generated text and video with SynthID* — https://deepmind.google/blog/watermarking-ai-generated-text-and-video-with-synthid/
- MarkLLM toolkit — https://github.com/THU-BPM/MarkLLM

**Attacks, limits, and analysis**
- Zhang et al., *Watermarks in the Sand: Impossibility of Strong Watermarking* — https://arxiv.org/abs/2311.04378
- Sadasivan et al., *Can AI-Generated Text Be Reliably Detected?* — https://arxiv.org/abs/2303.11156
- *Black-Box Detection of Language Model Watermarks* (ETH SRI Lab) — https://arxiv.org/abs/2405.20777
- ETH SRI Lab, *Probing Google DeepMind's SynthID-Text* — https://www.sri.inf.ethz.ch/blog/probingsynthid

---

## Appendix: glossary

- **Provenance** — verifiable record of where content came from and how it changed. (C2PA.)
- **Watermark** — a signal embedded *in the content itself* so origin can be tested later.
- **C2PA / Content Credentials** — the standard for cryptographically signed file provenance metadata.
- **Manifest / assertion / claim / claim signature** — C2PA's nested provenance data structure and its signature.
- **Hard binding** — exact hash of asset bytes; tamper-evident, breaks on any re-encode.
- **Soft binding** — robust fingerprint/watermark to re-discover a stripped manifest ("durable credentials").
- **Generative watermark** — a keyed bias in an LLM's token sampling, detectable with the key.
- **Green/red list, exponential/Gumbel, tournament sampling** — the three canonical generative-watermark designs.
- **Distortion-free** — a watermark that preserves the model's output distribution in expectation.
- **z-score / AUROC / FPR** — the statistics of watermark detection (evidence strength / detector quality / false-alarm rate).
- **Stylometry** — analysis of *writing style*; a model's stylistic fingerprint. **Not** a watermark.
- **Spoofing** — forging a *positive* detection on non-model text; the safety-critical attack.

---

*This document studies published provenance and watermarking techniques and how to reproduce them on
open models. It intentionally does not attempt to characterize, remove, or forge the specific
provenance marks Anthropic applies to Claude output — that would undermine a provenance/safety
mechanism (and, as Section 6 shows, cannot be done from outputs alone regardless).*
