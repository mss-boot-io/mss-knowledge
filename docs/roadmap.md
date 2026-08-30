# Implementation Roadmap

## Delivery principles

- Commit and push every coherent checkpoint before verification or the next work category.
- Build a vertical slice before expanding feature breadth.
- Keep durable truth outside Redis and prove rebuildability.
- Keep MCP and REST schemas stable while adapters evolve.
- Measure retrieval quality on real project knowledge before selecting final embedding and index profiles.
- Label designed, written, pushed, and verified states separately.

## Phase 0 — Foundation and technical validation

### Objectives

Establish repository governance, executable service skeletons, durable schemas, local infrastructure, and evidence for key technology choices.

### Deliverables

- architecture documents and ADRs;
- Go module and separate `gateway`, `worker`, and `ctl` binaries;
- configuration loading and validation;
- structured logging and graceful shutdown baseline;
- health, readiness, and version endpoints;
- domain types for documents, versions, jobs, search, and memory;
- ports for metadata, object, search, parser, embedding, reranker, audit, and authorization adapters;
- PostgreSQL initial migration;
- OpenAPI foundation contract;
- Dockerfiles and local Compose baseline;
- CI for format, vet, tests, race tests, and build;
- integration-spike harness for Redis hybrid search;
- parser and embedding spike interfaces;
- initial evaluation-question format.

### Technical experiments

Use real project documents to compare:

```text
heading-aware versus fixed chunking
candidate vector dimensions and precision
lexical-only versus vector-only versus hybrid
with and without reranking
code/exact-term handling
Chinese technical dictionary behavior
Redis memory per chunk
parser CPU, memory, and temporary-disk usage
```

### Exit criteria

- repository checks pass in CI;
- services start and report health/readiness honestly;
- configuration rejects invalid or insecure values;
- migrations apply and roll back in a disposable database;
- a small real corpus can be parsed, chunked, embedded, indexed, searched, and cited in a technical spike;
- index/profile choices are recorded from measurements rather than assumptions.

## Phase 1 — Read-only knowledge vertical slice

### Objectives

Deliver the smallest complete workflow that a cloud or local AI client can use safely.

### Capabilities

- knowledge-base CRUD;
- upload registration and presigned upload flow;
- immutable document versions;
- durable ingestion jobs with leases and retries;
- native text/Markdown/code parsing plus external document parser adapter;
- normalized `KnowledgeDocument` artifacts in S3;
- structure-aware chunks and manifests;
- embedding provider adapter and batching;
- Redis lexical/vector/hybrid index adapter;
- active-version publication transaction;
- REST `search` and `fetch`;
- MCP `search`, `fetch`, `list_knowledge_bases`, and metadata tools;
- OAuth/OIDC authentication;
- knowledge-base ACLs and result revalidation;
- stable citations;
- audit events, metrics, tracing, and structured errors;
- per-document, per-knowledge-base, and full Redis rebuild commands;
- minimal administration pages in mss-boot-admin or an API-first interim interface.

### Exit criteria

```text
upload/register
  -> parse
  -> chunk
  -> embed
  -> index
  -> publish
  -> search from REST and MCP
  -> fetch the exact versioned citation
```

Additional gates:

- unauthorized retrieval result count is zero in the two-tenant test suite;
- active-version and citation correctness is 100%;
- a forced worker interruption resumes without duplicate active versions;
- clearing Redis and rebuilding from PostgreSQL + S3 succeeds;
- ChatGPT/Grok/local MCP compatibility is tested where account capabilities permit it;
- successful push is not used as a substitute for these checks.

## Phase 2 — Sources and operations

### Capabilities

- S3 prefix source and periodic reconciliation;
- webpage import with SSRF protections;
- Git repository source with branch/commit/path metadata;
- incremental source updates;
- source cursors and tombstone handling;
- archive and delayed physical deletion;
- blue/green index-profile rebuild;
- consistency scanner and repair jobs;
- backup and restore automation;
- Search Playground with stage-level diagnostics;
- document detail view with normalized blocks, chunks, versions, and errors;
- operational dashboards and alerts.

### Exit criteria

- source sync is idempotent across repeated runs;
- deletes and renames do not leave visible stale content;
- blue/green rebuild has no mixed-version window;
- restore exercise meets the documented recovery procedure;
- operators can explain why a result ranked and trace it to exact source content.

