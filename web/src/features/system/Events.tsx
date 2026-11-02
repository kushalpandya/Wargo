// SPDX-License-Identifier: AGPL-3.0-only
import { memo, useDeferredValue, useMemo, useState } from "react";
import { useSearchParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, Boxes, CheckCircle2, ChevronRight, CircleAlert, Cpu, RefreshCw, Server, ShieldAlert } from "lucide-react";
import { api, qs } from "@/lib/api";
import { ago } from "@/lib/format";
import { cn } from "@/lib/cn";
import { useFillViewport } from "@/lib/hooks";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { SearchInput, Segmented, Select } from "@/components/ui/form";
import { Badge, EmptyState, ErrorNote, Notice, Skeleton } from "@/components/ui/misc";

type Source = "system" | "kernel" | "containers" | "wargo";
type Range = "1h" | "24h" | "7d";
type Level = "error" | "warning";

interface SysEvent {
  key: string;
  source: Source;
  level: Level;
  origin: string;
  unit?: string;
  message: string;
  count: number;
  first: number;
  last: number;
}

interface EventsResponse {
  events: SysEvent[];
  errors: number;
  warnings: number;
  sources: Record<Source, { available: boolean; note?: string }>;
  since: number;
  truncated?: boolean;
}

const SOURCES: { value: Source; label: string; icon: React.ReactNode }[] = [
  { value: "system", label: "System", icon: <Server /> },
  { value: "kernel", label: "Kernel", icon: <Cpu /> },
  { value: "containers", label: "Containers", icon: <Boxes /> },
  { value: "wargo", label: "Wargo", icon: <ShieldAlert /> },
];
const sourceOf = (s: Source) => SOURCES.find((x) => x.value === s)!;
const RANGE_LABEL: Record<Range, string> = { "1h": "last hour", "24h": "last 24 hours", "7d": "last 7 days" };

function EventRow({ e }: { e: SysEvent }) {
  const [open, setOpen] = useState(false);
  const src = sourceOf(e.source);
  const err = e.level === "error";
  return (
    <li className="border-b border-border last:border-0">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="flex w-full items-start gap-3 px-3.5 py-2.5 text-left hover:bg-surface-2 focus-visible:bg-surface-2 focus-visible:outline-none"
      >
        <span className={cn("mt-0.5 shrink-0 [&_svg]:size-4", err ? "text-crit" : "text-warn")}>
          {err ? <CircleAlert aria-label="Error" /> : <AlertTriangle aria-label="Warning" />}
        </span>
        <span className="min-w-0 flex-1">
          <span className={cn("block text-[0.8125rem] text-fg", open ? "whitespace-pre-wrap break-words" : "truncate")}>{e.message || "(no message)"}</span>
          <span className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-muted">
            <span className="inline-flex items-center gap-1 text-subtle [&_svg]:size-3">
              {src.icon}
              {src.label}
            </span>
            {e.origin && <span className="num truncate">{e.origin}</span>}
            {e.unit && e.unit !== e.origin && <span className="num hidden truncate text-subtle sm:inline">{e.unit}</span>}
          </span>
          {open && (
            <span className="num mt-1.5 block text-2xs text-subtle">
              {e.count > 1 ? `First ${new Date(e.first).toLocaleString()} · last ${new Date(e.last).toLocaleString()}` : new Date(e.last).toLocaleString()}
            </span>
          )}
        </span>
        <span className="flex shrink-0 flex-col items-end gap-1">
          <span className="num text-xs text-muted" title={new Date(e.last).toLocaleString()}>
            {ago(e.last)}
          </span>
          {e.count > 1 && (
            <Badge tone={err ? "crit" : "warn"} className="num" title={`${e.count.toLocaleString()} times`}>
              ×{e.count.toLocaleString()}
            </Badge>
          )}
        </span>
        <ChevronRight className={cn("mt-0.5 size-4 shrink-0 text-subtle transition-transform", open && "rotate-90")} aria-hidden="true" />
      </button>
    </li>
  );
}

