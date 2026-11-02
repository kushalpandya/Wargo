// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useMemo, useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useSearchParams } from "react-router";
import { AlertTriangle, ArrowRight, Download, ExternalLink, Magnet, Search as SearchIcon, SendToBack } from "lucide-react";
import { api, qs } from "@/lib/api";
import { ago, bytes } from "@/lib/format";
import { cn } from "@/lib/cn";
import { safeHttpUrl } from "@/lib/url";
import { useCapabilities } from "@/app/session";
import { Card } from "@/components/ui/card";
import { Button, IconButton } from "@/components/ui/button";
import { Select, SearchInput } from "@/components/ui/form";
import { EmptyState, ErrorNote, PageHeader, Spinner } from "@/components/ui/misc";
import { SortTh, Table, Td, Th, THead, Tr, useSort } from "@/components/ui/table";
import { CATEGORIES, type IndexerRef, type SearchResponse, type SearchResult } from "./api";
import { IndexerPicker, useResultActions, WithReason, type RowAction } from "./actions";

type SortKey = "seeders" | "size" | "age" | "name";
const PAGE = 100;

function parseList(v: string | null): number[] {
  return (v ?? "")
    .split(",")
    .map((x) => parseInt(x, 10))
    .filter((n) => Number.isFinite(n) && n >= 0);
}

function seedTone(n: number | null) {
  if (n === null) return "text-subtle";
  if (n >= 10) return "text-ok";
  if (n > 0) return "text-warn";
  return "text-crit";
}

function useElapsed(active: boolean) {
  const [s, setS] = useState(0);
  useEffect(() => {
    if (!active) return;
    const start = Date.now();
    setS(0);
    const t = window.setInterval(() => setS(Math.floor((Date.now() - start) / 1000)), 500);
    return () => window.clearInterval(t);
  }, [active]);
  return s;
}

// The last search (query + filters) is remembered for the lifetime of the
// page so results survive navigating away; React Query keeps the data.
const last = { params: "", filter: "" };

