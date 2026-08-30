# MSS Knowledge 完整设计与实施规划

状态：Draft for implementation  
目标版本：Foundation / v0.1  
适用范围：个人知识库、小团队知识基础设施、多 Agent 上下文平台

## 1. 项目定位

MSS Knowledge 是一套自托管的 AI 知识、记忆、上下文与检索基础设施。它不绑定某一个大模型厂商，不以聊天 UI 为核心，也不把某个向量数据库或 RAG 产品暴露给上层客户端。

它统一服务以下客户端：

- ChatGPT、Grok、Claude、Codex 等云端 AI；
- Claude Code、Codex CLI、本地桌面 Agent；
- 服务器 Agent、Kubernetes Pod Agent；
- mss-boot-admin 的 AI 模块；
- 未来自研的多 Agent 平台。

所有客户端通过稳定的 MCP 与 REST 合约访问系统，不直接依赖 Redis、PostgreSQL、S3、解析器或具体 Embedding 服务。

## 2. 核心目标

系统需要同时解决四类问题：

| 领域 | 内容 | 主要特征 |
|---|---|---|
| Knowledge | PDF、Markdown、Office、代码、网页、架构文档 | 长期、可版本化、可引用 |
| Memory | 用户偏好、架构决策、事实、约束、历史经验 | 跨会话、可纠正、可遗忘 |
| Context | 当前会话、任务计划、执行状态、Checkpoint、协同锁 | 实时、短期、高频更新 |
| Cache | Query Embedding、检索结果、语义回答缓存 | 可丢失、可过期、重性能 |

第一阶段优先完成 Knowledge 的只读闭环，Memory、Context 和 Cache 在相同架构边界内逐步加入。

## 3. 不可破坏的架构边界

```text
S3-compatible object storage
  = 文档内容真源

PostgreSQL
  = 元数据、权限、版本、任务、持久记忆、审计真源

Redis
  = 可重建的搜索索引、实时上下文、记忆索引和缓存

mss-knowledge-gateway
  = MCP、REST、认证、授权、搜索编排、引用、限流和审计入口
```

Redis 不能成为文档内容、权限或长期记忆的唯一真源。清空 Redis 后，必须能够仅依赖 PostgreSQL 与 S3 重建所有知识索引和长期记忆索引。

## 4. 总体架构

```text
                    AI Clients
 ChatGPT / Grok / Claude / Codex / custom agents
                         |
                 MCP Streamable HTTP
                      REST API
                         |
              mss-knowledge-gateway
                         |
       +-----------------+------------------+
       |                 |                  |
       v                 v                  v
   PostgreSQL          Redis          S3-compatible
   control plane       context        document truth
       ^                 ^                  |
       |                 |                  v
       +----------- ingestion worker <-----+
                         |
                parser / OCR adapter
                         |
                 embedding provider
                         |
                  optional reranker
```

公网只暴露 Gateway。PostgreSQL、Redis、Worker、Parser 和 Embedding 服务位于私有网络。

## 5. 数据职责

### 5.1 S3：文档内容真源

S3 保存：

- 原始文件；
- 标准化后的内部文档模型；
- Markdown 或 JSON 解析结果；
- Chunk Manifest；
- 页面图片、表格图片和附件；
- 导出包；
- 评测数据集；
- PostgreSQL 与 Redis 备份。

推荐对象结构：

```text
tenants/{tenant_id}/
  knowledge-bases/{kb_id}/
    documents/{document_id}/
      versions/{version_id}/
        raw/original.{ext}
        normalized/document.json
        normalized/document.md
        chunks/manifest.jsonl.gz
        assets/*
        metadata.json
```

规则：

1. 原始文件不可覆盖，只能产生新版本；
2. 内容身份使用 SHA-256，不依赖分片上传 ETag；
3. 推荐开启 Bucket Versioning 和服务端加密；
4. 不允许匿名访问；
5. 下载使用短期签名 URL 或 Gateway 转发；
6. 删除先软删除，再由延迟清理任务处理；
7. S3 事件只作为触发信号，最终一致性由扫描任务兜底。

### 5.2 PostgreSQL：控制面真源

PostgreSQL 保存：

