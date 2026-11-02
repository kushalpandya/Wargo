// SPDX-License-Identifier: AGPL-3.0-only
import { Fragment, useMemo, useState } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { ChevronRight, RefreshCw, ScrollText } from "lucide-react";
import { api, qs } from "@/lib/api";
import { cn } from "@/lib/cn";
import { ago, datetime } from "@/lib/format";
import { Button, IconButton } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { SearchInput } from "@/components/ui/form";
import { Badge, EmptyState, ErrorNote, Skeleton } from "@/components/ui/misc";
import { Table, Td, Th, THead } from "@/components/ui/table";
import { SettingsHeader } from "./shared";

interface Entry {
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

const PAGE = 100;

function Result({ r }: { r: string }) {
  const ok = r === "ok";
  return (
    <Badge tone={ok ? "ok" : "crit"} title={ok ? undefined : r}>
      {ok ? "OK" : "Error"}
    </Badge>
  );
}

function Details({ e }: { e: Entry }) {
  return (
    <dl className="grid gap-x-4 gap-y-1.5 text-xs sm:grid-cols-[7rem_1fr]">
      {e.result !== "ok" && (
        <>
          <dt className="text-muted">Error</dt>
          <dd className="break-words text-crit">{e.result.replace(/^error:\s*/, "")}</dd>
        </>
      )}
      <dt className="text-muted">Time</dt>
      <dd className="num">{datetime(e.ts)}</dd>
      <dt className="text-muted">User agent</dt>
      <dd className="num break-all">{e.ua || "—"}</dd>
      {e.details != null && (
        <>
          <dt className="text-muted">Details</dt>
          <dd>
            <pre className="num scroll-area max-h-48 overflow-auto whitespace-pre-wrap break-all rounded bg-surface-2 p-2">{JSON.stringify(e.details, null, 2)}</pre>
          </dd>
        </>
      )}
    </dl>
  );
}

export default function Audit() {
  const [q, setQ] = useState("");
  const [open, setOpen] = useState<number | null>(null);
  const query = useInfiniteQuery({
    queryKey: ["audit"],
    queryFn: ({ pageParam }) => api.get<Entry[]>(`/api/audit${qs({ limit: PAGE, before: pageParam || undefined })}`),
    initialPageParam: 0,
    getNextPageParam: (last) => (last.length < PAGE ? undefined : last[last.length - 1].id),
  });
  const all = useMemo(() => query.data?.pages.flat() ?? [], [query.data]);
  const rows = useMemo(() => {
    const s = q.trim().toLowerCase();
    if (!s) return all;
    return all.filter((e) => [e.action, e.target, e.result, e.ip, e.elevation ?? ""].some((v) => v.toLowerCase().includes(s)));
  }, [all, q]);

  return (
    <>
      <SettingsHeader
        title="Audit log"
        description="Every sign-in, setting change and privileged action, newest first."
        actions={
          <IconButton label="Refresh" variant="secondary" size="sm" onClick={() => query.refetch()} disabled={query.isFetching}>
            <RefreshCw className={cn(query.isFetching && "animate-spin")} />
          </IconButton>
        }
      />
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <SearchInput value={q} onChange={setQ} placeholder="Filter by action, target, IP…" className="w-full sm:w-80" label="Filter audit log" />
        <span className="text-xs text-muted" aria-live="polite">
          {q ? `${rows.length} of ${all.length} loaded entries` : `${all.length} entries loaded`}
        </span>
      </div>
      {query.isLoading ? (
        <Skeleton className="h-64 rounded-lg" />
      ) : query.error ? (
        <ErrorNote error={query.error} />
      ) : rows.length === 0 ? (
        <Card>
          <EmptyState icon={<ScrollText />} title={q ? "No matching entries" : "No audit entries yet"} />
        </Card>
      ) : (
        <Card className="overflow-hidden">
          {/* Desktop / tablet table */}
          <div className="hidden md:block">
            <Table>
              <THead sticky={false}>
                <tr>
                  <Th className="w-8" aria-label="Expand" />
                  <Th>Time</Th>
                  <Th>Action</Th>
                  <Th>Target</Th>
                  <Th>Result</Th>
                  <Th className="max-lg:hidden">Elevation</Th>
                  <Th className="max-lg:hidden">IP</Th>
                </tr>
              </THead>
              <tbody>
                {rows.map((e) => {
                  const expanded = open === e.id;
                  return (
                    <Fragment key={e.id}>
                      <tr className={cn("transition-colors hover:bg-surface-2", expanded && "bg-surface-2")}>
                        <Td className="w-8 pr-0">
                          <IconButton
                            label={expanded ? "Hide details" : "Show details"}
                            size="xs"
                            aria-expanded={expanded}
                            onClick={() => setOpen(expanded ? null : e.id)}
                          >
                            <ChevronRight className={cn("transition-transform", expanded && "rotate-90")} />
                          </IconButton>
                        </Td>
                        <Td className="num whitespace-nowrap text-muted" title={datetime(e.ts)}>
                          {ago(e.ts)}
                        </Td>
                        <Td className="num whitespace-nowrap">{e.action}</Td>
                        <Td className="max-w-[18rem] truncate" title={e.target}>
                          {e.target || <span className="text-muted">—</span>}
                        </Td>
                        <Td>
                          <Result r={e.result} />
                        </Td>
                        <Td className="max-lg:hidden">{e.elevation ? <Badge tone="accent">{e.elevation}</Badge> : <span className="text-muted">—</span>}</Td>
                        <Td className="num text-muted max-lg:hidden">{e.ip}</Td>
                      </tr>
                      {expanded && (
                        <tr className="bg-surface-2/50">
                          <td colSpan={7} className="border-b border-border px-4 py-3">
                            <Details e={e} />
                          </td>
                        </tr>
                      )}
                    </Fragment>
                  );
                })}
              </tbody>
            </Table>
          </div>
          {/* Phone list */}
          <ul className="divide-y divide-border md:hidden">
            {rows.map((e) => {
              const expanded = open === e.id;
              return (
                <li key={e.id}>
                  <button
                    type="button"
                    className="flex w-full items-start gap-2 px-3 py-2.5 text-left"
                    aria-expanded={expanded}
                    onClick={() => setOpen(expanded ? null : e.id)}
                  >
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2">
                        <span className="num truncate text-[0.8125rem] font-medium">{e.action}</span>
                        <Result r={e.result} />
                        {e.elevation && <Badge tone="accent">{e.elevation}</Badge>}
                      </div>
                      {e.target && <div className="truncate text-xs">{e.target}</div>}
                      <div className="num text-2xs text-muted">
                        {ago(e.ts)} · {e.ip}
                      </div>
                    </div>
                    <ChevronRight className={cn("mt-1 size-4 shrink-0 text-muted transition-transform", expanded && "rotate-90")} aria-hidden="true" />
                  </button>
                  {expanded && (
                    <div className="px-3 pb-3">
                      <Details e={e} />
                    </div>
                  )}
                </li>
              );
            })}
          </ul>
        </Card>
      )}
      {query.hasNextPage && (
        <div className="mt-3 flex justify-center">
          <Button size="sm" loading={query.isFetchingNextPage} onClick={() => query.fetchNextPage()}>
            Load more
          </Button>
        </div>
      )}
    </>
  );
}
