# Letting agents use Wargo over MCP

Date: 2026-11-25. Status: design, awaiting the owner's approval. Reviewed by a builder check; its questions are answered below. Calls marked *Claude's call* were not discussed with the owner and can be overruled.

## Why

Wargo already does, behind its web UI, most of what an always-on agent
(OpenClaw, Hermes, or a coding harness like Claude Code, OpenCode or Pi)
would want to do on the owner's server: check health, look at containers,
find and queue downloads, check and apply updates. An agent without Wargo
does this with shell commands: several calls per task, its own parsing, its
own retries, sudo, and nothing stopping it from doing something the owner
would never allow from the UI.

Exposing Wargo's features as MCP tools gives an agent the same guardrails the
web UI has (validation, safe defaults, confirmation of admin actions, the
audit log) in one tool call per task.

The owner's own case: OpenClaw runs around the clock on Deimos, which runs
Wargo in container mode behind a Cloudflare tunnel.

## Goals

- MCP is off until the owner turns it on in Settings. While it is off, the
  MCP endpoint does not exist.
- Each agent harness is authorized once by the owner before it can call
  anything, and each authorization can be revoked on its own.
- An agent does common Wargo tasks in about one tool call each, through a
  small set of task-shaped tools (about 20 to 30), not one tool per API route.
- Every tool runs through the same code the web UI uses, so the same checks,
  safe defaults and audit entries apply.
- Admin actions (those that need sudo or a passkey in the web UI today) can
  be requested by an agent but only run after the owner approves that exact
  action in Wargo, with their passkey or sudo password, within a time limit.
  The agent never sees or handles the owner's credentials.
- Every action invoked over MCP can be identified in the audit log: which
  agent, which tool, what it did, and for admin actions, that the owner
  approved it.

## Non-goals

- A terminal, running arbitrary commands, or reading and writing files over
  MCP. That is the "agent with a shell" situation this replaces.
- Managing Wargo itself over MCP: sign-in and security settings, MCP settings
  and agent tokens, integration credentials, notification settings.
- Power actions (reboot, shut down) in the first version.
- OAuth sign-in for MCP clients in the first version (see Authorization).
- MCP resources and prompts; tools only.
- Plex actions Wargo does not have today (library scans, playback
  control): those would be new Plex features first, then tools.
- Jellyfin and Emby integrations: Wargo has neither today. `media_activity`
  is shaped so they slot in later (note that Jellyfin keeps a full play
  history only with its Playback Reporting plugin).
- The GitHub Pages demo (MSW handlers): a follow-up.

## Anti-goals (a failure even if it works)

- An agent obtaining, relaying or replaying the owner's sudo password or a
  passkey assertion.
- An agent's action recorded in the audit log as if the owner did it, or
  with no way to tell which agent did it.
- An approval running anything other than exactly what the owner was shown.
- An agent approving its own request, creating tokens, or widening its own
  access. Agent tokens are accepted only on the MCP endpoint, never on
  `/api/*`, so nothing an agent holds can reach the approval or settings
  endpoints.
- A read-only agent changing anything.
- MCP reachable while it is turned off, or from the internet while it is set
  to local network only.
- An agent that polls every minute crowding the owner's own entries out of
  the audit log.

## Approaches considered

- **A. MCP built into Wargo (chosen by the owner).** Wargo serves the MCP
  endpoint itself with the official Go MCP library. Tools reuse the code the
  web UI uses, so validation, audit and approvals are shared. One binary, both
  native and container mode. Costs a new dependency and a tool layer in the
  server.
- **B. A separate MCP bridge** calling Wargo's HTTP API with a token. Keeps
  Wargo's core nearly unchanged, but is a second program to install and keep
  in step, still needs Wargo-side tokens, approvals and agent identity, and
  turns each tool into several HTTP calls.
- **C. Tools generated from the HTTP routes.** Quick and complete, but gives
  agents a long list of UI-shaped tools (against "few tool calls"), and makes
  it easy to expose an admin route by accident.

### How tools reach Wargo's code (Claude's call)

Most features keep their logic in their HTTP handlers (input checks, audit
entries, the "elevation required" response). Moving it all into
handler-independent functions first would be a large refactor across every
feature. Instead, **a tool calls the existing handler in process**: the tool
layer builds the request a browser would send (method, path, JSON body),
attaches the agent's identity to its context, and serves it through Wargo's
own API router without going through cookie sign-in. The handler then does
its usual checks and writes its usual audit entry, now marked as the agent's.

