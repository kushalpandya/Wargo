// SPDX-License-Identifier: AGPL-3.0-only
import { readPref, setPref } from "@/lib/prefs";
import { Fragment, useMemo, useState } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router";
import {
  ArrowUpCircle,
  Boxes,
  FileText,
  SquareTerminal,
  Info,
  Layers,
  MoreHorizontal,
  Network as NetworkIcon,
  Play,
  RefreshCw,
  RotateCw,
  Square,
} from "lucide-react";
import { useTopicValue } from "@/lib/ws";
import { ago, bytes, pct } from "@/lib/format";
import { cn } from "@/lib/cn";
import { useCapabilities } from "@/app/session";
import { Card } from "@/components/ui/card";
import { Button, IconButton } from "@/components/ui/button";
import { SearchInput, Segmented } from "@/components/ui/form";
import { Menu, MenuItem, MenuSeparator, Tooltip } from "@/components/ui/menu";
import {
  Badge,
  EmptyState,
  ErrorNote,
  Meter,
  PageHeader,
  Skeleton,
  StatusDot,
} from "@/components/ui/misc";
import { Table, Td, Th, THead, Tr } from "@/components/ui/table";
import { toast } from "@/components/ui/toast";
import { containerTone } from "@/features/containers/util";
import { ContainerSheet, type SheetTab } from "./ContainerSheet";
import { ContainerIcon } from "./ContainerIcon";
import { useCheckContainerUpdates, useContainers, useContainerUpdates } from "./api";
import { useContainerActions, type Action } from "./actions";
import {
  displayPorts,
  PortLinks,
  shortImage,
  splitImage,
  stateLabel,
  statusText,
  HealthBadge,
} from "./util";
import type { Container, ContainerStats } from "./types";

type Filter = "all" | "running" | "stopped" | "attention";

function needsAttention(c: Container) {
  return (
    c.health === "unhealthy" ||
    c.state === "restarting" ||
    c.state === "dead" ||
    !!c.updateAvailable
  );
}

function Summary({ list, checkedAt }: { list?: Container[]; checkedAt?: number | null }) {
  const checked = !!checkedAt;
  const s = useMemo(() => {
    if (!list) return null;
    return {
      running: list.filter((c) => c.state === "running").length,
      stopped: list.filter((c) => c.state !== "running").length,
      unhealthy: list.filter(
        (c) => c.health === "unhealthy" || c.state === "restarting",
      ).length,
      updates: list.filter((c) => c.updateAvailable).length,
      total: list.length,
    };
  }, [list]);
  const items = s
    ? [
        {
          label: "Running",
          value: s.running,
          sub: `of ${s.total} containers`,
          tone: "ok" as const,
        },
        {
          label: "Stopped",
          value: s.stopped,
          sub: s.stopped ? "exited or created" : "none",
          tone: undefined,
        },
        {
          label: "Unhealthy",
          value: s.unhealthy,
          sub: s.unhealthy ? "needs attention" : "all healthy",
          tone: s.unhealthy ? ("crit" as const) : undefined,
        },
        {
          label: "Updates",
          value: checked ? s.updates : "—",
          sub: checked
            ? s.updates
              ? `newer images · checked ${ago(checkedAt!)}`
              : `all up to date · checked ${ago(checkedAt!)}`
            : "not checked yet",
          tone: s.updates ? ("accent" as const) : undefined,
        },
      ]
    : [];
  return (
    <Card className="mb-4 grid grid-cols-2 sm:grid-cols-4 sm:divide-x sm:divide-border">
      {s
        ? items.map((it, i) => (
            <div
              key={it.label}
              className={cn(
                "min-w-0 px-4 py-3",
                i % 2 === 1 && "max-sm:border-l max-sm:border-border",
                i > 1 && "max-sm:border-t max-sm:border-border",
              )}
            >
              <div className="flex items-center gap-1.5 text-2xs font-medium uppercase tracking-wider text-subtle">
                {it.tone === "ok" && <StatusDot tone="ok" />}
                {it.label}
              </div>
              <div
                className={cn(
                  "num mt-0.5 text-xl font-medium leading-tight",
                  it.tone === "crit" && "text-crit",
                  it.tone === "accent" && "text-accent",
                )}
              >
                {it.value}
              </div>
              <div className="mt-0.5 truncate text-xs text-muted">{it.sub}</div>
            </div>
          ))
        : Array.from({ length: 4 }, (_, i) => (
            <div key={i} className="px-4 py-3">
              <Skeleton className="h-3 w-16" />
              <Skeleton className="mt-2 h-6 w-10" />
            </div>
          ))}
    </Card>
  );
}

