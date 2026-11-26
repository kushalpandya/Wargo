// SPDX-License-Identifier: AGPL-3.0-only
import { memo, useDeferredValue, useMemo, useState, type ReactNode } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Bot, BotMessageSquare, ChevronRight, Copy, FileText, RefreshCw, ScrollText, Timer, User } from "lucide-react";
import { api, qs } from "@/lib/api";
import { copyWithToast } from "@/lib/clipboard";
import { cn } from "@/lib/cn";
import { ago, datetime, durationMs } from "@/lib/format";
import { useFillViewport } from "@/lib/hooks";
import { Button, IconButton } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Dialog, Sheet } from "@/components/ui/dialog";
import { SearchInput } from "@/components/ui/form";
import { FilterMenu, radioGroup } from "@/components/ui/filter-menu";
import { Badge, EmptyState, ErrorNote, Field, OutputBlock, Skeleton, Spinner } from "@/components/ui/misc";
import { StatStrip, type StatItem } from "@/components/ui/stat-strip";
import { Table, Td, Th, THead, Tr } from "@/components/ui/table";
import { useAgents } from "@/features/mcp/api";

export type AuditSource = "user" | "system" | "job" | "agent";

export interface AuditOutput {
  lines?: string[];
  truncated?: boolean;
  log?: string;
}

export interface AuditEntry {
  id: number;
  ts: string;
  ip: string;
  ua: string;
  action: string;
  target: string;
  result: string;
  elevation?: string;
  details?: unknown;
  /** "user": a request from a browser; "system": Wargo itself; "job": the end of a job started earlier; "agent": an agent's MCP tool call. */
  source: AuditSource;
  /** The agent (its name at the time) and tool, for source "agent". */
  agent?: { id: string; name: string; tool: string };
  code?: string;
  /** The entry this one belongs to (a job's end points at its start). */
  ref?: number;
  session?: string;
  request?: string;
  route?: string;
  version?: string;
  durationMs?: number;
  hasOutput?: boolean;
  /** Only in GET /api/audit/{id}. */
  output?: AuditOutput;
}

type ResultFilter = "all" | "ok" | "error";
/** "agents": any agent; "agent:<id>": one agent (both asked of the server, so older entries count too). */
type SourceFilter = "all" | "user" | "wargo" | "agents" | `agent:${string}`;

const PAGE = 100;

/** Areas for the filter, by the first part of the action. */
const AREAS: Record<string, string> = {
  auth: "Sign-in",
  files: "Files",
  containers: "Containers",
  pkg: "Updates",
  brew: "Updates",
  term: "Terminal",
  procs: "Processes",
  integrations: "Integrations",
  prowlarr: "Integrations",
  transmission: "Integrations",
  notify: "Notifications",
  launcher: "Apps",
  appearance: "Settings",
  weather: "Settings",
  jobs: "Wargo",
  wargo: "Wargo",
  server: "Wargo",
  mcp: "Agents",
};
const areaOf = (action: string) => AREAS[action.split(".")[0]] ?? "Other";

const isOk = (r: string) => r === "ok";
const isCanceled = (r: string) => r === "canceled";
const errorText = (r: string) => r.replace(/^error:\s*/, "");

function Result({ r }: { r: string }) {
  if (isOk(r)) return <Badge tone="ok">OK</Badge>;
  if (isCanceled(r)) return <Badge>Canceled</Badge>;
  return (
    <Badge tone="crit" title={errorText(r)}>
      Error
    </Badge>
  );
}

const SOURCES: Record<AuditSource, { label: string; icon: typeof User; title: string }> = {
  user: { label: "Browser", icon: User, title: "Done from a browser" },
  system: { label: "Wargo", icon: Bot, title: "Done by Wargo itself" },
  job: { label: "Job", icon: Timer, title: "The end of a job started earlier" },
  agent: { label: "Agent", icon: BotMessageSquare, title: "Done by an agent over MCP" },
};

