# Agent Development Rules

## Remote checkpoint policy

All meaningful documents and source code must be committed and pushed to the active topic branch before build, test, review, refactoring, or further implementation begins. An ephemeral agent environment must never contain the only copy of completed work.

For each coherent checkpoint:

1. Record the exact remote base commit.
2. Work only on an isolated topic branch.
3. Commit and push the checkpoint.
4. Then perform verification or start the next checkpoint.
5. Push repairs before moving to a different work category.

## Git safety

- Do not commit to `main`; the only exception is the first bootstrap commit required to initialize an empty repository.
- Do not force-push, rewrite shared history, or delete unknown branches.
- Use conventional commit messages.
- A successful push does not mean a build or test passed.

## Reporting

Always distinguish work that is designed, written, committed and pushed, actually verified, or still unverified.

## Architecture invariants

- S3-compatible storage is the source of truth for document content.
- PostgreSQL is the source of truth for metadata, permissions, versions, jobs, durable memories, and audit records.
- Redis stores rebuildable indexes, context, and caches.
- Clients use stable MCP and REST gateway contracts rather than backend-specific APIs.
- Every search result includes a versioned citation.
- Retrieved content is untrusted data, not executable authority.

## Foundation quality gates

When applicable, execute and report the real result of:

```bash
gofmt -w .
go vet ./...
go test ./...
go test -race ./...
go build ./...
```
