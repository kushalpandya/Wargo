# Wargo for AI agents (MCP)

Wargo can let an AI agent look at your server and, if you allow it, do a few
things on it. The agent talks to Wargo over
[MCP](https://modelcontextprotocol.io) (the Model Context Protocol), the
common way coding assistants and agent harnesses use tools. You decide which
agents exist, what each may do, and you approve anything that needs admin
rights yourself, in Wargo, every time.

This is off until you turn it on. Nothing here changes how you use Wargo
yourself.

## Turn it on

Open **Settings → General** and turn on the switch in the **Agents (MCP)**
card's header. The card then shows the **URL** agents connect to, which is
your Wargo address followed by `/mcp`, for example
`https://wargo.example.com/mcp`. Turning the switch off hides the rest of the
card but keeps its settings and your agents for next time.

Under **Access**, choose where agents can connect from:

- **Local network only** (the default): this machine and private addresses
  (`127.0.0.1`, `10.x`, `172.16-31.x`, `192.168.x`, IPv6 local addresses).
  Anything from the internet is refused.
- **Anywhere**: any address that can reach Wargo, as long as it has a valid
  token. Use it when agents run somewhere else and reach Wargo through your
  tunnel or proxy.

Two things to know about the network rule:

- A reverse proxy on the same machine that adds no forwarding headers (no
  `X-Forwarded-For` and the like) looks like a local client, so "Local network
  only" lets it through. Proxies that forward real client addresses are
  handled correctly: Wargo looks at the visitor, not the proxy.
- With **Anywhere** behind a tunnel or proxy, set `auth.trusted_proxies` (see
  [REVERSE_PROXY.md](REVERSE_PROXY.md)) so Wargo sees each client by its own
  address. Without it every request appears to come from the proxy, which
  weakens the throttling of wrong tokens.

If your private network uses other ranges (a VPN such as Tailscale, for
example), add them to the config file and they count as local:

```toml
[mcp]
allowed_networks = ["100.64.0.0/10"]
```

If you protect Wargo with Cloudflare Access or another login proxy, agents
can't pass its login page. Give agents a way through for the `/mcp` path (for
example an Access service token sent as extra headers), or keep them on the
local network.

The **approval timeout** (1 to 60 minutes, 15 by default) is how long an
admin request waits for you.

You can turn MCP off again at any time. The next request from every agent is
refused, even on connections that are already open.

## Connect an agent

Choose **Connect an agent** under **Authorized agents** in Settings → General
and fill in:

- **A name**, such as "Claude Code on my laptop". It appears in the audit log.
- **An access level**:
  - **Read only**: tools that look but change nothing.
  - **Everyday actions**: also start, stop or restart containers, add,
    start, stop or remove torrents (and delete their downloaded files),
    check for updates, and ask a process to stop. None of these need admin
    rights.
  - **Admin requests**: also ask for admin actions (installing OS or
    Homebrew updates, updating a container). Each one waits for your
    approval.

Wargo then shows the agent's **token** once. Copy it now: it can't be shown
again. If you lose it, revoke the agent and connect it again. Wargo keeps only
a hash of the token. You can rename an agent, change its level, or revoke it
in the same list. A revoked token stops working on the next request. A changed
level applies to the next tool call (an agent that is already connected may
keep seeing its old tool list until it reconnects, but it can't call anything
its new level doesn't allow).

### Claude Code

Run this where the agent runs, with your address and token:

```sh
claude mcp add --transport http wargo https://wargo.example.com/mcp --header "Authorization: Bearer <token>"
```

### Other agents

Most MCP clients accept a JSON file of servers. Use this and adjust the
address and token:

```json
{
  "mcpServers": {
    "wargo": {
      "type": "http",
      "url": "https://wargo.example.com/mcp",
      "headers": { "Authorization": "Bearer <token>" }
    }
  }
}
```

Agent harnesses such as OpenCode, Pi, Hermes and OpenClaw use the same
address and the same `Authorization: Bearer <token>` header. Their config
syntax differs, so use the values above with the generic config and check
their own documentation for where it goes.

### Tell the agent to use it

Agents pick tools more reliably when their standing instructions mention
them. A line like this in the agent's instructions (for Claude Code, its
`CLAUDE.md`) works well:

```
For anything about <hostname>, use the Wargo tools first.
```

The dialog that shows the token includes this line with your server's name filled in.

## Approvals

Tools that change something only the admin may change don't run when the agent
calls them. The agent gets a pending request instead, and the action waits
for you. These tools wait:

- `apply_os_updates`
- `upgrade_homebrew`
- `update_container`

You are told in three ways: a notification (category **Agent approvals**, in
the bell and by push if you enabled it), the list under **Waiting for
approval** in Settings → General, and a link to the approval page
(`/approve/<id>`) that the agent can pass on. The agent may show the link, or
open it for you if its client supports that.

The approval page shows what was asked, by which agent, and when the request
expires. **Approve** runs exactly that request and nothing else. **Deny**
drops it. If the web app would normally ask for your password or a passkey for
that action (as it does when you do it yourself), the approval page asks the
same, because the approval is you doing the action. If it wouldn't, a plain
click is enough. The password or passkey goes straight from your browser to
Wargo. It never passes through the agent. While an approved request is
running, the page and the list show it as running and the buttons are off.

