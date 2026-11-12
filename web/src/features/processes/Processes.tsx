// SPDX-License-Identifier: AGPL-3.0-only
import { Fragment, memo, useDeferredValue, useMemo, useState, type ReactNode } from "react";
import { useFillViewport, useMedia } from "@/lib/hooks";
import { useColumns } from "@/lib/useColumns";
import type { ColumnDef } from "@/lib/columns";
import { ContextMenu as CM } from "radix-ui";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ListTree, Pause, Play, RefreshCw, Rows3 } from "lucide-react";
import { api } from "@/lib/api";
import { useTopic } from "@/lib/ws";
import { bytes, duration, pct } from "@/lib/format";
import { cn } from "@/lib/cn";
import { Card } from "@/components/ui/card";
import { Button, IconButton } from "@/components/ui/button";
import { SearchInput, Segmented } from "@/components/ui/form";
import { FilterMenu, radioGroup } from "@/components/ui/filter-menu";
import { EmptyState, ErrorNote, Meter, PageHeader, Skeleton } from "@/components/ui/misc";
import { SortTh, Table, Td, Th, THead, Tr, type SortDir, type SortState } from "@/components/ui/table";
import { ProcessSheet } from "./ProcessSheet";
import { cpuTone, memTone, stateNames, type Proc, type ProcList } from "./types";

type SortKey = "name" | "pid" | "user" | "cpu" | "mem" | "threads" | "state" | "start" | "command";

const accessors: Record<SortKey, (p: Proc) => string | number> = {
  name: (p) => p.name.toLowerCase(),
  pid: (p) => p.pid,
  user: (p) => p.user,
  cpu: (p) => p.cpu,
  mem: (p) => p.memRSS,
  threads: (p) => p.threads,
  state: (p) => p.state,
  start: (p) => p.startTime,
  command: (p) => p.command || p.name,
};

function comparator(sort: SortState<SortKey>) {
  const get = accessors[sort.key];
  const m = sort.dir === "asc" ? 1 : -1;
  return (a: Proc, b: Proc) => {
    const x = get(a);
    const y = get(b);
    if (x === y) return a.pid - b.pid;
    if (typeof x === "number" && typeof y === "number") return (x - y) * m;
    return String(x).localeCompare(String(y), undefined, { numeric: true }) * m;
  };
}

const toneText = { warn: "text-warn", crit: "text-crit" } as const;

interface Row {
  p: Proc;
  depth: number;
  last?: boolean;
}

/** Flattens procs into a tree ordered by cmp among siblings. */
function buildTree(list: Proc[], cmp: (a: Proc, b: Proc) => number): Row[] {
  const byPid = new Map(list.map((p) => [p.pid, p]));
  const kids = new Map<number, Proc[]>();
  const roots: Proc[] = [];
  for (const p of list) {
    if (p.ppid !== p.pid && byPid.has(p.ppid)) {
      const k = kids.get(p.ppid);
      if (k) k.push(p);
      else kids.set(p.ppid, [p]);
    } else roots.push(p);
  }
  const out: Row[] = [];
  const seen = new Set<number>();
  const walk = (ps: Proc[], depth: number) => {
    ps.sort(cmp);
    ps.forEach((p, i) => {
      if (seen.has(p.pid)) return;
      seen.add(p.pid);
      out.push({ p, depth, last: i === ps.length - 1 });
      const k = kids.get(p.pid);
      if (k) walk(k, depth + 1);
    });
  };
  walk(roots, 0);
  return out;
}

