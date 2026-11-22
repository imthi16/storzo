# Dedupe numbers: verification against primary sources

Retrieval date: **2026-11-22**. Retriever: OpenCode (qwen4-max-preview), worktree `docs/hybrid-spec-hardening`.
Scope: the 8.2% and 49.5% figures attributed to arXiv 2505.06252 (ZipLLM) in `docs/R&D/HANDOFF.md` /
`docs/R&D/PLAN-storage-direction.md` (Gemini report), and what the Hugging Face Xet docs actually say
about chunk-level dedupe. Evidence only; no product decision is made here.

## Sources fetched

| # | Source | URL | Version / notes |
|---|--------|-----|-----------------|
| S1 | ZipLLM abstract page | https://arxiv.org/abs/2505.06252 | v3, last revised 8 Nov 2025 |
| S2 | ZipLLM full text (HTML) | https://arxiv.org/html/2505.06252v3 | arXiv:2505.06252v3 [cs.DB], 08 Nov 2025, CC BY 4.0 |
| S3 | Xet protocol specification (index) | https://huggingface.co/docs/xet/index | Version 1.1.0 |
| S4 | Xet chunk-level deduplication spec | https://huggingface.co/docs/xet/deduplication | linked from S3 |
| S5 | Xet content-defined chunking spec | https://huggingface.co/docs/xet/chunking | linked from S3 |

All five fetched successfully on the retrieval date. Quotes below are verbatim from the fetched text
(math markup flattened where the HTML renders duplicated LaTeX).

## Claim 1: "byte-level chunk dedupe saves only about 8.2% on model weights"

**Verdict: WRONG as stated (misattributed granularity and population). The number 8.2% exists, but it is
whole-file dedup over all Hugging Face model repositories, not byte-chunk dedupe.**

What the paper actually says (S2, §3.5, Table 2 "FileDedup stats of Hugging Face model repositories"):

> Total files: 5,688,779 · Duplicate files: 1,182,818 · Total size: 11.89 PB ·
> Saved size: **0.97 PB (8.2%)** · Repos with files can be deduped: 506,337 (33.2%)

> "FileDedup identifies redundancy by computing cryptographic hashes of entire model files and
> eliminating duplicates with matching fingerprints. As shown in Table 2, out of 5.6 million model
> files, approximately 1.18 million files are exact duplicates."

Dataset/conditions: **all publicly accessible Hugging Face model repositories** (characterization cutoff
March 2025), whole-file exact-hash dedup. Not a chunk-level measurement, and not restricted to "model
weights" as a byte stream.

The actual chunk-level number in the paper is different (S2, §5.3.1, Table 5, on the evaluation sample):

> "As shown in Table 5, ChunkDedup achieves the highest data reduction ratio, removing up to **14.8%**
> of total data across all models."

