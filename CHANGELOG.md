# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Until 1.0.0, minor versions (0.x.0) may include breaking changes; they are
called out under **Changed** or **Removed**.

## [Unreleased]

### Added

- Mock LLM: `MOCKLLM_LANGUAGE=en|tr` picks the answer template language
  (default `en`). Only the template prose switches; citations keep their
  `[n]` labels either way.

## [0.1.0] - 2026-11-07

First public release: **Community Preview**.

### Added

- **Retrieval and answers**
  - Hybrid search: dense embeddings plus BM25 sparse vectors, fused with
    reciprocal rank fusion in Qdrant, with a dense-only fallback.
  - Optional reranking; if the reranker fails, the fused order is kept.
  - Answers cite their sources with `[n]` markers that link to the cited
    passages.
- **Document ingestion**
  - PDF, DOCX, XLSX, PPTX, TXT and Markdown, plus images and scanned PDF pages
    through Tesseract OCR (Turkish and English).
  - Heading-aware chunking (1500 characters, 300 overlap) and idempotent vector
    upserts.
  - Multipart uploads up to 10 GiB (16 MiB parts, each retried on failure),
    with automatic retry of failed documents.
- **Chat**
  - Token-by-token streaming over Server-Sent Events.
  - Per-request reasoning effort and answer styles.
  - Image attachments for models configured as vision-capable.
  - Skills (reusable instructions), scheduled tasks with an in-app
    notifications inbox, personalization, and durable user memory (per
    workspace, off by default).
  - Graceful drain during deploys: interrupted answers reconnect and resume
    without duplicating the question.
- **Quality**
  - Background evaluator that scores answer faithfulness, citation validity and
    citation coverage, and flags low-scoring conversations for admins.
- **Multi-tenancy**
  - Organization → Team (nested) → Workspace model with role-based access
    (STAFF, WORKSPACE_ADMIN, SUPER_ADMIN) and a separate platform-admin role
    for deployment-wide settings.
  - Email/password sign-in and invite links.
- **Embeddable support widget**
  - Dependency-free vanilla JavaScript rendered in a Shadow DOM.
  - Issued only to origins on the workspace's allow-list, using a separate
    token audience and signing secret from the admin API.
- **Optional features** (all off by default)
  - Web search through the Brave Search API, and a multi-query deep research
    mode.
  - MCP tool use for Jira through mcp-atlassian, read-only by default.
  - SSO (OpenID Connect) and SCIM provisioning APIs — **experimental**; there is
    no SSO button in the web UI yet.
- **Admin console**
  - Branding (white-label name, colors, logo and icon), audit log, usage
    analytics, a Qdrant vector map, org chart and directory, knowledge base
    with folders.
- **Deployment and operations**
  - Checksum-verified Linux installer (`install.sh`): downloads a pinned
    release, verifies it against the release's `SHA256SUMS`, generates every
    secret and prints the setup token. Re-running it upgrades in place.
  - Docker Compose deployment with Caddy as the single front door (automatic
    HTTPS when a domain is set).
  - Helm chart with single-node and HA profiles (`values-single.yaml`,
    `values-ha.yaml`: CloudNativePG, Valkey Sentinel, 3-node Qdrant) and
    Kustomize manifests (community preview).
  - Backup, restore and disaster-recovery drill scripts (`scripts/backup.sh`,
    `scripts/restore.sh`, `scripts/dr-drill.sh`); see
    [docs/backup-restore.md](docs/backup-restore.md).
- **Other**
  - Turkish and English user interface.
  - Deterministic mock LLM for offline demos and CI.

### Changed

- **Breaking:** durable user memory is now gated per workspace and **off by
  default**, including for existing workspaces; enable it in workspace
  settings (*Admin → Workspaces → Memory*). Remembered facts are sent to the
  external LLM with each answer. The global `MEMORY_ENABLED` switch still turns
  it off platform-wide; existing memories can still be viewed and cleared.
- **Breaking:** the bundled datastores are now Valkey 8 (instead of Redis 7)
  and SeaweedFS (instead of MinIO), Qdrant is pinned to 1.20.0, and Caddy owns
  ports 80/443 as the single front door. See *Upgrading from pre-release
  builds* below.
- The default timezone for new users and scheduled tasks is the browser's zone
  in the web UI, else `UTC` (`DEFAULT_TIMEZONE`); it used to be
  `Europe/Istanbul`. Existing users and tasks keep their timezone.

### Removed

- `LLMAAS_EVAL_MAX_CONCURRENCY`: it was documented but never read by any
  service. The eval worker's judge concurrency is `EVAL_MAX_CONCURRENCY`
  (default 4).
