# Wargo MCP Server Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an owner-authorized agent use Wargo's features over MCP (Streamable HTTP at `/mcp`), with access levels, owner approval for admin actions, and every call identifiable in the audit log.

**Architecture:** A new `internal/mcp` package owns agents (tokens), MCP settings, the `/mcp` endpoint (official Go SDK), the tool table, the in-process dispatcher that serves a tool's request through Wargo's private API mux with an agent identity in the context, and the pending-approval store. `internal/audit` learns a fourth source, `agent`. The web app gains Settings → Agents, an approval page, an agent filter on the Audit page and a notification category.

**Tech Stack:** Go 1.26, `github.com/modelcontextprotocol/go-sdk` v1.8.0, SQLite via `internal/store`, React 19 + TypeScript + TanStack Query + Radix, `node --test` for web unit tests.

**Spec:** `docs/superpowers/specs/2026-11-25-mcp-server-design.md` (read it; it is the authority).

## Global Constraints

- Never use an em dash or an en dash as punctuation in code, comments, strings, docs or commit messages. No AI attribution lines in commits.
- MCP is off by default. While off, `/mcp` answers 404 and no token is accepted.
- Connect-from setting: `"local"` (default) or `"anywhere"`. Local = loopback, RFC 1918, IPv6 ULA (`fc00::/7`), link-local, plus CIDRs in config `mcp.allowed_networks`. A request carrying `X-Forwarded-For` or `CF-Connecting-IP` from a peer that `httpx.ProxyTrust` does not trust is never local.
- Approval time limit: default 15 minutes, allowed 1 to 60.
- Access levels, exact strings: `"read"`, `"operate"`, `"admin"` (each includes the ones before).
- Tokens: prefix `wargo_` + base64url (no padding) of 32 random bytes; stored only as SHA-256 hex; sent only as `Authorization: Bearer`; accepted only on `/mcp`.
- New audit source string: `"agent"`. Agent entries carry agent id, agent name at the time, tool name.
- New notification category: `agent.approval` ("Agent approvals"), default mode `push`.
- `media_activity`: entries carry `provider` (`"plex"` today); inputs `provider` (optional), `limit` (default 10, max 50) per list, `from` / `to` (RFC 3339, optional).
- Admin tools always create a pending request before any handler call. No tool can request removal of conflicting packages, deletion of torrent data, power actions, Wargo's own container update, terminal or file operations.
- Streamable HTTP in stateless mode (ruling: newest spec revision expects it; no MCP session state to keep). Exception: if, in stateless mode, a tool handler cannot tell whether the client declared URL elicitation (Task 6), use stateful mode instead; the approval link behaviour matters more than statelessness. Decide in Task 3 with a test, record it in the commit message.
- Tests: `go test ./...` (and `-race` for `internal/mcp`), `GOOS=linux GOARCH=amd64 go vet ./internal/...`, `cd web && npm test && npm run build`.

## Review Focus

1. A request through a local tunnel (cloudflared connects from 127.0.0.1 with `CF-Connecting-IP`) while `trusted_proxies` is empty must be refused under "local", not admitted as loopback. Test pinned in Task 3.
2. An agent bearer token sent to `/api/*` or `/ws/*` (with or without a cookie) must never authenticate there. Test pinned in Task 3.
3. Approving runs exactly the stored request: an agent cannot change the arguments after the request is created, a request cannot be approved twice or after expiry, and the approval endpoints refuse anything but a browser session. Tests pinned in Task 6.
4. For every admin route, replaying the stored request without a credential must not perform side effects before the handler answers `elevation_required` (the approval page relies on that first replay to learn whether a credential is needed). Test per admin tool pinned in Task 7.
5. Turning MCP off or revoking an agent takes effect on the agent's very next request, including one sent on an already open connection. Test pinned in Task 3.

---

### Task 1: Audit source `agent`

**Files:**
- Modify: `internal/audit/audit.go`, `internal/audit/http.go`, `internal/store/store.go` (new migration)
- Test: `internal/audit/agent_test.go`

