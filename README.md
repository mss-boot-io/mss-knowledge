# MSS Knowledge

MSS Knowledge is a self-hosted knowledge, memory, context, and retrieval infrastructure for AI clients and agents.

It is not a chat UI and it is not tied to one model vendor. The system exposes stable MCP and REST contracts to ChatGPT, Grok, Codex, Claude, local agents, server-side agents, and applications built on mss-boot-admin.

## Architecture boundaries

- **S3-compatible object storage** is the source of truth for original and normalized document content.
- **PostgreSQL** is the source of truth for metadata, permissions, versions, ingestion jobs, durable memories, and audit records.
- **Redis** is a rebuildable real-time layer for hybrid retrieval, vector indexes, session state, memory indexes, task context, and caches.
- **The Go gateway** is the only public application entry point and owns authentication, authorization, MCP, REST, retrieval orchestration, citations, rate limits, and audit events.

```text
ChatGPT / Grok / Codex / Claude / custom agents
                         |
                    MCP + REST
                         |
                mss-knowledge-gateway
                         |
          +--------------+--------------+
          |              |              |
      PostgreSQL       Redis        S3-compatible
      control plane   context       document truth
                         |
                 ingestion worker
                         |
              parser + embedding API
```

## Repository status

The repository is in the foundation phase. The first implementation milestone is a read-only vertical slice:

```text
upload/register document
  -> persist version metadata
  -> parse and normalize
  -> chunk and embed
  -> index in Redis
  -> search/fetch through REST and MCP
  -> return versioned citations
```

The initial branch is `codex/mss-knowledge-foundation`. Work is checkpointed remotely before build, test, review, or follow-up changes so that an interrupted agent session cannot lose authored material.

## Documentation

- [Architecture overview](docs/architecture/overview.md)
- [Data model](docs/architecture/data-model.md)
- [Ingestion pipeline](docs/architecture/ingestion-pipeline.md)
- [Search and retrieval](docs/architecture/search-and-retrieval.md)
- [Memory, context, and cache](docs/architecture/memory-context-cache.md)
- [Security model](docs/architecture/security.md)
- [MCP contract](docs/api/mcp.md)
- [Implementation roadmap](docs/roadmap.md)
- [Agent checkpoint policy](AGENTS.md)

## Development

The codebase targets Go 1.26 and starts with a modular monolith that produces separate gateway, worker, and administration binaries. Infrastructure adapters remain behind ports so Redis, object storage, embedding providers, parsers, and search engines can be replaced without changing MCP clients.

```bash
make test
make build
```

## License

MIT. See [LICENSE](LICENSE).
