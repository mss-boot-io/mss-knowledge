# ADR-0003: Use Redis Query Engine for the core knowledge index

- Status: Accepted for the initial adapter
- Date: 2026-08-30

## Context

The knowledge index must support semantic similarity, exact technical terms, code symbols, full-text search, field weighting, tenant and knowledge-base filters, document metadata, time filters, and score fusion.

A simple native vector set is attractive for pure nearest-neighbor lookup but does not by itself satisfy the required hybrid and structured retrieval contract.

## Decision

The initial `SearchStore` adapter uses Redis Query Engine over structured chunk records with lexical fields, tags, numeric metadata, and a vector field.

Retrieval supports:

- lexical and exact-oriented search;
- vector nearest-neighbor search;
- tenant, knowledge-base, document, language, content-type, and security filters;
- hybrid rank fusion;
- version-aware result metadata.

Vector Sets may be used later for bounded use cases such as recommendation candidates, experiments, or simple semantic caches, but not as the foundation knowledge index.

## Consequences

Positive:

- one projection supports exact, full-text, vector, and filtered retrieval;
- Redis can also host session context and memory projections;
- the initial operational footprint remains small;
- technical queries are not forced through semantic similarity only.

Costs:

- Redis memory usage must be measured and controlled;
- index schema and language behavior require profile versioning;
- large-scale distribution may require application-level sharding or a future backend;
- Redis-specific query behavior remains inside the adapter and needs integration tests.

## Portability requirement

The public search contract and application service use a backend-neutral `SearchStore` interface. No REST or MCP schema exposes Redis query syntax, index names, or raw Redis scores as stable semantics.