- 租户、用户、Agent 和服务账号；
- 知识库与 ACL；
- 来源配置；
- 文档逻辑实体与版本；
- Chunk 目录和 S3 引用；
- 导入任务和阶段结果；
- Parser、Chunker、Embedding、Index Profile；
- 长期记忆；
- Session 元数据与重要任务 Checkpoint；
- 审计记录；
- Transactional Outbox。

`documents.active_version_id` 表示当前生效版本。新版本在完整写入 S3 和 Redis 并通过校验后才进行原子切换。

### 5.3 Redis：实时上下文层

Redis 逻辑上分为：

1. Knowledge Index：全文、标签、向量和混合检索；
2. Memory Index：长期记忆的全文和向量索引；
3. Session / Task State：会话流、任务状态、锁、短期上下文；
4. Cache：Query Embedding、检索结果、语义回答和限流状态。

起步阶段允许单实例，但知识索引实例使用 `noeviction`。语义缓存成熟后，应拆成 persistent 与 cache 两类实例，避免普通缓存驱逐搜索索引。

## 6. 服务边界

### 6.1 Gateway

`mss-knowledge-gateway` 使用 Go 开发并保持无状态，职责包括：

- MCP Server；
- REST API；
- OAuth/OIDC Resource Server；
- 租户和 ACL 解析；
- 查询标准化与搜索编排；
- Hybrid Search、去重、Rerank 和上下文打包；
- 版本有效性校验；
- 稳定引用生成；
- 限流、审计、指标和追踪。

### 6.2 Worker

`mss-knowledge-worker` 与 Gateway 位于同一仓库，但编译成独立二进制。职责包括：

- 消费或领取导入任务；
- 下载和验证对象；
- 文件类型识别和安全检查；
- 调用 Parser；
- 标准化、Chunk、Embedding；
- 写入 Redis 暂存索引；
- 验证数量和校验值；
- 发布新版本；
- 清理旧索引；
- 执行重建、补偿与一致性扫描。

第一阶段不拆成大量微服务。

### 6.3 Parser Adapter

统一接口屏蔽具体解析实现：

```go
type Parser interface {
    Supports(mimeType, filename string) bool
    Parse(ctx context.Context, input ParseInput) (*KnowledgeDocument, error)
}
```

候选实现：

- NativeTextParser；
- MarkdownParser；
- CodeParser；
- JSONYAMLParser；
- HTMLParser；
- DoclingParser；
- OCRParser。

Markdown、TXT、JSON、YAML 和常见代码由 Go 原生解析器处理；PDF、DOCX、PPTX、XLSX 与扫描件默认交给独立 Parser 服务。任何外部 Parser 的返回值都必须转换为内部 `KnowledgeDocument`，不能让领域层依赖第三方数据模型。

## 7. 内部文档模型

`KnowledgeDocument` 使用版本化 Schema，至少包含：

```json
{
  "schema_version": "1.0",
  "document_id": "doc_xxx",
  "version_id": "ver_xxx",
  "title": "Architecture",
  "language": "zh",
  "metadata": {
    "source_type": "s3",
    "source_uri": "s3://...",
    "content_sha256": "...",
    "mime_type": "application/pdf"
  },
  "blocks": [
    {
      "id": "block_001",
      "type": "heading",
      "level": 1,
      "text": "System Architecture",
      "page": 2,
      "heading_path": ["System Architecture"]
    }
  ]
}
```

Block 类型至少包括：

```text
heading, paragraph, list, code, table, image,
caption, quote, formula, page_break
```

## 8. 导入状态机

```text
RECEIVED
  -> STORED
  -> VALIDATING
  -> PARSING
  -> NORMALIZING
  -> CHUNKING
  -> EMBEDDING
  -> INDEXING
  -> PUBLISHING
  -> READY
```

异常状态：

```text
QUARANTINED, RETRY_WAIT, FAILED, CANCELLED
```

每一个阶段都必须：

- 可重试；
- 幂等；
- 有输入和输出指纹；
- 记录开始、结束、重试次数和错误码；
- 支持从最近成功阶段继续；
- 不依赖 Worker 内存状态。

Pipeline Fingerprint 至少由以下字段组成：

```text
tenant_id + object bucket/key/version + content_sha256
+ parser profile version + chunker profile version
+ embedding profile version + index profile version
```

### 8.1 版本发布