## Phase 3 — Agent memory and task context

### Capabilities

- Redis session streams with bounded retention;
- working task state and leased locks;
- immutable PostgreSQL task checkpoints;
- durable long-term memory records and Redis projections;
- memory scopes, provenance, sensitivity, validity, supersession, dispute, expiry, and forgetting;
- read-only MCP memory and context tools;
- explicitly authorized write APIs and MCP tools;
- cross-device task continuation.

### Exit criteria

- task state survives an agent or machine handoff through a durable checkpoint;
- a superseding decision does not erase history and default retrieval returns only the active decision;
- forget removes searchable projections and invalidates affected caches;
- memory writes cannot be triggered merely by instructions embedded in a document;
- cross-tenant and cross-scope memory leakage is zero.

## Phase 4 — Automatic memory candidates and semantic cache

### Capabilities

- candidate extraction from approved session and document sources;
- sensitive-data detection;
- duplicate and conflict detection;
- configurable human or policy approval;
- semantic response cache with complete compatibility fingerprint;
- knowledge-revision invalidation;
- cache policy by data class and risk;
- cost and latency measurement.

### Exit criteria

- no candidate becomes active durable memory without configured approval;
- conflicting memories are surfaced rather than silently overwritten;
- time-sensitive and protected queries bypass semantic cache by policy;
- cached citations are revalidated;
- savings are measured against a real workload, not claimed from vendor benchmarks.

## Phase 5 — Scale and platform integration

### Capabilities

- application-level search sharding by knowledge base;
- multi-worker scheduling and capacity controls;
- Redis high availability and split context/cache topology;
- Helm deployment;
- tenant quotas and usage metering;
- multiple search-store adapters;
- regional object storage and disaster recovery;
- mss-boot-admin productized AI Knowledge module;
- reusable SDKs for Go and selected clients.

### Exit criteria

- shard routing and cross-shard merge preserve ranking and authorization;
- one tenant cannot exhaust shared parser, embedding, Redis, or database resources;
- backend replacement does not change MCP contracts;
- regional recovery and data-residency policies are documented and tested.

## Foundation work breakdown

### Track A — Repository and quality

```text
A01 governance and checkpoint policy
A02 Go module and build layout
A03 CI and static checks
A04 container build
A05 release/version metadata
```

### Track B — Domain and control plane

```text
B01 identifiers and common errors
B02 document/version state machine
B03 ingestion job state machine
B04 search request/result model
B05 memory/context model
B06 PostgreSQL migration
B07 repository interfaces
```

### Track C — Gateway

```text
C01 lifecycle and graceful shutdown
C02 health/readiness/version
C03 REST error envelope
C04 search/fetch interfaces
C05 OpenAPI contract
C06 authentication and authorization ports
C07 MCP server adapter
```

### Track D — Worker

```text
D01 job leasing loop
D02 stage executor
D03 parser registry
D04 chunker contract
D05 embedding batch contract
D06 publisher contract
D07 retry classification
```

### Track E — Infrastructure adapters

```text
E01 PostgreSQL adapter
E02 S3 adapter
E03 Redis search adapter
E04 native parser adapters
E05 external parser adapter
E06 embedding provider adapter
E07 observability adapters
```

### Track F — Evaluation and security

```text
F01 two-tenant isolation fixtures
F02 golden question schema
F03 citation tests
F04 active-version tests
F05 prompt-injection boundary tests
F06 rebuild exercise
```

## Initial milestone sequence

1. Persist design, ADRs, repository rules, and roadmap.
2. Push a dependency-light Go skeleton that compiles with the standard library.
3. Verify locally and repair through pushed commits.
4. Add PostgreSQL migration and storage ports.
5. Add OpenAPI and a deterministic in-memory vertical slice for API contract tests.
6. Add CI and container baseline.
7. Add real PostgreSQL/S3/Redis adapters behind feature-complete interfaces.
8. Run the first end-to-end corpus spike and record measured profile decisions.

## Status vocabulary

Every issue, pull request, or progress report uses:

```text
Designed
Written
Committed and pushed
Locally verified
CI verified
Integration verified
Client compatibility verified
Not yet verified
```

These labels prevent remote persistence, compilation, integration, and product validation from being conflated.