function Summary({ data }: { data?: ProcList }) {
  const s = useMemo(() => {
    if (!data) return null;
    let cpu = 0,
      rss = 0,
      running = 0,
      threads = 0,
      zombies = 0;
    for (const p of data.procs) {
      cpu += p.cpu;
      rss += p.memRSS;
      threads += p.threads;
      if (p.state === "R") running++;
      if (p.state === "Z") zombies++;
    }
    return { cpu: cpu / Math.max(1, data.cpus), rss, running, threads, zombies };
  }, [data]);
  const items: { label: string; value: string; sub?: string; tone?: "warn" | "crit"; meter?: number }[] = s && data
    ? [
        { label: "Processes", value: String(data.procs.length), sub: `${s.threads.toLocaleString()} threads` },
        { label: "Running", value: String(s.running), sub: s.zombies ? `${s.zombies} zombie` : "0 zombies", tone: s.zombies ? "warn" : undefined },
        { label: "CPU", value: pct(s.cpu, 1), sub: `of ${data.cpus} threads`, tone: cpuTone(s.cpu), meter: s.cpu },
        {
          label: "Resident memory",
          value: bytes(s.rss),
          sub: `${pct((s.rss / data.totalMem) * 100, 0)} of ${bytes(data.totalMem, 0)}`,
          meter: (s.rss / data.totalMem) * 100,
        },
      ]
    : [];
  return (
    <Card className="mb-4 grid grid-cols-2 divide-border max-sm:[&>*:nth-child(n+3)]:border-t sm:grid-cols-4 sm:divide-x">
      {s
        ? items.map((it, i) => (
            <div key={it.label} className={cn("min-w-0 px-4 py-3", i % 2 === 1 && "max-sm:border-l max-sm:border-border")}>
              <div className="text-2xs font-medium uppercase tracking-wider text-subtle">{it.label}</div>
              <div className={cn("num mt-0.5 text-xl font-medium leading-tight", it.tone && toneText[it.tone])}>{it.value}</div>
              {/* Cells without a meter keep its space, so all cells are the same height. */}
              {it.meter !== undefined ? (
                <Meter value={it.meter} tone={it.tone ?? "accent"} className="mt-1.5" label={it.label} />
              ) : (
                <div className="mt-1.5 h-1.5" aria-hidden="true" />
              )}
              <div className="mt-1 truncate text-xs text-muted">{it.sub}</div>
            </div>
          ))
        : Array.from({ length: 4 }, (_, i) => (
            <div key={i} className="px-4 py-3">
              <Skeleton className="h-3 w-16" />
              <Skeleton className="mt-2 h-6 w-20" />
            </div>
          ))}
    </Card>
  );
}

const ProcRow = memo(function ProcRow({
  row,
  tree,
  now,
  onOpen,
  selected,
  self,
  cols,
}: {
  row: Row;
  tree: boolean;
  now: number;
  onOpen: (pid: number) => void;
  selected: boolean;
  self: boolean;
  cols: string[];
}) {
  const { p, depth } = row;
  const ct = cpuTone(p.cpu);
  const mt = memTone(p.memPct);
  const cells: Record<string, ReactNode> = {
    name: (
      <Td data-col="name">
        <div className="flex min-w-0 items-center" style={tree ? { paddingLeft: depth * 14 } : undefined}>
          {tree && depth > 0 && <span className="mr-1 shrink-0 text-subtle" aria-hidden="true">{row.last ? "└" : "├"}</span>}
          <span className={cn("truncate font-medium", p.kernel && "text-muted")} title={p.name}>
            {p.kernel ? `[${p.name}]` : p.name}
          </span>
          {self && <span className="ml-1.5 shrink-0 rounded border border-accent/30 bg-accent-soft px-1 text-2xs text-accent">wargo</span>}
        </div>
      </Td>
    ),
    pid: (
      <Td data-col="pid" className="num text-right text-muted">{p.pid}</Td>
    ),
    user: (
      <Td data-col="user" className="max-w-28 truncate text-muted max-lg:hidden">{p.user}</Td>
    ),
    cpu: (
      <Td data-col="cpu" className="text-right">
        <div className="flex items-center justify-end gap-2">
          <Meter value={Math.min(100, p.cpu)} tone={ct ?? "accent"} className="w-10 max-xl:hidden" label={`CPU ${p.cpu.toFixed(1)}%`} />
          <span className={cn("num w-12", ct ? toneText[ct] : p.cpu < 0.05 && "text-subtle")}>{p.cpu.toFixed(1)}</span>
        </div>
      </Td>
    ),
    mem: (
      <Td data-col="mem" className={cn("num text-right", mt ? toneText[mt] : "")}>
        {bytes(p.memRSS)}
        <span className="ml-1.5 text-2xs text-subtle max-xl:hidden">{p.memPct.toFixed(1)}%</span>
      </Td>
    ),
    threads: (
      <Td data-col="threads" className="num text-right text-muted max-lg:hidden">{p.threads}</Td>
    ),
    state: (
      <Td data-col="state" className="max-xl:hidden">
        <span className={cn("text-xs", p.state === "R" ? "text-ok" : p.state === "Z" || p.state === "D" ? "text-warn" : "text-muted")}>
          {stateNames[p.state] ?? p.state}
        </span>
      </Td>
    ),
    start: (
      <Td data-col="start" className="num whitespace-nowrap text-right text-xs text-muted max-lg:hidden" title={new Date(p.startTime).toLocaleString()}>
        {duration((now - p.startTime) / 1000)}
      </Td>
    ),
    command: (
      <Td data-col="command" className="num max-w-0 truncate text-xs text-muted max-md:hidden" title={p.command}>
        {p.command || <span className="text-subtle">—</span>}
      </Td>
    ),
  };
  return (
    <Tr
      onClick={() => onOpen(p.pid)}
      selected={selected}
      tabIndex={0}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onOpen(p.pid);
        }
      }}
      className="focus-visible:bg-surface-2 focus-visible:outline-none aria-selected:[&_.text-subtle]:text-muted"
    >
      {cols.map((id) => (
        <Fragment key={id}>{cells[id]}</Fragment>
      ))}
    </Tr>
  );
});

