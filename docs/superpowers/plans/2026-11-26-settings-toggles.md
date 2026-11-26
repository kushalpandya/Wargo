# Settings Header Switches Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Header switches on settings cards that turn features on and off without losing saved values, plus a visual consistency pass over Settings.

**Architecture:** A `CardHeader` switch prop gives every card the same switch and collapse behaviour. Two server changes back the switches that today would lose data: one shared update-check schedule with a separate on/off value, and an `enabled` flag per integration that makes a switched-off integration look unconfigured to the rest of Wargo.

**Tech Stack:** Go 1.26 (net/http, internal/store key/value), React 19, TanStack Query 5, Radix, Tailwind v4 (`cn()` is plain clsx and does not merge classes), `node --test` for web logic tests.

**Spec:** docs/superpowers/specs/2026-11-26-settings-toggles-design.md

## Global Constraints

- Never use an em dash or an en dash as punctuation in code, comments, copy, docs or commit messages.
- No AI attribution in commits.
- Off never deletes a saved value; switching back on restores it.
- Card-level switches live in the card header, not in a row.
- `cn()` does not merge Tailwind classes: never pass two classes for one CSS property; build the right class list instead.
- Web checks: `cd web && npx tsc --noEmit -p . && npm test && npm run build`. Go checks: `go vet ./... && go test ./...`.
- Settings changes are audited the way existing settings PUTs are.

## Review Focus

- An install upgraded with a saved `checkHours: 0` must come up switched off with daily remembered, not checking daily.
- Switching an integration off while the Downloads page or an MCP agent is using it: the next request answers as not configured, the poller stops, auto-remove does nothing.
- Switching an integration back on restores the same URL and secret without re-entering them.
- A never-configured integration: the switch shows on and the setup form is visible.
- Two-factor switch off then cancel the password dialog: the switch snaps back to on.

---

### Task 1: Shared header switch, and the Agents card list

**Files:** web/src/components/ui/card.tsx, web/src/features/settings/AgentsCard.tsx (and button.tsx only if needed).

