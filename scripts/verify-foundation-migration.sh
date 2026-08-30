#!/bin/sh
set -eu

: "${DATABASE_URL:?DATABASE_URL must be set}"

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
UP_MIGRATION="$ROOT_DIR/migrations/000001_foundation.up.sql"
DOWN_MIGRATION="$ROOT_DIR/migrations/000001_foundation.down.sql"

psql_exec() {
    psql -X "$DATABASE_URL" --set ON_ERROR_STOP=1 "$@"
}

cleanup() {
    psql_exec --file "$DOWN_MIGRATION" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

psql_exec --file "$UP_MIGRATION"

expected_tables=16
actual_tables=$(psql_exec --tuples-only --no-align --command "
SELECT count(*)
FROM pg_catalog.pg_tables
WHERE schemaname = 'public'
  AND tablename IN (
    'tenants',
    'principals',
    'knowledge_bases',
    'kb_acl_bindings',
    'sources',
    'processing_profiles',
    'documents',
    'document_versions',
    'chunks',
    'ingestion_jobs',
    'ingestion_stage_runs',
    'memories',
    'sessions',
    'task_checkpoints',
    'audit_events',
    'outbox_events'
  );")

if [ "$actual_tables" -ne "$expected_tables" ]; then
    echo "expected $expected_tables foundation tables, found $actual_tables" >&2
    exit 1
fi

active_version_constraint=$(psql_exec --tuples-only --no-align --command "
SELECT count(*)
FROM pg_catalog.pg_constraint
WHERE conname = 'documents_active_version_fk';")

if [ "$active_version_constraint" -ne 1 ]; then
    echo "documents_active_version_fk was not created" >&2
    exit 1
fi

claim_index=$(psql_exec --tuples-only --no-align --command "
SELECT count(*)
FROM pg_catalog.pg_indexes
WHERE schemaname = 'public'
  AND indexname = 'ingestion_jobs_claim_idx';")

if [ "$claim_index" -ne 1 ]; then
    echo "ingestion_jobs_claim_idx was not created" >&2
    exit 1
fi

psql_exec --file "$DOWN_MIGRATION"
trap - EXIT INT TERM

remaining_tables=$(psql_exec --tuples-only --no-align --command "
SELECT count(*)
FROM pg_catalog.pg_tables
WHERE schemaname = 'public';")

if [ "$remaining_tables" -ne 0 ]; then
    echo "foundation rollback left $remaining_tables public tables" >&2
    exit 1
fi

echo "foundation migration up/down verification passed"
