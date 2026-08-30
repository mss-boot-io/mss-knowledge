# Data Model

## Design principles

1. Every durable entity has a stable identifier independent of storage keys.
2. Document content is immutable per version.
3. PostgreSQL records business truth; Redis records rebuildable projections.
4. Deletion is initially logical and auditable.
5. Profiles are versioned so parser, chunker, embedding, and index changes are reproducible.
6. Every query result can be traced to a document version and content hash.

## Identifier format

Identifiers are opaque strings with a type prefix and sortable random payload. The initial implementation may use UUIDv7 while keeping the public representation independent of the library.

```text
ten_<id>  tenant
prn_<id>  principal
kb_<id>   knowledge base
src_<id>  source
doc_<id>  document
ver_<id>  document version
chk_<id>  chunk
job_<id>  ingestion job
mem_<id>  durable memory
ses_<id>  session
tsk_<id>  task/checkpoint
qry_<id>  query
```

## Core relationships

```text
Tenant
  +-- Principal
  +-- KnowledgeBase
        +-- ACLBinding
        +-- Source
        +-- Document
              +-- DocumentVersion
                    +-- ChunkDirectoryEntry
                    +-- IngestionJob
```

## Core tables

### `tenants`

```text
id, slug, name, status, settings_json,
created_at, updated_at, deleted_at
```

### `principals`

Represents a user, agent, service account, or integration identity.

```text
id, tenant_id, kind, external_subject, display_name,
status, attributes_json, created_at, updated_at, deleted_at
```

Unique constraint:

```text
(tenant_id, external_subject)
```

### `knowledge_bases`

```text
id, tenant_id, slug, name, description, status,
revision, default_language, search_shard_id,
created_by, created_at, updated_at, deleted_at
```

`revision` increments after any published content change that should invalidate retrieval or semantic caches.

### `kb_acl_bindings`

```text
id, tenant_id, kb_id, principal_id, role,
permissions_json, created_by, created_at, revoked_at
```

The foundation release authorizes primarily at knowledge-base scope. Document-level exceptions are deferred until a proven requirement exists.

### `sources`

```text
id, tenant_id, kb_id, kind, name, status,
configuration_ciphertext, cursor_json,
sync_policy_json, created_by, created_at, updated_at, deleted_at
```

Kinds initially include:

```text
upload, s3_prefix, web, git
```

Only `upload` is required in the first vertical slice.

### `documents`

Logical document identity across versions.

```text
id, tenant_id, kb_id, source_id, external_key,
title, status, active_version_id,
created_by, created_at, updated_at, deleted_at
```

Unique candidate:

```text
(tenant_id, source_id, external_key)
```

### `document_versions`

```text
id, tenant_id, kb_id, document_id, version_number,
status, source_uri, object_bucket, object_key, object_version_id,
filename, media_type, size_bytes, content_sha256,
parser_profile_id, chunker_profile_id,
embedding_profile_id, index_profile_id,
pipeline_fingerprint, normalized_object_key,
manifest_object_key, chunk_count, token_count,
error_code, error_detail_json,
created_by, created_at, published_at, superseded_at, deleted_at
```

Status:

```text
PROCESSING, READY, FAILED, QUARANTINED,
SUPERSEDED, DELETED
```

Important constraints:

- `(document_id, version_number)` is unique.
- `(tenant_id, pipeline_fingerprint)` may be used for deduplication with policy-aware exceptions.
- `documents.active_version_id` references a `READY` version of the same document.

### `chunks`

This table is a durable directory, not the primary text or vector store.

```text
id, tenant_id, kb_id, document_id, version_id,
parent_chunk_id, ordinal, content_type,
heading_path_json, page_start, page_end,
content_sha256, text_object_key, token_count,
created_at, deleted_at
```

Chunk text may be embedded in the S3 manifest or stored in separate compressed objects depending on measured access patterns. PostgreSQL retains enough metadata to verify and rebuild Redis.

### `ingestion_jobs`

```text
id, tenant_id, kb_id, document_id, version_id,
kind, state, current_stage, priority, attempt,
max_attempts, lease_owner, lease_expires_at,
input_fingerprint, stage_data_json,
error_code, error_message,
created_at, started_at, next_attempt_at,
completed_at, updated_at
```

Kinds:

```text
ingest, reindex, rebuild, delete, reconcile
```

### `ingestion_stage_runs`

Records detailed stage attempts.

```text
id, job_id, stage, attempt, status,
input_fingerprint, output_fingerprint,
metrics_json, error_code, error_message,
started_at, completed_at
```

### Profile tables

```text
parser_profiles
chunker_profiles
embedding_profiles
index_profiles
reranker_profiles
```

Each profile has:

```text
id, tenant_id nullable, name, version, status,
provider, configuration_json, fingerprint,
created_at, retired_at
```

Secrets are referenced through a secret provider; they are not stored in plaintext configuration JSON.

## Memory and context tables

### `memories`

```text
id, tenant_id, scope_type, scope_id, memory_type,
subject, content, importance, confidence, sensitivity,
source_type, source_reference_json,
valid_from, valid_to, expires_at,
supersedes_id, status,
created_by, created_at, updated_at, deleted_at
```

Status:

```text
active, superseded, forgotten, expired, disputed
```

Memory types:

```text
fact, preference, decision, constraint, procedure,
incident, episode, project_state
```

### `sessions`

```text
id, tenant_id, principal_id, client_id,
status, title, metadata_json,
created_at, updated_at, expires_at, closed_at
```

High-volume session events live in Redis Streams during their retention period. Durable summaries or audit-relevant events may be copied to PostgreSQL or S3.

### `task_checkpoints`

```text
id, tenant_id, task_id, principal_id, project_key,
sequence, state_json, source_reference_json,
created_at, expires_at
```

## Audit and reliable events

### `audit_events`

Append-only logical record:

```text
id, tenant_id, principal_id, action, resource_type,
resource_id, outcome, request_id, trace_id,
source_ip_hash, user_agent_hash,
metadata_json, created_at
```

Sensitive request bodies are not stored by default.

### `outbox_events`

```text
id, aggregate_type, aggregate_id, event_type,
payload_json, occurred_at, published_at,
attempt, last_error
```

The worker or relay publishes these events only after the surrounding PostgreSQL transaction commits.

## Redis projections

### Knowledge chunk key

```text
mk:chunk:{tenant_id}:{index_version}:{chunk_id}
```

Fields include IDs, active-filter metadata, title, headings, body, code, language, timestamps, content hash, and embedding.

### Durable memory projection

```text
mk:memory:{tenant_id}:{memory_id}
```

Contains searchable memory text and metadata; PostgreSQL remains authoritative.

### Session stream

```text
mk:session:{tenant_id}:{session_id}:events
```

### Task state

```text
mk:task:{tenant_id}:{task_id}:state
```

### Cache keys

All caches are namespaced by tenant and a complete compatibility fingerprint.

## Version publication invariant

A new document version becomes visible only when all conditions hold:

1. the original and normalized artifacts exist in S3;
2. the manifest hash matches PostgreSQL;
3. the expected chunk count exists in the target Redis index version;
4. the version status can transition to `READY`;
5. `documents.active_version_id` is atomically updated in the same PostgreSQL transaction;
6. the knowledge-base revision is incremented.

Redis search results that reference any other version are discarded before returning to a caller.