The agent can follow a request with `approval_status` (pending, approved,
denied or expired). Once approved, it reports the result, or the id of the
long job that was started, which `job_status` follows.

- **Expiry.** A request that nobody decides expires after the time limit and
  nothing runs. The agent can ask again.
- **Revoke while waiting.** Revoking an agent denies every request it still
  has waiting, and their notifications are marked as dealt with.
- **Disable or lower the level while waiting.** If you turn MCP off, or lower
  the agent's level while a request waits, approving it does nothing and the
  page says why. The request stays listed until it expires.
- **Restart.** Pending requests are kept in memory only. A restart of Wargo
  drops them, marks their notifications as dealt with, and the agent has to
  ask again.
- **Asking again.** An agent that asks for the same action again while its
  request still waits gets that same request back, with no new notification.
- Each agent may have at most 10 requests waiting, and all agents together at
  most 50.

## The audit log

Everything an agent does shows up in System Monitor → Audit with the source
**Agent** and the agent's name and the tool. Use the source filter to see all
agents or one agent.

- Read tools write one entry per tool, and repeated calls to the same tool in
  quick succession are folded into one entry with a count, so a chatty agent
  doesn't bury everything else.
- Refused calls are logged as "refused", folded the same way: a wrong or
  revoked token (with the client's address, never the token), and a tool the
  agent's level does not allow or that is not available right now.
- A change an agent makes is logged like any other change (for example a
  container update), marked as done by the agent.
- An admin request leaves a chain: "requested" (by the agent), the action
  itself (by the agent, linked to the request, with the session that approved
  it and how: password, passkey or none), and "approved" or "denied" (by you).
  A request that runs out of time logs "expired".
- Creating, renaming, changing and revoking agents, and changes to the MCP
  settings, are logged as yours.

## Tools

What an agent sees depends on its level and on what your host has: tools for
Docker, Transmission, Prowlarr, Plex, package updates or Homebrew only appear
when that feature is available. A higher level includes everything below it.

### Read only

- `system_overview`: uptime, load, CPU, memory, temperatures, disk usage,
  network rates and pending updates.
- `host_info`: hostname, model, CPU, memory, GPUs, OS and kernel, local IP,
  boot time, Wargo's version and mode.
- `storage`: drives and filesystems, with sizes, free space and temperatures.
- `list_processes`: top processes by CPU or memory, with secrets in command
  lines masked.
- `list_containers`: Docker containers with state, health and available
  updates.
- `container_logs`: the last lines of a container's log, with secrets masked.
- `updates_status`: pending OS, Homebrew and container updates and when they
  were last checked (at most 100 packages listed per source, with the full
  count).
- `recent_events`: errors and warnings of the last 24 hours, grouped, with
  secrets masked.
- `notifications`: Wargo's current notifications.
- `jobs`: running and recent long jobs.
- `job_status`: one job's progress, result and log tail, with secrets masked.
- `torrents`: what Transmission is downloading, active torrents first (50 by
  default, at most 200, with the total).
- `torrent_search`: search Prowlarr for torrents.
- `media_activity`: what Plex is playing, what was watched and what was added.
- `approval_status`: the state of the agent's own admin requests.

### Everyday actions

- `container_action`: start, stop or restart a Docker container (not Wargo's
  own).
- `torrent_add`: download a torrent from a magnet link or a `torrent_search`
  result.
- `torrent_action`: start, stop or remove a torrent. Removing keeps the
  downloaded files unless the agent asks to delete them too, as the web app's
  "Remove and delete data" does.
- `check_updates`: check for OS, Homebrew and container updates now. A part
  that needs admin rights waits for your approval.
- `process_signal`: ask a process to stop (SIGTERM). A process Wargo may not
  signal on its own waits for your approval.

### Admin requests

All three wait for your approval before anything happens.

- `apply_os_updates`: install pending OS package updates. If something blocks
  the upgrade (the package manager is busy, low disk space), the agent is told
  and nothing is requested.
- `upgrade_homebrew`: install pending Homebrew updates.
- `update_container`: pull and recreate a container from its latest image
  (not Wargo's own).

## What agents can never do

There is no tool, at any level, for:

- the terminal or running commands,
- browsing, reading or changing files,
- power actions (shutdown, reboot) or restarting Wargo,
- removing conflicting packages during an OS upgrade,
- updating or stopping Wargo's own container,
- changing Wargo's own settings, accounts, passkeys or other agents.

Tokens work only on `/mcp`. They don't sign in to the web app or reach any
other part of Wargo.

Names, log lines and other text that tools return come from containers,
processes and torrents, not from you. A well-behaved agent treats them as
data, but a careless one could be fooled by a container that is named like an
instruction. That is one reason the dangerous actions need your approval and
are limited to the three above. The exception is deleting a torrent's files,
which an Everyday actions agent can do as you can from the web app; keep
Transmission's download directory for downloads only. See
[SECURITY.md](SECURITY.md#agents-mcp).
