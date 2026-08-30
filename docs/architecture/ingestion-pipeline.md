# Ingestion Pipeline

## Objectives

The ingestion pipeline converts an immutable source object into a published, versioned, searchable document. It must be deterministic enough to reproduce, idempotent under retries, resumable after process loss, and safe against untrusted files.

## State machine

```text
RECEIVED
   |
   v
STORED
   |
   v
VALIDATING -----> QUARANTINED
   |
   v
PARSING
   |
   v
NORMALIZING
   |
   v
CHUNKING
   |
   v
EMBEDDING
   |
   v
INDEXING
   |
   v
VERIFYING
   |
   v
PUBLISHING
   |
   v
READY
```

Transient failures enter `RETRY_WAIT`; terminal failures enter `FAILED`; an operator or caller may move eligible work to `CANCELLED`.

## Job acquisition

The foundation implementation stores jobs in PostgreSQL and uses leasing rather than relying on an in-memory queue.

A worker claims work with a transaction equivalent to:

```sql
SELECT id
FROM ingestion_jobs
WHERE state IN ('PENDING', 'RETRY_WAIT')
  AND next_attempt_at <= now()
ORDER BY priority DESC, created_at ASC
FOR UPDATE SKIP LOCKED
LIMIT 1;
```

The worker sets `lease_owner` and `lease_expires_at`. A lost worker does not permanently own a job; another worker may reclaim an expired lease.

The transactional outbox may later drive RabbitMQ or another broker without changing job truth.

## Pipeline fingerprint

A pipeline fingerprint prevents unnecessary duplicate work and makes outputs reproducible. It includes:

```text
tenant ID
source bucket, key, and object version
content SHA-256
parser profile fingerprint
normalizer schema version
chunker profile fingerprint
embedding profile fingerprint
index profile fingerprint
```

Changing any relevant processing behavior creates a new fingerprint and therefore a new version or reindex job.

## Stages

### 1. Receive and store

- Create or resolve the logical document.
- Allocate a new document version.
- Store the original object under an immutable version path.
- Record media type, size, source metadata, object version, and SHA-256.
- Create the ingestion job only after the durable metadata transaction succeeds.

### 2. Validate

Validation includes:

- maximum object size;
- extension, declared MIME, and detected MIME consistency;
- archive expansion limits;
- optional malware scan;
- parser allow-list;
- encrypted or password-protected document policy;
- source authorization and tenant quota;
- duplicate content policy.

A suspicious object is quarantined rather than silently discarded.

### 3. Parse

The worker selects a parser through the parser registry. The parser receives a local or streamed object and returns parser-specific output plus diagnostics.

External parsers run with:

- no public listener;
- egress restrictions;
- resource and timeout limits;
- bounded temporary storage;
- no inherited cloud credentials;
- request IDs and trace context.

### 4. Normalize

Parser output is converted to the internal versioned `KnowledgeDocument` schema. Normalization assigns stable block IDs, heading paths, page references, language hints, source spans, and extracted asset references.

The normalized JSON and optional Markdown are uploaded to S3 before downstream stages start.

### 5. Chunk

Chunking is content-aware and profile-driven. The worker emits:

- child chunks for retrieval;
- optional parent sections for context expansion;
- token counts;
- page and heading references;
- content hashes;
- adjacency relationships;
- chunk manifest statistics.

The compressed manifest is written to S3 and its digest stored in PostgreSQL.

### 6. Embed

The embedding adapter batches texts within provider limits. Each response is validated for:

- vector count;
- configured dimension;
- finite numeric values;
- normalization requirements;
- model fingerprint consistency.

Document embeddings may be cached by `(content_sha256, embedding_profile_fingerprint)` when tenant policy permits.

### 7. Index

The target is a non-active index version. Index writes are batched and idempotent by chunk key. Each record includes tenant, knowledge-base, document, version, security, text, structured metadata, and vector fields.

### 8. Verify

Before publication, verify:

- expected and indexed chunk counts match;
- sampled records have the expected content and vector hashes;
- the index schema matches the profile;
- every Redis record references the candidate document version;
- the source, normalized object, and manifest still exist;
- no stage output fingerprint changed unexpectedly.

### 9. Publish

Within one PostgreSQL transaction:

1. mark the candidate version `READY`;
2. set `documents.active_version_id`;
3. mark the prior active version `SUPERSEDED` when present;
4. increment the knowledge-base revision;
5. write audit and outbox events.

Only after commit is the new version visible to retrieval.

### 10. Retire old projections

Old Redis entries are deleted asynchronously. Retrieval validates active versions, so delayed cleanup cannot expose obsolete content.

Retention policy determines when old S3 artifacts and database directory rows can be removed.

## Idempotency rules

- Every stage writes output to deterministic version-scoped keys.
- Repeating a completed stage with the same input fingerprint is a no-op or a verified overwrite of identical derived output.
- A stage transition uses compare-and-set semantics on job state and lease owner.
- External provider calls carry an internal request ID; responses are checked before persistence.
- Publication is the only visibility switch and is transactional.

## Retry classification

Retryable examples:

```text
network timeout
provider rate limit
transient S3 failure
Redis connection loss
worker shutdown
parser capacity exhaustion
```

Terminal or operator-action examples:

```text
unsupported format
corrupt document
password-protected input when disallowed
malware finding
invalid embedding dimension
quota violation
schema incompatibility
```

Backoff uses bounded exponential delay with jitter. The job records structured error codes rather than relying only on message text.

## Rebuild flows

### Reindex one document

Read the current normalized artifact and manifest from S3, regenerate embeddings only when the profile changed, write a new index version, verify, and switch the index profile reference.

### Rebuild one knowledge base

Enumerate active document versions from PostgreSQL, read manifests from S3, rebuild into a new Redis index namespace, run evaluation smoke tests, then atomically switch the knowledge base to the new index version.

### Full Redis recovery

Enumerate every active version and durable memory from PostgreSQL, reconstruct all projections from S3 and database records, verify counts and sampled hashes, then reopen search traffic.

## Consistency scanner

A scheduled scanner compares:

- active versions in PostgreSQL;
- required objects in S3;
- expected chunk counts and namespaces in Redis;
- stale PROCESSING versions and expired leases;
- orphaned objects and projections.

It emits repair jobs rather than mutating multiple systems inline.

## Observability

Each stage emits duration, input/output counts, bytes, token counts, provider requests, retry reason, and resource consumption. Logs include `trace_id`, `job_id`, `tenant_id`, `kb_id`, `document_id`, and `version_id` without exposing raw sensitive content by default.
