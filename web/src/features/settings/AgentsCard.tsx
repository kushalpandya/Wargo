// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useState } from "react";
import { Link } from "react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { BotMessageSquare, Check, Clock, Plus, Trash2 } from "lucide-react";
import { api } from "@/lib/api";
import { cn } from "@/lib/cn";
import { ago, datetime } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { Button, IconButton } from "@/components/ui/button";
import { Card, CardBody, CardHeader } from "@/components/ui/card";
import { ConfirmDialog, Dialog } from "@/components/ui/dialog";
import { FormField, Input, Segmented, Select, useRadioGroup } from "@/components/ui/form";
import { Badge, ErrorNote, Notice, Skeleton } from "@/components/ui/misc";
import { toast } from "@/components/ui/toast";
import {
  createAgent,
  invalidateAgents,
  levelLabel,
  levels,
  useAgents,
  useApprovals,
  useMCPSettings,
  useRevokeAgent,
  useSaveMCPSettings,
  useUpdateAgent,
  type Agent,
  type Level,
  type MCPSettings,
} from "@/features/mcp/api";
import { isNever, timeLeft } from "@/features/mcp/approval";
import { claudeCode, genericJSON, mcpURL, standingInstruction } from "@/features/mcp/snippets";
import { enabledToggle } from "./save";
import { BoxList, CodeBlock, SettingRow, SettingsList, StackedRow } from "./shared";

const networks: { value: MCPSettings["network"]; label: string }[] = [
  { value: "local", label: "Local network only" },
  { value: "anywhere", label: "Anywhere" },
];

/** Minutes an admin request waits, saved when the field is left. */
function ApprovalMinutes({ value, onSave }: { value: number; onSave: (n: number) => void }) {
  const [draft, setDraft] = useState(String(value));
  useEffect(() => setDraft(String(value)), [value]);
  const commit = () => {
    const n = Math.round(Number(draft));
    if (!Number.isFinite(n) || draft.trim() === "") return setDraft(String(value));
    const clamped = Math.min(60, Math.max(1, n));
    setDraft(String(clamped));
    if (clamped !== value) onSave(clamped);
  };
  return (
    <div className="flex items-center gap-2">
      <Input
        type="number"
        inputMode="numeric"
        min={1}
        max={60}
        aria-label="Approval timeout in minutes"
        className="w-20"
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => e.key === "Enter" && commit()}
      />
      <span className="text-sm text-muted">minutes</span>
    </div>
  );
}

/**
 * Agents (MCP): the switch sits in the header and the rest shows only while
 * it is on. Switching off keeps the settings and agents for next time.
 */
export function AgentsCard() {
  // Shows a change as soon as it is made (see useSettingsSave).
  const { data: v, error } = useMCPSettings();
  const update = useSaveMCPSettings();
  const on = !!v?.enabled;
  // The dialog lives here, outside the part that hides, so a refetch that
  // fails or finds MCP switched off elsewhere can't drop a token on screen.
  const [connecting, setConnecting] = useState(false);
  return (
    <Card>
      <CardHeader
        title="Agents (MCP)"
        icon={<BotMessageSquare />}
        tint="sky"
        meta={v && !v.enabled ? "AI agents using Wargo's tools" : undefined}
        toggle={enabledToggle("Allow agents (MCP)", v, update, error)}
      />
      {error && !v ? (
        <CardBody>
          <ErrorNote error={error} />
        </CardBody>
      ) : (
        on &&
        v && (
          <CardBody>
            <SettingsList>
              <StackedRow title="URL" description="Agents connect here over HTTP, sending their token as a bearer header.">
                <CodeBlock label="MCP URL">{mcpURL(location.origin)}</CodeBlock>
              </StackedRow>
              <SettingRow
                title="Access"
                description="Local network only: this machine and devices on your network. Anywhere: also agents reaching Wargo from outside, such as through a tunnel."
              >
                <Segmented label="Access" size="sm" value={v.network} onChange={(network) => update({ network })} options={networks} />
              </SettingRow>
              <SettingRow title="Approval timeout" description="How long an agent's admin request waits for your approval before it expires.">
                <ApprovalMinutes value={v.approvalMinutes} onSave={(approvalMinutes) => update({ approvalMinutes })} />
              </SettingRow>
              <AuthorizedAgents onConnect={() => setConnecting(true)} />
            </SettingsList>
          </CardBody>
        )
      )}
      <ConnectDialog open={connecting} onOpenChange={setConnecting} />
    </Card>
  );
}

