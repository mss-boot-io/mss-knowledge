BEGIN;

DROP TABLE IF EXISTS outbox_events;
DROP TABLE IF EXISTS audit_events;
DROP TABLE IF EXISTS task_checkpoints;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS memories;
DROP TABLE IF EXISTS ingestion_stage_runs;
DROP TABLE IF EXISTS ingestion_jobs;
DROP TABLE IF EXISTS chunks;
ALTER TABLE IF EXISTS documents DROP CONSTRAINT IF EXISTS documents_active_version_fk;
DROP TABLE IF EXISTS document_versions;
DROP TABLE IF EXISTS documents;
DROP TABLE IF EXISTS processing_profiles;
DROP TABLE IF EXISTS sources;
DROP TABLE IF EXISTS kb_acl_bindings;
DROP TABLE IF EXISTS knowledge_bases;
DROP TABLE IF EXISTS principals;
DROP TABLE IF EXISTS tenants;

COMMIT;