function Badges({ c }: { c: Container }) {
  return (
    <>
      <HealthBadge c={c} />
      {c.updateAvailable && (
        <Badge tone="accent">
          <ArrowUpCircle className="size-3" aria-hidden="true" /> update
        </Badge>
      )}
      {c.networkContainer && (
        <Tooltip content={`Uses the network of ${c.networkContainer}`}>
          <span className="inline-flex h-5 items-center gap-1 rounded border border-frost/25 bg-frost-soft px-1.5 text-2xs font-medium text-frost">
            <NetworkIcon className="size-3" aria-hidden="true" />
            via {c.networkContainer}
          </span>
        </Tooltip>
      )}
      {c.casaos && <Badge tone="neutral">CasaOS</Badge>}
    </>
  );
}

function Usage({ s, running }: { s?: ContainerStats; running: boolean }) {
  if (!running) return <span className="text-xs text-subtle">—</span>;
  if (!s) return <Skeleton className="h-3 w-16" />;
  const memPct = s.memLimit ? (s.mem / s.memLimit) * 100 : 0;
  return (
    <div className="grid grid-cols-[2.25rem_1fr_3.75rem] items-center gap-x-2 gap-y-1 text-xs">
      <span className="text-2xs uppercase tracking-wider text-subtle">CPU</span>
      <Meter
        value={Math.max(s.cpu, s.cpu > 0 ? 2 : 0)}
        tone={s.cpu >= 50 ? "warn" : "accent"}
        label={`CPU ${pct(s.cpu, 1)}`}
      />
      <span className="num text-right text-muted">{pct(s.cpu, 1)}</span>
      <span className="text-2xs uppercase tracking-wider text-subtle">Mem</span>
      <Meter
        value={Math.max(memPct, 2)}
        tone={memPct >= 80 ? "warn" : "frost"}
        label={`Memory ${bytes(s.mem)}`}
      />
      <span className="num text-right text-muted">{bytes(s.mem, 0)}</span>
    </div>
  );
}

function ActionMenu({
  c,
  onAction,
  onOpen,
  busy,
}: {
  c: Container;
  onAction: (a: Action, c: Container) => void;
  onOpen: (tab: SheetTab) => void;
  busy?: boolean;
}) {
  const running = c.state === "running" || c.state === "restarting";
  const nav = useNavigate();
  return (
    <Menu
      trigger={
        <IconButton
          label={`Actions for ${c.name}`}
          size="sm"
          onClick={(e) => e.stopPropagation()}
          disabled={busy}
        >
          <MoreHorizontal />
        </IconButton>
      }
    >
      <MenuItem icon={<Info />} onSelect={() => onOpen("overview")}>
        Details
      </MenuItem>
      <MenuItem icon={<FileText />} onSelect={() => onOpen("logs")}>
        Logs
      </MenuItem>
      {c.state === "running" && (
        <MenuItem icon={<SquareTerminal />} onSelect={() => nav(`/terminal?container=${encodeURIComponent(c.name)}`)}>
          Open shell
        </MenuItem>
      )}
      <MenuSeparator />
      {running ? (
        <>
          <MenuItem icon={<RotateCw />} onSelect={() => onAction("restart", c)}>
            Restart
          </MenuItem>
          <MenuItem
            icon={<Square />}
            danger
            onSelect={() => onAction("stop", c)}
          >
            Stop
          </MenuItem>
        </>
      ) : (
        <MenuItem icon={<Play />} onSelect={() => onAction("start", c)}>
          Start
        </MenuItem>
      )}
      <MenuSeparator />
      <MenuItem icon={<ArrowUpCircle />} onSelect={() => onAction("update", c)}>
        {c.updateAvailable ? "Update now" : "Pull & update"}
      </MenuItem>
    </Menu>
  );
}