Table 5 row: `ChunkDedup (FastCDC) · 520,551,953 unique hashes · 14.8% reduction · 2,560 MB/s`.
On the same sample, FileDedup is **3.2%** (Table 5; §5.2.1: "eventually reducing total storage by 3.2%
on the entire dataset"), and TensorDedup alone is **8.3%** (Table 5).

Dataset/conditions for 14.8% / 3.2% / 8.3%: a **random sample of 3,048 open-source LLM repositories,
43.19 TB raw** (Table 3), LoRA-only repos excluded, families Qwen2.5/Qwen3/Mistral/Llama-3/3.1/3.2/Gemma-2/3,
run on an EC2 c6a.48xlarge with the authors' FastCDC implementation. These are sample measurements, not
hub-wide measurements.

So HANDOFF's use of 8.2% as "byte-level chunk dedupe on model weights" is wrong on two counts
(granularity: file, not chunk; population: all HF model files, not the weights sample). The corrected
HANDOFF note of 2026-11-12 already says this; my independent fetch confirms that correction.

## Claim 2: "tensor-aware dedupe plus BitX about 49.5% (ZipLLM)"

**Verdict: WRONG number. The paper reports 54% (abstract) / 54.1% (evaluation). The string "49.5" does not
appear anywhere in the v3 full text I fetched. Also, 54.1% is dedup + compression combined, not dedupe alone.**

S1/S2 abstract:

> "By synergizing deduplication and compression around LLM family clustering, ZipLLM reduces model
> storage consumption by **54%**, over 20% higher than state-of-the-art deduplication and compression
> approaches."

S2 §1:

> "Evaluation results show that ZipLLM reduces the storage size of 3,048 sampled LLMs by **54.1%**,
> 20% higher than the state-of-the-art methods."

S2 §5.2.1:

> "The synergy between model-aware deduplication and compression allows ZipLLM to further boost the
> data reduction ratio to **54.1%**."

Dataset/conditions: the same 3,048-repo / 43.19 TB sample as Claim 1. The pipeline is FileDedup +
TensorDedup + BitX XOR-delta compression + zstd (§4.4). TensorDedup **alone** is 8.3% (Table 5); the
large gain comes from BitX delta compression of fine-tuned variants against their base models.
The nearest figure to 49.5 in the paper is **48.5%**, which is a *baseline* (BitX+FastCDC,
compress-then-dedup order, §5.2.1) — not ZipLLM's result.

The Gemini report's 49.5% is therefore unsupported by the cited source. The HANDOFF note of
2026-11-12 ("ZipLLM is 54%, not 49.5%") is confirmed.

## Claim 3 (direction-level): "chunk dedupe is weak for model weights vs model-aware reduction"

**Verdict: CORRECT as a direction, with the corrected numbers.** On the paper's sample: FastCDC chunk
dedup 14.8% vs ZipLLM end-to-end 54.1% (§5.2.1, Table 5). The paper also argues CDC is LLM-oblivious,
sequential, and metadata-heavy (520.5M unique chunks for 43.19 TB; projected >12.5 TB of metadata at
17 PB scale, §5.3.1). Caveats: this is one paper's sample and implementation; it measures dedup-only
vs dedup+compression, so it is not a like-for-like "dedupe vs dedupe" comparison (TensorDedup alone is
8.3%, below FastCDC's 14.8% — the win comes from combining tensor dedup with delta compression).

## What the Hugging Face Xet docs actually say about chunk-level dedupe

Sources S3–S5. The Xet docs are a **protocol specification**, not a benchmark report:

- Chunk-level dedupe is real and central (S4): "Deduplication in Xet operates at the chunk level rather
  than the file level, providing fine-grained deduplication capabilities that can identify shared
  content even when files differ significantly." Listed use cases include "Model checkpoints that share
  common layers or parameters".
- Mechanism (S4/S5): Gearhash-based content-defined chunking, **target 64 KiB, min 8 KiB, max 128 KiB**;
  chunks hashed with a Blake3-based MerkleHash; chunks grouped into xorbs (≤64 MiB, ≤8,192 chunks);
  three dedupe tiers: local session (in-memory), cached shard metadata (local disk), and a **global
  cross-repository dedupe API** with HMAC-protected chunk hashes (eligibility: first chunk of every
  file, plus hashes where `last-8-bytes-as-LE-u64 % 1024 == 0`).
- Fragmentation is explicitly managed (S4): implementations SHOULD keep long continuous chunk runs
  together rather than dedupe every possible chunk.
- **The Xet docs make no quantitative storage-reduction claim for model weights.** No percentage
  savings appears in S3/S4/S5. The "up to a 50% reduction" figure quoted in the ZipLLM paper (§2.2)
  is attributed to an early Hugging Face blog post (paper reference [80]), not to the Xet spec docs;
  I did not fetch that blog post, so that figure is **not verified** here.

Implication for the HANDOFF's "Xet-style dedupe idea for model files": Xet-style CDC dedupe is exactly
what ZipLLM measures as FastCDC ChunkDedup-like (14.8% on their sample, dedupe only). The docs support
"this mechanism exists and is designed for ML checkpoints", but they do not promise a savings rate,
and the paper's evidence says chunk dedupe alone captures far less than model-aware dedup+compression
on LLM corpora. Note the paper's FastCDC numbers are the authors' own implementation on their sample,
not a measurement of Hugging Face's production Xet system.

## Summary table

| Figure as used in HANDOFF/Gemini | What the source says | Conditions | Verdict |
|---|---|---|---|
| 8.2% "byte-level chunk dedupe on model weights" | 8.2% = **whole-file** dedup, 0.97 PB of 11.89 PB (Table 2) | All public HF model repos, cutoff Mar 2025 | Wrong attribution; number exists for file dedup |
| Chunk dedupe on the evaluation sample | FastCDC **14.8%**; FileDedup 3.2%; TensorDedup 8.3% (Table 5) | 3,048 sampled LLM repos, 43.19 TB | Correct figures to use |
| 49.5% "tensor dedupe + BitX" | ZipLLM **54% / 54.1%**; no "49.5" in v3; nearest is 48.5% (a baseline) | Same 3,048-repo sample; dedup **+ compression** | Wrong number |
| "Chunk dedupe weak for models" (direction) | 14.8% vs 54.1% end-to-end | Same sample; not like-for-like dedupe-only | Direction holds |
| Xet docs promise big dedupe savings | Docs describe CDC chunk dedupe (64 KiB target, global CAS dedupe); **no % claim** | Protocol spec v1.1.0 | No savings rate documented |
| ~20 weeks vs 9–12 months effort estimates | Not checked — Gemini report's own estimates, no primary source | — | **Not verified** (out of scope of this check) |

## Not verified

- The Hugging Face blog post behind the paper's "up to 50%" reference ([80]) — not fetched.
- The Gemini effort estimates (20 weeks vs 9–12 months) — no primary source exists to check.
- Any measurement of Hugging Face's production Xet savings — the paper measures its own FastCDC
  implementation, not Xet production.