- [ ] Add an optional header switch to `CardHeader`: props for checked, onCheckedChange, an accessible label and disabled/loading. It renders the existing `Switch` from `@/components/ui/form` after any other `actions`. A card whose body is hidden must not show the header's bottom border (today AgentsCard passes `className="last:border-b-0"`; make that the header's default behaviour so no caller needs it).
- [ ] Move AgentsCard's switch to the new prop.
- [ ] Restyle "Authorized agents" like the Passkeys list in Security.tsx `PasskeysCard`: one `ul` with `divide-y divide-border rounded-md border border-border`, rows `px-3 py-2.5`. Each row: name and address badge on line one, "Added X · Used Y" on line two, then the small access `Select`, then the revoke `IconButton` (variant `danger`, size `sm` or `xs` to match the Select's 28px, label `Revoke <name>`) at the far right. Remove the `tall` prop from IconButton if nothing else uses it. Keep the existing indentation under the heading only if it still reads well inside the box; otherwise drop it.
- [ ] Checks pass; commit.

### Task 2: Server: one update-check schedule with an on/off value

**Files:** internal/schedule/schedule.go (+ test), internal/pkg/autocheck.go, internal/docker/checks.go, wherever brew builds its schedule, and their tests.

- [ ] `schedule.Settings` gains an on/off field (e.g. `Enabled bool \`json:"enabled"\``) alongside `CheckHours`. The effective interval is `CheckHours` when enabled, else 0. GET returns both. PUT accepts both; a PUT with only `checkHours` keeps working (treat a missing `enabled` as unchanged). Audit as today.
- [ ] Migration on load: a saved value without the on/off field derives it from `CheckHours > 0`; a saved `CheckHours` of 0 becomes `Enabled false, CheckHours 24`.
- [ ] Docker's container-image checks use the same store key and allowed values as packages (`pkg.SettingsKey`, allowed 0, 6, 12, 24, 72, 168, default 24) so all three sources share one value and one wake. Migrate a saved `docker.settings` value only if `pkg.settings` has none; otherwise drop it. Keep the existing HTTP routes working (`/api/pkg/settings`, `/api/brew/settings`, `/api/containers/settings` all read and write the shared value) so the web app and old clients agree.
- [ ] Manual checks (Check now) are unaffected by the switch.
- [ ] Tests: migration of `{checkHours:0}`, of `{checkHours:12}`, of a docker-only saved value; switching off keeps the frequency and stops scheduling; a PUT through any route is seen by all three. Commit.

### Task 3: Updates card

**Files:** web/src/features/settings/General.tsx.

- [ ] "Update checks" becomes "Updates" with the header switch bound to the shared on/off value. One "Check frequency" row (Every 6 hours, Every 12 hours, Daily, Every 3 days, Weekly), description naming the sources this host has (OS packages, Homebrew, container images), plus "Last checked X" from the newest of the sources' last checks. Off: header only. A host with none of the sources: keep the "Nothing to check on this host." body and no switch.
- [ ] Commit.

### Task 4: Server: integration on/off without losing settings

**Files:** internal/integrations/service.go, plexsvc.go, poller.go, autoremove.go, handlers.go, their tests, and internal/mcp if a tool's availability reads integration state directly.

- [ ] Each integration's stored settings gain a disabled marker (e.g. `Disabled bool \`json:"disabled,omitempty"\``) so existing saved settings stay enabled.
- [ ] New route `PUT /api/integrations/{name}/enabled` with body `{"enabled": bool}` for prowlarr, transmission and plex: flips the marker, keeps URL and secret, audits `integrations.<name>.enable` / `.disable`, wakes the poller.
- [ ] While disabled, the integration counts as not configured everywhere it is consulted: capabilities (so pages and MCP tools hide), the Transmission poller and auto-remove, search, queue, Plex activity, and the handlers (answer as they do when unconfigured). Status (`GET /api/integrations`) still reports `configured` plus a new `enabled` so the settings page can show the switch off with the form kept.
- [ ] Saving settings (PUT with a URL) keeps the current enabled state. Saving with an empty URL still clears (the web app stops offering it, but keep the API).
- [ ] Tests: disable keeps URL and secret and makes capabilities report unavailable; poller and auto-remove do nothing while disabled; enable restores; status shows enabled false. Commit.

### Task 5: Integration cards

**Files:** web/src/features/torrents/IntegrationSettings.tsx (and its types).

- [ ] Each card gets the header switch bound to `enabled`. A never-configured integration shows the switch on with its setup form; the switch is disabled until it has been saved once, since there is nothing to keep. Off: header only, keeping the logo and status pill. Remove the Disconnect buttons, their confirm dialogs and the "disconnected" toast path.
- [ ] After toggling, invalidate the integrations status and capabilities queries.
- [ ] Commit.

### Task 6: Weather and Security cards

**Files:** web/src/features/weather/WeatherSettings.tsx, web/src/features/settings/Security.tsx.

- [ ] Weather: header switch replaces the "Show the weather on Home" row; Units and Location only while on.
- [ ] Two-factor: header switch replaces the On/Off badge and the Set up and Turn off buttons. On opens the existing enroll dialog; off opens the existing password dialog; the switch shows the server's state, so cancelling either dialog leaves it as it was. Recovery codes row only while on. Authenticator description stays.
- [ ] Passkey delete button: `IconButton` variant `danger`.
- [ ] Header icon tints: Two-factor `mint`, Passkeys `iris`, Active sessions `sky`.
- [ ] Commit.

### Task 7: Consistency pass

**Files:** web/src/features/settings/*, web/src/features/weather/WeatherSettings.tsx, web/src/features/torrents/IntegrationSettings.tsx, AdminApprovals.tsx.

- [ ] Every settings card header has a tinted icon chip (integration logos stay as they are); tints spread so neighbouring cards differ.
- [ ] Rows use `SettingRow` / `SettingsList` (title `text-sm font-medium`, description `text-xs text-muted`); lists use the bordered box style; buttons inside cards are `size="sm"`; no switches left in rows where a header switch applies.
- [ ] Replace any em or en dash used as punctuation in settings copy (e.g. the passkeys description).
- [ ] Commit.
