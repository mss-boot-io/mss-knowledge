# MCP Contract

## Purpose

The MCP surface is the stable compatibility boundary for ChatGPT, Grok, Codex, Claude, desktop agents, and custom applications. Clients must not need to know whether retrieval is backed by Redis, another vector store, or a future federated search implementation.

## Transport

Remote clients use MCP Streamable HTTP at:

```text
POST /mcp
GET  /mcp
```

The server validates origin where applicable, requires HTTPS and bearer authentication, and enforces request and response size limits.

Local command-line clients may use a future `mss-knowledge-mcp-proxy` process over stdio. The proxy forwards to the authenticated remote endpoint and stores credentials through the operating system's secure credential facility rather than a plaintext project file.

## Versioning policy

The public tool names and required arguments are intentionally small and stable.

- Do not rename or remove a published field in the same major contract version.
- Add behavior through optional fields with safe defaults.
- Ignore unknown optional fields only where the protocol and validation policy permit it.
- Return structured error codes in addition to human-readable messages.
- Publish schema revisions and compatibility notes.
- Keep opaque IDs opaque; clients must not parse their format.

## Foundation tools

The public v1 tool set is read-only.

### `search`

Searches one or more knowledge bases visible to the caller.

Input:

```json
{
  "query": "Why is Redis a rebuildable layer?",
  "knowledge_base_ids": ["kb_..."],
  "mode": "balanced",
  "top_k": 8,
  "filters": {
    "document_ids": [],
    "content_types": [],
    "languages": [],
    "updated_after": null,
    "updated_before": null
  },
  "include": {
    "scores": false,
    "diagnostics": false
  }
}
```

Rules:

- `query` is required and bounded in length.
- `knowledge_base_ids` narrows the caller's already-authorized set; it cannot expand access.
- `mode` is `exact`, `fast`, or `balanced` in v1.
- `top_k` is capped by server policy.
- unsupported filters fail validation rather than being silently ignored.
- diagnostics require a privileged scope.

Output:

```json
{
  "query_id": "qry_...",
  "mode": "balanced",
  "degraded": false,
  "results": [
    {
      "id": "chk_...",
      "document_id": "doc_...",
      "version_id": "ver_...",
      "knowledge_base_id": "kb_...",
      "title": "Architecture Overview",
      "heading_path": ["Stable boundaries", "Redis"],
      "text": "Redis stores rebuildable real-time state...",
      "source_uri": "knowledge://...",
      "page_start": null,
      "page_end": null,
      "content_sha256": "...",
      "updated_at": "2026-08-30T00:00:00Z",
      "scores": null
    }
  ],
  "next_cursor": null
}
```

`degraded` is true when an allowed fallback occurred, such as lexical-only search after an embedding-provider failure.

### `fetch`

Fetches a versioned search result or document fragment by opaque ID.

Input:

```json
{
  "id": "chk_...",
  "include_neighbors": true,
  "neighbor_limit": 1,
  "include_source_download": false
}
```

Output:

```json
{
  "id": "chk_...",
  "document_id": "doc_...",
  "version_id": "ver_...",
  "knowledge_base_id": "kb_...",
  "title": "Architecture Overview",
  "heading_path": ["Stable boundaries", "Redis"],
  "text": "...",
  "neighbors": [],
  "source_uri": "knowledge://...",
  "source_download_url": null,
  "page_start": null,
  "page_end": null,
  "content_sha256": "...",
  "updated_at": "2026-08-30T00:00:00Z"
}
```

The server resolves the caller's current authorization and the active document version before returning content. A stale search-result ID cannot bypass a later ACL or version change.

### `list_knowledge_bases`

Returns knowledge bases visible to the caller.

Input:

```json
{
  "cursor": null,
  "limit": 50
}
```

Output entries contain ID, name, description, revision, and caller capabilities. They do not disclose unauthorized counts or source metadata.

### `get_document_metadata`

Returns metadata for one currently authorized document without returning its full content.

Input:

```json
{
  "document_id": "doc_..."
}
```

Output includes title, active version, media type, timestamps, source kind, and available citation information.

## Deferred tools

The following tools are designed but not exposed in the public foundation release:

```text
memory_search
session_context_get
task_checkpoint_get
create_note
upload_document
update_document_metadata
memory_upsert
memory_supersede
memory_forget
task_checkpoint_put
```

Read-only memory and context tools may be enabled before write tools when their authorization and privacy tests are complete.

## Error model

Tool errors use stable codes:

```text
INVALID_ARGUMENT
UNAUTHENTICATED
PERMISSION_DENIED
NOT_FOUND
CONFLICT
RATE_LIMITED
PAYLOAD_TOO_LARGE
DEPENDENCY_UNAVAILABLE
DEGRADED_NOT_ALLOWED
INTERNAL
```

Example:

```json
{
  "code": "PERMISSION_DENIED",
  "message": "The requested resource is not available to this principal.",
  "request_id": "req_...",
  "retryable": false
}
```

To reduce resource-enumeration leakage, unauthorized fetches may use the same public response as missing resources.

## Authentication and scopes

Minimum scopes:

```text
knowledge.search  search and list visible knowledge bases
knowledge.read    fetch chunks, metadata, or source downloads
```

Future scopes:

```text
knowledge.write
knowledge.delete
memory.read
memory.write
session.read
session.write
admin.manage
audit.read
```

The Gateway maps the token subject to an internal principal. Token claims are inputs to policy evaluation, not direct database filters supplied by the client.

## Tool annotations and side effects

Foundation tools are declared read-only and idempotent. Future write tools must accurately declare side effects, destructive behavior, and retry semantics.

A tool response never instructs the server to call another tool. The AI client decides subsequent actions under its own instruction hierarchy and user confirmation policy.

## Pagination and limits

- list operations use opaque cursors;
- search `top_k` has a server maximum;
- fetch neighbor expansion has a small maximum;
- source downloads use short-lived URLs;
- tool output has a maximum text and token budget;
- large documents are fetched by chunk or bounded range, not returned whole.

## Audit behavior

Every MCP call records principal, tenant, tool, resource identifiers, outcome, request ID, trace ID, latency, and policy decision summary. Full queries and document text are not logged by default.

## Compatibility test matrix

Before a release, execute tool discovery and calls through:

```text
reference MCP client
ChatGPT custom connector/app where available
Grok custom MCP connector where available
Codex or local MCP client
Claude-compatible MCP client
mss-knowledge-mcp-proxy
```

A schema successfully registered by a cloud client is not proof that authorization, retrieval quality, citations, or writes have been verified.
