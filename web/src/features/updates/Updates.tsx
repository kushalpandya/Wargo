// SPDX-License-Identifier: AGPL-3.0-only
import { useState } from "react";
import { useSearchParams } from "react-router";
import { Tabs } from "radix-ui";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Boxes, CircleCheck, Download, HardDrive, Lock, PackageCheck, PackageSearch, RefreshCw, RotateCw, ShieldCheck, TriangleAlert } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { ago, bytes, datetime } from "@/lib/format";
import { cn } from "@/lib/cn";
import { useCapabilities } from "@/app/session";
import { Card, CardBody, CardHeader, Stat } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { SearchInput } from "@/components/ui/form";
import { Badge, EmptyState, ErrorNote, Notice, Skeleton, Spinner } from "@/components/ui/misc";
import { toast } from "@/components/ui/toast";
import { statusKey, usePkgStatus, pendingCount, type CheckResult, type JobSnapshot, type PkgStatus, type Precheck } from "./api";
import { useContainers, useContainerUpdates } from "@/features/containers/api";
import { ContainerUpdates } from "./ContainerUpdates";
import { GroupedUpdates, PlainList, matches } from "./UpdateList";
import { RebootButton, RunPanel } from "./RunPanel";

const DISMISS_KEY = "wargo.pkg.dismissedRun";

const isArch = (b?: string) => b === "pacman" || b === "pamac";

function ignoreCanceled(title: string) {
  return (e: unknown) => {
    if (e instanceof ApiError && e.code === "elevation_canceled") return;
    toast.error(title, e);
  };
}

function UpgradeDialog({
  open,
  onOpenChange,
  status,
  onConfirm,
  loading,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  status: PkgStatus;
  onConfirm: () => void;
  loading: boolean;
}) {
  const pre = useQuery({
    queryKey: ["pkg", "precheck"],
    queryFn: () => api.get<Precheck>("/api/pkg/precheck"),
    enabled: open,
    staleTime: 0,
    gcTime: 0,
  });
  const c = status.lastCheck;
  const n = c?.updates.length ?? 0;
  const lowDisk = pre.data?.disk.filter((d) => d.low) ?? [];
  const blocked = pre.data?.blocked;
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={`Install ${n} update${n === 1 ? "" : "s"}?`}
      description={`${status.distro} · ${status.backend}${c?.downloadSize ? ` · ${bytes(c.downloadSize)} to download` : ""}`}
      footer={
        <>
          <Button autoFocus onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button variant="primary" icon={<Download />} loading={loading} disabled={pre.isLoading || !!blocked} onClick={onConfirm}>
            Update all
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3 text-sm">
        {status.upgradeCommand && (
          <div>
            <div className="mb-1 text-2xs font-semibold uppercase tracking-wider text-subtle">Runs as root</div>
            <code className="num block break-all rounded-md border border-border bg-surface-2 px-2.5 py-1.5 text-xs">{status.upgradeCommand}</code>
          </div>
        )}
        <ul className="flex flex-col gap-2 text-muted">
          <li className="flex gap-2">
            <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warn" />
            <span>
              <span className="text-fg">Don't reboot or power off</span> the server until the upgrade finishes. It runs as a system
              service, so it keeps going if you close this page.
            </span>
          </li>
          {isArch(status.backend) && (
            <li className="flex gap-2">
              <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warn" />
              <span>
                Partial upgrades are unsupported on Arch-based systems, so <span className="text-fg">every repository package</span> is
                upgraded together. The package databases are synced first, so the final list can differ slightly from this one.
              </span>
            </li>
          )}
          {status.backend === "apt" && (
            <li className="flex gap-2">
              <ShieldCheck className="mt-0.5 size-4 shrink-0 text-frost" />
              <span>Your modified config files are kept; the maintainer's versions are saved next to them for review.</span>
            </li>
          )}
          {(c?.extras.length ?? 0) > 0 && (
            <li className="flex gap-2">
              <ShieldCheck className="mt-0.5 size-4 shrink-0 text-frost" />
              <span>AUR and Flatpak packages are not touched.</span>
            </li>
          )}
        </ul>
        {pre.isLoading && (
          <div className="flex items-center gap-2 text-muted">
            <Spinner /> Checking locks and free space…
          </div>
        )}
        {pre.error && <ErrorNote error={pre.error} />}
        {pre.data && (
          <div className="flex flex-col gap-2">
            {pre.data.lock.locked && (
              <Notice tone="crit" icon={<Lock />}>
                The package manager is busy: {pre.data.lock.reason}
                {pre.data.lock.path && <span className="num block text-xs text-muted">{pre.data.lock.path}</span>}
              </Notice>
            )}
            {lowDisk.length > 0 && (
              <Notice tone={blocked ? "crit" : "warn"} icon={<HardDrive />}>
                Low free space: {lowDisk.map((d) => `${d.path} ${bytes(d.free)}`).join(", ")}. At least 2 GB is recommended.
              </Notice>
            )}
            {!blocked && lowDisk.length === 0 && (
              <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted">
                <span className="inline-flex items-center gap-1.5">
                  <CircleCheck className="size-3.5 text-ok" /> No package manager running
                </span>
                {pre.data.disk.map((d) => (
                  <span key={d.path} className="inline-flex items-center gap-1.5">
                    <CircleCheck className="size-3.5 text-ok" />
                    <span className="num">
                      {d.path} {bytes(d.free)} free
                    </span>
                  </span>
                ))}
              </div>
            )}
          </div>
        )}
        {status.fake && (
          <Notice tone="warn">Development mode: this runs a simulated upgrade script. Nothing is installed.</Notice>
        )}
      </div>
    </Dialog>
  );
}

