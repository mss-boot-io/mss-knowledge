# Security Model

## Security objectives

MSS Knowledge stores private documents and exposes them to powerful AI clients. The security model must prevent cross-tenant disclosure, unauthorized document access, credential leakage, prompt-injection privilege escalation, parser abuse, and untraceable writes.

The default posture is deny-by-default and fail-closed for authentication and authorization.

## Trust boundaries

```text
Untrusted internet clients
        |
        v
Edge proxy / WAF
        |
        v
Gateway trust boundary
        |
        +---------- private service network ----------+
        |            |             |                 |
   PostgreSQL      Redis          Worker            Parser
                                      |
                                external providers
```

Untrusted inputs include uploaded files, parsed text, URLs, repository content, MCP arguments, model-generated text, webhook payloads, and metadata from external sources.

## Authentication

The Gateway acts as an OAuth/OIDC resource server. It validates:

- issuer;
- signature and allowed algorithms;
- audience/resource indicator;
- expiry and not-before time;
- token type;
- subject;
- tenant mapping;
- required scopes;
- revocation or session policy when supported.

The system does not implement password storage in the foundation release. Human and service authentication is delegated to a standards-compliant IAM.

Remote MCP and REST traffic requires HTTPS. Browser or public clients use authorization code with PKCE. Service accounts use an approved non-interactive flow and narrowly scoped credentials.

## Authorization

Authorization decisions use authoritative PostgreSQL records. The initial boundary is knowledge-base scope.

Processing order:

1. validate token;
2. resolve the internal principal and tenant;
3. load active ACL bindings and role permissions;
4. derive allowed knowledge bases and operations;
5. apply candidate filters in Redis;
6. revalidate every final result before returning it.

A caller-provided `tenant_id`, `principal_id`, or ACL filter is never trusted as an authorization grant.

## MCP tool safety

The v1 public MCP surface is read-only. Tool metadata and server policy distinguish read-only operations from future side effects.

Future write tools require:

- a separate write scope;
- idempotency key;
- explicit resource target;
- bounded input size;
- audit record;
- confirmation policy for destructive operations;
- no implicit execution based solely on retrieved text.

Tool output is data. It cannot modify the model's system instructions or grant additional tools.

## Prompt injection

All retrieved and parsed content is treated as untrusted evidence. The system must preserve this distinction in tool descriptions and response structures.

Controls:

- do not concatenate retrieved text into privileged server instructions;
- return structured citations and metadata;
- keep read and write tools separate;
- do not interpret instructions inside documents as authorization;
- do not let documents select credentials, tenant IDs, network targets, or tools;
- sanitize rendered HTML in the management UI;
- flag likely injection strings for observability without censoring legitimate source content;
- require the calling agent to maintain its own instruction hierarchy.

## Upload security

Validation includes:

- object and expanded-archive size limits;
- extension, declared MIME, and detected MIME checks;
- decompression ratio and file-count limits;
- encrypted/password-protected document policy;
- parser allow-list;
- optional malware scanning;
- tenant quota and rate limits;
- SHA-256 calculation;
- quarantine path and reason codes.

Original files are stored privately. Downloads use short-lived signed URLs or authenticated Gateway streaming.

## Parser isolation

Parser services process hostile input and have a stricter sandbox than ordinary application services:

- private listener only;
- no inherited database or cloud-root credentials;
- no unrestricted network egress;
- CPU, memory, process, time, and temporary-disk limits;
- read-only container filesystem where feasible;
- per-request working directory;
- cleanup after success or failure;
- SSRF-safe asset and URL handling;
- structured diagnostics without raw secret dumps.

URL ingestion resolves and validates destinations before every redirect. It blocks loopback, link-local, private, metadata, and disallowed address ranges, including DNS rebinding checks.

## Object storage

- separate least-privilege credentials for Gateway and Worker;
- Gateway can issue or broker upload/download operations but does not receive broad administrative credentials;
- Worker permissions are restricted to the configured bucket and prefixes;
- versioning and server-side encryption are recommended;
- public access is disabled;
- lifecycle deletion is delayed and auditable;
- secrets never appear in object metadata or logs.

## Database and Redis

PostgreSQL and Redis are not publicly exposed. Connections use authentication and encryption where supported by the network environment.

Redis is not an authorization authority. Even when ACL metadata is projected for filtering, PostgreSQL remains the final source of truth.

Backups are encrypted and access-controlled. Restore procedures are tested using isolated credentials and environments.

## Secrets

Configuration references secrets through environment injection or a secret manager. Secrets are not committed to the repository, stored in profile JSON, copied to S3 manifests, returned by APIs, or logged.

Rotation must not require reindexing document content. Provider profile identity excludes raw secret material while retaining a stable credential reference.

## Logging and audit

Every security-relevant operation records:

```text
principal
tenant
action
resource
outcome
request_id
trace_id
time
policy decision metadata
```

Raw document text, queries, tokens, credentials, and signed URLs are excluded by default. Optional content logging requires an explicit tenant policy and retention period.

Audit events are append-only at the application level. Administrative export and retention are separate from normal search logs.

## Rate and resource limits

Limits apply by tenant, principal, client, endpoint, and operation:

- requests per interval;
- concurrent searches;
- upload size and count;
- parser concurrency;
- embedding token and request budgets;
- result count and context-token budget;
- MCP payload size;
- fetch range and signed-URL lifetime.

A single tenant cannot exhaust all parser, embedding, Redis, or database capacity.

## Tenant isolation tests

Security tests must create at least two tenants with overlapping document names and queries, then prove:

- search does not return cross-tenant candidates;
- fetch cannot resolve another tenant's opaque ID;
- cache keys cannot cross authorization fingerprints;
- stale ACL projections cannot bypass PostgreSQL revalidation;
- signed URLs cannot be generated without current read permission;
- logs and metrics do not expose content or credentials.

The acceptance criterion for ACL leakage is zero.

## Supply chain

- pin Go module versions and container image digests for releases;
- generate an SBOM;
- run dependency and container vulnerability scans;
- use minimal runtime images and non-root users;
- protect release and deployment workflows;
- do not execute repository hooks or document macros during ingestion;
- review parser and model-provider upgrades as profile-version changes.

## Incident response

The system must support:

- revoking a principal or client;
- disabling one knowledge base or source;
- quarantining a document version;
- rotating S3, database, Redis, IAM, and provider credentials;
- invalidating caches by tenant or knowledge-base revision;
- rebuilding Redis from trusted sources;
- tracing a response back to exact chunks and versions;
- exporting relevant audit events without exposing unrelated tenants.