**Interfaces:**
- Produces:
  - `const SourceAgent = "agent"`
  - `type AgentRef struct { ID string \`json:"id"\`; Name string \`json:"name"\`; Tool string \`json:"tool"\` }`
  - `func WithAgent(ctx context.Context, a AgentRef) context.Context` and `func AgentFrom(ctx context.Context) (AgentRef, bool)`
  - `Entry.Agent *AgentRef \`json:"agent,omitempty"\``
  - `func (l *Log) WriteFolded(r *http.Request, ev Event, window time.Duration) int64`: if the newest entry with the same source, agent id, action and target was written less than `window` ago, increment `details.count` on it (starting from 1) and update its timestamp instead of inserting; otherwise insert with `count: 1`.
  - `GET /api/audit` accepts `source=agent` and `agent=<id>`.

- [ ] **Step 1: Write the failing tests** in `internal/audit/agent_test.go`:
  - `TestAgentContextSetsSource`: a request whose context has `WithAgent(ctx, AgentRef{ID:"a1", Name:"OpenClaw", Tool:"list_containers"})` and a `WithSession` tag; `Write` stores `Source == "agent"`, `Agent == &AgentRef{"a1","OpenClaw","list_containers"}`, the IP from the request.
  - `TestAgentFilter`: entries from two agents and one user; `List(source="agent")` returns the agent ones, `agent=a1` only a1's.
  - `TestWriteFoldedFolds`: three `WriteFolded` calls 1 s apart (fake clock) with window 10 min give one entry with `details.count == 3`; a call after the window inserts a new entry.
  - `TestWriteFoldedKeepsDistinctTargets`: different targets are not folded.
- [ ] **Step 2: Run** `go test ./internal/audit/ -run 'Agent|Folded'`; expect compile failures.
- [ ] **Step 3: Implement.** Migration appends `ALTER TABLE audit_log ADD COLUMN agent_id TEXT; ALTER TABLE audit_log ADD COLUMN agent_name TEXT; ALTER TABLE audit_log ADD COLUMN tool TEXT;` plus an index on `agent_id`. `originOf` sets `source = SourceAgent` when `AgentFrom` succeeds. Keep the existing redaction of details.
- [ ] **Step 4: Run** `go test ./internal/audit/ ./internal/store/`; expect PASS.
- [ ] **Step 5: Commit** `Record which agent wrote an audit entry`.

### Task 2: Agents, MCP settings and their API

**Files:**
- Create: `internal/mcp/agents.go`, `internal/mcp/settings.go`, `internal/mcp/http.go`, `internal/mcp/service.go`
- Modify: `internal/store/store.go` (migration), `internal/config/config.go` (`MCP struct { AllowedNetworks []string \`toml:"allowed_networks"\` }`), `cmd/wargo` feature registration
- Test: `internal/mcp/agents_test.go`, `internal/mcp/http_test.go`

**Interfaces:**
- Produces:
  - `type Level string` with `LevelRead = "read"`, `LevelOperate = "operate"`, `LevelAdmin = "admin"`, and `func (l Level) Allows(need Level) bool`.
  - `type Agent struct { ID, Name string; Level Level; CreatedAt, LastUsedAt time.Time; LastIP string }` (JSON camelCase; no hash in JSON).
  - `type Agents struct` over table `mcp_agents(id TEXT PRIMARY KEY, name TEXT NOT NULL, level TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, created_at INTEGER NOT NULL, last_used_at INTEGER, last_ip TEXT)` with `Create(ctx, name string, level Level) (Agent, token string, err error)`, `List(ctx) ([]Agent, error)`, `SetLevel(ctx, id string, l Level) error`, `Rename(ctx, id, name string) error`, `Revoke(ctx, id string) error`, `ByToken(ctx, token string) (Agent, bool)` (constant-time compare not needed on a hash lookup), `Touch(ctx, id, ip string)` (throttled to one write per minute per agent).
  - `type Settings struct { Enabled bool \`json:"enabled"\`; Network string \`json:"network"\`; ApprovalMinutes int \`json:"approvalMinutes"\` }` stored under key `mcp.settings`; defaults `{false, "local", 15}`; validation rejects other networks and minutes outside 1..60.
  - `type Service struct` constructed by `func New(d *server.Deps) (*Service, error)` (or the registration pattern the server uses), implementing `Registrar` and `Capable` (`"mcp" -> {"enabled": bool}`).
  - Browser API (private mux, cookie session): `GET /api/mcp/settings`, `PUT /api/mcp/settings`, `GET /api/mcp/agents`, `POST /api/mcp/agents` (`{name, level}` -> `{agent, token}`, token returned only here), `PATCH /api/mcp/agents/{id}` (`{name?, level?}`), `DELETE /api/mcp/agents/{id}`. Audited as `mcp.settings`, `mcp.agent.create`, `mcp.agent.update`, `mcp.agent.revoke` (source user). These handlers refuse a request whose context has an agent identity (`audit.AgentFrom`), with 403.

