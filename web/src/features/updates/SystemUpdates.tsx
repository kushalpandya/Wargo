// SPDX-License-Identifier: AGPL-3.0-only
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AppWindow, CircleCheck, Hammer, HardDrive, Lock, PackageCheck, PackageSearch, RotateCw } from "lucide-react";
import { api } from "@/lib/api";
import { ago, bytes, bytesParts } from "@/lib/format";
import { Card, Stat } from "@/components/ui/card";
import { Badge, EmptyState, ErrorNote, Notice, Spinner } from "@/components/ui/misc";
import { statusKey, usePkgStatus, type JobSnapshot, type PkgStatus, type Precheck } from "./api";
import { PackageListCard, ReadOnlyCard } from "./UpdateList";
import { PKG_STAGES, RebootButton, RunPanel } from "./RunPanel";
import {
  DialogPoint,
  PendingStat,
  SourceToolbar,
  UpdateAllButton,
  UpdateAllDialog,
  UpdateSummary,
  CheckNotices,
  TabSkeleton,
  ignoreCanceled,
  runVisible,
  useDismissedRun,
} from "./shared";

const isArch = (b?: string) => b === "pacman" || b === "pamac";

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
  const lowDisk = pre.data?.disk.filter((d) => d.low) ?? [];
  const blocked = pre.data?.blocked;
  return (
    <UpdateAllDialog
      open={open}
      onOpenChange={onOpenChange}
      count={c?.updates.length ?? 0}
      description={`${status.distro} · ${status.backend}${c?.downloadSize ? ` · ${bytes(c.downloadSize)} to download` : ""}`}
      commandLabel="Runs as root"
      command={status.upgradeCommand}
      fake={status.fake}
      loading={loading}
      disabled={pre.isLoading || !!blocked}
      onConfirm={onConfirm}
      points={
        <>
          <DialogPoint>
            <span className="text-fg">Don't reboot or power off</span> the server until the upgrade finishes. It runs as a system service,
            so it keeps going if you close this page.
          </DialogPoint>
          {isArch(status.backend) && (
            <DialogPoint>
              Partial upgrades are unsupported on Arch-based systems, so <span className="text-fg">every repository package</span> is
              upgraded together. The package databases are synced first, so the final list can differ slightly from this one.
            </DialogPoint>
          )}
          {status.backend === "apt" && (
            <DialogPoint tone="info">
              Your modified config files are kept; the maintainer's versions are saved next to them for review.
            </DialogPoint>
          )}
          {(c?.extras.length ?? 0) > 0 && <DialogPoint tone="info">AUR and Flatpak packages are not touched.</DialogPoint>}
        </>
      }
    >
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
    </UpdateAllDialog>
  );
}

function Summary({ status, checking }: { status: PkgStatus; checking: boolean }) {
  const c = status.lastCheck;
  const aur = c?.extras.filter((u) => u.source === "aur").length ?? 0;
  const flat = c?.extras.filter((u) => u.source === "flatpak").length ?? 0;
  const n = c?.updates.length ?? 0;
  const size = c?.downloadSize ? bytesParts(c.downloadSize) : undefined;
  return (
    <UpdateSummary
      checking={checking}
      checkedAt={c?.at}
      checkedSub={c ? `took ${(c.durationMs / 1000).toFixed(1)} s` : "not checked yet"}
      note="Packages are installed only when you click Update all, after you approve it."
      stats={
        <>
          <PendingStat check={c} from="from repositories" />
          <Stat
            label="Download"
            value={size ? size[0] : "—"}
            unit={size?.[1]}
            sub={c && n > 0 ? (c.downloadSize ? "approximate, cached files excluded" : "size unknown") : " "}
          />
          <Stat
            label={status.extras.includes("aur") ? "AUR · Flatpak" : "Flatpak"}
            value={c ? (status.extras.includes("aur") ? `${aur} · ${flat}` : flat) : "—"}
            sub="read-only, not installed here"
          />
        </>
      }
    />
  );
}