1. PostgreSQL 创建 `PROCESSING` 版本；
2. 原始文件和标准化结果写入 S3；
3. Chunk Manifest 写入 S3；
4. 新 Chunk 写入独立 index version；
5. 校验 Chunk 数量、哈希和索引状态；
6. PostgreSQL 事务切换 `active_version_id`；
7. 版本进入 `READY`；
8. 异步清理旧版本索引和过期对象。

搜索结果从 Redis 返回后仍需进行 active-version 校验，旧索引未及时删除也不能被返回。

## 9. Chunk 策略

默认候选：

```text
目标 512 tokens
允许 300-900 tokens
重叠 80-100 tokens
```

实际切分必须结构感知：

- 普通文章按标题层级和段落；
- Markdown 保留标题、列表和代码块；
- Go、Java、Rust 等代码优先按 symbol、函数、结构体和 impl；
- YAML/JSON 按对象边界和配置路径；
- 表格重复表头并按行组切分；
- 日志按时间窗口、请求 ID 或固定行数；
- PDF 保留页码、标题路径和表格关系。

采用 Parent-Child 模型：Child 用于索引，返回时可扩展 Parent、前后邻接 Chunk，以兼顾精度与上下文完整性。

## 10. Embedding 设计

Embedding Provider 必须可替换和版本化：

```go
type EmbeddingProvider interface {
    EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
    EmbedQuery(ctx context.Context, query string) ([]float32, error)
    Dimensions() int
    Fingerprint() string
}
```

支持：

- OpenAI-compatible API；
- 云端 Embedding API；
- 本地 TEI；
- 本地 vLLM；
- 自定义 HTTP Provider。

Profile 保存：

```text
provider, model_id, model_revision, dimension, vector_type,
normalization, query_instruction, max_input_tokens, batch_size,
profile_version
```

模型升级时创建新 Profile，并通过双索引和原子切换完成重建，不能原地改变向量维度。

## 11. 搜索设计

核心知识检索使用 Redis Query Engine，而不是仅使用简单 Vector Set。原因是知识库需要同时具备：

- 全文和代码精确检索；
- 标签、租户、知识库、文档类型和时间过滤；
- 向量相似度；
- 混合融合；
- 字段权重；
- 版本和权限约束。

Chunk 索引包含：

```text
tenant_id, kb_id, document_id, version_id, chunk_id,
parent_chunk_id, ordinal, title, heading_path, body, code,
keywords, language, content_type, security_level,
page_start, page_end, created_at, updated_at,
content_sha256, embedding
```

检索链路：

```text
Authentication
  -> resolve allowed KBs
  -> query normalization and language detection
  -> optional query embedding cache
  -> lexical + vector retrieval
  -> ACL filter
  -> score fusion
  -> active-version validation
  -> deduplication and diversity
  -> optional rerank
  -> neighbor expansion
  -> token-budget packing
  -> results with citations
```

模式：

- `exact`：错误码、函数名、配置项、commit SHA、命令；
- `fast`：Hybrid，不启用 Reranker；
- `balanced`：Hybrid + Rerank，默认；
- `deep`：后期加入查询分解和多轮检索。

默认限制同一文档最多占据若干独立命中，避免超长文档淹没其他来源。

每个结果必须返回：

```text
document_id, version_id, chunk_id, title, heading_path,
source_uri, page range, content_sha256, updated_at,
lexical/vector/rerank score
```

## 12. 权限模型

第一阶段以知识库为主要授权边界：

```text
Tenant -> Knowledge Base -> Document -> Version -> Chunk
```

角色：

```text
owner, admin, editor, reader, agent
```

Scope：

```text
knowledge.search, knowledge.read, knowledge.write, knowledge.delete,
memory.read, memory.write, memory.delete,
session.read, session.write,
admin.manage, audit.read
```

Gateway 先从 PostgreSQL 解析允许访问的知识库，再将允许集合写入 Redis Filter。最终返回前再次执行授权检查，不能只依赖搜索过滤条件。

## 13. Agent Memory

三层模型：

### 13.1 Session Memory

保存当前对话、工具调用、临时参数和最近摘要。实现为 Redis Stream + TTL，Session 元数据落 PostgreSQL。

### 13.2 Working / Task Memory

保存计划、执行阶段、分支、commit、已完成事项、下一步、协同锁和错误状态。高频状态在 Redis，重要 Checkpoint 持久化到 PostgreSQL。

### 13.3 Long-term Memory

