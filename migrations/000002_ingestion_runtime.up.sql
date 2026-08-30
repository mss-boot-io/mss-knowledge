BEGIN;

ALTER TABLE document_versions
    ADD COLUMN normalized_object_version_id text NOT NULL DEFAULT '',
    ADD COLUMN normalized_sha256 text NOT NULL DEFAULT '',
    ADD COLUMN manifest_object_version_id text NOT NULL DEFAULT '';

ALTER TABLE document_versions
    ADD CONSTRAINT document_versions_normalized_sha256_check
    CHECK (normalized_sha256 = '' OR normalized_sha256 ~ '^[0-9a-f]{64}$');

COMMIT;