function ImageCell({ c }: { c: Container }) {
  const [, tag] = splitImage(c.image);
  const showVersion = !!c.version && c.version !== tag;
  return (
    <span
      className="num truncate text-xs text-muted"
      title={`${c.image}${showVersion ? ` · version ${c.version}` : ""}`}
    >
      {shortImage(c.image)}
      <span className="text-subtle">:</span>
      <span className={cn(tag === "latest" ? "text-subtle" : "text-fg")}>
        {tag}
      </span>
      {showVersion && (
        <>
          <span className="text-subtle"> · </span>
          <span className="text-fg">{c.version}</span>
        </>
      )}
    </span>
  );
}

export default function Page() {
  const nav = useNavigate();
  const { id } = useParams();
  const [params, setParams] = useSearchParams();
  const caps = useCapabilities().data?.docker;
  const [q, setQ] = useState("");
  const [filter, setFilter] = useState<Filter>("all");
  const [group, setGroup] = useState(
    () => readPref("wargo.containers.group") === "1",
  );
  const list = useContainers();
  const stats =
    useTopicValue<Record<string, ContainerStats>>("containers.stats");
  const updates = useContainerUpdates();
  const check = useCheckContainerUpdates({
    onSuccess: (st) => {
      const res = Object.values(st.results);
      const n = res.filter((u) => u.updateAvailable).length;
      const failed = res.filter(
        (u) => u.error && u.updateAvailable === null,
      ).length;
      toast.ok(
        n
          ? `${n} update${n === 1 ? "" : "s"} available`
          : "All images are up to date",
        failed ? `${failed} image(s) could not be checked` : undefined,
      );
    },
    onError: (e) => toast.error("Update check failed", e),
  });
  const checking = check.isPending || !!updates.data?.checking;
  const actions = useContainerActions();

  const data = list.data;
  const ql = q.trim().toLowerCase();
  const rows = useMemo(() => {
    let l = data ?? [];
    if (filter === "running") l = l.filter((c) => c.state === "running");
    if (filter === "stopped") l = l.filter((c) => c.state !== "running");
    if (filter === "attention") l = l.filter(needsAttention);
    if (ql)
      l = l.filter(
        (c) =>
          c.name.toLowerCase().includes(ql) ||
          c.image.toLowerCase().includes(ql) ||
          c.compose?.project.toLowerCase().includes(ql) ||
          c.id.startsWith(ql),
      );
    return [...l].sort(
      (a, b) =>
        Number(b.state === "running") - Number(a.state === "running") ||
        a.name.localeCompare(b.name),
    );
  }, [data, filter, ql]);
  const groups = useMemo(() => {
    if (!group) return [{ key: "", rows }];
    const m = new Map<string, Container[]>();
    for (const c of rows) {
      const k = c.compose?.project ?? "";
      const g = m.get(k);
      if (g) g.push(c);
      else m.set(k, [c]);
    }
    return [...m.entries()]
      .sort(([a], [b]) => (a === "" ? 1 : b === "" ? -1 : a.localeCompare(b)))
      .map(([key, rows]) => ({ key, rows }));
  }, [rows, group]);

  const selected = id
    ? data?.find(
        (c) =>
          c.id === id ||
          c.fullId === id ||
          c.name === id ||
          c.fullId.startsWith(id),
      )
    : undefined;
  const tab = (params.get("tab") as SheetTab) || "overview";
  const open = (c: Container, t: SheetTab = "overview") =>
    nav(`/containers/${c.id}${t !== "overview" ? `?tab=${t}` : ""}`);
  const setGroupPersist = (v: boolean) => {
    setGroup(v);
    setPref("wargo.containers.group", v ? "1" : "0");
  };

  if (caps && !caps.available) {
    return (
      <div>
        <PageHeader title="Containers" />
        <Card>
          <EmptyState icon={<Boxes />} title="Docker is not available">
            {caps.error ?? "Wargo could not reach the Docker daemon."}
          </EmptyState>
        </Card>
      </div>
    );
  }

  return (
    <div>
      <PageHeader
        title="Containers"
        description={
          caps?.version
            ? `Docker ${caps.version}${caps.casaos ? " · CasaOS detected" : ""}`
            : "Docker containers on this host"
        }
        actions={
          <>
            <Button
              size="sm"
              icon={
                <RefreshCw className={cn(checking && "animate-spin")} />
              }
              onClick={() => check.mutate()}
              loading={false}
              disabled={checking}
            >
              {checking ? "Checking…" : "Check for updates"}
            </Button>
          </>
        }
      />
      <Summary list={data} checkedAt={updates.data?.checkedAt} />
      <Card>
        <div className="flex flex-wrap items-center gap-2 border-b border-border px-3 py-2.5">
          <SearchInput
            value={q}
            onChange={setQ}
            placeholder="Filter by name, image or project…"
            className="min-w-0 flex-1 basis-56"
            label="Filter containers"
          />
          <Segmented<Filter>
            label="Show"
            value={filter}
            onChange={setFilter}
            options={[
              { value: "all", label: "All" },
              { value: "running", label: "Running" },
              { value: "stopped", label: "Stopped" },
              {
                value: "attention",
                label: "Attention",
                title: "Unhealthy, restarting or with updates",
              },
            ]}
          />
          <Button size="sm" variant={group ? "primary" : "secondary"} aria-pressed={group} icon={<Layers />} onClick={() => setGroupPersist(!group)} className="max-md:h-8">
            Group by project
          </Button>
        </div>
        {list.error ? (
          <div className="p-4">
            <ErrorNote error={list.error} />
          </div>
        ) : !data ? (
          <div className="space-y-2 p-4">
            {Array.from({ length: 6 }, (_, i) => (
              <Skeleton key={i} className="h-10" />
            ))}
          </div>
        ) : rows.length === 0 ? (
          <EmptyState
            icon={<Boxes />}
            title={data.length ? "No matching containers" : "No containers yet"}
          >
            {data.length
              ? "Try a different filter."
              : "Containers you create with Docker or CasaOS show up here."}
          </EmptyState>
        ) : (
          <>
            <div className="max-md:hidden">
              <Table className="table-fixed">
                <THead>
                  <tr>
                    <Th className="w-[30%] lg:w-[26%]">Container</Th>
                    <Th className="w-[18%] max-lg:hidden">Image</Th>
                    <Th className="w-[16%]">Status</Th>
                    <Th>Ports</Th>
                    <Th className="w-44 max-lg:w-36">Usage</Th>
                    <Th className="w-12">
                      <span className="sr-only">Actions</span>
                    </Th>
                  </tr>
                </THead>
                <tbody>
                  {groups.map((g) => (
                    <Fragment key={g.key || "_"}>
                      {group && (
                        <tr className="bg-surface-2/60">
                          <td
                            colSpan={6}
                            className="border-b border-border px-3 py-1.5 text-2xs font-semibold uppercase tracking-wider text-subtle"
                          >
                            {g.key || "Standalone"}{" "}
                            <span className="num ml-1 font-normal">
                              {g.rows.length}
                            </span>
                          </td>
                        </tr>
                      )}
                      {g.rows.map((c) => {
                        const running = c.state === "running";
                        return (
                          <Tr
                            key={c.id}
                            onClick={() => open(c)}
                            selected={selected?.id === c.id}
                            tabIndex={0}
                            onKeyDown={(e) => {
                              if (
                                e.key === "Enter" &&
                                e.target === e.currentTarget
                              )
                                open(c);
                            }}
                            className="focus-visible:bg-surface-2 focus-visible:outline-none aria-selected:[&_.text-subtle]:text-muted"
                          >
                            <Td className="py-2">
                              <div className="flex min-w-0 items-center gap-2.5">
                                <ContainerIcon
                                  c={c}
                                  status={containerTone(c)}
                                  statusLabel={stateLabel(c)}
                                />
                                <div className="min-w-0">
                                  <div className="truncate font-medium">
                                    {c.name}
                                  </div>
                                  <div className="mt-0.5 flex flex-wrap items-center gap-1 empty:hidden">
                                    <Badges c={c} />
                                  </div>
                                  <div className="flex min-w-0 lg:hidden">
                                    <ImageCell c={c} />
                                  </div>
                                </div>
                              </div>
                            </Td>
                            <Td className="max-lg:hidden">
                              <div className="flex min-w-0">
                                <ImageCell c={c} />
                              </div>
                            </Td>
                            <Td>
                              <div
                                className={cn(
                                  "truncate text-xs",
                                  running ? "text-fg" : "text-muted",
                                )}
                                title={c.status}
                              >
                                {statusText(c)}
                              </div>
                              {c.compose && !group && (
                                <div className="truncate text-2xs text-subtle">
                                  {c.compose.project}
                                </div>
                              )}
                            </Td>
                            <Td className="py-2">
                              <PortLinks c={c} max={3} />
                            </Td>
                            <Td className="py-2">
                              <Usage s={stats?.[c.id]} running={running} />
                            </Td>
                            <Td
                              className="px-1.5 text-right"
                              onClick={(e) => e.stopPropagation()}
                            >
                              <ActionMenu
                                c={c}
                                onAction={actions.request}
                                onOpen={(t) => open(c, t)}
                                busy={actions.busy === c.id}
                              />
                            </Td>
                          </Tr>
                        );
                      })}
                    </Fragment>
                  ))}
                </tbody>
              </Table>
            </div>
            <div className="md:hidden">
              {groups.map((g) => (
                <section
                  key={g.key || "_"}
                  aria-label={group ? g.key || "Standalone" : undefined}
                >
                  {group && (
                    <h2 className="border-b border-border bg-surface-2/60 px-4 py-1.5 text-2xs font-semibold uppercase tracking-wider text-subtle">
                      {g.key || "Standalone"}{" "}
                      <span className="num ml-1 font-normal">
                        {g.rows.length}
                      </span>
                    </h2>
                  )}
                  <ul>
                    {g.rows.map((c) => {
                      const s = stats?.[c.id];
                      return (
                        <li
                          key={c.id}
                          className="border-b border-border px-4 py-2.5 last:border-b-0"
                        >
                          <div className="flex items-start gap-2">
                            <button
                              type="button"
                              onClick={() => open(c)}
                              className="flex min-w-0 flex-1 items-start gap-2.5 text-left"
                            >
                              <ContainerIcon
                                c={c}
                                status={containerTone(c)}
                                statusLabel={stateLabel(c)}
                                className="mt-0.5"
                              />
                              <div className="min-w-0 flex-1">
                                <div className="flex items-baseline gap-2">
                                  <span className="truncate font-medium">
                                    {c.name}
                                  </span>
                                  {c.state === "running" && s && (
                                    <span className="num ml-auto shrink-0 text-xs text-muted">
                                      {pct(s.cpu, 1)} · {bytes(s.mem, 0)}
                                    </span>
                                  )}
                                </div>
                                <div className="flex min-w-0">
                                  <ImageCell c={c} />
                                </div>
                                <div className="mt-0.5 truncate text-xs text-muted">
                                  {statusText(c)}
                                </div>
                                <div className="mt-1 flex flex-wrap items-center gap-1 empty:hidden">
                                  <Badges c={c} />
                                </div>
                              </div>
                            </button>
                            <div className="flex shrink-0 flex-col items-end gap-1.5">
                              <ActionMenu
                                c={c}
                                onAction={actions.request}
                                onOpen={(t) => open(c, t)}
                                busy={actions.busy === c.id}
                              />
                            </div>
                          </div>
                          {displayPorts(c).ports.length > 0 && (
                            <PortLinks
                              c={c}
                              max={4}
                              className="mt-1.5 pl-[2.625rem]"
                            />
                          )}
                        </li>
                      );
                    })}
                  </ul>
                </section>
              ))}
            </div>
          </>
        )}
      </Card>
      <ContainerSheet
        id={id ?? null}
        summary={selected}
        tab={tab}
        onTab={(t) =>
          setParams(t === "overview" ? {} : { tab: t }, { replace: true })
        }
        onClose={() => nav("/containers")}
        stats={selected ? stats?.[selected.id] : undefined}
        onAction={actions.request}
        busy={!!selected && actions.busy === selected.id}
      />
      {actions.element}
    </div>
  );
}
