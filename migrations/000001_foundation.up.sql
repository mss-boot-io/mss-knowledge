BEGIN;

CREATE TABLE tenants (
    id              text PRIMARY KEY,
    slug            text NOT NULL UNIQUE,
    name            text NOT NULL,
    status          text NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active', 'suspended', 'deleted')),
    settings_json   jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    deleted_at      timestamptz
);

CREATE TABLE principals (
    id                  text PRIMARY KEY,
    tenant_id           text NOT NULL REFERENCES tenants(id),
    kind                text NOT NULL
                        CHECK (kind IN ('user', 'agent', 'service_account', 'integration')),
    external_subject    text NOT NULL,
    display_name        text NOT NULL,
    status              text NOT NULL DEFAULT 'active'
                        CHECK (status IN ('active', 'disabled', 'deleted')),
    attributes_json     jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    deleted_at          timestamptz,
    UNIQUE (tenant_id, external_subject)
);

CREATE INDEX principals_tenant_status_idx
    ON principals (tenant_id, status)
    WHERE deleted_at IS NULL;

CREATE TABLE knowledge_bases (
    id                  text PRIMARY KEY,
    tenant_id           text NOT NULL REFERENCES tenants(id),
    slug                text NOT NULL,
    name                text NOT NULL,
    description         text NOT NULL DEFAULT '',
    status              text NOT NULL DEFAULT 'active'
                        CHECK (status IN ('active', 'disabled', 'deleted')),
    revision            bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    default_language    text NOT NULL DEFAULT '',
    search_shard_id     text NOT NULL DEFAULT 'default',
    created_by          text NOT NULL REFERENCES principals(id),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    deleted_at          timestamptz,
    UNIQUE (tenant_id, slug)
);

CREATE INDEX knowledge_bases_tenant_status_idx
    ON knowledge_bases (tenant_id, status)
    WHERE deleted_at IS NULL;

CREATE TABLE kb_acl_bindings (
    id                  text PRIMARY KEY,
    tenant_id           text NOT NULL REFERENCES tenants(id),
    kb_id               text NOT NULL REFERENCES knowledge_bases(id),
    principal_id        text NOT NULL REFERENCES principals(id),
    role                text NOT NULL
                        CHECK (role IN ('owner', 'admin', 'editor', 'reader', 'agent')),
    permissions_json    jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_by          text NOT NULL REFERENCES principals(id),
    created_at          timestamptz NOT NULL DEFAULT now(),
    revoked_at          timestamptz
);

CREATE UNIQUE INDEX kb_acl_bindings_active_unique_idx
    ON kb_acl_bindings (kb_id, principal_id)
    WHERE revoked_at IS NULL;

CREATE INDEX kb_acl_bindings_principal_idx
    ON kb_acl_bindings (tenant_id, principal_id, kb_id)
    WHERE revoked_at IS NULL;

CREATE TABLE sources (
    id                      text PRIMARY KEY,
    tenant_id               text NOT NULL REFERENCES tenants(id),
    kb_id                   text NOT NULL REFERENCES knowledge_bases(id),
    kind                    text NOT NULL
                            CHECK (kind IN ('upload', 's3_prefix', 'web', 'git')),
    name                    text NOT NULL,
    status                  text NOT NULL DEFAULT 'active'
                            CHECK (status IN ('active', 'disabled', 'error', 'deleted')),
    configuration_ciphertext bytea,
    cursor_json             jsonb NOT NULL DEFAULT '{}'::jsonb,
    sync_policy_json        jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_by              text NOT NULL REFERENCES principals(id),
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now(),
    deleted_at              timestamptz
);

CREATE INDEX sources_kb_status_idx
    ON sources (tenant_id, kb_id, status)
    WHERE deleted_at IS NULL;

CREATE TABLE processing_profiles (
    id                  text PRIMARY KEY,
    tenant_id           text REFERENCES tenants(id),
    kind                text NOT NULL
                        CHECK (kind IN ('parser', 'chunker', 'embedding', 'index', 'reranker')),
    name                text NOT NULL,
    version             text NOT NULL,
    status              text NOT NULL DEFAULT 'active'
                        CHECK (status IN ('active', 'retired')),
    provider            text NOT NULL,
    configuration_json  jsonb NOT NULL DEFAULT '{}'::jsonb,
    fingerprint         text NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    retired_at          timestamptz,
    UNIQUE NULLS NOT DISTINCT (tenant_id, kind, name, version),
    UNIQUE NULLS NOT DISTINCT (tenant_id, kind, fingerprint)
);