- [ ] **Step 1: Failing tests:** `TestCreateReturnsTokenOnceAndStoresHash` (token has prefix `wargo_` and 43 base64url chars after it; table holds `sha256hex(token)`, never the token); `TestByTokenAfterRevoke` (false); `TestLevelAllows` (read < operate < admin); `TestSettingsDefaultsAndValidation` (absent -> defaults; minutes 0 and 61 rejected; network "lan" rejected); `TestAgentAPIRefusesAgentContext`; `TestCreateIsAudited` (entry `mcp.agent.create`, target agent name, no token in details).
- [ ] **Step 2: Run** `go test ./internal/mcp/`; expect FAIL.
- [ ] **Step 3: Implement** the files above; register the feature in `cmd/wargo` like the others.
- [ ] **Step 4: Run** `go test ./internal/mcp/ ./internal/store/ ./internal/config/`; expect PASS.
- [ ] **Step 5: Commit** `Add agents and MCP settings, off by default`.

### Task 3: The `/mcp` endpoint, network rule and bearer tokens

**Files:**
- Modify: `go.mod`/`go.sum` (add `github.com/modelcontextprotocol/go-sdk@v1.8.0`), `internal/server/server.go` (route `/mcp` before the SPA, not through `au.Require`), `internal/mcp/service.go`
- Create: `internal/mcp/endpoint.go`, `internal/mcp/network.go`
- Test: `internal/mcp/endpoint_test.go`, `internal/mcp/network_test.go`

**Interfaces:**
- Consumes: `Agents.ByToken`, `Agents.Touch`, `Settings`, `httpx.ProxyTrust`, `auth.NewThrottle()`.
- Produces:
  - `func (s *Service) Handler() http.Handler`: in order: 404 unless `Settings.Enabled`; 403 `mcp_network` unless the network rule admits the client; if an `Origin` header is present it must pass `auth.Service.AllowedOrigin`; bearer check via the SDK's `auth.RequireBearerToken` with a `TokenVerifier` backed by `Agents.ByToken` (401 with `WWW-Authenticate: Bearer` on failure; failures counted on the throttle per client IP, 429 while blocked); then `mcp.NewStreamableHTTPHandler(..., &mcp.StreamableHTTPOptions{Stateless: true})`. Settings and the agent are re-read on every request (no caching across requests), so revoke and disable apply on the next request.
  - `func Local(ip net.IP, extra []*net.IPNet) bool` and `func (s *Service) admits(r *http.Request) bool` implementing the Global Constraints rule.
  - `func (s *Service) agentFrom(ctx) (Agent, bool)` reading the verified token info in tool handlers.
  - A server with server info name `wargo` and the version string; no tools yet except as registered in later tasks. The `*mcp.Server` is built per request by the handler factory from the verified agent (its level) and current capabilities, so `tools/list` only holds what that agent may call (used by Task 4).
  - `TestClientCapabilitiesVisible`: a client that declares URL elicitation in `initialize` is seen as declaring it inside a later `tools/call` handler (decides stateless versus stateful, see Global Constraints).
- [ ] **Step 1: Failing tests:**
  - `TestDisabledIs404` and `TestDisabledRefusesValidToken`.
  - `TestLocalRule`: 127.0.0.1, 10.1.2.3, 192.168.1.5, `fd00::1`, `fe80::1` local; 8.8.8.8 not; 8.8.8.8 local when `allowed_networks = ["8.8.8.0/24"]`.
  - `TestForwardedFromUntrustedPeerIsNotLocal` (Review Focus 1): peer 127.0.0.1, header `CF-Connecting-IP: 203.0.113.9`, no trusted proxies, network "local" -> 403; same with trusted proxy 127.0.0.1/32 -> client 203.0.113.9 -> 403; with "anywhere" -> passes to auth.
  - `TestBadTokenIs401AndThrottled`; `TestValidTokenInitializes` (an MCP `initialize` over the handler with the SDK client succeeds).
  - `TestRevokeAppliesNextRequest` and `TestDisableAppliesNextRequest` (Review Focus 5): two calls on one client; revoke or disable between them; the second fails.
  - `TestTokenRefusedOnAPI` (Review Focus 2): `GET /api/capabilities` and `GET /ws/events` with `Authorization: Bearer <valid agent token>` and no cookie -> 401; with a valid cookie plus the bearer, the request is treated as the browser session (the bearer is ignored, not upgraded).
