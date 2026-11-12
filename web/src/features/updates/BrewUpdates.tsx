// SPDX-License-Identifier: AGPL-3.0-only
import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Beer, CircleCheck, KeyRound, PackageSearch, Pin } from "lucide-react";
import { api } from "@/lib/api";
import { ago, plural } from "@/lib/format";
import { Card, Stat } from "@/components/ui/card";
import { Badge, EmptyState, ErrorNote, Notice } from "@/components/ui/misc";
import { brewKey, useBrewStatus, type BrewStatus, type JobSnapshot } from "./api";
import { PackageListCard, ReadOnlyCard } from "./UpdateList";
import { BREW_STAGES, RunPanel } from "./RunPanel";
import {
  CheckNotices,
  DialogPoint,
  PendingStat,
  SourceToolbar,
  TabSkeleton,
  UpdateAllButton,
  UpdateAllDialog,
  UpdateSummary,
  ignoreCanceled,
  runVisible,
  useDismissedRun,
} from "./shared";

function Summary({ status, checking }: { status: BrewStatus; checking: boolean }) {
  const c = status.lastCheck;
  const ups = c?.updates ?? [];
  const formulae = ups.filter((u) => u.source === "formula").length;
  const casks = ups.filter((u) => u.source === "cask").length;
  const pw = ups.filter((u) => u.needsPassword).length;
  const pinned = c?.extras.length ?? 0;
  return (
    <UpdateSummary
      checking={checking}
      checkedAt={c?.at}
      checkedSub={c ? `took ${(c.durationMs / 1000).toFixed(1)} s` : "not checked yet"}
      note="Checks refresh Homebrew's package lists with brew update. Packages are installed only when you click Update all."
      stats={
        <>
          <PendingStat check={c} from="formulae and casks" />
          <Stat
            label="Formulae · Casks"
            value={c ? `${formulae} · ${casks}` : "—"}
            sub={c ? (pinned ? `${pinned} pinned, skipped` : "nothing pinned") : " "}
          />
          <Stat
            label="Need password"
            value={c ? pw : "—"}
            tone={pw ? "warn" : undefined}
            sub={c ? (pw ? "installers that run as admin" : "none asks for one") : " "}
          />
        </>
      }
    />
  );
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
  status: BrewStatus;
  onConfirm: () => void;
  loading: boolean;
}) {
  const c = status.lastCheck;
  const ups = c?.updates ?? [];
  const pw = ups.filter((u) => u.needsPassword);
  const pinned = c?.extras.length ?? 0;
  return (
    <UpdateAllDialog
      open={open}
      onOpenChange={onOpenChange}
      count={ups.length}
      description={`Homebrew${status.version ? ` ${status.version}` : ""} · ${status.prefix ?? ""}`}
      commandLabel="Runs as you"
      command={status.upgradeCommand}
      fake={status.fake}
      loading={loading}
      onConfirm={onConfirm}
      points={
        <>
          {pw.length > 0 ? (
            <DialogPoint>
              <span className="text-fg">{pw.map((u) => u.name).join(", ")}</span> {pw.length === 1 ? "installs" : "install"} with
              administrator rights, so Wargo asks for your password next. It is handed to Homebrew's{" "}
              <span className="num text-fg">sudo</span> for this update only and never stored.
            </DialogPoint>
          ) : (
            <DialogPoint tone="info">Homebrew runs as your user; no administrator password is needed.</DialogPoint>
          )}
          <DialogPoint>
            <span className="text-fg">Keep Wargo running</span> until the update finishes: Homebrew runs as part of Wargo, so restarting it
            stops the update. Closing this page is fine.
          </DialogPoint>
          <DialogPoint>Apps being updated may quit while their new version is installed.</DialogPoint>
          {pinned > 0 && (
            <DialogPoint tone="info">
              {pinned} pinned {plural(pinned, "formula", "formulae")} {pinned === 1 ? "stays" : "stay"} at {pinned === 1 ? "its" : "their"}{" "}
              current version.
            </DialogPoint>
          )}
        </>
      }
    />
  );
}

