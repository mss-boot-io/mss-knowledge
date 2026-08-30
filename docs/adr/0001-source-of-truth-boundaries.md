# ADR-0001: Separate document, control-plane, and real-time sources of truth

- Status: Accepted
- Date: 2026-08-30

## Context

A knowledge platform must survive search-index loss, storage upgrades, embedding-model changes, authorization changes, and partial ingestion failures. Storing every concern only in Redis would make document versions, permissions, and durable memories dependent on a rebuild-unfriendly in-memory projection. Storing large immutable files in PostgreSQL would increase cost and operational coupling.

## Decision

Use three explicit persistence boundaries:

- S3-compatible object storage is authoritative for original and normalized document content and derived manifests.
- PostgreSQL is authoritative for metadata, permissions, versions, jobs, durable memories, checkpoints, audit records, and publication state.
- Redis contains rebuildable lexical/vector indexes, real-time context, projected durable memories, and disposable caches.

The Gateway is the public contract boundary and revalidates authorization and active document versions against PostgreSQL.

## Consequences

Positive:

- Redis can be cleared, upgraded, or replaced without losing irreplaceable knowledge.
- immutable S3 versions preserve provenance and enable deterministic rebuilds;
- PostgreSQL transactions provide a reliable publication switch and audit trail;
- backend adapters can evolve without changing MCP clients.

Costs:

- ingestion writes coordinated artifacts to multiple systems;
- consistency scanning and repair jobs are required;
- PostgreSQL and S3 must be backed up and restored together with clear recovery points;
- the system needs explicit publication and rebuild procedures.

## Required invariant

A release is not considered operationally verified until a test environment has cleared Redis and rebuilt all active knowledge and durable-memory projections solely from PostgreSQL and S3.