- [ ] **Step 2: Run** `go test ./internal/mcp/ ./internal/server/`; expect FAIL.
- [ ] **Step 3: Implement.** In `server.go` add a case for `p == "/mcp"` (exact, plus `/mcp/` if the SDK needs it) dispatching to the MCP service handler, with the same Host check as everything else. Pass the service the private mux after all features registered (needed by Task 4).
- [ ] **Step 4: Run** the tests; expect PASS. Run `go test -race ./internal/mcp/`.
- [ ] **Step 5: Commit** `Serve MCP at /mcp for authorized agents`.

### Task 4: Tool table and in-process dispatcher

**Files:**
- Create: `internal/mcp/dispatch.go`, `internal/mcp/tools.go`
- Test: `internal/mcp/dispatch_test.go`

**Interfaces:**
- Produces:
  - `type route struct { Method, Pattern string }` (pattern as registered, e.g. `"POST /api/containers/{id}/{action}"`).
  - `type ToolSpec struct { Name, Description string; Level Level; Admin bool; ReadOnly, Destructive, Idempotent bool; Needs string /* capability key, "" if none */; Routes []route }`, registered with `func (s *Service) addTool(spec ToolSpec, handler any)` which (a) wraps the SDK `mcp.AddTool`, (b) sets annotations from the booleans, (c) checks the caller's level before the handler runs (error result `forbidden_level`), and (d) hides the tool from `tools/list` when the agent's level or the capability does not allow it (per-request server construction via the handler factory in Task 3).
  - `func (s *Service) call(ctx context.Context, spec ToolSpec, method, path string, body any) (*Response, error)` with `type Response struct { Status int; Body json.RawMessage; Err *httpx.Error }`: refuses (programming error) any method+path that does not match one of `spec.Routes`; builds an `http.Request` with the agent identity (`audit.WithAgent`) and a session tag `agent:<first 8 of id>` (`audit.WithSession`), the agent's client IP and user agent carried over for audit and throttling, a request ID; serves it through the private mux with an `httptest.ResponseRecorder`-like writer; decodes an error body into `*httpx.Error` (so `Err.Code == "elevation_required"` is detectable).
  - `func untrusted(s string) string`: no transformation of meaning; trims and caps length (ruling: 512 chars per field) for third-party text fields.
- [ ] **Step 1: Failing tests:** `TestCallRefusesUndeclaredRoute`; `TestCallCarriesAgentIntoAudit` (a test handler that calls `audit.Write` produces an entry with source agent and the tool name); `TestCallDecodesElevationRequired`; `TestReadLevelCannotCallOperateTool`; `TestToolHiddenWithoutCapability`; `TestToolHiddenAboveLevel`.
- [ ] **Step 2: Run**; expect FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/mcp/`; expect PASS.
- [ ] **Step 5: Commit** `Route MCP tools through Wargo's own handlers`.

### Task 5: Read-only tools

**Files:**
- Create: `internal/mcp/tools_read.go`
- Modify: `internal/integrations/plex/plex.go` (`History` and `RecentlyAdded` gain a time window), `internal/integrations/plexsvc.go` (`GET /api/plex/activity` accepts `limit`, `from`, `to`)
- Test: `internal/mcp/tools_read_test.go`, `internal/integrations/plex/plex_test.go`

