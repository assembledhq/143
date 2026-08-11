#!/usr/bin/env python3
"""
Watermark-identifiability study — analysis core (v2, hardened after adversarial review).

HONEST QUESTION (the only thing we can answer):
    From a model's OUTPUT TEXT ALONE — without the secret key, the exact scheme
    (context width, hashing, gamma), and the model's tokenization — can a
    Kirchenbauer-style keyed green-list watermark be *identified/recovered*?

What this study does and does NOT claim (baked in per adversarial review):
  * It does NOT test "is Claude watermarked." A watermark is invisible by design.
  * "Absence of evidence != evidence of absence." Stylometry is ORTHOGONAL and
    cannot rule a watermark in or out.
  * Thresholds come from the EMPIRICAL null (key-scan), not Gaussian theory,
    because repeated n-grams make the word-level z-test overdispersed.
  * Deep-tail theoretical numbers are labeled theoretical-and-contingent.

Design pieces:
  1. STYLOMETRY        — what "scan the outputs for patterns" actually recovers (= style).
  2. NULL KEY-SCAN     — many random keys on the real corpus; empirical null (overdispersed).
  3. INDEPENDENCE FIX  — same scan over DISTINCT n-grams -> restores ~N(0,1) (explains overdispersion).
  4. POSITIVE CONTROL  — inject a known-key delta-bias watermark; detect with:
        (a) true key + correct scheme  -> strong z
        (b) wrong keys + correct scheme -> empirical null (invisible without key)
        (c) true key + WRONG scheme (h=2) -> collapses (invisible without the scheme)
  5. KEY-SPACE ARG     — strongest false-positive z over N keys vs an unsearchable ~2^128 space.
  6. CAPACITY          — entropy proxies: a watermark can only live in high-entropy positions.

Pure standard library. Deterministic. blake2b keyed hashing for speed.
"""

import sys, os, re, json, math, random, statistics
from hashlib import blake2b
from collections import Counter, defaultdict

GAMMA = 0.5
Z_ONESIDED_THRESH = 4.0
N_NULL_KEYS = 4000     # random keys, full-token detector, empirical null on real corpus
N_DEDUP_KEYS = 1500    # random keys, distinct-ngram detector (independence demo)
N_WRONG_KEYS = 500     # wrong-key scan per positive-control delta
SYNTH_LEN = 8000
DELTAS = (0.5, 1.0, 2.0)
SEED = 20260811

random.seed(SEED)
WORD_RE = re.compile(r"[a-zA-Z][a-zA-Z'\-]*")
SEP = b"\x1f"

FUNCTION_WORDS = {
    "the","a","an","and","or","but","if","of","to","in","on","for","with","as",
    "that","this","it","is","was","are","were","be","been","being","at","by",
    "from","into","about","than","then","so","because","while","which","who",
    "not","no","yes","i","you","he","she","they","we","them","us","our","your",
}

# ---------- pseudo-random primitive: keyed hash -> uniform [0,1) ----------

def u01_keyed(blob, key_bytes):
    d = blake2b(blob, key=key_bytes, digest_size=8).digest()
    return int.from_bytes(d, "big") / 2.0**64

def one_sided_p(z):
    return 0.5 * math.erfc(z / math.sqrt(2))

def key_bytes_of(tag, i):
    return f"{tag}:{i}:{SEED}".encode()

# ---------- load / tokenize ----------

def load_corpus(path):
    texts = []
    if os.path.isdir(path):
        for fn in sorted(os.listdir(path)):
            if fn.endswith(".txt"):
                texts.append(open(os.path.join(path, fn), encoding="utf-8").read())
    else:
        texts.append(open(path, encoding="utf-8").read())
    return texts

def tokens_of(texts):
    words = []
    for t in texts:
        words.extend(w.lower() for w in WORD_RE.findall(t))
    return words

# ---------- context/token blobs for a given context width h ----------

def blobs_for(tokens, h):
    """Per-position byte blob = context(h prev words) + SEP + current word."""
    out = []
    for i in range(h, len(tokens)):
        ctx = SEP.join(tokens[i-h+0:i][k].encode() for k in range(h)) if h > 0 else b""
        out.append(ctx + SEP + tokens[i].encode())
    return out

def detect_z(blobs, key_bytes, gamma=GAMMA):
    green = 0
    for b in blobs:
        if u01_keyed(b, key_bytes) < gamma:
            green += 1
    T = len(blobs)
    if T == 0:
        return 0.0, 0, 0
    z = (green - gamma*T) / math.sqrt(T*gamma*(1-gamma))
    return z, green, T

