// SPDX-License-Identifier: AGPL-3.0-only
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { PackageCheck } from "lucide-react";
import { api } from "@/lib/api";
import { ago } from "@/lib/format";
import { useCapabilities } from "@/app/session";
import { Card, CardBody, CardHeader } from "@/components/ui/card";
import { Select } from "@/components/ui/form";
import { ErrorNote, Skeleton } from "@/components/ui/misc";
import { containerUpdatesKey, useContainerUpdatesStatus } from "@/features/containers/api";
import { useBrewStatus, usePkgStatus } from "@/features/updates/api";
import { AgentsCard, PendingCard } from "./AgentsCard";
import { NotificationsCard } from "./NotificationsCard";
import { enabledToggle, useSettingsSave } from "./save";
import { SettingRow, SettingsStack } from "./shared";

const every: Record<number, string> = {
  6: "Every 6 hours",
  12: "Every 12 hours",
  24: "Daily",
  72: "Every 3 days",
  168: "Weekly",
};

type UpdateSettings = { enabled: boolean; checkHours: number };

/** Every source shares one value, so all three routes read and write the same thing. */
const settingsPaths = ["/api/pkg/settings", "/api/brew/settings", "/api/containers/settings"];

function sourceList(names: string[]) {
  if (names.length <= 1) return names.join("");
  return names.slice(0, -1).join(", ") + " and " + names[names.length - 1];
}

function UpdatesCard() {
  const qc = useQueryClient();
  const { data: caps, isLoading: capsLoading, error: capsError } = useCapabilities();
  const pkgOn = !!caps?.pkg?.available;
  const brewOn = !!caps?.brew?.available;
  const dockerOn = !!caps?.docker?.available;
  const any = pkgOn || brewOn || dockerOn;

  // One route only: they all return the same value.
  const path = pkgOn ? settingsPaths[0] : brewOn ? settingsPaths[1] : settingsPaths[2];
  const key = [path];
  // Shows a change as soon as it is made (see useSettingsSave).
  const { data: v, error } = useQuery({ queryKey: key, queryFn: () => api.get<UpdateSettings>(path), enabled: any });
  const update = useSettingsSave<UpdateSettings>({
    queryKey: key,
    scope: "update-settings",
    put: (s) => api.put<UpdateSettings>(path, s),
    onSaved: () => {
      for (const p of settingsPaths) if (p !== path) qc.invalidateQueries({ queryKey: [p], exact: true });
      qc.invalidateQueries({ queryKey: containerUpdatesKey, exact: true });
    },
  });

  // "Last checked" shows only while on.
  const on = !!v?.enabled;
  const pkgLast = usePkgStatus({ enabled: pkgOn && on }).data?.lastCheck?.at;
  const brewLast = useBrewStatus({ enabled: brewOn && on }).data?.lastCheck?.at;
  const ctrLast = useContainerUpdatesStatus({ enabled: dockerOn && on }).data?.checkedAt;
  // Times arrive as ISO strings (pkg, brew) and unix ms (containers).
  const times = [pkgLast && Date.parse(pkgLast), brewLast && Date.parse(brewLast), ctrLast].filter((x): x is number => typeof x === "number" && x > 0);
  const last = times.length ? Math.max(...times) : undefined;

  const sources = [pkgOn && "OS packages", brewOn && "Homebrew", dockerOn && "container images"].filter((x): x is string => !!x);

  return (
    <Card>
      <CardHeader
        title="Updates"
        icon={<PackageCheck />}
        tint="mint"
        meta={any && v && !v.enabled ? "Background checks for new updates" : undefined}
        toggle={any ? enabledToggle("Check for updates", v, update, error) : capsLoading ? { label: "Check for updates", loading: true } : undefined}
      />
      {capsLoading ? (
        <CardBody>
          <Skeleton className="h-9 w-full" />
        </CardBody>
      ) : capsError || !caps ? (
        <CardBody>
          <ErrorNote error={capsError} />
        </CardBody>
      ) : !any ? (
        <CardBody>
          <p className="text-sm text-muted">Nothing to check on this host.</p>
        </CardBody>
      ) : !v ? (
        <CardBody>{error ? <ErrorNote error={error} /> : <Skeleton className="h-9 w-full" />}</CardBody>
      ) : v.enabled ? (
        <CardBody>
          <SettingRow
            title="Check frequency"
            description={
              <>
                Looks for updates to {sourceList(sources)} in the background, without asking for approval. Installing is always up to you.
                {last && <span className="num"> Last checked {ago(last)}.</span>}
              </>
            }
          >
            <Select aria-label="Check frequency" className="w-auto" value={v.checkHours} onChange={(e) => update({ checkHours: Number(e.target.value) })}>
              {Object.entries(every).map(([h, label]) => (
                <option key={h} value={h}>
                  {label}
                </option>
              ))}
            </Select>
          </SettingRow>
        </CardBody>
      ) : null}
    </Card>
  );
}

export default function General() {
  return (
    <SettingsStack>
      <UpdatesCard />
      <AgentsCard />
      <PendingCard />
      <NotificationsCard />
    </SettingsStack>
  );
}
