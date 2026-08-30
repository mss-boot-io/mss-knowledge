# Search and Retrieval

## Goals

Retrieval must support semantic questions, exact technical terms, code symbols, metadata filters, version correctness, tenant isolation, and stable citations. The public contract must remain independent of the concrete search backend.

## Search modes

### `exact`

Optimized for error codes, configuration keys, command fragments, commit SHAs, filenames, symbols, and quoted phrases. Vector generation is optional and normally skipped.

### `fast`

Runs lexical and vector retrieval with score fusion and authorization filters, but no external reranker. Intended for high-frequency agent calls.

### `balanced`

Runs hybrid retrieval, retrieves a larger candidate set, applies an optional reranker, expands context, and returns a compact final set. This is the default mode.

### `deep`

Reserved for query decomposition, multiple retrieval rounds, timeline synthesis, and cross-knowledge-base investigation. It is outside the foundation milestone.

## Search store abstraction

```go
type SearchStore interface {
    IndexBatch(ctx context.Context, records []ChunkRecord) error
    DeleteVersion(ctx context.Context, versionID string) error
    Search(ctx context.Context, request StoreSearchRequest) ([]StoreHit, error)
    Check(ctx context.Context) error
}
```

The initial adapter targets Redis Query Engine. Future adapters may target Qdrant, Milvus, OpenSearch, or pgvector without changing REST or MCP schemas.

## Indexed fields

```text
tenant_id        TAG
kb_id            TAG
document_id      TAG
version_id       TAG
chunk_id         TAG
language         TAG
content_type     TAG
security_level   TAG

title            TEXT, high weight
heading_path     TEXT, high weight
keywords         TEXT
code             TEXT, exact-oriented
body             TEXT

page_start       NUMERIC
page_end         NUMERIC
updated_at       NUMERIC, sortable

embedding        VECTOR
```

The index profile owns vector algorithm, dimension, precision, distance metric, language behavior, field weights, and candidate limits.

## Request processing

```text
request validation
  -> authenticate caller
  -> resolve tenant and principal
  -> resolve allowed knowledge bases in PostgreSQL
  -> normalize query
  -> detect exact tokens and language
  -> compute or reuse query embedding
  -> build backend request with ACL filters
  -> execute lexical/vector search
  -> fuse candidate scores
  -> validate active versions against PostgreSQL
  -> validate authorization again
  -> deduplicate and diversify
  -> optional rerank
  -> expand parent and neighboring chunks
  -> pack within token and result budgets
  -> emit response and audit metadata
```

## Authorization filtering

The Gateway never lets a caller supply arbitrary tenant filters. It derives the effective tenant and allowed knowledge-base IDs from the authenticated principal.

The Redis query includes those IDs as candidate filters for efficiency. Each final result is revalidated against authoritative PostgreSQL state before exposure.

If the allowed set is too large for one query expression, the Gateway partitions the search or routes by application-level shard and merges results.

## Query normalization

Normalization may include:

- Unicode normalization;
- whitespace cleanup;
- language detection;
- preservation of quoted phrases and code tokens;
- extraction of explicit filters;
- technical dictionary aliases;
- provider-specific query instruction for embeddings.

The original query is preserved for audit and debugging. Normalization must not silently remove security-sensitive constraints.

## Hybrid scoring

The initial implementation exposes backend scores but treats them as adapter-specific values. The orchestration layer normalizes and combines:

```text
lexical score
vector similarity
optional freshness signal
optional title/heading signal
optional reranker score
```

Reciprocal Rank Fusion is the safest default for combining heterogeneous rankings. Linear weighting may be enabled only with a measured profile.

## Candidate and final limits

Example balanced defaults, subject to evaluation:

```text
lexical candidates: 50
vector candidates: 50
fused candidates: 50
rerank candidates: 20
final independent hits: 8
maximum independent hits from one document: 3
```

Neighbor expansion occurs after independent-hit selection so one long document does not consume the whole result set.

## Active-version correctness

Each hit carries `document_id` and `version_id`. The Gateway loads or caches the authoritative active-version mapping and discards any hit whose version is no longer active.

A stale Redis projection can therefore reduce recall temporarily but cannot return obsolete content as current truth.

## Context expansion

A final hit may include:

- the matched child chunk;
- its parent section summary or text;
- previous and next chunks within bounds;
- table header context;
- document title and heading path.

Expansion obeys a total token budget and does not cross document-version boundaries.

## Citations

Every result includes:

```text
chunk_id
document_id
version_id
knowledge_base_id
title
heading_path
source_uri
page_start/page_end when available
content_sha256
updated_at
```

Canonical internal URI:

```text
knowledge://{tenant_id}/{kb_id}/{document_id}/{version_id}/{chunk_id}
```

A fetch operation resolves this URI or opaque result ID and returns the exact versioned text. Signed source-file URLs are generated only when the caller has `knowledge.read` permission and explicitly requests them.

## Response shape

```json
{
  "query_id": "qry_...",
  "mode": "balanced",
  "results": [
    {
      "id": "chk_...",
      "document_id": "doc_...",
      "version_id": "ver_...",
      "title": "Architecture",
      "heading_path": ["Search", "Hybrid retrieval"],
      "text": "...",
      "source_uri": "knowledge://...",
      "page_start": 10,
      "page_end": 11,
      "scores": {
        "lexical": 0.0,
        "vector": 0.0,
        "fused": 0.0,
        "rerank": 0.0
      },
      "content_sha256": "...",
      "updated_at": "2026-08-30T00:00:00Z"
    }
  ]
}
```

Scores may be omitted when a client does not request diagnostics.

## Query embedding cache

A cached query embedding key includes the normalized query, embedding profile fingerprint, tenant policy scope, and normalization version. Cached embeddings are derived data and may use TTL plus bounded eviction.

## Reranking

Reranking is optional and provider-driven. A reranker receives only the minimum candidate text required and must obey tenant data-egress policy. Timeout or provider failure falls back to fused retrieval rather than failing the entire query unless the caller explicitly requires reranking.

## Quality evaluation

The evaluation corpus includes Chinese, English, mixed-language, code, exact identifiers, incident history, and cross-document questions.

Required metrics:

```text
Recall@5
Recall@10
MRR
nDCG
citation completeness
active-version accuracy
ACL leakage
latency by stage
```

The initial acceptance target is zero unauthorized results and 100% active-version/citation correctness. Relevance targets are established from the first real corpus rather than synthetic-only benchmarks.

## Failure behavior

- PostgreSQL authorization unavailable: fail closed.
- Redis unavailable: return service unavailable unless a future fallback store is configured.
- Embedding provider unavailable: `exact` can continue; hybrid modes may fall back to lexical when policy permits and must report degradation.
- Reranker unavailable: continue with fused results.
- Stale index entries: discard through active-version validation.
- Citation source missing from S3: suppress the hit, emit a consistency alert, and enqueue repair.
