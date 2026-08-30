# ADR-0004: Keep the public MCP v1 surface read-only

- Status: Accepted
- Date: 2026-08-30

## Context

Cloud and local AI clients need a common way to discover and retrieve knowledge. Write-capable tools add materially different risks: accidental side effects, prompt-injection escalation, duplicate retries, destructive operations, wider OAuth scopes, and client-specific confirmation behavior.

The first product goal is accurate, authorized, versioned retrieval rather than remote knowledge mutation.

## Decision

The public MCP v1 release exposes read-only tools:

```text
search
fetch
list_knowledge_bases
get_document_metadata
```

The stable core is `search` plus `fetch`. Write tools remain designed but disabled until authentication, authorization, idempotency, audit, confirmation, and client compatibility tests are complete.

REST administration may gain authenticated write APIs earlier for controlled first-party use, but this does not automatically enable equivalent public MCP tools.

## Consequences

Positive:

- smaller and safer cloud-client integration surface;
- retrieval and citation quality can be validated independently;
- reduced prompt-injection blast radius;
- narrower OAuth scopes and simpler audit expectations;
- stable core schemas can be adopted by multiple clients.

Costs:

- agents cannot initially save notes, upload documents, or update memory through MCP;
- first-party administration uses REST or management UI;
- a later write release requires additional tool schemas and policy work.

## Enablement criteria for writes

A write tool may be enabled only when it has:

- a separate least-privilege scope;
- explicit side-effect metadata;
- an idempotency contract;
- bounded and validated input;
- complete audit records;
- a confirmation policy for destructive actions;
- tenant-isolation and prompt-injection tests;
- verified behavior in each supported client class.