CREATE TABLE documents (
    id                  text PRIMARY KEY,
    tenant_id           text NOT NULL REFERENCES tenants(id),
    kb_id               text NOT NULL REFERENCES knowledge_bases(id),
    source_id           text REFERENCES sources(id),
    external_key        text NOT NULL,
    title               text NOT NULL,
    status              text NOT NULL DEFAULT 'active'
                        CHECK (status IN ('active', 'disabled', 'deleted')),
    active_version_id   text,
    created_by          text NOT NULL REFERENCES principals(id),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    deleted_at          timestamptz,
    UNIQUE NULLS NOT DISTINCT (tenant_id, source_id, external_key)
);

CREATE INDEX documents_kb_status_idx
    ON documents (tenant_id, kb_id, status)
    WHERE deleted_at IS NULL;

CREATE TABLE document_versions (
    id                      text PRIMARY KEY,
    tenant_id               text NOT NULL REFERENCES tenants(id),
    kb_id                   text NOT NULL REFERENCES knowledge_bases(id),
    document_id             text NOT NULL REFERENCES documents(id),
    version_number          bigint NOT NULL CHECK (version_number > 0),
    status                  text NOT NULL DEFAULT 'PROCESSING'
                            CHECK (status IN ('PROCESSING', 'READY', 'FAILED', 'QUARANTINED', 'SUPERSEDED', 'DELETED')),
    source_uri              text NOT NULL,
    object_bucket           text NOT NULL,
    object_key              text NOT NULL,
    object_version_id       text NOT NULL DEFAULT '',
    filename                text NOT NULL,
    media_type              text NOT NULL,
    size_bytes              bigint NOT NULL CHECK (size_bytes >= 0),
    content_sha256          text NOT NULL CHECK (content_sha256 ~ '^[0-9a-f]{64}$'),
    parser_profile_id       text NOT NULL REFERENCES processing_profiles(id),
    chunker_profile_id      text NOT NULL REFERENCES processing_profiles(id),
    embedding_profile_id    text NOT NULL REFERENCES processing_profiles(id),
    index_profile_id        text NOT NULL REFERENCES processing_profiles(id),
    pipeline_fingerprint    text NOT NULL,
    normalized_object_key   text NOT NULL DEFAULT '',
    manifest_object_key     text NOT NULL DEFAULT '',
    manifest_sha256         text NOT NULL DEFAULT '',
    chunk_count             bigint NOT NULL DEFAULT 0 CHECK (chunk_count >= 0),
    token_count             bigint NOT NULL DEFAULT 0 CHECK (token_count >= 0),
    error_code              text NOT NULL DEFAULT '',
    error_detail_json       jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_by              text NOT NULL REFERENCES principals(id),
    created_at              timestamptz NOT NULL DEFAULT now(),
    published_at            timestamptz,
    superseded_at           timestamptz,
    deleted_at              timestamptz,
    UNIQUE (document_id, version_number)
);

ALTER TABLE documents
    ADD CONSTRAINT documents_active_version_fk
    FOREIGN KEY (active_version_id) REFERENCES document_versions(id)
    DEFERRABLE INITIALLY DEFERRED;

CREATE INDEX document_versions_document_status_idx
    ON document_versions (tenant_id, document_id, status, version_number DESC);

CREATE INDEX document_versions_pipeline_idx
    ON document_versions (tenant_id, pipeline_fingerprint);