def key_scan(blobs, n_keys, tag, gamma=GAMMA):
    zs = []
    for k in range(n_keys):
        z, _, _ = detect_z(blobs, key_bytes_of(tag, k), gamma)
        zs.append(z)
    zs_sorted = sorted(zs)
    def pct(p): return round(zs_sorted[min(n_keys-1, int(p*n_keys))], 3)
    return {
        "n_keys": n_keys,
        "z_mean": round(statistics.mean(zs), 4),
        "z_stdev": round(statistics.pstdev(zs), 4),
        "z_min": round(min(zs), 3),
        "z_max": round(max(zs), 3),              # strongest false positive from n_keys tries
        "frac_over_z4_onesided": round(sum(1 for z in zs if z > Z_ONESIDED_THRESH)/n_keys, 6),
        "pct": {"p50": pct(0.50), "p99": pct(0.99), "p999": pct(0.999)},
    }

# ---------- stylometry ----------

def stylometry(texts, tokens):
    full = "\n\n".join(texts)
    n = len(tokens)
    types = Counter(tokens)
    hapax = sum(1 for _, c in types.items() if c == 1)
    sent_lens = []
    for t in texts:
        for p in re.split(r"[.!?]+", t):
            wc = len(WORD_RE.findall(p))
            if wc > 0: sent_lens.append(wc)
    def r1000(c): return round(1000.0*c/n, 3) if n else 0.0
    punct = {"em_dash": full.count("—")+full.count("--"), "comma": full.count(","),
             "semicolon": full.count(";"), "colon": full.count(":"), "paren": full.count("(")}
    bigrams = Counter(zip(tokens, tokens[1:]))
    func = sum(types[w] for w in FUNCTION_WORDS)
    return {
        "n_tokens": n, "n_types": len(types),
        "type_token_ratio": round(len(types)/n, 4),
        "hapax_fraction": round(hapax/len(types), 4),
        "mean_word_len": round(sum(len(w) for w in tokens)/n, 3),
        "n_sentences": len(sent_lens),
        "sent_len_mean": round(statistics.mean(sent_lens), 2),
        "sent_len_stdev": round(statistics.pstdev(sent_lens), 2),
        "sent_len_cv_burstiness": round(statistics.pstdev(sent_lens)/statistics.mean(sent_lens), 3),
        "punct_per_1000": {k: r1000(v) for k, v in punct.items()},
        "function_word_fraction": round(func/n, 4),
        "top_content_unigrams": [(w, c) for w, c in types.most_common(60) if w not in FUNCTION_WORDS][:12],
        "top_bigrams": [(" ".join(bg), c) for bg, c in bigrams.most_common(12)],
    }

# ---------- positive control: synthesize watermarked text under a known key ----------

def build_bigram_model(tokens):
    model = defaultdict(Counter)
    for a, b in zip(tokens, tokens[1:]):
        model[a][b] += 1
    return model, Counter(tokens)

def synth_watermarked(tokens, key_star_bytes, delta, length=SYNTH_LEN, gamma=GAMMA):
    """Generate from the corpus's own bigram stats, adding a delta logit-bias to
    green tokens under key_star (word-level, h=1). delta=0 => base distribution."""
    model, unigram = build_bigram_model(tokens)
    vocab = list(unigram.keys())
    rng = random.Random((hash((delta, SEED)) & 0xffffffff))
    out = [rng.choice(vocab)]
    for _ in range(length-1):
        prev = out[-1]
        cnts = model.get(prev)
        if cnts and len(cnts) >= 2:
            words = list(cnts.keys()); base = [math.log(cnts[w]) for w in words]
        else:
            words = rng.sample(vocab, min(40, len(vocab))); base = [math.log(unigram[w]) for w in words]
        logits = []
        for w, b in zip(words, base):
            blob = prev.encode() + SEP + w.encode()
            g = delta if u01_keyed(blob, key_star_bytes) < gamma else 0.0
            logits.append(b + g)
        m = max(logits); exps = [math.exp(l-m) for l in logits]; s = sum(exps)
        r = rng.random()*s; acc = 0.0; pick = words[-1]
        for w, e in zip(words, exps):
            acc += e
            if acc >= r: pick = w; break
        out.append(pick)
    return out

# ---------- capacity ----------

def capacity(tokens):
    uni = Counter(tokens); n = sum(uni.values())
    h1 = -sum((c/n)*math.log2(c/n) for c in uni.values())
    model, _ = build_bigram_model(tokens)
    forced = total = 0; cond = 0.0
    for prev, cnts in model.items():
        tot = sum(cnts.values())
        h = -sum((c/tot)*math.log2(c/tot) for c in cnts.values())
        cond += tot*h
        if max(cnts.values())/tot > 0.9: forced += tot
        total += tot
    return {"unigram_entropy_bits_per_word": round(h1, 3),
            "bigram_conditional_entropy_bits": round(cond/total, 3),
            "frac_low_entropy_positions_gt0.9": round(forced/total, 4),
            "note": "word-level proxy; a watermark can only bias high-entropy positions"}