function Summary({ status, checking }: { status: PkgStatus; checking: boolean }) {
  const c = status.lastCheck;
  const aur = c?.extras.filter((u) => u.source === "aur").length ?? 0;
  const flat = c?.extras.filter((u) => u.source === "flatpak").length ?? 0;
  const n = c?.updates.length ?? 0;
  return (
    <Card className="mb-4">
      <div className="grid grid-cols-2 gap-x-4 gap-y-3 p-3.5 sm:grid-cols-4">
        <Stat
          label="Pending"
          value={c && !c.error ? n : "—"}
          unit={c && !c.error ? (n === 1 ? "package" : "packages") : undefined}
          sub={c?.stale ? "outdated — check again" : c ? (n ? "from repositories" : "up to date") : "not checked yet"}
        />
        <Stat
          label="Download"
          value={c?.downloadSize ? bytes(c.downloadSize).split(" ")[0] : "—"}
          unit={c?.downloadSize ? bytes(c.downloadSize).split(" ")[1] : undefined}
          sub={c && n > 0 ? (c.downloadSize ? "approximate, cached files excluded" : "size unknown") : " "}
        />
        <Stat
          label={status.extras.includes("aur") ? "AUR · Flatpak" : "Flatpak"}
          value={c ? (status.extras.includes("aur") ? `${aur} · ${flat}` : flat) : "—"}
          sub="read-only, not installed here"
        />
        <div className="min-w-0">
          <div className="text-2xs font-medium uppercase tracking-wider text-subtle">Last checked</div>
          <div className="mt-0.5 flex items-baseline gap-1">
            {checking ? (
              <span className="inline-flex items-center gap-1.5 text-sm text-accent">
                <Spinner /> Checking…
              </span>
            ) : (
              <span className="num text-xl font-medium leading-tight" title={c ? datetime(c.at) : undefined}>
                {c ? ago(c.at) : "never"}
              </span>
            )}
          </div>
          <div className="mt-0.5 truncate text-xs text-muted">{c ? `took ${(c.durationMs / 1000).toFixed(1)} s` : "checks run only on request"}</div>
        </div>
      </div>
      <div className="flex items-start gap-2 border-t border-border px-3.5 py-2 text-xs text-muted">
        <ShieldCheck className="mt-px size-3.5 shrink-0 text-frost" />
        <span>Wargo never checks or installs updates on its own. Packages are installed only when you click Update all.</span>
      </div>
    </Card>
  );
}

function useDismissed(): [string | null, (id: string) => void] {
  const [v, setV] = useState<string | null>(() => localStorage.getItem(DISMISS_KEY));
  return [
    v,
    (id) => {
      localStorage.setItem(DISMISS_KEY, id);
      setV(id);
    },
  ];
}