/** How many identical read-only agent calls an mcp.read entry stands for. */
function foldedCount(e: AuditEntry): number | undefined {
  if (e.action !== "mcp.read") return undefined;
  const n = (e.details as { count?: unknown } | null | undefined)?.count;
  return typeof n === "number" ? n : undefined;
}

/** The agent and tool behind an agent entry, and how many calls it folds. */
function AgentLine({ e }: { e: AuditEntry }) {
  const n = foldedCount(e);
  if (!e.agent) return null;
  return (
    <span className="text-xs font-normal text-muted">
      {e.agent.name} · <span className="num">{e.agent.tool}</span>
      {n && n > 1 ? <span className="num"> ×{n}</span> : null}
    </span>
  );
}

function Source({ s, withLabel }: { s: AuditSource; withLabel?: boolean }) {
  const { label, icon: Icon, title } = SOURCES[s] ?? SOURCES.system;
  return (
    <span className="inline-flex items-center gap-1.5 text-muted" title={title}>
      <Icon className="size-3.5 shrink-0" aria-hidden />
      {withLabel ? label : <span className="sr-only">{label}</span>}
    </span>
  );
}

function Section({ title, children, action }: { title: string; children: ReactNode; action?: ReactNode }) {
  return (
    <section className="border-b border-border px-4 py-3 last:border-0">
      <div className="mb-1 flex min-h-6 items-center justify-between gap-2">
        <h3 className="text-2xs font-semibold uppercase tracking-wider text-subtle">{title}</h3>
        {action}
      </div>
      {children}
    </section>
  );
}

function None() {
  return <span className="text-muted">None</span>;
}

/** The full log file an entry points at, while Wargo still keeps it. */
function LogDialog({ entry, onClose }: { entry: AuditEntry | null; onClose: () => void }) {
  const q = useQuery({
    queryKey: ["audit", "log", entry?.id],
    queryFn: () => api.get<{ path: string; lines: string[]; truncated: boolean }>(`/api/audit/${entry!.id}/log`),
    enabled: !!entry,
    retry: false,
  });
  return (
    <Dialog
      open={!!entry}
      onOpenChange={(o) => !o && onClose()}
      title="Log"
      description={entry?.output?.log}
      size="xl"
      footer={
        <>
          {q.data && (
            <Button icon={<Copy />} onClick={() => copyWithToast(q.data.lines.join("\n"))}>
              Copy
            </Button>
          )}
          <Button onClick={onClose}>Close</Button>
        </>
      }
    >
      {q.isLoading ? (
        <div className="flex justify-center py-6 text-muted">
          <Spinner />
        </div>
      ) : q.error ? (
        <ErrorNote error={q.error} />
      ) : (
        q.data && (
          <>
            {q.data.truncated && <p className="mb-2 text-xs text-muted">Showing the end of the log.</p>}
            <OutputBlock className="max-h-[60dvh]" label="Log">
              {q.data.lines.join("\n")}
            </OutputBlock>
          </>
        )
      )}
    </Dialog>
  );
}

/** A linked entry: the start of a job, or its end. */
function RelatedRow({ e, current, onOpen }: { e: AuditEntry; current: AuditEntry; onOpen: (e: AuditEntry) => void }) {
  const relation = e.id === current.ref ? "Started by" : "Followed by";
  return (
    <li>
      <button
        type="button"
        onClick={() => onOpen(e)}
        className="flex min-h-11 w-full items-center gap-3 rounded px-2 py-1.5 text-left hover:bg-surface-2"
      >
        <div className="min-w-0 flex-1">
          <div className="text-2xs text-subtle">{relation}</div>
          <div className="num truncate text-sm font-medium">{e.action}</div>
          <div className="truncate text-xs text-subtle">{[e.target, ago(e.ts)].filter(Boolean).join(" · ")}</div>
        </div>
        <Result r={e.result} />
        <ChevronRight className="size-4 shrink-0 text-subtle" aria-hidden />
      </button>
    </li>
  );
}