保存偏好、事实、决策、约束、流程、事故经验和项目状态。PostgreSQL 为真源，Redis 建立全文与向量索引。

长期记忆类型：

```text
fact, preference, decision, constraint, procedure,
incident, episode, project_state
```

新记忆与旧记忆冲突时不直接覆盖，而是使用 `supersedes_id` 保留演变轨迹。第一版仅允许显式写入、替代和遗忘，不自动把模型推断写成长期事实。

## 14. 语义缓存

语义缓存的身份不能只由问题向量决定，还必须包括：

```text
tenant_id, principal scope, model and version,
system prompt hash, tool schema hash,
knowledge revision hash, temperature class
```

默认禁止缓存：

- 敏感个人信息；
- 当前时间、价格和实时状态；
- 金融或高风险决策；
- 写操作结果；
- 权限相关结果；
- 低确定性回答。

知识库发布新版本时递增 revision，使相关缓存自然失效。

## 15. REST API 范围

第一阶段：

```text
GET/POST/PATCH/DELETE /v1/knowledge-bases
POST /v1/documents/uploads
POST /v1/documents/uploads/{id}/complete
GET /v1/documents
GET /v1/documents/{id}
GET /v1/documents/{id}/versions
POST /v1/documents/{id}/reindex
DELETE /v1/documents/{id}
POST /v1/search
GET /v1/chunks/{id}
GET /v1/documents/{id}/content
GET /v1/admin/ingestion-jobs
GET /v1/admin/indexes
POST /v1/admin/indexes/rebuild
GET /healthz
GET /readyz
GET /version
```

后续加入 Memory、Session 和 Task Checkpoint API。所有 API 使用 OpenAPI 3.1 描述。

## 16. MCP 设计

公网使用 MCP Streamable HTTP，本地提供 stdio proxy 连接远端 Gateway。

v1 保持只读，并优先稳定两个核心工具：

```text
search
fetch
```

辅助工具：

```text
list_knowledge_bases
get_document_metadata
memory_search
session_context_get
task_checkpoint_get
```

写工具在后续阶段增加，并要求独立 Scope、幂等键、审计和副作用声明。

MCP 参数只增加可选字段，不随意重命名，避免云端客户端保存工具快照后失效。

## 17. 安全设计

### 17.1 身份认证

Gateway 是 OAuth/OIDC Resource Server，不自行存储用户密码。推荐接入标准 IAM。远程连接必须使用 HTTPS、PKCE、Bearer Token、Audience 校验和最小 Scope。

### 17.2 网络

公网仅暴露：

```text
/mcp
/v1/*
/.well-known/*
/healthz
```

不暴露 Redis、PostgreSQL、Worker、Parser、Embedding 服务和内部管理端口。

### 17.3 文档是不可信输入

- 文档中的指令不能改变系统或工具权限；
- 检索内容只作为数据；
- 读写工具隔离；
- URL 导入需要 SSRF 防护；
- Parser 不得任意访问外部或内网地址；
- 上传执行 MIME 检测、大小限制、解压限制和可选病毒扫描；
- 敏感字段和查询日志需要脱敏；
- 所有写操作记录调用者、参数摘要、结果和 trace_id。

## 18. 管理界面

管理能力可作为 mss-boot-admin 的独立 AI Knowledge 模块接入，后端仍保持独立。

页面：

```text
Dashboard
Knowledge Bases
Documents
Sources
Ingestion Jobs
Search Playground
Memories
Sessions
Agent Clients
Model Profiles
Index Management
Audit Logs
System Health
```

Search Playground 需要展示标准化 Query、语言、Profile、过滤条件、各阶段分数、命中文档版本和最终上下文。Document Detail 需要展示原始文件、版本、标准化文档、Block、Chunk、页码、索引状态与错误记录。

第一阶段不做聊天 UI。

## 19. 部署

### 19.1 起步部署

Docker Compose：

```text
gateway
worker
postgres
redis-context
parser
openresty
prometheus
grafana
```

云端依赖：

```text
S3-compatible object storage
embedding API
optional reranker API
```

建议起步资源：8 vCPU、32 GB RAM、200 GB NVMe。4C16G 可用于轻量开发，但大型 PDF、OCR 和 Office 解析应限制并发。

### 19.2 Kubernetes

Gateway、Worker、Parser 使用 Deployment；PostgreSQL、Redis 可使用受管服务或专用 StatefulSet。Gateway 水平扩容，Worker 按积压扩容，Parser 独立设置 CPU、内存与临时磁盘限制。