function SystemUpdates() {
  const { data: st, isLoading, error } = usePkgStatus();
  const qc = useQueryClient();
  const [q, setQ] = useState("");
  const [confirm, setConfirm] = useState(false);
  const [dismissed, dismiss] = useDismissed();

  const check = useMutation({
    mutationFn: () => api.post<CheckResult>("/api/pkg/check"),
    onSuccess: (r) => {
      qc.setQueryData<PkgStatus>(statusKey, (s) => (s ? { ...s, lastCheck: r, checking: false } : s));
      toast.ok(r.updates.length ? `${r.updates.length} updates available` : "System is up to date");
    },
    onError: ignoreCanceled("Update check failed"),
    onSettled: () => qc.invalidateQueries({ queryKey: statusKey }),
  });

  const upgrade = useMutation({
    mutationFn: () => api.post<JobSnapshot>("/api/pkg/upgrade"),
    onSuccess: (job) => {
      setConfirm(false);
      qc.setQueryData<PkgStatus>(statusKey, (s) => (s ? { ...s, running: job } : s));
    },
    onError: ignoreCanceled("Could not start the upgrade"),
  });

  if (isLoading) {
    return (
      <div>
        <Skeleton className="mb-4 h-8 w-48" />
        <Skeleton className="mb-4 h-28 w-full" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }
  if (error || !st) return <ErrorNote error={error ?? "Unavailable"} />;

  const c = st.lastCheck;
  const checking = check.isPending || st.checking;
  const running = !!st.running;
  const n = c?.updates.length ?? 0;
  // A finished run stays visible until dismissed or superseded by a newer check.
  const runSuperseded =
    !!st.lastRun?.finishedAt && !!c && !c.stale && new Date(c.at).getTime() > new Date(st.lastRun.finishedAt).getTime();
  const showRun = running || (!!st.lastRun && st.lastRun.id !== dismissed && !runSuperseded);
  const canUpgrade = st.available && !!c && !c.error && !c.stale && n > 0 && !running && !checking;
  const aur = c?.extras.filter((u) => u.source === "aur") ?? [];
  const flat = c?.extras.filter((u) => u.source === "flatpak") ?? [];
  const shownCount = c ? c.updates.filter((u) => matches(u, q)).length : 0;

  const checkBtn = (
    <Button
      variant={c && !c.stale ? "secondary" : "primary"}
      icon={<RefreshCw className={cn(checking && "animate-spin")} />}
      disabled={checking || running || !st.available}
      onClick={() => check.mutate()}
    >
      {checking ? "Checking…" : "Check for updates"}
    </Button>
  );

  return (
    <div>
      <SystemToolbar
        status={st}
        actions={
          <>
            {checkBtn}
            {(c?.updates.length ?? 0) > 0 && (
              <Button variant="primary" icon={<Download />} disabled={!canUpgrade} onClick={() => setConfirm(true)}>
                Update all
              </Button>
            )}
          </>
        }
      />

      {!st.available && (
        <Notice tone="crit" className="mb-4">
          {st.backend ? `${st.backend} is not installed on this host.` : "No supported package manager was found on this host."}
        </Notice>
      )}

      {st.reboot.required && !(showRun && st.lastRun?.rebootRequired && !running) && !running && (
        <div className="mb-4 flex flex-wrap items-center gap-3 rounded-lg border border-warn/30 bg-warn-soft px-3.5 py-2.5">
          <RotateCw className="size-4 shrink-0 text-warn" />
          <div className="min-w-0 flex-1 text-sm">
            <span className="font-medium">Reboot recommended.</span> <span className="text-muted">{st.reboot.reason}</span>
          </div>
          <RebootButton />
        </div>
      )}

      {showRun && <RunPanel status={st} onDismiss={st.lastRun ? () => dismiss(st.lastRun!.id) : undefined} />}

      <Summary status={st} checking={checking} />

      {c?.error && <ErrorNote className="mb-4" error={`Last check failed: ${c.error}`} />}
      {c?.stale && !running && (
        <Notice tone="frost" className="mb-4">
          This list is from before the last upgrade. Check again to see what is still pending.
        </Notice>
      )}

      {!c ? (
        <Card>
          <EmptyState
            icon={<PackageSearch />}
            title="No update check yet"
            action={checkBtn}
          >
            Wargo doesn't look for updates in the background. Check now to ask <span className="num text-fg">{st.backend || "the package manager"}</span>{" "}
            for pending updates; nothing is installed.
            {st.needsRootToCheck && " Refreshing the package databases requires administrator approval."}
          </EmptyState>
        </Card>
      ) : !c.error && n === 0 ? (
        <Card>
          <EmptyState icon={<CircleCheck className="text-ok" />} title="Your system is up to date">
            No repository updates were found {ago(c.at)}.
          </EmptyState>
        </Card>
      ) : n > 0 ? (
        <Card>
          <CardHeader
            title="Repository updates"
            icon={<PackageCheck />}
            meta={q ? `${shownCount} of ${n}` : `${n}`}
            actions={<SearchInput value={q} onChange={setQ} placeholder="Filter packages" className="w-40 sm:w-56" />}
          />
          <GroupedUpdates updates={c.updates} query={q} />
        </Card>
      ) : null}

      {aur.length > 0 && (
        <Card className="mt-4">
          <CardHeader title="AUR" meta={`${aur.length}`} actions={<Badge>read-only</Badge>} />
          <CardBody className="border-b border-border py-2.5 text-xs text-muted">
            Wargo doesn't build AUR packages. Install them manually with <span className="num text-fg">pamac</span> or{" "}
            <span className="num text-fg">yay</span> over SSH.
          </CardBody>
          <PlainList items={aur} query={q} />
        </Card>
      )}

      {flat.length > 0 && (
        <Card className="mt-4">
          <CardHeader title="Flatpak" meta={`${flat.length}`} actions={<Badge>read-only</Badge>} />
          <CardBody className="border-b border-border py-2.5 text-xs text-muted">
            Update apps with <span className="num text-fg">flatpak update</span> over SSH.
          </CardBody>
          <PlainList items={flat} query={q} />
        </Card>
      )}

      <UpgradeDialog open={confirm} onOpenChange={setConfirm} status={st} loading={upgrade.isPending} onConfirm={() => upgrade.mutate()} />
    </div>
  );
}

function SystemToolbar({ status, actions }: { status: PkgStatus; actions: React.ReactNode }) {
  return (
    <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
      <div className="flex min-w-0 flex-wrap items-center gap-2 text-sm text-muted">
        <span>{status.distro || "Unknown distribution"}</span>
        {status.backend && <Badge tone="frost">{status.backend}</Badge>}
        {status.fake && <Badge tone="warn">simulated upgrades</Badge>}
      </div>
      <div className="flex flex-wrap items-center gap-2">{actions}</div>
    </div>
  );
}

type Tab = "system" | "containers";

const tabCls =
  "inline-flex h-10 items-center gap-2 border-b-2 border-transparent px-3 text-[0.8125rem] font-medium text-muted hover:text-fg data-[state=active]:border-accent data-[state=active]:text-fg [&_svg]:size-4";

function Count({ n, loading, tone }: { n?: number; loading?: boolean; tone?: "accent" }) {
  if (loading) return <Spinner className="size-3" />;
  if (n === undefined) return null;
  return (
    <span
      className={cn(
        "num inline-flex h-4 min-w-4 items-center justify-center rounded-full px-1 text-2xs font-semibold leading-none",
        n > 0 && tone === "accent" ? "bg-accent-soft text-accent" : "bg-surface-3 text-muted",
      )}
    >
      {n > 999 ? "999+" : n}
    </span>
  );
}

/** Updates page: system packages and container images, as tabs. */
export default function UpdatesPage() {
  const [params, setParams] = useSearchParams();
  const pkg = usePkgStatus();
  const docker = useCapabilities().data?.docker;
  const dockerOn = docker?.available !== false;
  const containers = useContainers({ enabled: dockerOn });
  const updates = useContainerUpdates({ enabled: dockerOn });

  const pkgN = pkg.data ? pendingCount(pkg.data) : undefined;
  const ctrN = containers.data ? containers.data.filter((c) => c.updateAvailable).length : undefined;
  const param = params.get("tab");
  // Without an explicit tab, open the one that has something to do.
  const tab: Tab = param === "containers" || param === "system" ? param : pkgN === 0 && (ctrN ?? 0) > 0 ? "containers" : "system";

  return (
    <div className="mx-auto max-w-[1600px]">
      <Tabs.Root value={tab} onValueChange={(v) => setParams(v === "system" ? {} : { tab: v }, { replace: true })}>
        <div className="mb-4 border-b border-border">
          <h1 className="text-lg font-semibold tracking-tight">Updates</h1>
          <Tabs.List className="-mb-px mt-2 flex gap-1 overflow-x-auto [scrollbar-width:none]" aria-label="Update sources">
            <Tabs.Trigger value="system" className={tabCls}>
              <PackageCheck /> System packages <Count n={pkgN} loading={!!pkg.data?.checking || !!pkg.data?.running} tone="accent" />
            </Tabs.Trigger>
            <Tabs.Trigger value="containers" className={tabCls}>
              <Boxes /> Containers <Count n={dockerOn ? ctrN : undefined} loading={!!updates.data?.checking} tone="accent" />
            </Tabs.Trigger>
          </Tabs.List>
        </div>
        <Tabs.Content value="system" className="focus-visible:outline-none">
          <SystemUpdates />
        </Tabs.Content>
        <Tabs.Content value="containers" className="focus-visible:outline-none">
          <ContainerUpdates
            docker={docker}
            list={containers.data}
            listError={containers.error}
            updates={updates.data}
            updatesError={updates.error}
          />
        </Tabs.Content>
      </Tabs.Root>
    </div>
  );
}