CREATE TABLE chunks (
    id                  text PRIMARY KEY,
    tenant_id           text NOT NULL REFERENCES tenants(id),
    kb_id               text NOT NULL REFERENCES knowledge_bases(id),
    document_id         text NOT NULL REFERENCES documents(id),
    version_id          text NOT NULL REFERENCES document_versions(id),
    parent_chunk_id     text,
    ordinal             integer NOT NULL CHECK (ordinal >= 0),
    content_type        text NOT NULL,
    heading_path_json   jsonb NOT NULL DEFAULT '[]'::jsonb,
    page_start          integer,
    page_end            integer,
    content_sha256      text NOT NULL CHECK (content_sha256 ~ '^[0-9a-f]{64}$'),
    text_object_key     text NOT NULL DEFAULT '',
    token_count         integer NOT NULL DEFAULT 0 CHECK (token_count >= 0),
    created_at          timestamptz NOT NULL DEFAULT now(),
    deleted_at          timestamptz,
    UNIQUE (version_id, ordinal),
    CHECK (page_start IS NULL OR page_start >= 0),
    CHECK (page_end IS NULL OR page_end >= page_start)
);

ALTER TABLE chunks
    ADD CONSTRAINT chunks_parent_chunk_fk
    FOREIGN KEY (parent_chunk_id) REFERENCES chunks(id)
    DEFERRABLE INITIALLY DEFERRED;

CREATE INDEX chunks_version_idx ON chunks (tenant_id, version_id, ordinal);

CREATE TABLE ingestion_jobs (
    id                  text PRIMARY KEY,
    tenant_id           text NOT NULL REFERENCES tenants(id),
    kb_id               text NOT NULL REFERENCES knowledge_bases(id),
    document_id         text NOT NULL REFERENCES documents(id),
    version_id          text NOT NULL REFERENCES document_versions(id),
    kind                text NOT NULL
                        CHECK (kind IN ('ingest', 'reindex', 'rebuild', 'delete', 'reconcile')),
    state               text NOT NULL DEFAULT 'PENDING'
                        CHECK (state IN ('PENDING', 'RUNNING', 'RETRY_WAIT', 'SUCCEEDED', 'FAILED', 'CANCELLED')),
    current_stage       text NOT NULL DEFAULT 'RECEIVED'
                        CHECK (current_stage IN ('RECEIVED', 'STORED', 'VALIDATING', 'PARSING', 'NORMALIZING', 'CHUNKING', 'EMBEDDING', 'INDEXING', 'VERIFYING', 'PUBLISHING', 'READY')),
    priority            integer NOT NULL DEFAULT 0,
    attempt             integer NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    max_attempts        integer NOT NULL DEFAULT 5 CHECK (max_attempts > 0),
    lease_owner         text NOT NULL DEFAULT '',
    lease_expires_at    timestamptz,
    input_fingerprint   text NOT NULL,
    stage_data_json     jsonb NOT NULL DEFAULT '{}'::jsonb,
    error_code          text NOT NULL DEFAULT '',
    error_message       text NOT NULL DEFAULT '',
    created_at          timestamptz NOT NULL DEFAULT now(),
    started_at          timestamptz,
    next_attempt_at     timestamptz NOT NULL DEFAULT now(),
    completed_at        timestamptz,
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CHECK (attempt <= max_attempts),
    CHECK ((state = 'RUNNING' AND lease_owner <> '' AND lease_expires_at IS NOT NULL)
        OR state <> 'RUNNING')
);

CREATE INDEX ingestion_jobs_claim_idx
    ON ingestion_jobs (priority DESC, next_attempt_at, created_at)
    WHERE state IN ('PENDING', 'RETRY_WAIT');

CREATE INDEX ingestion_jobs_version_idx
    ON ingestion_jobs (tenant_id, version_id, created_at DESC);

CREATE TABLE ingestion_stage_runs (
    id                  bigserial PRIMARY KEY,
    job_id              text NOT NULL REFERENCES ingestion_jobs(id) ON DELETE CASCADE,
    stage               text NOT NULL,
    attempt             integer NOT NULL CHECK (attempt > 0),
    status              text NOT NULL
                        CHECK (status IN ('RUNNING', 'SUCCEEDED', 'FAILED', 'SKIPPED')),
    input_fingerprint   text NOT NULL,
    output_fingerprint  text NOT NULL DEFAULT '',
    metrics_json        jsonb NOT NULL DEFAULT '{}'::jsonb,
    error_code          text NOT NULL DEFAULT '',
    error_message       text NOT NULL DEFAULT '',
    started_at          timestamptz NOT NULL DEFAULT now(),
    completed_at        timestamptz,
    UNIQUE (job_id, stage, attempt)
);