Rules that keep this safe:

- A tool can only call the routes it declares; the tool layer has a fixed
  table from tool to route(s), and nothing else is reachable.
- The access level check happens in the tool layer before any call.
- The agent's identity is a distinct context value, not a fake browser
  session: handlers and `audit` can tell the two apart, and anything that
  requires a browser session (sessions, passkeys, settings) refuses it.
- Responses are reshaped by the tool into what an agent needs (short,
  stable fields), not passed through raw.

Features whose logic already lives in callable functions (for example the
Transmission client, the metrics collector) may be called directly where
that is simpler, as long as they write the same audit entries.

## Design

### Turning MCP on

Settings gets a new section, **Agents**, with:

- **Allow agents (MCP)**: off by default. While off, `/mcp` answers 404,
  existing agent tokens stay stored but are not accepted, and no MCP code
  runs. Turning it off takes effect for calls already in flight at their
  next request.
- **Where agents can connect from**: "Local network only" (default) or
  "Anywhere". Local means loopback, private ranges (RFC 1918, IPv6 ULA and
  link-local) and the networks in a new optional `mcp.allowed_networks`
  config entry. The address used is the client address as Wargo already
  works it out (`trusted_proxies`). A request that arrives with forwarding
  headers (`X-Forwarded-For`, `CF-Connecting-IP`) from a proxy that is not
  trusted counts as not local, so a tunnel (like Deimos's cloudflared, which
  connects from the local machine) cannot make internet traffic look local.
- **Approval time limit**: how long an admin request waits for approval.
  Default 15 minutes, from 1 to 60.
- The list of connected agents (below).

The capabilities response gains `mcp: { enabled }` so the UI knows.

### Connecting an agent (authorization)

"Connect an agent" asks for:

- a name ("OpenClaw on Deimos"),
- an access level:
  - **Read only**: tools that look but change nothing.
  - **Everyday actions**: also tools that change things without admin
    rights (start or stop a container, add or remove a torrent).
  - **Admin requests**: also tools for admin actions, each of which still
    waits for the owner's approval.

Wargo then shows a token once, with ready-to-paste setup for common clients
(Claude Code's `claude mcp add --transport http` with an `Authorization`
header, and a generic JSON config for others). Tokens:

- are random (at least 256 bits), with a recognisable prefix (for example
  `wargo_`) so secret scanners and Wargo's audit redaction catch them;
- are stored only as hashes, like sessions;
- are sent as `Authorization: Bearer <token>` on every MCP request, never in
  the URL;
- are accepted only on `/mcp`.

The agents list shows name, access level, when it was connected, when and
from which address it was last used, and a Revoke button that takes effect
immediately. The access level can be changed without a new token. Creating,
changing and revoking agents need a signed-in browser session and are
audited as the owner's actions.

Failed token attempts are throttled per client address with Wargo's existing
sign-in throttle.

**OAuth** (the MCP specification's sign-in flow, where the client opens the
browser and the owner signs in to Wargo) is left for later: tokens work in
every harness the owner named, including headless ones, and the spec makes
authorization optional. The 401 response should still follow the spec's
shape (`WWW-Authenticate`) so OAuth can be added without breaking clients.
*Claude's call.*

### Transport

Streamable HTTP at `/mcp` on Wargo's existing listener, using the official Go
SDK (`github.com/modelcontextprotocol/go-sdk`, v1.8.0 at the time of
writing; it supports spec revisions up to 2026-07-28, bearer token
middleware and URL elicitation). Whether to run it stateless is the
builder's choice; stateless is what the newest spec revision expects and
avoids keeping MCP sessions in memory. The existing Host check (DNS
rebinding) applies; a request with an `Origin` header must pass Wargo's
existing origin check.

### Tools (first version)

Names and exact inputs are the builder's; the set, and the access level of
each, is the design. Each tool carries MCP annotations (read-only,
destructive, idempotent) that match what it does.

Read only:

- `system_overview`: host, uptime, load, CPU, memory, temperatures, disk
  usage, network rates, and pending update counts, in one call.
- `host_info`: what the About page shows about the host: hostname, model
  and board, CPU, memory, GPUs, architecture, virtualization, OS and
  kernel, init, packages, shell, timezone, locale, local IP, boot time, the
  desktop (name, version, Wayland or X11, compositor, session owner), and
  Wargo's version and mode. Kept apart from `system_overview` because it
  rarely changes. The public IP is left out (it comes from an outside
  service). *Claude's call.*