/** One entry's details, in a side sheet like a process's. */
function AuditSheet({ entry, onOpen, onClose }: { entry: AuditEntry | null; onOpen: (e: AuditEntry) => void; onClose: () => void }) {
  const full = useQuery({
    queryKey: ["audit", "entry", entry?.id],
    queryFn: () => api.get<{ entry: AuditEntry; related: AuditEntry[] }>(`/api/audit/${entry!.id}`),
    enabled: !!entry,
  });
  const [logOf, setLogOf] = useState<AuditEntry | null>(null);
  // The list row shows at once; the full entry (output, links) follows.
  const loaded = entry && full.data?.entry.id === entry.id ? full.data : undefined;
  const e = loaded?.entry ?? entry;
  const related = loaded?.related.filter((r) => r.id !== entry?.id) ?? [];
  const out = e?.output;
  return (
    <Sheet
      open={!!entry}
      onOpenChange={(o) => !o && onClose()}
      title={e?.action ?? ""}
      description={e ? datetime(e.ts) : undefined}
      actions={
        e && (
          <IconButton label="Copy entry as JSON" size="sm" onClick={() => copyWithToast(JSON.stringify({ entry: e, related }, null, 2), "Entry copied")}>
            <Copy />
          </IconButton>
        )
      }
    >
      {e && (
        <>
          {!isOk(e.result) && !isCanceled(e.result) && (
            <Section title="Error">
              <p className="break-words text-sm text-crit">{errorText(e.result)}</p>
              {e.code && (
                <p className="mt-1.5">
                  <Badge tone="crit" className="num">
                    {e.code}
                  </Badge>
                </p>
              )}
            </Section>
          )}
          {(out?.lines?.length || out?.log) && (
            <Section
              title="Output"
              action={
                out.lines?.length ? (
                  <IconButton label="Copy output" size="xs" onClick={() => copyWithToast(out.lines!.join("\n"))}>
                    <Copy />
                  </IconButton>
                ) : undefined
              }
            >
              {out.lines?.length ? (
                <>
                  {out.truncated && <p className="mb-1 text-xs text-muted">The last lines:</p>}
                  <OutputBlock label="Output">{out.lines.join("\n")}</OutputBlock>
                </>
              ) : null}
              {out.log && (
                <Button size="sm" icon={<FileText />} className="mt-2" onClick={() => setLogOf(e)}>
                  Open full log
                </Button>
              )}
            </Section>
          )}
          {e.hasOutput && !out && full.isLoading && (
            <Section title="Output">
              <Skeleton className="h-16" />
            </Section>
          )}
          <Section title="Entry">
            <dl className="text-sm">
              <Field label="Result">
                <Result r={e.result} />
              </Field>
              <Field label="Target">{e.target ? <span title={e.target}>{e.target}</span> : <None />}</Field>
              <Field label="By">
                <Source s={e.source} withLabel />
              </Field>
              {e.agent && (
                <>
                  <Field label="Agent">
                    <span title={e.agent.id}>{e.agent.name}</span>
                  </Field>
                  <Field label="Tool" mono>
                    {e.agent.tool}
                  </Field>
                </>
              )}
              {foldedCount(e) !== undefined && (
                <Field label="Calls" mono>
                  {foldedCount(e)}
                </Field>
              )}
              <Field label="Duration" mono>
                {durationMs(e.durationMs) ?? <None />}
              </Field>
              <Field label="Elevation">{e.elevation ? <Badge tone="accent">{e.elevation}</Badge> : <None />}</Field>
              <Field label="IP address" mono>
                {e.ip || <None />}
              </Field>
              <Field label="Session" mono>
                {e.session || <None />}
              </Field>
              <Field label="Wargo version" mono>
                {e.version || <None />}
              </Field>
            </dl>
          </Section>
          {(e.route || e.request) && (
            <Section title="Request">
              <dl className="text-sm">
                <Field label="Route" mono>
                  {e.route ? <span title={e.route}>{e.route}</span> : <None />}
                </Field>
                <Field label="Request ID" mono>
                  {e.request || <None />}
                </Field>
              </dl>
            </Section>
          )}
          {related.length > 0 && (
            <Section title="Related">
              <ul className="-mx-2">
                {related.map((r) => (
                  <RelatedRow key={r.id} e={r} current={e} onOpen={onOpen} />
                ))}
              </ul>
            </Section>
          )}
          <Section title="User agent">
            <p className="num break-all text-xs">{e.ua || <None />}</p>
          </Section>
          {e.details != null && (
            <Section title="Details">
              <OutputBlock label="Details">{JSON.stringify(e.details, null, 2)}</OutputBlock>
            </Section>
          )}
          {full.error && (
            <div className="p-4">
              <ErrorNote error={full.error} />
            </div>
          )}
        </>
      )}
      <LogDialog entry={logOf} onClose={() => setLogOf(null)} />
    </Sheet>
  );
}