export default function SearchPage({ embedded }: { embedded?: boolean } = {}) {
  const [params, setParams] = useSearchParams();
  useEffect(() => {
    if (!params.get("q") && last.params) {
      const next = new URLSearchParams(params);
      for (const [k, v] of new URLSearchParams(last.params)) next.set(k, v);
      setParams(next, { replace: true });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const q = params.get("q") ?? "";
  const cats = parseList(params.get("cat"));
  const idx = parseList(params.get("idx"));
  const catKey = cats.join(",");
  const idxKey = idx.join(",");
  const [draft, setDraft] = useState(q);
  const [filter, setFilterState] = useState(last.filter);
  const setFilter = (v: string) => {
    last.filter = v;
    setFilterState(v);
  };
  useEffect(() => {
    if (!q) return;
    const keep = new URLSearchParams();
    for (const k of ["q", "cat", "idx"]) {
      const v = params.get(k);
      if (v) keep.set(k, v);
    }
    last.params = keep.toString();
  }, [q, params]);
  const [limit, setLimit] = useState(PAGE);
  useEffect(() => setDraft(q), [q]);

  const caps = useCapabilities().data;
  const canQueue = !!(caps?.prowlarr?.hasTransmissionClient || caps?.transmission?.available);
  const queueReason = canQueue
    ? undefined
    : "No download client: add Transmission as a download client in Prowlarr, or connect Transmission in Settings → Integrations.";

  const indexers = useQuery({
    queryKey: ["prowlarr", "indexers"],
    queryFn: () => api.get<{ indexers: IndexerRef[] }>("/api/prowlarr/indexers"),
    staleTime: 5 * 60_000,
  });

  const search = useQuery({
    queryKey: ["prowlarr", "search", q, catKey, idxKey],
    queryFn: ({ signal }) => api.get<SearchResponse>(`/api/prowlarr/search${qs({ q, categories: catKey, indexers: idxKey })}`, signal),
    enabled: q.trim() !== "",
    // Keep results until the page is reloaded; "Search" refetches explicitly.
    staleTime: Infinity,
    gcTime: Infinity,
    retry: false,
    refetchOnWindowFocus: false,
  });
  const elapsed = useElapsed(search.isFetching);

  // Keep only the five most recent searches in memory.
  const qc = useQueryClient();
  useEffect(() => {
    const old = qc
      .getQueryCache()
      .findAll({ queryKey: ["prowlarr", "search"] })
      .sort((a, b) => b.state.dataUpdatedAt - a.state.dataUpdatedAt)
      .slice(5);
    old.forEach((query) => qc.removeQueries({ queryKey: query.queryKey, exact: true }));
  }, [qc, search.dataUpdatedAt]);

  const update = (patch: Record<string, string | null>) => {
    const next = new URLSearchParams(params);
    for (const [k, v] of Object.entries(patch)) {
      if (v) next.set(k, v);
      else next.delete(k);
    }
    setParams(next, { replace: false });
    setLimit(PAGE);
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const v = draft.trim();
    if (!v) return;
    if (v === q) search.refetch();
    else update({ q: v });
  };

  const filtered = useMemo(() => {
    const list = search.data?.results ?? [];
    const f = filter.trim().toLowerCase();
    if (!f) return list;
    const words = f.split(/\s+/);
    return list.filter((r) => {
      const hay = `${r.title} ${r.indexer} ${r.categories.map((c) => c.name).join(" ")}`.toLowerCase();
      return words.every((w) => hay.includes(w));
    });
  }, [search.data, filter]);

  const { sorted, sort, toggle, setSort } = useSort<SearchResult, SortKey>(
    filtered,
    { key: "seeders", dir: "desc" },
    {
      seeders: (r) => r.seeders ?? -1,
      size: (r) => r.size,
      age: (r) => (r.age ? -new Date(r.age).getTime() : undefined),
      name: (r) => r.title,
    },
  );
  const actions = useResultActions(() => search.refetch());
  const shown = sorted.slice(0, limit);
  const indexerList = indexers.data?.indexers ?? search.data?.indexers ?? [];
  const indexerCount = idx.length || indexerList.length;

  const toggleCat = (id: number) => {
    const next = cats.includes(id) ? cats.filter((c) => c !== id) : [...cats, id];
    update({ cat: next.join(",") || null });
  };

  return (
    <div className="@container">
      {!embedded && <PageHeader title="Torrent search" description={`Searches ${idx.length ? "the selected" : "all enabled"} Prowlarr indexers.`} />}

      <Card className={cn(embedded && "flex min-h-96 flex-col")}>
        <form onSubmit={submit} role="search" className="flex flex-wrap items-center gap-2 border-b border-border p-3">
          <div className="flex min-w-0 flex-1 basis-64">
            <div className="relative min-w-0 flex-1">
              <SearchIcon className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-subtle" />
              <input
                type="search"
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
                placeholder="Search torrents, e.g. debian 12 netinst"
                aria-label="Search query"
                autoFocus={!q}
                maxLength={200}
                enterKeyHint="search"
                className="h-9 w-full min-w-0 rounded-l-md rounded-r-none border border-r-0 border-border bg-surface-2 pl-9 pr-3 text-[0.9375rem] text-fg placeholder:text-subtle hover:border-border-strong focus:border-focus focus:outline-none focus:ring-2 focus:ring-focus/25 [&::-webkit-search-cancel-button]:hidden"
              />
            </div>
            <button
              type="submit"
              aria-label={search.isFetching ? "Searching" : "Search"}
              title="Search"
              disabled={!draft.trim() || search.isFetching}
              className="flex h-9 w-10 shrink-0 items-center justify-center rounded-r-md border border-accent bg-accent text-accent-fg transition-colors hover:bg-accent-strong focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus/25 disabled:cursor-not-allowed disabled:border-border disabled:bg-surface-3 disabled:text-subtle"
            >
              {search.isFetching ? <Spinner className="size-4" /> : <ArrowRight className="size-4" />}
            </button>
          </div>
          <IndexerPicker indexers={indexerList} value={idx} onChange={(v) => update({ idx: v.join(",") || null })} />
          <div className="flex w-full flex-wrap items-center gap-1.5" role="group" aria-label="Categories">
            <span className="mr-1 text-2xs font-semibold uppercase tracking-wider text-subtle">Category</span>
            <CatChip active={cats.length === 0} onClick={() => update({ cat: null })}>
              All
            </CatChip>
            {CATEGORIES.map((c) => (
              <CatChip key={c.id} active={cats.includes(c.id)} onClick={() => toggleCat(c.id)}>
                {c.label}
              </CatChip>
            ))}
          </div>
        </form>

        {!q ? (
          <Intro />
        ) : search.isFetching && !search.data ? (
          <div className="flex flex-col items-center gap-3 px-6 py-14 text-center" role="status" aria-live="polite">
            <Spinner className="size-6 text-accent" />
            <p className="font-medium">Searching {indexerCount ? `${indexerCount} indexer${indexerCount === 1 ? "" : "s"}` : "indexers"}…</p>
            <p className="num text-xs text-muted">{elapsed}s · public indexers can take up to 30 seconds</p>
          </div>
        ) : search.error ? (
          <div className="p-3.5">
            <ErrorNote error={search.error} />
          </div>
        ) : search.data ? (
          <>
            <div className="flex flex-wrap items-center gap-2 border-b border-border px-3 py-2">
              <SearchInput
                value={filter}
                onChange={(v) => (setFilter(v), setLimit(PAGE))}
                placeholder="Filter results…"
                label="Filter results"
                className="min-w-0 flex-1 basis-48 @2xl:max-w-xs"
              />
              <div className="flex items-center gap-1.5 @3xl:hidden">
                <label htmlFor="sort-results" className="text-2xs font-semibold uppercase tracking-wider text-subtle">
                  Sort
                </label>
                <Select
                  id="sort-results"
                  value={`${sort.key}:${sort.dir}`}
                  onChange={(e) => {
                    const [key, dir] = e.target.value.split(":") as [SortKey, "asc" | "desc"];
                    setSort({ key, dir });
                  }}
                  className="w-auto"
                >
                  <option value="seeders:desc">Most seeders</option>
                  <option value="size:desc">Largest</option>
                  <option value="size:asc">Smallest</option>
                  <option value="age:desc">Newest</option>
                  <option value="name:asc">Name</option>
                </Select>
              </div>
              <span className="num ml-auto text-xs text-muted" role="status">
                {filtered.length !== search.data.results.length ? `${filtered.length} of ` : ""}
                {search.data.results.length} results · {(search.data.tookMs / 1000).toFixed(1)}s{search.isFetching && " · refreshing…"}
              </span>
            </div>
            {search.data.errors.length > 0 && <IndexerErrors errors={search.data.errors} />}
            {sorted.length === 0 ? (
              <EmptyState className="flex-1" icon={<SearchIcon />} title={filter ? "No results match the filter" : "No results"}>
                {filter ? "Clear the filter to see all results." : "Try fewer or different words, another category, or all indexers."}
              </EmptyState>
            ) : (
              <>
                <div className="@max-3xl:hidden">
                  <Table className="table-fixed">
                    <THead>
                      <tr>
                        <SortTh k="name" sort={sort} onSort={toggle} defaultDir="asc">
                          Title
                        </SortTh>
                        <SortTh k="size" sort={sort} onSort={toggle} align="right" className="w-24">
                          Size
                        </SortTh>
                        <SortTh k="seeders" sort={sort} onSort={toggle} align="right" className="w-24">
                          S / L
                        </SortTh>
                        <SortTh k="age" sort={sort} onSort={toggle} align="right" className="w-28 @max-5xl:hidden">
                          Age
                        </SortTh>
                        <Th className="w-[7.5rem] text-right">
                          <span className="sr-only">Actions</span>
                        </Th>
                      </tr>
                    </THead>
                    <tbody>
                      {shown.map((r) => (
                        <ResultRow
                          key={r.id}
                          r={r}
                          busy={actions.busy[r.id]}
                          magnetOnly={actions.magnetOnly[r.id]}
                          actions={actions}
                          queueReason={queueReason}
                        />
                      ))}
                    </tbody>
                  </Table>
                </div>
                <ul className="@3xl:hidden" aria-label="Search results">
                  {shown.map((r) => (
                    <ResultCard key={r.id} r={r} busy={actions.busy[r.id]} magnetOnly={actions.magnetOnly[r.id]} actions={actions} queueReason={queueReason} />
                  ))}
                </ul>
                {sorted.length > limit && (
                  <div className="flex justify-center border-t border-border p-3">
                    <Button size="sm" onClick={() => setLimit((l) => l + PAGE)}>
                      Show {Math.min(PAGE, sorted.length - limit)} more
                    </Button>
                  </div>
                )}
              </>
            )}
          </>
        ) : null}
      </Card>
      {actions.dialog}
    </div>
  );
}

function CatChip({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={cn(
        "inline-flex h-7 items-center rounded-full border px-2.5 text-xs font-medium transition-colors @max-3xl:h-8",
        active ? "border-accent/40 bg-accent-soft text-accent" : "border-border bg-surface-2 text-muted hover:border-border-strong hover:text-fg",
      )}
    >
      {children}
    </button>
  );
}

function Intro() {
  return (
    <EmptyState className="flex-1" icon={<SearchIcon />} title="Search your indexers">
      <ul className="mt-1 space-y-1 text-left">
        <li>Press Enter to search every enabled indexer at once; results are merged and sorted by seeders.</li>
        <li>Be specific — add a version, year or resolution, e.g. “ubuntu 24.04 desktop”.</li>
        <li>Use categories or pick indexers to narrow down slow or noisy searches.</li>
      </ul>
    </EmptyState>
  );
}

function IndexerErrors({ errors }: { errors: SearchResponse["errors"] }) {
  return (
    <details className="group border-b border-border bg-warn-soft/40 px-3.5 py-2 text-xs">
      <summary className="flex cursor-pointer list-none items-center gap-2 text-warn">
        <AlertTriangle className="size-3.5 shrink-0" />
        <span>
          {errors.length} indexer{errors.length === 1 ? "" : "s"} unavailable: {errors.map((e) => e.indexer).join(", ")}
        </span>
        <span className="ml-auto text-muted group-open:hidden">Details</span>
      </summary>
      <ul className="mt-1.5 space-y-0.5 pl-5 text-muted">
        {errors.map((e) => (
          <li key={e.indexerId}>
            <span className="text-fg">{e.indexer}</span> — {e.message}
          </li>
        ))}
      </ul>
    </details>
  );
}

interface RowProps {
  r: SearchResult;
  busy?: RowAction;
  magnetOnly?: boolean;
  actions: ReturnType<typeof useResultActions>;
  queueReason?: string;
}

function torrentReason(r: SearchResult, magnetOnly?: boolean) {
  if (!r.hasTorrent || magnetOnly) return "This result only provides a magnet link";
  return undefined;
}

function Title({ r }: { r: SearchResult }) {
  // Indexer-supplied: only ever link to http(s) pages.
  const info = safeHttpUrl(r.infoUrl);
  const cats = r.categories.filter((c) => !c.name.includes("/")).slice(0, 2);
  const sub = r.categories.find((c) => c.name.includes("/"));
  return (
    <div className="min-w-0">
      <div className="break-words font-medium leading-snug [overflow-wrap:anywhere]">
        {r.title}
        {info && (
          <a
            href={info}
            target="_blank"
            rel="noopener noreferrer"
            className="-my-2 ml-0.5 inline-flex size-6 items-center justify-center rounded align-middle text-subtle hover:text-frost @max-3xl:size-8"
            aria-label={`Open on ${r.indexer}`}
            title={`Open on ${r.indexer}`}
          >
            <ExternalLink className="size-3.5" />
          </a>
        )}
      </div>
      <div className="mt-1 flex flex-wrap items-center gap-1.5 text-xs">
        <span className="text-muted">{r.indexer}</span>
        {(sub ? [sub] : cats).map((c) => (
          <span key={c.id} className="rounded border border-border bg-surface-2 px-1.5 py-px text-2xs text-muted">
            {c.name}
          </span>
        ))}
      </div>
    </div>
  );
}

function Peers({ r }: { r: SearchResult }) {
  return (
    <span className="num whitespace-nowrap">
      <span className={seedTone(r.seeders)} aria-label={`${r.seeders ?? "unknown"} seeders`}>
        {r.seeders ?? "—"}
      </span>
      <span className="text-subtle"> / </span>
      <span className="text-muted" aria-label={`${r.leechers ?? "unknown"} leechers`}>
        {r.leechers ?? "—"}
      </span>
    </span>
  );
}

function ResultRow({ r, busy, magnetOnly, actions, queueReason }: RowProps) {
  const tReason = torrentReason(r, magnetOnly);
  return (
    <Tr className="align-top">
      <Td className="py-2">
        <Title r={r} />
      </Td>
      <Td className="num py-2 text-right text-muted">{bytes(r.size)}</Td>
      <Td className="py-2 text-right">
        <Peers r={r} />
      </Td>
      <Td className="num py-2 text-right text-xs text-muted @max-5xl:hidden" title={r.age ? new Date(r.age).toLocaleString() : undefined}>
        {ago(r.age)}
      </Td>
      <Td className="py-1.5 text-right">
        <div className="flex justify-end gap-0.5">
          <IconButton label="Copy magnet link" size="sm" disabled={!!busy || !r.hasMagnet} onClick={() => actions.copyMagnet(r.id, r.title)}>
            {busy === "magnet" ? <Spinner /> : <Magnet />}
          </IconButton>
          <WithReason reason={tReason}>
            <IconButton label="Download .torrent" size="sm" disabled={!!busy || !!tReason} onClick={() => actions.downloadTorrent(r.id, r.title)}>
              {busy === "torrent" ? <Spinner /> : <Download />}
            </IconButton>
          </WithReason>
          <WithReason reason={queueReason}>
            <IconButton label="Queue in Transmission" size="sm" disabled={!!busy || !!queueReason} onClick={() => actions.queue(r.id, r.title)}>
              {busy === "queue" ? <Spinner /> : <SendToBack />}
            </IconButton>
          </WithReason>
        </div>
      </Td>
    </Tr>
  );
}

function ResultCard({ r, busy, magnetOnly, actions, queueReason }: RowProps) {
  const tReason = torrentReason(r, magnetOnly);
  return (
    <li className="border-b border-border px-3.5 py-3">
      <Title r={r} />
      <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
        <span className="num text-muted">{bytes(r.size)}</span>
        <Peers r={r} />
        <span className="num text-muted">{ago(r.age)}</span>
      </div>
      <div className="mt-2.5 grid grid-cols-3 gap-1.5">
        <Button
          size="sm"
          className="h-8"
          disabled={!!busy || !r.hasMagnet}
          loading={busy === "magnet"}
          icon={<Magnet />}
          onClick={() => actions.copyMagnet(r.id, r.title)}
        >
          Magnet
        </Button>
        <WithReason reason={tReason} className="w-full">
          <Button
            size="sm"
            className="h-8 w-full"
            disabled={!!busy || !!tReason}
            loading={busy === "torrent"}
            icon={<Download />}
            onClick={() => actions.downloadTorrent(r.id, r.title)}
          >
            .torrent
          </Button>
        </WithReason>
        <WithReason reason={queueReason} className="w-full">
          <Button
            size="sm"
            className="h-8 w-full"
            disabled={!!busy || !!queueReason}
            loading={busy === "queue"}
            icon={<SendToBack />}
            onClick={() => actions.queue(r.id, r.title)}
          >
            Queue
          </Button>
        </WithReason>
      </div>
    </li>
  );
}
