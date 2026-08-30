# MSS Knowledge

MSS Knowledge is a self-hosted knowledge, memory, context, and retrieval infrastructure for AI clients and agents.

The project is designed around four stable boundaries:

- S3-compatible object storage is the source of truth for document content.
- PostgreSQL is the source of truth for metadata, permissions, versions, jobs, and durable memories.
- Redis is a rebuildable real-time search, context, memory-index, and cache layer.
- A Go gateway exposes stable MCP and REST interfaces to ChatGPT, Grok, Codex, Claude, and custom agents.

> The repository has just been bootstrapped. Design documents and implementation are developed on topic branches and merged through pull requests.