**Interfaces:**
- Consumes: `addTool`, `call` (Task 4).
- Produces tools (level `read`, `ReadOnly` true), each with short, stable output fields and a `Needs` capability where the feature can be missing: `system_overview` (`/api/system/info`, `/api/system/metrics`, `/api/system/storage`, update counts from `/api/pkg/updates`, `/api/brew/status`, `/api/containers/updates`), `host_info` (`/api/system/info` including `specs` and `specs.desktop`, plus `/api/system/self` for Wargo's version and mode; no public IP), `storage`, `list_processes` (`sort`: `cpu`|`memory`, `limit` default 15 max 100), `list_containers`, `container_logs` (`lines` default 100 max 500; lines passed through the audit redaction patterns), `updates_status`, `recent_events` (`limit` default 50 max 200), `notifications`, `jobs`, `job_status` (`id`), `torrents`, `torrent_search` (`query`, `limit` default 10 max 25; each result has a `resultId` usable by `torrent_add`), `media_activity`, `approval_status` (stub returning `not_found` until Task 6).
  - `plex.Client.History(ctx, n int, from, to time.Time) ([]Metadata, error)` sends `viewedAt>=` / `viewedAt<=` (unix seconds) when set; `RecentlyAdded(ctx, n int, from, to time.Time)` sends `addedAt>=` / `addedAt<=`, and if Plex ignores them (verify against the fake and note it), filters on Wargo's side after paging; zero times mean no bound. Existing callers pass zero times and keep `n = 8`.
  - `media_activity` output: `{ "streams": [...], "watched": [...], "added": [...] }`, every entry with `provider: "plex"`, title fields, `at` (RFC 3339), and for streams user, player, progress; an unknown `provider` input returns an error naming the known ones.
  - Read tools write their audit entries with `WriteFolded(r, ev, 10*time.Minute)` under action `mcp.read`, target the tool name (ruling on the window: 10 minutes).
- [ ] **Step 1: Failing tests:** one test per tool against the feature fakes the existing tests use (Docker fake client, transmissiontest fake, Prowlarr test server, Plex test server), asserting the key output fields; `TestMediaActivityDateRange` (fake Plex receives `viewedAt>=` and `viewedAt<=`; `limit` 60 capped to 50); `TestMediaActivityProviderField`; `TestContainerLogsRedacted` (a line holding `password=hunter2` comes back redacted); `TestReadCallsFold` (5 `list_containers` calls -> one `mcp.read` entry with count 5); `TestHostInfoIncludesDesktopAndSpecs` (output has hostname, OS, kernel, CPU model, memory, desktop name and version when detected, Wargo version; no public IP field).
- [ ] **Step 2: Run**; expect FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/mcp/ ./internal/integrations/...`; expect PASS.
- [ ] **Step 5: Commit** `Add read-only MCP tools`.

### Task 6: Pending approvals

**Files:**
- Create: `internal/mcp/approvals.go`, `internal/mcp/approvals_http.go`
- Modify: `internal/notify/notify.go` (category `AgentApproval Category = "agent.approval"`, in `Categories`, default `ModePush`), `internal/mcp/tools_read.go` (`approval_status`)
- Test: `internal/mcp/approvals_test.go`

**Interfaces:**
- Produces:
  - `type Pending struct { ID string; Agent audit.AgentRef; AgentID string; Tool string; Args json.RawMessage; Method, Path string; Body json.RawMessage; Summary string; Created, Expires time.Time; State string /* pending|approved|denied|expired */; Result json.RawMessage; JobID string; Ref int64 }`
  - `type Approvals struct` (in memory, mutex): `Create(ctx, agent Agent, spec ToolSpec, args, method, path string, body any, summary string) (Pending, error)` (writes `mcp.approval.requested`, source agent, and keeps its id as `Ref`; raises `notify.AgentApproval` with link `/approve/<id>`), `Get(id)`, `List()`, `Deny(r, id)`, `Approve(r *http.Request, id string) (*Response, error)`, and expiry (a request past `Expires` becomes `expired`, writing `mcp.approval.expired` once).
  - `Approve` replays the stored method, path and body unchanged through `call`, with the agent identity, plus the approver's `X-Wargo-Elevation` header, `Origin`, client IP and user agent copied from `r`, and `audit.WithSession` set to the approver's session tag. If the handler answers `elevation_required`, `Approve` returns that error unchanged (state stays pending) so the browser's existing elevation flow prompts and retries. Otherwise the state becomes approved, `Result`/`JobID` are kept, and `mcp.approval.approved` is written with `Ref` to the request entry and the elevation method (or `none`). The action's own audit entry (written by the handler) gets `Ref` to the request entry.
  - Browser API (cookie session only, refuse agent context): `GET /api/mcp/approvals`, `GET /api/mcp/approvals/{id}`, `POST /api/mcp/approvals/{id}/approve`, `POST /api/mcp/approvals/{id}/deny`.
  - `func (s *Service) approvalURL(id string) string`: first entry of `auth.origins` + `/approve/<id>`, or `""` when none.
  - Agent-facing result for a new request: if the client declared URL elicitation, return the SDK's URL-elicitation-required error (`mcp.URLElicitationRequiredError`, code -32042) with that URL, the id as elicitation id and a message naming the action; otherwise a tool result `{ "approval": { "id", "state": "pending", "expires", "url" } , "message": "Waiting for the owner's approval in Wargo; check approval_status." }`. With no URL, never use elicitation.
  - `approval_status(id)` returns state, and when approved the result or job id; only for the calling agent's own requests.
- [ ] **Step 1: Failing tests:** `TestApproveRunsStoredRequestUnchanged` (Review Focus 3: the replayed body equals the stored body byte for byte); `TestApproveTwiceRefused`; `TestApproveAfterExpiryRefused`; `TestDeny`; `TestElevationRequiredPassesThrough` (handler wants a credential; first approve returns `elevation_required`; second with header succeeds); `TestApprovalEndpointsRefuseAgentContext`; `TestApprovalStatusOnlyOwnRequests`; `TestURLElicitationWhenDeclared` and `TestPlainResultWhenNotDeclared`; `TestNoURLWithoutOrigins`; `TestApprovalAuditChain` (requested -> action entry -> approved, linked by `Ref`); `TestApprovalNotification` (category `agent.approval`, link `/approve/<id>`).
- [ ] **Step 2: Run**; expect FAIL.
- [ ] **Step 3: Implement.** Expiry uses `Settings.ApprovalMinutes` at creation time.
- [ ] **Step 4: Run** `go test -race ./internal/mcp/ ./internal/notify/`; expect PASS.
- [ ] **Step 5: Commit** `Hold admin requests from agents for the owner's approval`.

### Task 7: Action and admin tools

**Files:**
- Create: `internal/mcp/tools_act.go`
- Test: `internal/mcp/tools_act_test.go`

**Interfaces:**
- Consumes: `addTool`, `call`, `Approvals.Create`.
- Produces:
  - Level `operate`: `container_action` (`id`, `action`: `start`|`stop`|`restart`; the self-container guard stays, so stopping Wargo's own container is refused), `torrent_add` (`magnet` or `resultId`, exactly one), `torrent_action` (`id`, `action`: `start`|`stop`|`remove`; remove always keeps data), `check_updates` (`/api/pkg/check`, `/api/brew/check`, `/api/containers/check-updates`; a part that answers `elevation_required` becomes a pending request), `process_signal` (`pid`; TERM only; `elevation_required` becomes a pending request).
  - Level `admin`, `Admin: true`, always pending first: `apply_os_updates` (calls `GET /api/pkg/precheck` first; if anything blocks the upgrade it returns the blockers and creates nothing; never sends `resolveConflicts`), `upgrade_homebrew`, `update_container` (`id`; refuses Wargo's own container before creating anything).
  - An operate tool's `elevation_required` result is converted into a pending request with the same stored request.
- [ ] **Step 1: Failing tests:** per tool against the existing fakes (happy path, audit entry source agent with the action name the handler uses, e.g. `containers.restart`); `TestTorrentRemoveKeepsData` (fake Transmission receives `delete-local-data: false`); `TestUpdateContainerRefusesSelf`; `TestApplyOSUpdatesStopsOnBlockers` (no pending request, blockers listed); `TestApplyOSUpdatesNeverSendsResolveConflicts`; `TestAdminToolsCreatePendingFirst` (no handler call before approval); `TestAdminReplayWithoutCredentialHasNoSideEffects` (Review Focus 4: for `apply_os_updates`, `upgrade_homebrew`, `update_container`, replaying without a credential either completes the action as the UI would without one, or answers `elevation_required` with no audit `*.started` entry, job, or store write before it); `TestProcessSignalNeedsApprovalOnEPERM`.
- [ ] **Step 2: Run**; expect FAIL.
- [ ] **Step 3: Implement.** If Step 1's side-effect test fails for a handler, move that handler's elevation check ahead of its side effects (smallest change, same behaviour for the browser).
- [ ] **Step 4: Run** `go test -race ./internal/mcp/ ./internal/pkg/ ./internal/brew/ ./internal/docker/ ./internal/procs/`; expect PASS.
- [ ] **Step 5: Commit** `Add MCP tools that act, with approval for admin actions`.

### Task 8: Web: Settings → Agents, approval page, audit and notifications

**Files:**
- Create: `web/src/features/settings/Agents.tsx`, `web/src/features/mcp/api.ts`, `web/src/features/mcp/Approve.tsx`, `web/src/features/mcp/snippets.ts`, `web/src/features/mcp/snippets.test.ts`
- Modify: `web/src/features/settings/Settings.tsx` (section `agents`, "Agents"), `web/src/app/routes.tsx` (`/approve/:id`), `web/src/app/session.ts` (capability `mcp?: { enabled: boolean }`), `web/src/features/system/Audit.tsx` (source `agent` with its own icon and label "Agent", the "By" filter gains "Agents" and a per-agent choice, rows show the agent's name and tool), `web/src/features/settings/NotificationsCard.tsx` (category label "Agent approvals"), `web/src/features/notifications/api.ts` (category type)
- Test: `web/src/features/mcp/snippets.test.ts`

**Interfaces:**
- Consumes: the browser APIs from Tasks 2 and 6.
- Produces:
  - Settings → Agents: switch "Allow agents (MCP)"; "Where agents can connect from" (Local network only / Anywhere); "Approval time limit" (minutes, 1 to 60); agents list (name, access, connected, last used and from where, change access, revoke with confirmation); "Connect an agent" dialog (name, access level with one-line explanations) that shows the token once with copy buttons and setup snippets; pending requests list linking to `/approve/<id>`.
  - `snippets.ts`: `function claudeCode(url: string, token: string): string` -> `claude mcp add --transport http wargo <url> --header "Authorization: Bearer <token>"`; `function genericJSON(url: string, token: string): string` -> a JSON block `{ "mcpServers": { "wargo": { "type": "http", "url": <url>, "headers": { "Authorization": "Bearer <token>" } } } }`; `function mcpURL(origin: string): string` -> `<origin>/mcp`.
  - Approval page `/approve/:id`: agent name, tool, summary of exactly what will run, time left (live countdown), Approve (POST approve; the existing `api.ts` elevation retry shows the passkey/sudo dialog when the server answers `elevation_required`), Deny; states for approved (with result or a link to the job), denied, expired, not found.
- [ ] **Step 1: Failing tests** in `snippets.test.ts`: exact strings for `claudeCode` and `genericJSON` with a sample URL and token; `mcpURL("https://wargo.example")` is `https://wargo.example/mcp`.
- [ ] **Step 2: Run** `cd web && npm test`; expect FAIL.
- [ ] **Step 3: Implement** the UI with the existing components (`Card`, `SettingRow`, `Switch`, `Dialog`, `ConfirmDialog`, `Badge`, `toast`, `CodeBlock`).
- [ ] **Step 4: Run** `cd web && npm test && npm run build`; expect PASS.
- [ ] **Step 5: Commit** `Add Agents settings, approval page and agent audit entries to the web app`.

### Task 9: Docs and an end-to-end check

**Files:**
- Create: `docs/MCP.md` (enable, connect an agent per harness, access levels, approvals, audit, network rule, the tool list)
- Modify: `docs/SECURITY.md` (an "Agents (MCP)" section: tokens, scopes, network rule, approvals, what is never exposed), `README.md` (one line and a link)
- Test: `internal/mcp/e2e_test.go`

- [ ] **Step 1: Failing test** `TestEndToEnd`: with the SDK client over `httptest`: enable MCP, create an `admin` agent, `tools/list` contains the expected names, `list_containers` works, `update_container` returns a pending approval, approving through the browser API (test session) runs it, `approval_status` reports approved, and the audit log holds the chain with source agent.
- [ ] **Step 2: Run**; expect FAIL until docs and wiring are complete (the test may already pass; then proceed).
- [ ] **Step 3: Write the docs.** No dashes as punctuation.
- [ ] **Step 4: Run** the full checks from Global Constraints; expect PASS.
- [ ] **Step 5: Commit** `Document MCP for agents`.
