// SPDX-License-Identifier: AGPL-3.0-only
import { memo, useDeferredValue, useMemo, useState, type ReactNode } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { RefreshCw, ScrollText } from "lucide-react";
import { api, qs } from "@/lib/api";
import { cn } from "@/lib/cn";
import { ago, datetime } from "@/lib/format";
import { useFillViewport } from "@/lib/hooks";
import { Button, IconButton } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Sheet } from "@/components/ui/dialog";
import { SearchInput } from "@/components/ui/form";
import { FilterMenu, radioGroup } from "@/components/ui/filter-menu";
import { Badge, EmptyState, ErrorNote, Field, Skeleton } from "@/components/ui/misc";
import { StatStrip, type StatItem } from "@/components/ui/stat-strip";
import { Table, Td, Th, THead, Tr } from "@/components/ui/table";

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
}

type ResultFilter = "all" | "ok" | "error";

const PAGE = 100;

function Result({ r }: { r: string }) {
  const ok = r === "ok";
  return (
    <Badge tone={ok ? "ok" : "crit"} title={ok ? undefined : r}>
      {ok ? "OK" : "Error"}
    </Badge>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="border-b border-border px-4 py-3 last:border-0">
      <h3 className="mb-1 text-2xs font-semibold uppercase tracking-wider text-subtle">{title}</h3>
      {children}
    </section>
  );
}

/** One entry's details, in a side sheet like a process's. */
function AuditSheet({ entry, onClose }: { entry: AuditEntry | null; onClose: () => void }) {
  const e = entry;
  return (
    <Sheet open={!!e} onOpenChange={(o) => !o && onClose()} title={e?.action ?? ""} description={e ? datetime(e.ts) : undefined}>
      {e && (
        <>
          {e.result !== "ok" && (
            <Section title="Error">
              <p className="break-words text-sm text-crit">{e.result.replace(/^error:\s*/, "")}</p>
            </Section>
          )}
          <Section title="Entry">
            <dl className="text-sm">
              <Field label="Result">
                <Result r={e.result} />
              </Field>
              <Field label="Target">{e.target ? <span title={e.target}>{e.target}</span> : <span className="text-muted">None</span>}</Field>
              <Field label="Elevation">{e.elevation ? <Badge tone="accent">{e.elevation}</Badge> : <span className="text-muted">None</span>}</Field>
              <Field label="IP address" mono>
                {e.ip}
              </Field>
            </dl>
          </Section>
          <Section title="User agent">
            <p className="num break-all text-xs">{e.ua || <span className="text-muted">None</span>}</p>
          </Section>
          {e.details != null && (
            <Section title="Details">
              <pre className="num scroll-area max-h-80 overflow-auto whitespace-pre-wrap break-all rounded bg-surface-2 p-2 text-xs">{JSON.stringify(e.details, null, 2)}</pre>
            </Section>
          )}
        </>
      )}
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
      const ok = e.result === "ok";
      if (!ok) errors++;
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
  const [elevated, setElevated] = useState(false);
  const [open, setOpen] = useState<AuditEntry | null>(null);
  const dq = useDeferredValue(q.trim().toLowerCase());
  const query = useInfiniteQuery({
    queryKey: ["audit"],
    queryFn: ({ pageParam }) => api.get<AuditEntry[]>(`/api/audit${qs({ limit: PAGE, before: pageParam || undefined })}`),
    initialPageParam: 0,
    getNextPageParam: (last) => (last.length < PAGE ? undefined : last[last.length - 1].id),
  });
  const all = useMemo(() => query.data?.pages.flat() ?? [], [query.data]);
  // Filters apply to the entries loaded so far; "Load more" fetches older ones.
  const rows = useMemo(
    () =>
      all.filter(
        (e) =>
          (result === "all" || (result === "ok") === (e.result === "ok")) &&
          (!elevated || !!e.elevation) &&
          (!dq || [e.action, e.target, e.result, e.ip, e.elevation ?? ""].some((v) => v.toLowerCase().includes(dq))),
      ),
    [all, result, elevated, dq],
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
          <SearchInput value={q} onChange={setQ} placeholder="Filter by action, target or IP…" className="min-w-0 flex-1" label="Filter audit log" />
          <FilterMenu
            active={(result !== "all" ? 1 : 0) + (elevated ? 1 : 0)}
            onClear={() => {
              setResult("all");
              setElevated(false);
            }}
            groups={[
              radioGroup<ResultFilter>("Result", result, setResult, [
                { value: "all", label: "All results" },
                { value: "ok", label: "OK" },
                { value: "error", label: "Errors" },
              ]),
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
                    <Th>Action</Th>
                    <Th>Target</Th>
                    <Th className="w-24">Result</Th>
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
                      <Td className="num whitespace-nowrap font-medium">{e.action}</Td>
                      <Td className="max-w-[18rem] truncate" title={e.target}>
                        {e.target}
                      </Td>
                      <Td>
                        <Result r={e.result} />
                      </Td>
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
                      <div className="min-w-0 flex-1">
                        <div className="num truncate text-sm font-medium">{e.action}</div>
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
      <AuditSheet entry={open} onClose={() => setOpen(null)} />
    </div>
  );
});