- `storage`: drives and filesystems.
- `list_processes`: top processes by CPU or memory, with a limit.
- `list_containers`: containers with state, health, image, update available.
- `container_logs`: the last lines of a container's log (capped, redacted).
- `updates_status`: pending OS, Homebrew and container updates, with what
  the last check found and when it ran.
- `recent_events`: recent system events (as on the Events page).
- `notifications`: current notifications.
- `jobs` / `job_status`: running and recent long jobs (upgrades) and their
  progress and result.
- `torrents`: downloads with progress and status.
- `torrent_search`: search Prowlarr, returning a short ranked list with ids
  that `torrent_add` accepts.
- `media_activity`: current streams, recently watched and recently added on
  the owner's media servers, so an agent can confirm that a download has
  reached the library. Named for media in general, not one server (owner's
  decision): every entry has a `provider` field naming the server it came
  from (`plex` today; `jellyfin` or `emby` once those integrations exist),
  and the tool takes an optional `provider` input to ask one server only.
  With several servers set up it merges their lists by time. Today it
  covers Plex, with the same data as Wargo's Plex view, and appears only
  when a media integration is set up and reachable. Adding a provider later
  is a new data source for this tool, not a new tool. Wargo's page shows
  the 8 latest plays and additions;
  the tool takes an optional `limit` (default 10, at most 50) for each list,
  and every entry carries its watched or added time. It also takes an
  optional date range (`from`, `to`) for plays and additions, with the limit
  applied inside the range, so "what did I watch about a year ago" is one
  call. Wargo asks Plex for just that window: checked on the owner's Plex
  (1,931 plays kept, back to 2018), `/status/sessions/history/all` filters
  with `viewedAt>=` and `viewedAt<=` on Plex's side. Whether
  `/library/recentlyAdded` filters by `addedAt` the same way is still to
  confirm; if not, Wargo pages on its side, still one tool call for the
  agent. It can only report history Plex has kept. *Claude's call.*
- `approval_status`: the state of an admin request (below).

Everyday actions:

- `container_action`: start, stop or restart (the self-container guard
  stays).
- `torrent_add`: a magnet link or a search result id.
- `torrent_action`: start, stop, or remove keeping files. Removing files is
  not offered over MCP. *Claude's call.*
- `check_updates`: run the update checks now, where that needs no admin
  rights.
- `process_signal`: ask a process to stop (TERM), for processes Wargo can
  signal without admin rights.

Admin requests (each waits for approval):

- `apply_os_updates`, with the same pre-check the UI shows. When packages
  block the upgrade, the tool reports them and stops: an agent cannot ask
  for conflicting packages to be removed; the owner does that in the UI.
  *Claude's call.*