/** The "Homebrew" tab: outdated formulae and casks of this user's Homebrew. */
export function BrewUpdates() {
  const { data: st, isLoading, error } = useBrewStatus();
  const qc = useQueryClient();
  const [q, setQ] = useState("");
  const [confirm, setConfirm] = useState(false);
  const [dismissed, dismiss] = useDismissedRun("wargo.brew.dismissedRun");

  const upgrade = useMutation({
    mutationFn: () => api.post<JobSnapshot>("/api/brew/upgrade"),
    onSuccess: (job) => {
      setConfirm(false);
      qc.setQueryData<BrewStatus>(brewKey, (s) => (s ? { ...s, running: job } : s));
    },
    onError: ignoreCanceled("Could not start the update"),
  });

  if (isLoading) return <TabSkeleton />;
  if (error || !st) return <ErrorNote error={error ?? "Unavailable"} />;

  const c = st.lastCheck;
  const checking = st.checking;
  const running = !!st.running;
  const n = c?.updates.length ?? 0;
  const pinned = c?.extras ?? [];
  const showRun = runVisible(st, dismissed);
  const canUpgrade = st.canUpgrade && !!c && !c.error && !c.stale && n > 0 && !running && !checking;

  return (
    <div>
      <SourceToolbar
        info={
          <>
            <span>Homebrew{st.version ? ` ${st.version}` : ""}</span>
            {st.prefix && <span className="num">· {st.prefix}</span>}
            {st.fake && <Badge tone="warn">simulated updates</Badge>}
          </>
        }
        actions={
          <>
            {n > 0 && st.canUpgrade && <UpdateAllButton count={n} disabled={!canUpgrade} onClick={() => setConfirm(true)} />}
          </>
        }
      />

      {st.problem && (
        <Notice tone="warn" className="mb-4">
          {st.problem}
        </Notice>
      )}

      {showRun && (
        <RunPanel
          source={{
            id: "brew",
            running: st.running,
            lastRun: st.lastRun,
            backend: "brew",
            fake: st.fake,
            logPath: "/api/brew/log",
            stages: BREW_STAGES,
            liveNotice:
              "Keep Wargo running until this finishes: Homebrew runs as part of Wargo, so restarting Wargo stops the update. Closing this page is safe.",
          }}
          onDismiss={st.lastRun ? () => dismiss(st.lastRun!.id) : undefined}
        />
      )}

      <Summary status={st} checking={checking} />

      <CheckNotices check={c} running={running} />

      {!c ? (
        <Card>
          <EmptyState icon={<PackageSearch />} title={checking ? "Checking Homebrew…" : "No update check yet"}>
            Check for updates to refresh Homebrew and list outdated formulae and casks; nothing is installed.
          </EmptyState>
        </Card>
      ) : !c.error && n === 0 ? (
        <Card>
          <EmptyState icon={<CircleCheck className="text-ok" />} title="Homebrew is up to date">
            No outdated formulae or casks were found {ago(c.at)}.
          </EmptyState>
        </Card>
      ) : n > 0 ? (
        <PackageListCard
          title="Outdated packages"
          icon={<Beer />}
          updates={c.updates}
          query={q}
          onQuery={setQ}
          by="kind"
          footer={
            c.updates.some((u) => u.needsPassword) && (
              <div className="flex items-start gap-2 border-t border-border px-4 py-2 text-xs text-muted">
                <KeyRound className="mt-px size-3.5 shrink-0 text-warn" />
                <span>Casks marked password install with administrator rights. Update all asks for your password once for all of them.</span>
              </div>
            )
          }
        />
      ) : null}

      {pinned.length > 0 && (
        <ReadOnlyCard title="Pinned" icon={<Pin />} items={pinned} query={q} badge="skipped" repoLabel="Tap">
            Pinned formulae keep their version. Run <span className="num text-fg">brew unpin &lt;name&gt;</span> in a terminal to include
            them.
        </ReadOnlyCard>
      )}

      <UpgradeDialog open={confirm} onOpenChange={setConfirm} status={st} loading={upgrade.isPending} onConfirm={() => upgrade.mutate()} />
    </div>
  );
}