/** Admin requests waiting for the owner, each opening its approval page. */
export function PendingCard() {
  const on = !!useMCPSettings().data?.enabled;
  const { data } = useApprovals(on);
  const pending = (on && data?.filter((p) => p.state === "pending")) || [];
  const now = useNow(1000, pending.length > 0);
  if (!pending.length) return null;
  return (
    <Card>
      <CardHeader title="Waiting for approval" icon={<Clock />} tint="butter" meta={pending.length} />
      <CardBody>
        <BoxList>
          {pending.map((p) => (
            <li key={p.id} className="flex flex-col gap-2 px-3 py-2.5 sm:flex-row sm:items-center sm:gap-4">
              <div className="min-w-0 flex-1">
                <div className="flex min-w-0 flex-wrap items-baseline gap-x-2">
                  <span className="truncate text-sm font-medium">{p.agent.name}</span>
                  <span className="num text-xs text-muted">{p.tool}</span>
                </div>
                <div className="mt-0.5 text-xs text-muted [overflow-wrap:anywhere]">{p.summary}</div>
              </div>
              <div className="flex shrink-0 items-center gap-3">
                <span className="num text-xs text-muted">{p.running ? "Running" : `${timeLeft(p.expires, now) ?? "Expired"} left`}</span>
                <Button asChild size="sm" variant="primary">
                  <Link to={`/approve/${encodeURIComponent(p.id)}`}>Review</Link>
                </Button>
              </div>
            </li>
          ))}
        </BoxList>
      </CardBody>
    </Card>
  );
}

/** Access levels as a radio list with a line about each. */
function LevelPicker({ value, onChange }: { value: Level; onChange: (l: Level) => void }) {
  const radio = useRadioGroup(
    levels.map((l) => l.value),
    value,
    onChange,
  );
  return (
    <div {...radio.group} aria-label="Access level" className="flex flex-col gap-2">
      {levels.map((l) => {
        const active = l.value === value;
        return (
          <button
            key={l.value}
            type="button"
            {...radio.item(l.value)}
            onClick={() => onChange(l.value)}
            className={cn(
              "flex items-start gap-3 rounded-md border px-3 py-2.5 text-left transition-colors",
              active ? "border-accent bg-accent-soft" : "border-border hover:border-border-strong",
            )}
          >
            <span className="min-w-0 flex-1">
              <span className="block text-sm font-medium">{l.label}</span>
              <span className="mt-0.5 block text-xs text-muted">{l.hint}</span>
            </span>
            <Check className={cn("mt-0.5 size-4 shrink-0 text-accent", !active && "invisible")} aria-hidden="true" />
          </button>
        );
      })}
    </div>
  );
}

/**
 * Connect an agent: name and access, then its token, shown once. The token
 * lives only in this dialog's state and is dropped when it closes.
 */
function ConnectDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [level, setLevel] = useState<Level>("read");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [created, setCreated] = useState<{ agent: Agent; token: string } | null>(null);
  const info = useQuery({
    queryKey: ["system", "info"],
    queryFn: () => api.get<{ hostname: string }>("/api/system/info"),
    staleTime: 2 * 60_000,
    enabled: open,
  });
  useEffect(() => {
    setName("");
    setLevel("read");
    setErr(null);
    setBusy(false);
    setCreated(null);
  }, [open]);

  const submit = async () => {
    if (!name.trim() || busy) return;
    setBusy(true);
    setErr(null);
    try {
      setCreated(await createAgent(name.trim(), level));
      invalidateAgents(qc);
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  };

  const url = mcpURL(location.origin);
  const hostname = info.data?.hostname;
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={created ? `Connect ${created.agent.name}` : "Connect an agent"}
      description={created ? "Set up the agent with this token. It is shown only once." : "Give the agent a name you'll recognise and choose what it may do."}
      size="lg"
      footer={
        created ? (
          <Button variant="primary" onClick={() => onOpenChange(false)}>
            Done
          </Button>
        ) : (
          <>
            <Button onClick={() => onOpenChange(false)}>Cancel</Button>
            <Button variant="primary" loading={busy} disabled={!name.trim()} onClick={submit}>
              Connect
            </Button>
          </>
        )
      }
    >
      {created ? (
        <div className="flex flex-col gap-4">
          <Notice tone="warn">Copy the token now. Once you close this dialog it can't be shown again; to get a new one, revoke this agent and connect it again.</Notice>
          <FormField label="Token">
            <CodeBlock label="token">{created.token}</CodeBlock>
          </FormField>
          <FormField label="Claude Code" hint="Run this where the agent runs.">
            <CodeBlock label="Claude Code command">{claudeCode(url, created.token)}</CodeBlock>
          </FormField>
          <FormField label="Other clients" hint="For clients set up with a JSON file of MCP servers.">
            <CodeBlock label="JSON config">{genericJSON(url, created.token)}</CodeBlock>
          </FormField>
          {hostname && (
            <FormField label="Standing instructions" hint="Add this line to the agent's own instructions so it uses Wargo without being asked.">
              <CodeBlock label="standing instruction">{standingInstruction(hostname)}</CodeBlock>
            </FormField>
          )}
        </div>
      ) : (
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            submit();
          }}
        >
          <FormField label="Name" htmlFor="agent-name">
            <Input id="agent-name" autoFocus autoComplete="off" maxLength={100} placeholder="OpenClaw on my laptop" value={name} onChange={(e) => setName(e.target.value)} />
          </FormField>
          <div className="flex flex-col gap-1">
            <span className="text-ui font-medium">Access</span>
            <LevelPicker value={level} onChange={setLevel} />
          </div>
          {err ? <ErrorNote error={err} /> : null}
        </form>
      )}
    </Dialog>
  );
}