- `MINIO_CONSOLE_PORT`: SeaweedFS has no MinIO console.
- The compose `full` profile and its Redpanda and PgBouncer services (never
  wired to any service), with `make up-full` and the `EVENT_BUS`,
  `REDPANDA_BROKERS`, `PGBOUNCER_HOST` and `PGBOUNCER_PORT` settings. Every
  service uses Valkey Streams and connects to PostgreSQL directly; the Helm
  chart's CloudNativePG pooler option is unaffected.

### Security

- One-time setup token required to create the first admin account.
- Refresh-token rotation with reuse detection; reuse, logout, password change,
  role change and deactivation revoke sessions.
- Login lockout per account, and a per-IP rate limit on sign-in.
- **Breaking:** an unset `RAGGIE_ENV` now means production. Services refuse to
  start with default, placeholder or short secrets unless `RAGGIE_ENV` is a
  development value; set `RAGGIE_ENV=development` for local setups.
- **Breaking:** the embeddable widget can no longer trigger web or deep search
  unless `WIDGET_WEB_SEARCH_ENABLED=true` and the workspace has web search
  enabled. Internal chats are unchanged.
- Chat history sent by the browser is reduced to plain user and assistant turns
  (limited in number and length), so it cannot inject system or tool messages;
  the widget ignores browser-sent history and uses the stored conversation.
- Each edge replica caps concurrent chat streams (`EDGE_MAX_SSE_CONNECTIONS`,
  default 12,500); extra chats get a retryable "busy" answer.
- MCP tools (Jira) are available only to signed-in internal users, never to the
  widget, and write tools are off by default.
- The installer enables Valkey and Qdrant authentication with generated
  secrets; production refuses known default values for them.
- Web: same-origin (CSRF) checks on every mutating API route, a Content
  Security Policy and security headers, and model-rendered images allowed only
  from the same origin (blocks markdown image exfiltration).
- SSRF guard for web-search page fetching (public IPs only, limited redirects
  and content types).
- Document parsing runs in disposable child processes with time, size,
  archive-expansion and spreadsheet-cell limits.
- Authorization matrix tests cover every HTTP route of the edge and the control
  plane, plus cross-organization isolation tests.
- Go fuzz targets and property-based tests for parsers and request handling.
- CI scanning with gitleaks, semgrep, bandit, gosec, govulncheck, pip-audit,
  `pnpm audit`, trivy and hadolint.

### Upgrading from pre-release builds

Installs made from the repository before 0.1.0 need these steps; the full
procedure, with the helper scripts, is in [docs/upgrade.md](docs/upgrade.md).

- **`RAGGIE_ENV` unset now means production**: set `RAGGIE_ENV=development` on
  laptops, or replace every default/placeholder secret before starting.
- **The edge needs `DATABASE_URL` in production**: outside development it
  refuses to start without it
  ([details](docs/upgrade.md#edge-needs-database_url-in-production)).
- **Memory is off per workspace**, existing workspaces included: turn it back
  on under *Admin → Workspaces → Memory* where you want it.
- **Widget web search** needs `WIDGET_WEB_SEARCH_ENABLED=true` on top of the
  workspace toggle.
- **Default timezone** for new users and scheduled tasks is the browser's zone,
  else `UTC`; existing users and tasks keep theirs.
- **Removed settings**: `LLMAAS_EVAL_MAX_CONCURRENCY` (use
  `EVAL_MAX_CONCURRENCY`), `MINIO_CONSOLE_PORT`, and the compose `full`
  profile (use `core`; `make up-full` is gone) with `EVENT_BUS`,
  `REDPANDA_BROKERS`, `PGBOUNCER_HOST` and `PGBOUNCER_PORT`.
- **Redis → Valkey**: Valkey cannot read Redis 7.4 data files, so drain the
  workers before switching
  ([Valkey](docs/upgrade.md#valkey-drain-the-workers-first)).
- **MinIO → SeaweedFS**: copy the stored files before switching
  ([SeaweedFS](docs/upgrade.md#seaweedfs-copy-the-files-before-switching)).
- **Caddy front door**: Caddy now owns ports 80/443; stop any other proxy on
  the host and point `WIDGET_EMBED_BASE_URL` at the Caddy origin.
- **Qdrant 1.20.0**: Qdrant cannot downgrade, and a single node cannot skip a
  minor version; run the latest patch of every minor in between first
  ([upgrade guide](docs/upgrade.md#upgrading-to-010-valkey-and-seaweedfs)).

`scripts/upgrade/0.1.0/preflight.sh` checks most of this on the running old
stack before you start.

### Known limitations

See the "Known limitations" section of the [README](README.md).

[Unreleased]: https://github.com/alicangunduz/raggie/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/alicangunduz/raggie/releases/tag/v0.1.0
