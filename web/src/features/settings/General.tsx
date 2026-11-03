// SPDX-License-Identifier: AGPL-3.0-only
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { LayoutGrid, PackageCheck, Pencil } from "lucide-react";
import { api } from "@/lib/api";
import { ago } from "@/lib/format";
import { useCapabilities } from "@/app/session";
import { Button } from "@/components/ui/button";
import { Card, CardBody, CardHeader } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { Segmented, Select } from "@/components/ui/form";
import { ErrorNote, Skeleton } from "@/components/ui/misc";
import { toast } from "@/components/ui/toast";
import { TileEditor } from "@/features/launcher/TileEditor";
import { useLauncherSettings, useSaveLauncherSettings, useTiles, type LauncherSettings as LS } from "@/features/launcher/api";
import { containerUpdatesKey, useContainerUpdatesStatus } from "@/features/containers/api";
import { usePkgStatus } from "@/features/updates/api";
import { SettingRow, SettingsList, SettingsStack, usePatchSave } from "./shared";

function LauncherCard() {
  const { data, isLoading, error } = useLauncherSettings();
  const tiles = useTiles();
  const save = useSaveLauncherSettings();
  const [editing, setEditing] = useState(false);
  const [v, update] = usePatchSave<LS>(data, save);
  const n = tiles.data?.length ?? 0;

  return (
    <Card>
      <CardHeader title="App launcher" icon={<LayoutGrid />} tint="iris" meta={tiles.data ? `${n} app${n === 1 ? "" : "s"}` : undefined} />
      <CardBody>
        {isLoading ? (
          <Skeleton className="h-16" />
        ) : error ? (
          <ErrorNote error={error} />
        ) : (
          v && (
            <SettingsList>
              <SettingRow
                title="Show apps"
                description={
                  !v.enabled
                    ? "Apps are hidden and their online checks are paused."
                    : v.placement === "page"
                      ? "On their own page in the sidebar."
                      : "At the top of Home."
                }
              >
                <Segmented
                  label="Where to show apps"
                  value={!v.enabled ? "off" : v.placement === "page" ? "page" : "overview"}
                  onChange={(choice) => update(choice === "off" ? { enabled: false } : { enabled: true, placement: choice })}
                  options={[
                    { value: "overview", label: "On Home" },
                    { value: "page", label: "Sidebar" },
                    { value: "off", label: "Hidden" },
                  ]}
                />
              </SettingRow>
              <SettingRow title="Apps" description="Shortcuts to your services, with an online check every minute.">
                <Button size="sm" icon={<Pencil />} onClick={() => setEditing(true)}>
                  Manage apps
                </Button>
              </SettingRow>
            </SettingsList>
          )
        )}
      </CardBody>
      <Dialog open={editing} onOpenChange={setEditing} title="Apps" description="Add, edit, reorder or remove your shortcuts." size="xl">
        <TileEditor />
      </Dialog>
    </Card>
  );
}

const every: Record<number, string> = {
  0: "Off",
  1: "Every hour",
  3: "Every 3 hours",
  6: "Every 6 hours",
  12: "Every 12 hours",
  24: "Daily",
  72: "Every 3 days",
  168: "Weekly",
};

/** A background-check interval saved on the server (GET/PUT {checkHours}). */
function IntervalSelect({ path, label, hours }: { path: string; label: string; hours: number[] }) {
  const qc = useQueryClient();
  const key = [path];
  const q = useQuery({ queryKey: key, queryFn: () => api.get<{ checkHours: number }>(path) });
  const m = useMutation({
    mutationFn: (checkHours: number) => api.put<{ checkHours: number }>(path, { checkHours }),
    onSuccess: (v) => {
      qc.setQueryData(key, v);
      qc.invalidateQueries({ queryKey: containerUpdatesKey, exact: true });
      qc.invalidateQueries({ queryKey: ["capabilities"] });
    },
    onError: (e) => toast.error("Could not save", e),
  });
  if (q.isLoading) return <Skeleton className="h-9 w-36" />;
  if (!q.data) return <ErrorNote error={q.error} />;
  return (
    <Select aria-label={label} className="w-auto" value={m.isPending ? m.variables : q.data.checkHours} onChange={(e) => m.mutate(Number(e.target.value))}>
      {hours.map((h) => (
        <option key={h} value={h}>
          {every[h]}
        </option>
      ))}
    </Select>
  );
}

function UpdateChecksCard() {
  const { data: caps } = useCapabilities();
  const pkgOn = !!caps?.pkg?.available;
  const dockerOn = !!caps?.docker?.available;
  const pkgLast = usePkgStatus({ enabled: pkgOn }).data?.lastCheck?.at;
  const ctrLast = useContainerUpdatesStatus({ enabled: dockerOn }).data?.checkedAt;

  return (
    <Card>
      <CardHeader title="Update checks" icon={<PackageCheck />} tint="mint" />
      <CardBody>
        {!pkgOn && !dockerOn ? (
          <p className="text-sm text-muted">Nothing to check on this host.</p>
        ) : (
          <SettingsList>
            {pkgOn && (
              <SettingRow
                title="System packages"
                description={
                  <>
                    Looks for OS updates in the background, without asking for approval. Installing is always up to you.
                    {pkgLast && <span className="num"> Last checked {ago(pkgLast)}.</span>}
                  </>
                }
              >
                <IntervalSelect path="/api/pkg/settings" label="System package checks" hours={[0, 6, 12, 24, 72, 168]} />
              </SettingRow>
            )}
            {dockerOn && (
              <SettingRow
                title="Container images"
                description={
                  <>
                    Asks each image's registry whether a newer version exists.
                    {ctrLast && <span className="num"> Last checked {ago(ctrLast)}.</span>}
                  </>
                }
              >
                <IntervalSelect path="/api/containers/settings" label="Container image checks" hours={[0, 1, 3, 6, 12, 24, 72, 168]} />
              </SettingRow>
            )}
          </SettingsList>
        )}
      </CardBody>
    </Card>
  );
}

export default function General() {
  return (
    <SettingsStack>
      <LauncherCard />
      <UpdateChecksCard />
    </SettingsStack>
  );
}
