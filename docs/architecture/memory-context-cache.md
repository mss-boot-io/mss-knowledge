# Memory, Context, and Cache

## Domain separation

Knowledge, memory, context, and cache are related but are not interchangeable.

| Domain | Example | Durability | Authority |
|---|---|---:|---|
| Knowledge | architecture document, PDF, source file | long-lived | versioned document source |
| Long-term memory | a project decision or user preference | durable until superseded/forgotten | PostgreSQL record with provenance |
| Session context | recent messages and tool calls | time-bounded | session event stream |
| Working context | current task plan, branch, checkpoint | short-lived with durable checkpoints | Redis plus selected PostgreSQL checkpoints |
| Cache | query embedding or reusable answer | disposable | never authoritative |

The system must never promote an LLM inference into durable memory without an explicit policy and provenance.

## Session memory

Session memory captures high-frequency events for a bounded period:

```text
user_message
assistant_message
tool_request
tool_result
summary
context_marker
error
```

Recommended Redis Stream key:

```text
mk:session:{tenant_id}:{session_id}:events
```

Properties:

- monotonically ordered stream IDs;
- tenant and session ownership checks at the Gateway;
- configurable TTL and maximum length;
- message bodies encrypted or minimized according to tenant policy;
- optional durable summary written to PostgreSQL or S3 before expiry.

The foundation phase defines interfaces but does not expose session write tools to public MCP clients.

## Working and task context

Working context supports agents that continue a task across machines or runtimes.

Example state:

```json
{
  "task_id": "tsk_...",
  "project_key": "mss-knowledge",
  "phase": "foundation",
  "branch": "codex/mss-knowledge-foundation",
  "base_sha": "...",
  "last_remote_commit": "...",
  "completed": ["architecture docs"],
  "next": ["gateway skeleton"],
  "locks": [],
  "updated_at": "2026-08-30T00:00:00Z"
}
```

Redis stores the current state for low-latency access. Important milestones are appended to `task_checkpoints` in PostgreSQL. A checkpoint is immutable; a newer checkpoint advances the sequence rather than replacing history.

Distributed locks are leases with owner, purpose, fencing token, and expiry. A lock alone never proves that a side effect is safe; storage writes remain idempotent.

## Long-term memory

Durable memory stores compact, high-value facts that should be retrieved independently from full documents.

Memory types:

```text
fact
preference
decision
constraint
procedure
incident
episode
project_state
```

Required metadata:

```text
memory ID
tenant and scope
subject and content
type
importance and confidence
sensitivity
source type and source reference
validity interval
creator
creation time
status
supersedes relationship
```

### Scope

A memory can be scoped to:

```text
tenant
principal
project
knowledge base
agent
session lineage
```

The Gateway derives effective readable scopes from identity and policy. Callers cannot expand their scope by supplying arbitrary identifiers.

### Provenance

A durable memory must identify where it came from:

- explicit user statement;
- approved extraction from a versioned document;
- task checkpoint;
- operator-created record;
- imported legacy memory.

Model-generated summaries include the model/profile fingerprint and the source event IDs used to create them.

### Conflict and supersession

A newer statement does not erase history. It creates a new memory with `supersedes_id`, while the previous memory becomes `superseded`.

Conflicting memories without sufficient authority may be marked `disputed` and excluded from default retrieval until resolved.

### Forgetting

`forget` is an explicit state transition plus projection deletion. Depending on compliance policy, the durable row may be tombstoned rather than physically removed immediately. Search indexes and caches must be invalidated promptly.

## Memory retrieval

Durable memory is projected into Redis for lexical and vector search. Retrieval filters on tenant, scope, type, status, sensitivity, and validity interval.

Memory ranking may combine:

```text
semantic relevance
exact subject match
importance
confidence
recency
scope specificity
```

The response distinguishes memory from document evidence and exposes provenance so an agent can decide how much to trust it.

## Automatic memory extraction

Automatic extraction is deliberately outside the first release. A later pipeline produces memory candidates rather than active memories:

```text
session/document events
  -> candidate extraction
  -> sensitive-data filter
  -> duplicate/conflict detection
  -> policy or human approval
  -> durable memory
  -> Redis projection
```

Candidate extraction must not silently store secrets, temporary statements, speculative model output, or instructions embedded in retrieved documents.

## Query embedding cache

Query embeddings are safe derived data when policy allows storage. The cache identity includes:

```text
normalized query hash
normalization version
embedding profile fingerprint
tenant policy scope
language/instruction mode
```

A profile change automatically yields a new namespace. Entries use TTL and bounded eviction.

## Retrieval-result cache

A retrieval-result cache may store hit IDs and scores, not unbounded source text. Its identity includes:

```text
tenant
principal authorization fingerprint
normalized query
search profile
knowledge-base revision set
result and token budgets
```

A knowledge-base revision change invalidates previous results by namespace rather than requiring a global delete scan.

## Semantic response cache

Semantic response caching is a later optional capability. A candidate can be reused only when all compatibility attributes match:

```text
tenant and authorization scope
model and version
system prompt hash
tool schema hash
knowledge revision hash
response policy
temperature class
semantic similarity threshold
```

Do not cache by default:

- time-sensitive or current-state queries;
- personal or highly sensitive content;
- financial, medical, legal, or other high-risk conclusions;
- write-operation responses;
- permission decisions;
- low-confidence outputs;
- responses with unresolved tool side effects.

Cached output is never treated as authoritative data; citations are revalidated before delivery.

## Redis topology

Foundation deployment may use one instance with `noeviction`, but the target topology separates durable projections from expendable caches:

```text
redis-context
  knowledge index
  memory index
  session streams
  task state

redis-cache
  query embeddings
  retrieval cache
  semantic cache
  rate limits
  short-lived locks
```

This prevents a cache workload from evicting search or memory projections.

## Privacy and retention

Retention is configured per tenant and domain:

- session events: short and bounded;
- task state: until completion plus grace period;
- durable memories: until superseded, expired, or forgotten;
- caches: TTL only;
- audit records: separate compliance retention.

Sensitive payload logging is disabled by default. Data export and forgetting operations produce audit events without copying the forgotten content into audit metadata.
