BEGIN;

ALTER TABLE document_versions
    DROP CONSTRAINT IF EXISTS document_versions_normalized_sha256_check,
    DROP COLUMN IF EXISTS manifest_object_version_id,
    DROP COLUMN IF EXISTS normalized_sha256,
    DROP COLUMN IF EXISTS normalized_object_version_id;

COMMIT;