## 20. 持久化与灾难恢复

Redis 启用 AOF everysec 与定期 RDB，但搜索索引仍被视为派生数据。

恢复顺序：

```text
1. 恢复 PostgreSQL
2. 校验 S3 原始文件和 Manifest
3. 恢复 Redis 快照或执行全量重建
4. 重建长期记忆索引
5. 恢复 Gateway 对外服务
```

验收前必须实际完成一次“清空 Redis，仅从 PostgreSQL + S3 完整重建”的演练。

## 21. 扩展路径

阶段一优先单节点纵向扩容与主从高可用。阶段二按知识库进行应用层分片：PostgreSQL 保存 `search_shard_id`，Gateway 并发查询多个 Shard 并统一融合。Search Backend 从第一天起通过接口抽象：

```go
type SearchStore interface {
    IndexBatch(ctx context.Context, records []ChunkRecord) error
    DeleteVersion(ctx context.Context, versionID string) error
    HybridSearch(ctx context.Context, req SearchRequest) ([]SearchHit, error)
    Rebuild(ctx context.Context, profile IndexProfile) error
}
```

未来可加入 Redis、Qdrant、Milvus、OpenSearch、pgvector 等实现，而不改变 MCP 合约。

## 22. 可观测性

必须具备：

- OpenTelemetry Trace；
- Prometheus Metrics；
- 结构化日志；
- Query ID、Job ID、Trace ID；
- 解析、Embedding、索引、搜索、Rerank 分段耗时；
- Redis 内存、索引规模、导入积压、失败率和缓存命中率告警。

日志默认不记录完整敏感查询，保留可配置的哈希、摘要或脱敏文本。

## 23. 质量评测

建立中文、英文、代码、精确术语、错误码、跨文档和时间线问题组成的 Golden Dataset。

指标：

```text
Recall@5, Recall@10, MRR, nDCG,
Citation Accuracy, Active Version Accuracy,
ACL Leakage, Search Latency
```

初始质量门槛：

```text
Recall@10 >= 0.85
引用版本正确率 = 100%
未授权结果 = 0
```

必须比较结构感知 Chunk、不同维度与精度、纯向量与 Hybrid、有无 Reranker、默认词典与技术词典。

## 24. 实施阶段

### Phase 0：Foundation 与技术验证

- 建立仓库、设计、ADR、开发规则和 CI；
- 建立 Gateway、Worker、CLI 骨架；
- 定义领域模型与 Ports；
- 建立 PostgreSQL 初始 Schema；
- 建立本地 Compose；
- 验证 Redis Hybrid Search、Parser 和 Embedding；
- 整理首批真实文档和 Golden Dataset。

### Phase 1：只读知识库 MVP

- Knowledge Base CRUD；
- 预签名上传与文档版本；
- 导入状态机；
- Parser、Chunk、Embedding；
- Redis Hybrid Search；
- REST search/fetch；
- MCP search/fetch；
- OAuth、ACL、审计、引用；
- 索引重建；
- 基础管理 UI。

### Phase 2：来源与运维

- S3 Prefix Sync；
- 一致性扫描；
- 网页和 Git 来源；
- 增量更新；
- 删除、归档和蓝绿索引；
- 备份恢复和完整监控；
- Search Playground。

### Phase 3：Agent Memory

- Session Memory；
- Long-term Memory；
- Task Checkpoint；
- Scope、Supersede、Forget；
- 只在明确授权下开放 MCP 写工具。

### Phase 4：自动记忆与语义缓存

- Memory Candidate Extraction；
- 审核与冲突检测；
- 敏感信息过滤；
- Semantic Cache；
- Knowledge Revision Invalidation。

### Phase 5：规模化

- Redis Sharding；
- 多 Worker 和 Helm；
- 租户配额、计量和跨区域灾备；
- 多 Search Backend。

## 25. v1 明确不做

```text
完整聊天 UI
自托管大语言模型
自动回答生成
知识图谱
全自动长期记忆提取
复杂工作流编排
细粒度 Chunk ACL
自动执行文档中的指令
公众多租户 SaaS
```

第一版的唯一目标是：

> 可靠地存、可靠地解析、准确地检索、安全地提供给 AI，并返回可验证的版本化引用。