const SIGN_INS = new Set(["auth.session", "auth.passkey_login"]);
const SIGN_IN_STEPS = new Set(["auth.password", "auth.second_factor", "auth.passkey_login"]);

/** Counts over the entries loaded so far (the newest first; older ones load on demand). */
function Summary({ entries }: { entries: AuditEntry[] | null }) {
  const items = useMemo<StatItem[] | null>(() => {
    if (!entries) return null;
    let errors = 0,
      elevated = 0,
      signIns = 0,
      failed = 0;
    for (const e of entries) {
      const ok = isOk(e.result);
      if (!ok && !isCanceled(e.result)) errors++;
      if (e.elevation) elevated++;
      if (ok && SIGN_INS.has(e.action)) signIns++;
      if (!ok && SIGN_IN_STEPS.has(e.action)) failed++;
    }
    const oldest = entries[entries.length - 1];
    return [
      { label: "Entries", value: entries.length.toLocaleString(), sub: oldest ? `oldest ${ago(oldest.ts)}` : "none yet" },
      { label: "Errors", value: errors.toLocaleString(), sub: errors ? "actions that failed" : "nothing failed", tone: errors ? "crit" : undefined },
      { label: "Sign-ins", value: signIns.toLocaleString(), sub: failed ? `${failed} failed attempt${failed === 1 ? "" : "s"}` : "no failed attempts", tone: failed ? "warn" : undefined },
      { label: "Elevated", value: elevated.toLocaleString(), sub: "with extra rights" },
    ];
  }, [entries]);
  return <StatStrip items={items} />;
}