/** System → Events: grouped errors and warnings from the OS, Docker and Wargo. */
export const EventsPanel = memo(function EventsPanel() {
  const [params, setParams] = useSearchParams();
  const range = (["1h", "24h", "7d"].includes(params.get("range") ?? "") ? params.get("range") : "24h") as Range;
  const level = (params.get("level") === "error" ? "error" : "warning") as Level;
  const source = (SOURCES.some((s) => s.value === params.get("source")) ? params.get("source") : "all") as Source | "all";
  const [q, setQ] = useState("");
  const dq = useDeferredValue(q.trim().toLowerCase());

  // Filters live in the URL so a view can be linked to.
  const set = (k: string, v: string | null) => {
    const next = new URLSearchParams(params);
    if (v === null) next.delete(k);
    else next.set(k, v);
    setParams(next, { replace: true });
  };

  const query = useQuery({
    queryKey: ["system", "events", range, level],
    queryFn: () => api.get<EventsResponse>(`/api/system/events${qs({ range, level })}`),
    refetchInterval: 60_000,
    placeholderData: (prev) => prev,
  });
  const data = query.data;
  const [listEl, setListEl] = useState<HTMLDivElement | null>(null);
  const [footEl, setFootEl] = useState<HTMLParagraphElement | null>(null);
  const listH = useFillViewport(listEl, { min: 320, reserve: (footEl?.offsetHeight ?? 0) + 1 });

  const shown = useMemo(
    () =>
      (data?.events ?? []).filter(
        (e) =>
          (source === "all" || e.source === source) &&
          (!dq || e.message.toLowerCase().includes(dq) || e.origin.toLowerCase().includes(dq) || (e.unit ?? "").toLowerCase().includes(dq)),
      ),
    [data, source, dq],
  );
  const counts = useMemo(() => {
    const c: Record<string, number> = { all: data?.events.length ?? 0 };
    for (const e of data?.events ?? []) c[e.source] = (c[e.source] ?? 0) + 1;
    return c;
  }, [data]);

  // Sources that couldn't be read, or only partly (journal permissions).
  // The journal backs both System and Kernel, so notes are de-duplicated.
  const notes = [
    ...new Set(
      data ? (Object.entries(data.sources) as [Source, { available: boolean; note?: string }][]).filter(([s, st]) => st.note && (!st.available || s === "system")).map(([, st]) => st.note!) : [],
    ),
  ];
  const unavailable = data ? (Object.entries(data.sources) as [Source, { available: boolean }][]).filter(([, st]) => !st.available).map(([s]) => sourceOf(s).label) : [];

  return (
    <div className="flex flex-col gap-3">
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3">
        <Card className="p-3.5">
          <div className="text-2xs font-semibold uppercase tracking-wider text-subtle">Errors</div>
          <div className={cn("num mt-1 text-2xl font-medium", data?.errors ? "text-crit" : "text-fg")}>{data ? data.errors.toLocaleString() : "–"}</div>
          <div className="text-xs text-muted">{RANGE_LABEL[range]}</div>
        </Card>
        <Card className="p-3.5">
          <div className="text-2xs font-semibold uppercase tracking-wider text-subtle">Warnings</div>
          <div className={cn("num mt-1 text-2xl font-medium", level === "warning" && data?.warnings ? "text-warn" : "text-fg")}>
            {data ? (level === "warning" ? data.warnings.toLocaleString() : "Hidden") : "–"}
          </div>
          <div className="text-xs text-muted">{RANGE_LABEL[range]}</div>
        </Card>
        <Card className="col-span-2 p-3.5 sm:col-span-1">
          <div className="text-2xs font-semibold uppercase tracking-wider text-subtle">Distinct messages</div>
          <div className="num mt-1 text-2xl font-medium">{data ? data.events.length.toLocaleString() : "–"}</div>
          <div className="text-xs text-muted">repeats are grouped</div>
        </Card>
      </div>

      {notes.map((n) => (
        <Notice key={n} tone="frost">
          {n}
        </Notice>
      ))}

      <Card>
        <div className="flex flex-wrap items-center gap-2 border-b border-border px-3 py-2.5">
          <SearchInput value={q} onChange={setQ} placeholder="Filter messages…" label="Filter events" className="min-w-0 flex-1 basis-44" />
          <Segmented<Level>
            label="Levels"
            value={level}
            onChange={(v) => set("level", v === "warning" ? null : v)}
            options={[
              { value: "error", label: "Errors" },
              { value: "warning", label: "Errors & warnings" },
            ]}
          />
          <label htmlFor="events-source" className="sr-only">
            Source
          </label>
          <Select id="events-source" className="w-auto" value={source} onChange={(e) => set("source", e.target.value === "all" ? null : e.target.value)}>
            <option value="all">All sources ({counts.all})</option>
            {SOURCES.map((s) => (
              <option key={s.value} value={s.value}>
                {s.label} ({counts[s.value] ?? 0})
              </option>
            ))}
          </Select>
          <label htmlFor="events-range" className="sr-only">
            Time range
          </label>
          <Select id="events-range" className="w-auto" value={range} onChange={(e) => set("range", e.target.value === "24h" ? null : e.target.value)}>
            <option value="1h">Last hour</option>
            <option value="24h">Last 24 hours</option>
            <option value="7d">Last 7 days</option>
          </Select>
          <Button
            size="sm"
            variant="ghost"
            icon={<RefreshCw className={cn(query.isFetching && "animate-spin")} />}
            onClick={() => api.get<EventsResponse>(`/api/system/events${qs({ range, level, fresh: 1 })}`).then(() => query.refetch())}
            aria-label="Refresh events"
            title="Refresh"
          />
        </div>

        {query.isLoading ? (
          <div className="space-y-2 p-3.5">
            {Array.from({ length: 6 }, (_, i) => (
              <Skeleton key={i} className="h-11" />
            ))}
          </div>
        ) : query.error ? (
          <div className="p-3.5">
            <ErrorNote error={query.error} />
          </div>
        ) : shown.length === 0 ? (
          <EmptyState icon={<CheckCircle2 />} title={data?.events.length ? "Nothing matches" : `No ${level === "error" ? "errors" : "errors or warnings"} in the ${RANGE_LABEL[range]}`}>
            {data?.events.length ? "Try another source or clear the filter." : unavailable.length ? `${unavailable.join(", ")} could not be read.` : "Everything looks quiet."}
          </EmptyState>
        ) : (
          // Scrolls within the viewport (with a minimum height) instead of growing the page.
          <div ref={setListEl} style={{ maxHeight: listH }} className="scroll-area" tabIndex={0} role="region" aria-label="Event list">
            <ul aria-label="Events">
              {shown.map((e) => (
                <EventRow key={e.key} e={e} />
              ))}
            </ul>
          </div>
        )}
        {data?.truncated && <p ref={setFootEl} className="border-t border-border px-3.5 py-2 text-xs text-muted">Showing the 500 most recent distinct messages.</p>}
      </Card>
    </div>
  );
});