function MobileRow({ row, tree, onOpen }: { row: Row; tree: boolean; onOpen: (pid: number) => void }) {
  const { p, depth } = row;
  const ct = cpuTone(p.cpu);
  const mt = memTone(p.memPct);
  return (
    <li>
      <button
        type="button"
        onClick={() => onOpen(p.pid)}
        className="flex min-h-12 w-full items-center gap-3 border-b border-border px-4 py-2 text-left hover:bg-surface-2"
        style={tree ? { paddingLeft: 14 + Math.min(depth, 6) * 12 } : undefined}
      >
        <div className="min-w-0 flex-1">
          <div className={cn("truncate text-sm font-medium", p.kernel && "text-muted")}>{p.kernel ? `[${p.name}]` : p.name}</div>
          <div className="num truncate text-xs text-subtle">
            {p.pid} · {p.user}
          </div>
        </div>
        <div className="shrink-0 text-right">
          <div className={cn("num text-sm", ct && toneText[ct])}>{p.cpu.toFixed(1)}%</div>
          <div className={cn("num text-xs text-muted", mt && toneText[mt])}>{bytes(p.memRSS)}</div>
        </div>
      </button>
    </li>
  );
}

// Desktop table columns (phones use the card list). Name stays first;
// Command takes the leftover width. Visibility follows the viewport.
const COLUMNS: ColumnDef[] = [
  { id: "name", width: 220, min: 120, pinned: "start" },
  { id: "pid", width: 80 },
  { id: "user", width: 112 },
  { id: "cpu", width: 120 },
  { id: "mem", width: 120 },
  { id: "threads", width: 64 },
  { id: "state", width: 96 },
  { id: "start", width: 96 },
  { id: "command", elastic: true },
];
const HEADERS: Record<string, { label: string; className?: string; align?: "right"; dir?: SortDir }> = {
  name: { label: "Name", dir: "asc" },
  pid: { label: "PID", align: "right", dir: "asc" },
  user: { label: "User", className: "max-lg:hidden", dir: "asc" },
  cpu: { label: "CPU %", align: "right" },
  mem: { label: "Memory", align: "right" },
  threads: { label: "Thr", align: "right", className: "max-lg:hidden" },
  state: { label: "State", className: "max-xl:hidden", dir: "asc" },
  start: { label: "Uptime", align: "right", className: "max-lg:hidden" },
  command: { label: "Command", className: "max-md:hidden" },
};
/** Narrowest the Command column gets before the table scrolls sideways. */
const COMMAND_MIN = 240;

const PAGE = 200;

export default function Page() {
  return <ProcessesPanel />;
}