- `upgrade_homebrew`,
- `update_container` (Wargo's own container excluded: updating it restarts
  Wargo; the owner does that in the UI. *Claude's call.*),
- `process_signal` when the process needs admin rights,
- `check_updates` where refreshing package lists needs admin rights.

Tools only appear in the tool list when their feature is available
(capabilities) and the agent's access level allows them.

**Untrusted text.** Several outputs carry text from third parties: torrent
and container names, log lines, process command lines, event messages. Tools
return them in clearly named fields, never mixed into instructions, and tool
descriptions say such fields are data. Logs are capped and run through the
audit redaction patterns. Nothing in a tool result is pre-formatted as
something the agent should follow.

### Admin approval

**Every admin tool waits for approval, always** (owner's decision), not
only when the handler would ask for sudo or a passkey. Some of these actions
need no credential in the web UI today (updating a container, Homebrew on
Linux and in container mode); over MCP they still wait for the owner. So the
tool layer creates the pending request before calling any handler, and
nothing in the admin list reaches a handler unapproved. The same applies when
an everyday tool reaches a handler that answers "elevation required" (for
example `process_signal` on another user's process): it becomes a pending
request.

Wargo records a **pending request**:

- the agent, the tool and its arguments,
- the exact internal request to run (method, path, body),
- the action and target the handler named, and a plain description for the
  approval page,
- created and expiry time (the approval time limit).

Pending requests are kept in memory; a restart drops them (the agent sees
them as expired). *Claude's call.*

The agent gets back, at once (no waiting inside the tool call):

- If its client declared URL elicitation, an MCP URL-elicitation-required
  error pointing at the approval page, so the client shows the owner the
  link right away.
- Otherwise, a normal result: the request id, the approval page link, the
  expiry, and the instruction to check `approval_status`.

**The link's address** (Claude's call): the agent usually connects by
loopback, a LAN address or a Docker name, which is useless on the owner's
phone, and a passkey only works on the address it was registered on. So the
absolute link uses the first entry of `auth.origins` (the address the owner
configured for their browsers, such as Deimos's tunnel domain). With no
origin configured, the agent gets no link and no URL elicitation, only
"approve it in Wargo", and the owner approves from the notification or the
pending list in Wargo.

Wargo also raises a notification in a new category, **Agent approvals**
(push by default when push is set up), linking to the approval page.

The **approval page** (`/approve/<id>` in the web app) needs a normal
signed-in session. It shows the agent's name, the tool, exactly what will run
(the same summary and details the UI shows for that action), and the time
left. **Approve** asks for the passkey or sudo password through the existing
elevation dialog when the action needs one (as it would in the web UI);
for an action the web UI runs without a credential, Approve is a plain
click from the signed-in session. **Deny** refuses it. The link itself
carries no credentials or sign-in (the spec forbids pre-authenticated
links).

On approve, Wargo runs the stored internal request, unchanged, with the
owner's elevation credential attached when there is one, still under the
agent's identity and marked as approved by the owner's session. It runs immediately, whether or
not the agent is asking; long actions become jobs as today. The approval
endpoint refuses a request that is expired, already decided, or whose
stored action no longer matches what the page was showing.

`approval_status` returns pending, denied, expired, or approved with the
action's result (or its job id for long actions).

The owner expects admin requests only while they are around (their agent
asks while they are working with it), which is why requests expire quickly
by default rather than waiting for the morning.

### Audit

- A new source, **agent**, beside user, system and job.
- Every entry written while serving an MCP call has source agent and
  records the agent's id and its name at the time, and the tool name. The
  client address and user agent are recorded as for any request.
- Actions are written by the handlers as today (same action names, targets
  and details), so an agent's `containers.restart` reads like the owner's,
  except for the source and the agent.
- An admin request writes a request entry when it is created; the action's
  entry links to it (the existing `ref` link); expiry and denial are written
  too. The action's entry records the elevation method (or none, for a
  plain-click approval) and that the owner's session approved it.
- Read-only calls are recorded compactly: identical calls from the same
  agent close together are folded into one entry with a count, so polling
  never floods the log. *Claude's call* on the folding window.
- Creating, changing and revoking agents, and turning MCP on or off, are the
  owner's actions (source user).
- The Audit page shows agent entries with their own icon and the agent's
  name, and can filter to agents, and to one agent.

### Web UI

- Settings → Agents: the switch, where agents can connect from, the approval
  time limit, the agents list, Connect an agent (with the one-time token and
  setup snippets), change access, revoke.
- The approval page, and a list of pending requests in Settings → Agents
  (each opening its approval page).
- Audit page changes above.
- The Agent approvals notification category in notification settings.

## Testing

- Off means off: `/mcp` is 404 and tokens are refused while disabled.
- Tokens: stored hashed, refused on `/api/*`, refused after revoke, throttled
  on failure, never logged.
- Local network only: allowed and refused addresses, including forwarded
  requests through a proxy that is and is not trusted.
- Access levels: a read-only agent cannot call an action tool, and the tool
  layer cannot reach a route a tool does not declare.
- Each tool against the fakes the feature tests already use (Docker,
  Transmission, Prowlarr, package backends), including its audit entry with
  source agent.
- Approval: request, approve (passkey and sudo), deny, expiry, double
  approve, approve after expiry, the stored request runs unchanged, the
  agent cannot reach the approval endpoint, `approval_status` in each state,
  and the URL elicitation response for clients that declare it.
- Audit folding of repeated read-only calls.
- Web: the Agents settings, the approval page, the Audit page's agent
  filter.

## Left to the builder

- Tool names, input and output schemas, and descriptions.
- Storage layout for agents and settings, and where the code lives.
- Stateless or stateful Streamable HTTP.
- How the in-process call is wired (constructing the request, the context
  values) within the rules above.
- The read-only folding window and how the count is shown.
- Exact Settings and approval page layout and wording.