/** Agents holding a token: their access, last use, and revoking. */
function AuthorizedAgents({ onConnect }: { onConnect: () => void }) {
  const { data, isLoading, error } = useAgents();
  const update = useUpdateAgent();
  const revoke = useRevokeAgent();
  const [revoking, setRevoking] = useState<Agent | null>(null);
  // Re-render each minute so the "ago" times stay current.
  useNow(60_000, !!data?.length);
  return (
    <StackedRow
      title="Authorized agents"
      action={
        <Button size="sm" variant="primary" icon={<Plus />} onClick={onConnect}>
          Connect an agent
        </Button>
      }
    >
      {isLoading ? (
        <Skeleton className="h-16" />
      ) : error ? (
        <ErrorNote error={error} />
      ) : !data?.length ? (
        <p className="text-xs text-muted">None yet. Connect an agent to give it a token for Wargo's tools.</p>
      ) : (
        <BoxList>
          {data.map((a) => (
            // Phones: name and revoke on the first line, access below. sm and up: one line.
            <li key={a.id} className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 gap-y-2 px-3 py-2.5 sm:grid-cols-[minmax(0,1fr)_auto_auto]">
              <div className="min-w-0">
                <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5">
                  <span className="truncate text-sm font-medium">{a.name}</span>
                  {a.lastIp && (
                    <Badge className="num" title="Address of its last request">
                      {a.lastIp}
                    </Badge>
                  )}
                </div>
                <div className="text-xs text-muted">
                  Added <span title={datetime(a.createdAt)}>{ago(a.createdAt)}</span>
                  {" · "}
                  {isNever(a.lastUsedAt) ? "Never used" : <span title={datetime(a.lastUsedAt)}>Used {ago(a.lastUsedAt)}</span>}
                </div>
              </div>
              <Select
                aria-label={`Access for ${a.name}`}
                small
                className="col-span-2 row-start-2 w-auto justify-self-start sm:col-span-1 sm:col-start-2 sm:row-start-1"
                value={update.isPending && update.variables?.id === a.id ? update.variables.level : a.level}
                onChange={(e) => {
                  const level = e.target.value as Level;
                  update.mutate(
                    { id: a.id, level },
                    {
                      onSuccess: () => toast.ok(`${a.name}: ${levelLabel(level)}`),
                      onError: (err) => toast.error("Could not change access", err),
                    },
                  );
                }}
              >
                {levels.map((l) => (
                  <option key={l.value} value={l.value}>
                    {l.label}
                  </option>
                ))}
              </Select>
              <IconButton
                label={`Revoke ${a.name}`}
                size="xs"
                variant="danger"
                className="col-start-2 row-start-1 self-start sm:col-start-3 sm:self-center"
                onClick={() => setRevoking(a)}
              >
                <Trash2 />
              </IconButton>
            </li>
          ))}
        </BoxList>
      )}
      <ConfirmDialog
        open={!!revoking}
        onOpenChange={(o) => !o && setRevoking(null)}
        title={`Revoke ${revoking?.name ?? "agent"}?`}
        description="Its token stops working at once, and requests it is waiting on can no longer be approved."
        confirmLabel="Revoke"
        danger
        loading={revoke.isPending}
        onConfirm={() =>
          revoking &&
          revoke.mutate(revoking.id, {
            onSuccess: () => {
              toast.ok(`Revoked ${revoking.name}`);
              setRevoking(null);
            },
            onError: (e) => toast.error("Could not revoke", e),
          })
        }
      />
    </StackedRow>
  );
}