/** System Monitor → Audit: every sign-in, setting change and privileged action. Laid out like Processes. */
export const AuditPanel = memo(function AuditPanel() {
  const [q, setQ] = useState("");
  const [result, setResult] = useState<ResultFilter>("all");
  const [source, setSource] = useState<SourceFilter>("all");
  const [area, setArea] = useState("all");
  const [elevated, setElevated] = useState(false);
  const [open, setOpen] = useState<AuditEntry | null>(null);
  const dq = useDeferredValue(q.trim().toLowerCase());
  // Agent filters go to the server: agent entries can be few among many.
  const server = source === "agents" ? { source: "agent" } : source.startsWith("agent:") ? { agent: source.slice(6) } : {};
  const query = useInfiniteQuery({
    queryKey: ["audit", "list", server],
    queryFn: ({ pageParam }) => api.get<AuditEntry[]>(`/api/audit${qs({ ...server, limit: PAGE, before: pageParam || undefined })}`),
    initialPageParam: 0,
    getNextPageParam: (last) => (last.length < PAGE ? undefined : last[last.length - 1].id),
  });
  const all = useMemo(() => query.data?.pages.flat() ?? [], [query.data]);
  const areas = useMemo(() => [...new Set(all.map((e) => areaOf(e.action)))].sort(), [all]);
  // One choice per agent: the connected ones, and any others (revoked since) seen in the entries.
  const agentList = useAgents().data;
  const agentChoices = useMemo(() => {
    const names = new Map<string, string>();
    for (const a of agentList ?? []) names.set(a.id, a.name);
    for (const e of all) if (e.agent && !names.has(e.agent.id)) names.set(e.agent.id, e.agent.name);
    return [...names].map(([id, name]) => ({ value: `agent:${id}` as const, label: name })).sort((a, b) => a.label.localeCompare(b.label));
  }, [agentList, all]);
  // Filters apply to the entries loaded so far; "Load more" fetches older ones.
  const rows = useMemo(
    () =>
      all.filter(
        (e) =>
          (result === "all" || (result === "ok" ? isOk(e.result) : !isOk(e.result) && !isCanceled(e.result))) &&
          (source !== "user" || e.source === "user") &&
          (source !== "wargo" || e.source === "system" || e.source === "job") &&
          (area === "all" || areaOf(e.action) === area) &&
          (!elevated || !!e.elevation) &&
          (!dq ||
            [e.action, e.target, e.result, e.ip, e.elevation ?? "", e.code ?? "", e.request ?? "", e.agent?.name ?? "", e.agent?.tool ?? ""].some((v) =>
              v.toLowerCase().includes(dq),
            )),
      ),
    [all, result, source, area, elevated, dq],
  );

  const [tableEl, setTableEl] = useState<HTMLDivElement | null>(null);
  const [listEl, setListEl] = useState<HTMLDivElement | null>(null);
  const [footerEl, setFooterEl] = useState<HTMLDivElement | null>(null);
  // Leave room for the footer row and the card's bottom border.
  const reserve = (footerEl?.offsetHeight ?? 45) + 1;
  const tableH = useFillViewport(tableEl, { min: 320, reserve });
  const listH = useFillViewport(listEl, { min: 320, reserve });

  return (
    <div>
      {/* The System Monitor page's "Audit" tab is the visible title. */}
      <h2 className="sr-only">Audit</h2>
      <Summary entries={query.data ? all : null} />
      <Card>
        {/* One line at every width, like Processes: the search shrinks, the buttons don't wrap. */}
        <div className="flex items-center gap-2 border-b border-border px-3 py-2.5">
          <SearchInput value={q} onChange={setQ} placeholder="Filter by action, target, code or IP…" className="min-w-0 flex-1" label="Filter audit log" />
          <FilterMenu
            active={(result !== "all" ? 1 : 0) + (source !== "all" ? 1 : 0) + (area !== "all" ? 1 : 0) + (elevated ? 1 : 0)}
            onClear={() => {
              setResult("all");
              setSource("all");
              setArea("all");
              setElevated(false);
            }}
            groups={[
              radioGroup<ResultFilter>("Result", result, setResult, [
                { value: "all", label: "All results" },
                { value: "ok", label: "OK" },
                { value: "error", label: "Errors" },
              ]),
              radioGroup<SourceFilter>("By", source, setSource, [
                { value: "all", label: "Anyone" },
                { value: "user", label: "From a browser" },
                { value: "wargo", label: "Wargo and jobs" },
                { value: "agents", label: "Agents" },
                ...agentChoices,
              ]),
              radioGroup("Area", area, setArea, [{ value: "all", label: "All areas" }, ...areas.map((a) => ({ value: a, label: a }))]),
              { kind: "check", title: "Only", items: [{ label: "Elevated actions", checked: elevated, onChange: setElevated }] },
            ]}
          />
          <IconButton label="Refresh now" size="sm" variant="secondary" onClick={() => query.refetch()} disabled={query.isFetching} className="shrink-0">
            <RefreshCw className={cn(query.isFetching && "animate-spin")} />
          </IconButton>
        </div>
        {query.error ? (
          <div className="p-4">
            <ErrorNote error={query.error} />
          </div>
        ) : query.isLoading ? (
          <div className="space-y-2 p-4">
            {Array.from({ length: 10 }, (_, i) => (
              <Skeleton key={i} className="h-6" />
            ))}
          </div>
        ) : rows.length === 0 ? (
          <EmptyState title={all.length ? "No matching entries" : "No audit entries yet"} icon={<ScrollText />}>
            {all.length ? "Try a different filter, or load older entries." : undefined}
          </EmptyState>
        ) : (
          <>
            {/* Scrollable list sized to the viewport (with a minimum), so the page itself doesn't grow. */}
            <div ref={setTableEl} style={{ height: tableH }} className="scroll-area min-h-80 max-md:hidden" tabIndex={0} role="region" aria-label="Audit table">
              <Table>
                <THead>
                  <tr>
                    <Th className="w-28">Time</Th>
                    <Th className="w-10">
                      <span className="sr-only">By</span>
                    </Th>
                    <Th>Action</Th>
                    <Th>Target</Th>
                    <Th className="w-24">Result</Th>
                    <Th className="max-xl:hidden">Duration</Th>
                    <Th className="max-lg:hidden">Elevation</Th>
                    <Th className="max-lg:hidden">IP</Th>
                  </tr>
                </THead>
                <tbody>
                  {rows.map((e) => (
                    <Tr
                      key={e.id}
                      onClick={() => setOpen(e)}
                      selected={open?.id === e.id}
                      tabIndex={0}
                      onKeyDown={(ev) => {
                        if (ev.key === "Enter" || ev.key === " ") {
                          ev.preventDefault();
                          setOpen(e);
                        }
                      }}
                      className="focus-visible:bg-surface-2 focus-visible:outline-none"
                    >
                      <Td className="num whitespace-nowrap text-muted" title={datetime(e.ts)}>
                        {ago(e.ts)}
                      </Td>
                      <Td>
                        <Source s={e.source} />
                      </Td>
                      <Td className="font-medium">
                        <span className="num whitespace-nowrap">{e.action}</span> <AgentLine e={e} />
                      </Td>
                      <Td className="max-w-[18rem] truncate" title={e.target}>
                        {e.target}
                      </Td>
                      <Td>
                        <Result r={e.result} />
                      </Td>
                      <Td className="num whitespace-nowrap text-muted max-xl:hidden">{durationMs(e.durationMs)}</Td>
                      <Td className="max-lg:hidden">{e.elevation && <Badge tone="accent">{e.elevation}</Badge>}</Td>
                      <Td className="num text-muted max-lg:hidden">{e.ip}</Td>
                    </Tr>
                  ))}
                </tbody>
              </Table>
            </div>
            <div ref={setListEl} style={{ height: listH }} className="scroll-area min-h-80 md:hidden" tabIndex={0} role="region" aria-label="Audit list">
              <ul aria-label="Audit entries">
                {rows.map((e) => (
                  <li key={e.id}>
                    <button type="button" onClick={() => setOpen(e)} className="flex min-h-12 w-full items-center gap-3 border-b border-border px-4 py-2 text-left hover:bg-surface-2">
                      <Source s={e.source} />
                      <div className="min-w-0 flex-1">
                        <div className="truncate text-sm font-medium">
                          <span className="num">{e.action}</span> <AgentLine e={e} />
                        </div>
                        <div className="truncate text-xs text-subtle">{[e.target, e.ip].filter(Boolean).join(" · ")}</div>
                      </div>
                      <div className="flex shrink-0 flex-col items-end gap-1">
                        <span className="num text-xs text-muted">{ago(e.ts)}</span>
                        <span className="flex items-center gap-1">
                          {e.elevation && <Badge tone="accent">{e.elevation}</Badge>}
                          <Result r={e.result} />
                        </span>
                      </div>
                    </button>
                  </li>
                ))}
              </ul>
            </div>
          </>
        )}
        {!query.isLoading && !query.error && (
          <div ref={setFooterEl} className="flex flex-wrap items-center gap-3 border-t border-border px-4 py-2.5 text-xs text-muted">
            <span className="num" aria-live="polite">
              Showing {rows.length} of {all.length} loaded
            </span>
            {query.hasNextPage && (
              <Button size="sm" loading={query.isFetchingNextPage} onClick={() => query.fetchNextPage()}>
                Load {PAGE} older
              </Button>
            )}
          </div>
        )}
      </Card>
      <AuditSheet entry={open} onOpen={setOpen} onClose={() => setOpen(null)} />
    </div>
  );
});
