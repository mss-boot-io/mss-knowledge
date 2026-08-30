# ADR-0002: Start as a modular monolith with ports and adapters

- Status: Accepted
- Date: 2026-08-30

## Context

The platform needs separate runtime roles for public API traffic, asynchronous ingestion, administration, and later local MCP proxying. Splitting every role into an independent repository or microservice at the beginning would increase deployment, versioning, tracing, and transaction complexity before real scaling data exists.

At the same time, directly coupling domain logic to Redis, PostgreSQL, S3, a parser, or one embedding API would make future replacement expensive.

## Decision

Use one Go repository and one module, producing separate binaries:

```text
mss-knowledge-gateway
mss-knowledge-worker
mss-knowledge-ctl
mss-knowledge-mcp-proxy  # introduced when remote MCP is implemented
```

Organize code around domain and application packages with infrastructure hidden behind interfaces. Transport adapters depend on application services; infrastructure adapters implement ports; the domain does not import concrete storage clients.

## Consequences

Positive:

- simple local and small-team deployment;
- atomic changes across API, worker, schema, and adapters;
- shared domain invariants and error types;
- independent process scaling where needed;
- storage and provider replacement remains feasible.

Costs:

- package boundaries require review discipline;
- one module can accumulate accidental coupling if imports are not controlled;
- independent release cadence for one component is deferred;
- large-scale teams may later need repository or service extraction.

## Extraction rule

A module is split into a separate service only when measured scaling, isolation, ownership, security, or deployment requirements justify the operational cost. No split is based only on conceptual purity.