/** Process list; `embedded` leaves the title to the System page's tab. */
// Memoised: parents re-render on every 1 s metrics tick.
export const ProcessesPanel = memo(function ProcessesPanel({ embedded }: { embedded?: boolean }) {
  const qc = useQueryClient();
  const [live, setLive] = useState(true);
  const [q, setQ] = useState("");
  const [scope, setScope] = useState<"mine" | "all">("all");
  const [view, setView] = useState<"list" | "tree">("list");
  const [kernel, setKernel] = useState(false);
  const [limit, setLimit] = useState(PAGE);
  const [sort, setSort] = useState<SortState<SortKey>>({ key: "cpu", dir: "desc" });
  const [open, setOpen] = useState<number | null>(null);
  const columns = useColumns("wargo.columns.processes", COLUMNS);
  const { order } = columns;
  const lg = useMedia("(min-width: 64rem)");
  const xl = useMedia("(min-width: 80rem)");
  const cols = useMemo(() => {
    const vis = new Set(COLUMNS.map((c) => c.id));
    if (!lg) for (const id of ["user", "threads", "start"]) vis.delete(id);
    if (!xl) vis.delete("state");
    return order(vis);
  }, [order, lg, xl]);
  const tableMin = cols.reduce((sum, id) => sum + (columns.width(id) ?? COMMAND_MIN), 0);
  const query = useQuery({
    queryKey: ["procs"],
    queryFn: () => api.get<ProcList>("/api/procs"),
    refetchOnWindowFocus: false,
  });
  useTopic<ProcList>(live ? "procs" : null, (d) => qc.setQueryData(["procs"], d));
  const data = query.data;
  const dq = useDeferredValue(q.trim().toLowerCase());

  const onSort = (k: SortKey, d: SortDir = "desc") =>
    setSort((s) => (s.key === k ? { key: k, dir: s.dir === "asc" ? "desc" : "asc" } : { key: k, dir: d }));

  const rows = useMemo<Row[]>(() => {
    if (!data) return [];
    let list = data.procs;
    if (scope === "mine") list = list.filter((p) => p.uid === data.uid);
    if (!kernel) list = list.filter((p) => !p.kernel);
    const match = (p: Proc) =>
      !dq || p.name.toLowerCase().includes(dq) || String(p.pid) === dq || p.user.toLowerCase() === dq || p.command.toLowerCase().includes(dq);
    const cmp = comparator(sort);
    if (view === "tree") {
      if (dq) {
        const byPid = new Map(list.map((p) => [p.pid, p]));
        const keep = new Set<number>();
        for (const p of list) {
          if (!match(p)) continue;
          let cur: Proc | undefined = p;
          while (cur && !keep.has(cur.pid)) {
            keep.add(cur.pid);
            cur = byPid.get(cur.ppid);
          }
        }
        list = list.filter((p) => keep.has(p.pid));
      }
      return buildTree([...list], cmp);
    }
    return list.filter(match).sort(cmp).map((p) => ({ p, depth: 0 }));
  }, [data, scope, kernel, dq, sort, view]);

  const shown = rows.slice(0, limit);
  const [tableEl, setTableEl] = useState<HTMLDivElement | null>(null);
  const [listEl, setListEl] = useState<HTMLDivElement | null>(null);
  const [footerEl, setFooterEl] = useState<HTMLDivElement | null>(null);
  // Leave room for the footer row and the card's bottom border.
  const reserve = (footerEl?.offsetHeight ?? 45) + 1;
  const tableH = useFillViewport(tableEl, { min: 320, reserve });
  const listH = useFillViewport(listEl, { min: 320, reserve });
  const now = data?.sampledAt ?? Date.now();
  const tree = view === "tree";

  const Header = embedded ? SectionHeader : PageHeader;
  return (
    <div>
      <Header
        title="Processes"
        description={data ? `${data.procs.length} processes on this host` : "Live view of everything running on this host"}
        actions={
          <>
            <Button
              size="sm"
              variant={live ? "secondary" : "primary"}
              aria-pressed={!live}
              onClick={() => setLive((v) => !v)}
              icon={live ? <Pause /> : <Play />}
              title={live ? "Pause live updates" : "Resume live updates"}
            >
              {live ? "Pause" : "Resume"}
            </Button>
            <IconButton label="Refresh now" size="sm" variant="secondary" onClick={() => query.refetch()}>
              <RefreshCw className={cn(query.isFetching && "animate-spin")} />
            </IconButton>
          </>
        }
      />
      <Summary data={data} />
      <Card>
        <div className="flex flex-wrap items-center gap-2 border-b border-border px-3 py-2.5">
          <SearchInput value={q} onChange={(v) => (setQ(v), setLimit(PAGE))} placeholder="Filter by name, PID, user or command…" className="min-w-0 flex-1 basis-56" label="Filter processes" />
          <FilterMenu
            active={(scope !== "all" ? 1 : 0) + (kernel ? 1 : 0)}
            onClear={() => {
              setScope("all");
              setKernel(false);
            }}
            groups={[
              radioGroup<"mine" | "all">("Owner", scope, setScope, [
                { value: "all", label: "All users" },
                { value: "mine", label: "Mine" },
              ]),
              { kind: "check", title: "Include", items: [{ label: "Kernel threads", checked: kernel, onChange: setKernel }] },
            ]}
          />
          <Segmented
            label="Layout"
            value={view}
            onChange={setView}
            options={[
              { value: "list", label: <><Rows3 /> List</>, title: "Flat list" },
              { value: "tree", label: <><ListTree /> Tree</>, title: "Tree view" },
            ]}
          />
          {!live && <span className="text-xs font-medium text-warn" role="status">Paused</span>}
        </div>
        {query.error ? (
          <div className="p-4">
            <ErrorNote error={query.error} />
          </div>
        ) : !data ? (
          <div className="space-y-2 p-4">
            {Array.from({ length: 10 }, (_, i) => (
              <Skeleton key={i} className="h-6" />
            ))}
          </div>
        ) : rows.length === 0 ? (
          <EmptyState title="No matching processes" icon={<ListTree />}>
            Try a different filter or include all users.
          </EmptyState>
        ) : (
          <>
            {/* Scrollable list sized to the viewport (with a minimum), so the page itself doesn't grow. */}
            <div ref={setTableEl} style={{ height: tableH }} className="scroll-area min-h-80 max-md:hidden" tabIndex={0} role="region" aria-label="Process table">
              <Table className="table-fixed" data-columns-root="" style={{ minWidth: tableMin }}>
                <THead>
                  <CM.Root>
                    <CM.Trigger asChild>
                      <tr>
                        {cols.map((id) => {
                          const h = HEADERS[id];
                          const w = columns.width(id);
                          const extra = (
                            <>
                              {columns.drop?.id === id && <span aria-hidden="true" className={cn("absolute inset-y-1 z-20 w-0.5 rounded bg-accent", columns.drop.side === "before" ? "-left-px" : "-right-px")} />}
                              <span {...columns.gripProps(id)} />
                            </>
                          );
                          const hp = columns.headerProps(id);
                          if (id === "command")
                            return (
                              <Th key={id} {...hp} className={cn("relative", h.className)} aria-description={columns.describe(id)} tabIndex={0}>
                                {h.label}
                                {extra}
                              </Th>
                            );
                          return (
                            <SortTh key={id} k={id as SortKey} sort={sort} onSort={onSort} defaultDir={h.dir} align={h.align} className={h.className} thProps={{ ...hp, style: { ...hp.style, width: w } }} description={columns.describe(id)} extra={extra}>
                              {h.label}
                            </SortTh>
                          );
                        })}
                      </tr>
                    </CM.Trigger>
                    <CM.Portal>
                      <CM.Content className="z-50 min-w-44 rounded-md border border-border-strong bg-surface p-1 shadow-xl focus:outline-none">
                        <CM.Item onSelect={columns.reset} className="flex h-7 cursor-default select-none items-center rounded px-2 text-ui outline-none data-[highlighted]:bg-surface-3">
                          Reset columns
                        </CM.Item>
                      </CM.Content>
                    </CM.Portal>
                  </CM.Root>
                </THead>
                <tbody>
                  {shown.map((r) => (
                    <ProcRow key={r.p.pid} row={r} tree={tree} now={now} onOpen={setOpen} selected={open === r.p.pid} self={r.p.pid === data.self} cols={cols} />
                  ))}
                </tbody>
              </Table>
            </div>
            <div className="flex items-center gap-2 border-b border-border px-4 py-2 md:hidden">
              <span className="text-2xs font-medium uppercase tracking-wider text-subtle">Sort</span>
              <Segmented
                size="sm"
                label="Sort by"
                value={sort.key === "mem" ? "mem" : sort.key === "name" ? "name" : "cpu"}
                onChange={(k) => setSort({ key: k, dir: k === "name" ? "asc" : "desc" })}
                options={[
                  { value: "cpu", label: "CPU" },
                  { value: "mem", label: "Memory" },
                  { value: "name", label: "Name" },
                ]}
              />
            </div>
            <div ref={setListEl} style={{ height: listH }} className="scroll-area min-h-80 md:hidden" tabIndex={0} role="region" aria-label="Process list">
              <ul aria-label="Processes">
                {shown.map((r) => (
                  <MobileRow key={r.p.pid} row={r} tree={tree} onOpen={setOpen} />
                ))}
              </ul>
            </div>
            <div ref={setFooterEl} className="flex flex-wrap items-center gap-3 border-t border-border px-4 py-2.5 text-xs text-muted">
              <span className="num">
                Showing {shown.length} of {rows.length}
              </span>
              {rows.length > shown.length && (
                <>
                  <Button size="sm" onClick={() => setLimit((l) => l + PAGE)}>
                    Show {Math.min(PAGE, rows.length - shown.length)} more
                  </Button>
                  <Button size="sm" variant="ghost" onClick={() => setLimit(rows.length)}>
                    Show all
                  </Button>
                </>
              )}
            </div>
          </>
        )}
      </Card>
      <ProcessSheet pid={open} onClose={() => setOpen(null)} onOpen={setOpen} list={data} live={live} />
    </div>
  );
});

function SectionHeader({ title, description, actions }: { title: string; description?: React.ReactNode; actions?: React.ReactNode }) {
  return (
    <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
      <div className="min-w-0">
        {/* The System page's "Processes" tab is the visible title. */}
        <h2 id="processes-heading" className="sr-only">
          {title}
        </h2>
        {description && <p className="mt-0.5 text-sm text-muted">{description}</p>}
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  );
}
