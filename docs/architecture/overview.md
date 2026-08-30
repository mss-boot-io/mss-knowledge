# Architecture Overview

## Purpose

MSS Knowledge is a self-hosted context infrastructure for AI clients and agents. It separates durable knowledge, durable control-plane state, and rebuildable real-time indexes so the system can evolve without coupling clients to one storage or model provider.

The canonical Chinese design is available in [`docs/design/complete-design.zh-CN.md`](../design/complete-design.zh-CN.md).

## System context

```text
AI clients and agents
        |
        | MCP Streamable HTTP / REST
        v
mss-knowledge-gateway
        |
        +-----------+------------+-------------+
        |           |            |             |
        v           v            v             v
   PostgreSQL     Redis      S3-compatible   IAM/OIDC
        ^           ^            |
        |           |            v
        +------ ingestion worker +
                    |
             parser adapters
                    |
            embedding provider
                    |
             optional reranker
```

## Stable boundaries

### S3-compatible object storage

Stores immutable original document versions, normalized documents, chunk manifests, extracted assets, exports, evaluation data, and backups. A document update creates a new version instead of overwriting the old object.

### PostgreSQL

Stores the durable control plane: tenants, principals, knowledge bases, ACL bindings, sources, documents, versions, ingestion jobs, profiles, durable memories, checkpoints, audit events, and transactional outbox events.

### Redis

Stores rebuildable real-time state: lexical and vector indexes, session streams, working context, long-term-memory indexes, embedding caches, semantic caches, rate limits, and short-lived locks. Loss of Redis must not destroy irreplaceable knowledge or authorization state.

### Gateway

Owns every public contract. It authenticates callers, resolves authorization, orchestrates retrieval, validates active versions, creates citations, enforces limits, emits audit records, and exposes MCP and REST endpoints.

### Worker

Executes idempotent ingestion stages: validate, parse, normalize, chunk, embed, index, verify, and publish. It also performs rebuild, compensation, cleanup, and consistency scans.

## Deployment model

The foundation release is a modular monolith in one Go repository with separate binaries:

```text
mss-knowledge-gateway
mss-knowledge-worker
mss-knowledge-ctl
mss-knowledge-mcp-proxy   # later foundation checkpoint
```

This keeps deployment simple while preserving domain and adapter boundaries. Services may be split only after measured scaling or isolation requirements justify it.

## Primary data flows

### Ingestion

```text
register/upload
  -> persist PROCESSING version
  -> store original in S3
  -> parse to KnowledgeDocument
  -> write normalized artifacts and manifest to S3
  -> create chunks and embeddings
  -> index a new index version in Redis
  -> verify counts and hashes
  -> atomically switch active_version_id in PostgreSQL
  -> asynchronously retire the old Redis index entries
```

### Search

```text
authenticate
  -> resolve allowed knowledge bases
  -> normalize query
  -> exact/lexical/vector retrieval
  -> fuse scores
  -> validate active versions and ACLs
  -> deduplicate and diversify
  -> optional rerank
  -> expand neighbors
  -> pack within token budget
  -> return versioned citations
```

### Recovery

```text
restore PostgreSQL
  -> verify S3 artifacts
  -> restore Redis snapshot or rebuild every index
  -> rebuild durable-memory indexes
  -> reopen the gateway
```

A full Redis rebuild from PostgreSQL and S3 is a mandatory release exercise.

## Foundation non-goals

The first vertical slice does not include a chat UI, a hosted LLM, automatic long-term-memory extraction, a knowledge graph, public multi-tenant SaaS billing, complex workflow orchestration, or document-driven tool execution.

## Architectural quality attributes

- **Durability:** original content and control-plane facts survive loss of the search layer.
- **Portability:** clients depend only on MCP and REST contracts.
- **Security:** retrieved documents are untrusted data and never grant authority.
- **Traceability:** every result identifies document, version, chunk, source, and content hash.
- **Operability:** every ingestion stage is observable, retryable, and recoverable.
- **Cost control:** model, parser, vector precision, and search backends are replaceable and profile-driven.