/** The "System updates" tab: OS packages from the distribution's package manager. */
export function SystemUpdates() {
  const { data: st, isLoading, error } = usePkgStatus();
  const qc = useQueryClient();
  const [q, setQ] = useState("");
  const [confirm, setConfirm] = useState(false);
  const [dismissed, dismiss] = useDismissedRun("wargo.pkg.dismissedRun");

  const upgrade = useMutation({
    mutationFn: () => api.post<JobSnapshot>("/api/pkg/upgrade"),
    onSuccess: (job) => {
      setConfirm(false);
      qc.setQueryData<PkgStatus>(statusKey, (s) => (s ? { ...s, running: job } : s));
    },
    onError: ignoreCanceled("Could not start the update"),
  });

  if (isLoading) return <TabSkeleton />;
  if (error || !st) return <ErrorNote error={error ?? "Unavailable"} />;

  const c = st.lastCheck;
  const checking = st.checking;
  const running = !!st.running;
  const n = c?.updates.length ?? 0;
  const showRun = runVisible(st, dismissed);
  const canUpgrade = st.available && !!c && !c.error && !c.stale && n > 0 && !running && !checking;
  const aur = c?.extras.filter((u) => u.source === "aur") ?? [];
  const flat = c?.extras.filter((u) => u.source === "flatpak") ?? [];

  return (
    <div>
      <SourceToolbar
        info={
          <>
            <span>{st.distro || "Unknown distribution"}</span>
            {st.backend && <Badge tone="frost">{st.backend}</Badge>}
            {st.fake && <Badge tone="warn">simulated updates</Badge>}
          </>
        }
        actions={
          <>
            {n > 0 && <UpdateAllButton count={n} disabled={!canUpgrade} onClick={() => setConfirm(true)} />}
          </>
        }
      />

      {!st.available && (
        <Notice tone="crit" className="mb-4">
          {st.backend ? `${st.backend} is not installed on this host.` : "No supported package manager was found on this host."}
        </Notice>
      )}

      {st.reboot.required && !(showRun && st.lastRun?.rebootRequired) && !running && (
        <div className="mb-4 flex flex-wrap items-center gap-3 rounded-lg border border-warn/30 bg-warn-soft px-4 py-2.5">
          <RotateCw className="size-4 shrink-0 text-warn" />
          <div className="min-w-0 flex-1 text-sm">
            <span className="font-medium">Reboot recommended.</span> <span className="text-muted">{st.reboot.reason}</span>
          </div>
          <RebootButton />
        </div>
      )}

      {showRun && (
        <RunPanel
          source={{
            id: "pkg",
            running: st.running,
            lastRun: st.lastRun,
            backend: st.backend,
            fake: st.fake,
            logPath: "/api/pkg/log",
            stages: PKG_STAGES,
            system: true,
            liveNotice:
              "Don't reboot or power off the server until this finishes. The upgrade runs as a system service, so closing this page or restarting Wargo is safe.",
          }}
          onDismiss={st.lastRun ? () => dismiss(st.lastRun!.id) : undefined}
        />
      )}

      <Summary status={st} checking={checking} />

      <CheckNotices check={c} running={running} />

      {!c ? (
        <Card>
          <EmptyState icon={<PackageSearch />} title={checking ? "Checking packages…" : "No update check yet"}>
            Check for updates to ask <span className="num text-fg">{st.backend || "the package manager"}</span> for pending updates; nothing is
            installed.
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
        <PackageListCard title="Repository updates" icon={<PackageCheck />} updates={c.updates} query={q} onQuery={setQ} />
      ) : null}

      {aur.length > 0 && (
        <ReadOnlyCard title="AUR" icon={<Hammer />} items={aur} query={q}>
            Wargo doesn't build AUR packages. Install them manually with <span className="num text-fg">pamac</span> or{" "}
            <span className="num text-fg">yay</span> over SSH.
        </ReadOnlyCard>
      )}

      {flat.length > 0 && (
        <ReadOnlyCard title="Flatpak" icon={<AppWindow />} items={flat} query={q}>
            Update apps with <span className="num text-fg">flatpak update</span> over SSH.
        </ReadOnlyCard>
      )}

      <UpgradeDialog open={confirm} onOpenChange={setConfirm} status={st} loading={upgrade.isPending} onConfirm={() => upgrade.mutate()} />
    </div>
  );
}
