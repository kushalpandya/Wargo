// SPDX-License-Identifier: AGPL-3.0-only
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { PackageCheck } from "lucide-react";
import { api } from "@/lib/api";
import { ago } from "@/lib/format";
import { useCapabilities } from "@/app/session";
import { Card, CardBody, CardHeader } from "@/components/ui/card";
import { Select } from "@/components/ui/form";
import { ErrorNote, Skeleton } from "@/components/ui/misc";
import { toast } from "@/components/ui/toast";
import { containerUpdatesKey, useContainerUpdatesStatus } from "@/features/containers/api";
import { useBrewStatus, usePkgStatus } from "@/features/updates/api";
import { AgentsCard, PendingCard } from "./AgentsCard";
import { NotificationsCard } from "./NotificationsCard";
import { SettingRow, SettingsList, SettingsStack } from "./shared";

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
  const brewOn = !!caps?.brew?.available;
  const dockerOn = !!caps?.docker?.available;
  const pkgLast = usePkgStatus({ enabled: pkgOn }).data?.lastCheck?.at;
  const brewLast = useBrewStatus({ enabled: brewOn }).data?.lastCheck?.at;
  // OS packages and Homebrew share one interval (saved once, read by both).
  const pkgTitle = pkgOn && brewOn ? "System packages and Homebrew" : pkgOn ? "System packages" : "Homebrew";
  const pkgSeen = [pkgLast, brewLast].filter(Boolean).sort().pop();
  const ctrLast = useContainerUpdatesStatus({ enabled: dockerOn }).data?.checkedAt;

  return (
    <Card>
      <CardHeader title="Update checks" icon={<PackageCheck />} tint="mint" />
      <CardBody>
        {!pkgOn && !brewOn && !dockerOn ? (
          <p className="text-sm text-muted">Nothing to check on this host.</p>
        ) : (
          <SettingsList>
            {(pkgOn || brewOn) && (
              <SettingRow
                title={pkgTitle}
                description={
                  <>
                    {pkgOn ? "Looks for OS updates" : "Refreshes Homebrew and looks for outdated packages"} in the background
                    {pkgOn && brewOn ? ", and for Homebrew updates," : ""} without asking for approval. Installing is always up to you.
                    {pkgSeen && <span className="num"> Last checked {ago(pkgSeen)}.</span>}
                  </>
                }
              >
                <IntervalSelect path={pkgOn ? "/api/pkg/settings" : "/api/brew/settings"} label={`${pkgTitle} checks`} hours={[0, 6, 12, 24, 72, 168]} />
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
      <UpdateChecksCard />
      <AgentsCard />
      <PendingCard />
      <NotificationsCard />
    </SettingsStack>
  );
}
