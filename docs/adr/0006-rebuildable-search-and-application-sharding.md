# ADR-0006: Treat search as rebuildable and scale through application-level shards

- Status: Accepted
- Date: 2026-08-30

## Context

Redis is an effective initial hybrid-search and context engine for the expected personal and small-team corpus. However, the architecture must not assume that one Redis instance or one distribution mode will satisfy every future scale. Search indexes are derived from durable document and metadata sources.

## Decision

1. Redis search projections are always rebuildable from PostgreSQL and S3.
2. The first deployment scales vertically and may add replica/failover topology.
3. When one search instance is insufficient, knowledge bases are assigned to application-level search shards through control-plane metadata.
4. The Gateway routes or fans out queries, merges shard rankings, revalidates authorization and active versions, and applies the final reranker.
5. Search remains behind a backend-neutral interface so another engine can be introduced without changing MCP or REST contracts.

## Consequences

Positive:

- no dependency on a specific distributed-search mode;
- predictable tenant and knowledge-base placement;
- gradual scaling without immediate platform complexity;
- migration to another backend remains possible;
- recovery procedures use durable truth rather than replicas alone.

Costs:

- the Gateway needs shard routing and cross-shard ranking logic later;
- capacity planning and rebalance operations become application concerns;
- global search latency depends on the slowest participating shard;
- consistent evaluation is required when different backend/profile versions coexist.

## Required controls

- `knowledge_bases.search_shard_id` or equivalent versioned placement metadata;
- per-shard health and capacity metrics;
- bounded fan-out;
- deterministic rank fusion;
- blue/green shard migration;
- full and scoped rebuild commands;
- no backend-specific identifiers in public citations.