CREATE TABLE memories (
    id                  text PRIMARY KEY,
    tenant_id           text NOT NULL REFERENCES tenants(id),
    scope_type          text NOT NULL
                        CHECK (scope_type IN ('tenant', 'principal', 'project', 'knowledge_base', 'agent')),
    scope_id            text NOT NULL,
    memory_type         text NOT NULL
                        CHECK (memory_type IN ('fact', 'preference', 'decision', 'constraint', 'procedure', 'incident', 'episode', 'project_state')),
    subject             text NOT NULL,
    content             text NOT NULL,
    importance          double precision NOT NULL DEFAULT 0.5 CHECK (importance BETWEEN 0 AND 1),
    confidence          double precision NOT NULL DEFAULT 1 CHECK (confidence BETWEEN 0 AND 1),
    sensitivity         text NOT NULL DEFAULT 'internal',
    source_type         text NOT NULL,
    source_reference_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    valid_from          timestamptz,
    valid_to            timestamptz,
    expires_at          timestamptz,
    supersedes_id       text REFERENCES memories(id),
    status              text NOT NULL DEFAULT 'active'
                        CHECK (status IN ('active', 'superseded', 'forgotten', 'expired', 'disputed')),
    created_by          text NOT NULL REFERENCES principals(id),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    deleted_at          timestamptz,
    CHECK (valid_from IS NULL OR valid_to IS NULL OR valid_from <= valid_to)
);

CREATE INDEX memories_scope_active_idx
    ON memories (tenant_id, scope_type, scope_id, memory_type)
    WHERE status = 'active' AND deleted_at IS NULL;

CREATE TABLE sessions (
    id                  text PRIMARY KEY,
    tenant_id           text NOT NULL REFERENCES tenants(id),
    principal_id        text NOT NULL REFERENCES principals(id),
    client_id           text NOT NULL,
    status              text NOT NULL DEFAULT 'active'
                        CHECK (status IN ('active', 'closed', 'expired')),
    title               text NOT NULL DEFAULT '',
    metadata_json       jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    expires_at          timestamptz,
    closed_at           timestamptz
);

CREATE INDEX sessions_principal_status_idx
    ON sessions (tenant_id, principal_id, status, updated_at DESC);

CREATE TABLE task_checkpoints (
    id                  text PRIMARY KEY,
    tenant_id           text NOT NULL REFERENCES tenants(id),
    task_id             text NOT NULL,
    principal_id        text NOT NULL REFERENCES principals(id),
    project_key         text NOT NULL,
    sequence            bigint NOT NULL CHECK (sequence > 0),
    state_json          jsonb NOT NULL,
    source_reference_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at          timestamptz NOT NULL DEFAULT now(),
    expires_at          timestamptz,
    UNIQUE (tenant_id, task_id, sequence)
);

CREATE INDEX task_checkpoints_latest_idx
    ON task_checkpoints (tenant_id, task_id, sequence DESC);

CREATE TABLE audit_events (
    id                  bigserial PRIMARY KEY,
    tenant_id           text NOT NULL REFERENCES tenants(id),
    principal_id        text REFERENCES principals(id),
    action              text NOT NULL,
    resource_type       text NOT NULL,
    resource_id         text NOT NULL DEFAULT '',
    outcome             text NOT NULL,
    request_id          text NOT NULL DEFAULT '',
    trace_id            text NOT NULL DEFAULT '',
    source_ip_hash      text NOT NULL DEFAULT '',
    user_agent_hash     text NOT NULL DEFAULT '',
    metadata_json       jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at          timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX audit_events_tenant_created_idx
    ON audit_events (tenant_id, created_at DESC);

CREATE TABLE outbox_events (
    id                  bigserial PRIMARY KEY,
    aggregate_type      text NOT NULL,
    aggregate_id        text NOT NULL,
    event_type          text NOT NULL,
    payload_json        jsonb NOT NULL,
    occurred_at         timestamptz NOT NULL DEFAULT now(),
    published_at        timestamptz,
    attempt             integer NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    last_error          text NOT NULL DEFAULT ''
);

CREATE INDEX outbox_events_unpublished_idx
    ON outbox_events (occurred_at, id)
    WHERE published_at IS NULL;

COMMIT;