# ---------- main ----------

def main():
    path = sys.argv[1] if len(sys.argv) > 1 else "scratchpad/corpus"
    texts = load_corpus(path)
    tokens = tokens_of(texts)
    res = {"corpus_path": path, "n_samples": len(texts)}
    res["stylometry"] = stylometry(texts, tokens)

    # blobs for the real corpus
    blobs_h1 = blobs_for(tokens, 1)
    distinct_h1 = list(set(blobs_h1))

    # (2) empirical null on real corpus (full tokens, overdispersed)
    null_full = key_scan(blobs_h1, N_NULL_KEYS, "null-full")
    # (3) independence fix: distinct n-grams -> ~N(0,1)
    null_distinct = key_scan(distinct_h1, N_DEDUP_KEYS, "null-distinct")

    # (4) positive control
    key_star = b"TRUE-SECRET-KEY-20260811"
    pos = {}
    for delta in DELTAS:
        wm = synth_watermarked(tokens, key_star, delta)
        wm_h1 = blobs_for(wm, 1)
        wm_h2 = blobs_for(wm, 2)
        z_true, green, T = detect_z(wm_h1, key_star)               # (a) true key + correct scheme
        wrong = key_scan(wm_h1, N_WRONG_KEYS, f"wrong-d{delta}")   # (b) wrong keys + correct scheme
        z_wrongscheme, _, _ = detect_z(wm_h2, key_star)            # (c) true key + WRONG scheme (h=2)
        pos[f"delta_{delta}"] = {
            "z_true_key_correct_scheme": round(z_true, 2),
            "green_fraction": round(green/T, 4),
            "one_sided_p_true_key": one_sided_p(z_true),
            "wrong_key_scan": {"z_mean": wrong["z_mean"], "z_stdev": wrong["z_stdev"],
                                "z_max_best_false_positive": wrong["z_max"]},
            "z_true_key_WRONG_scheme_h2": round(z_wrongscheme, 2),
            "gap_true_vs_best_decoy": round(z_true - wrong["z_max"], 2),
        }

    # (5) key-space argument (empirical + theoretical, clearly separated)
    p_theory = one_sided_p(Z_ONESIDED_THRESH)
    keyspace = {
        "empirical_strongest_false_positive_z_over_%d_keys" % N_NULL_KEYS: null_full["z_max"],
        "empirical_frac_keys_over_z4": null_full["frac_over_z4_onesided"],
        "theoretical_one_sided_tail_at_z4": p_theory,
        "theoretical_caveat": ("Theoretical tail assumes i.i.d. Bernoulli, which repeated n-grams "
            "violate (see null_distinct vs null_full). Use the EMPIRICAL null for thresholds. "
            "Resolving a %.1e rate empirically would need ~1e5-1e6 keys, not %d." % (p_theory, N_NULL_KEYS)),
        "search_argument": ("A real key space is cryptographically large (e.g. 2^128). Random/brute-forced "
            "keys yield the empirical null above; the one true key is a single unsearchable point, and any "
            "threshold loose enough to admit a weak watermark also admits many false positives. Hence the "
            "scheme/key is NOT identifiable from outputs alone."),
    }

    res["key_dependence"] = {
        "detector": "word-level green-list, gamma=0.5, one-sided z, keyed blake2b",
        "null_full_tokens_real_corpus": null_full,
        "null_distinct_ngrams_real_corpus": null_distinct,
        "overdispersion_note": ("null_full z_stdev > 1 because repeated bigrams are perfectly correlated "
            "green/red clusters; scoring DISTINCT n-grams (null_distinct) restores independence and z_stdev -> ~1. "
            "This is why thresholds must come from the empirical null."),
        "positive_control": pos,
        "keyspace_argument": keyspace,
    }
    res["capacity"] = capacity(tokens)

    # (6) indistinguishability + precise conclusion (guard against overclaim)
    res["defensible_conclusion"] = (
        "From output text alone — without the secret key, the exact scheme (context width, hashing, gamma), "
        "and the model's tokenization — a Kirchenbauer-style keyed green-list watermark is NOT identifiable: "
        "random and brute-forced keys yield a null indistinguishable from unwatermarked text, and the key space "
        "is too large to search without false positives swamping any true hit. The positive control shows the "
        "signal is unmistakable WITH the key+scheme (z_true >> best decoy) and vanishes without EITHER the key "
        "(wrong-key scan) OR the scheme (h2 mismatch). This is keyless non-identifiability within ONE watermark "
        "family; it is NOT evidence that outputs are unwatermarked, and it says nothing about other families "
        "(Aaronson/Gumbel, distortion-free) invisible to this detector by construction. "
        "Absence of evidence is not evidence of absence."
    )
    print(json.dumps(res, indent=2, default=str))

if __name__ == "__main__":
    main()
