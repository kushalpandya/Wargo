// SPDX-License-Identifier: AGPL-3.0-only
import { LayoutGrid } from "lucide-react";
import { Card, CardBody, CardHeader } from "@/components/ui/card";
import { Segmented, Switch } from "@/components/ui/form";
import { ErrorNote, Skeleton } from "@/components/ui/misc";
import { toast } from "@/components/ui/toast";
import { TileEditor } from "@/features/launcher/TileEditor";
import { useLauncherSettings, useSaveLauncherSettings, type LauncherSettings as LS } from "@/features/launcher/api";
import { SettingRow, SettingsHeader } from "./shared";

export default function LauncherSettings() {
  const { data, isLoading, error } = useLauncherSettings();
  const save = useSaveLauncherSettings();
  const update = (patch: Partial<LS>) => {
    if (!data) return;
    save.mutate({ ...data, ...patch }, { onError: (e) => toast.error("Could not save", e) });
  };
  const v = save.isPending && save.variables ? save.variables : data;

  return (
    <>
      <SettingsHeader title="App launcher" description="Shortcuts to the services in your homelab, with optional online checks." />
      <div className="flex flex-col gap-4">
        <Card>
          <CardHeader title="Display" icon={<LayoutGrid />} />
          <CardBody className="divide-y divide-border">
            {isLoading ? (
              <Skeleton className="h-16" />
            ) : error ? (
              <ErrorNote error={error} />
            ) : (
              v && (
                <>
                  <div className="pb-3">
                    <Switch
                      checked={v.enabled}
                      onCheckedChange={(enabled) => update({ enabled })}
                      label="Enable app launcher"
                      description="Health checks run every minute while the launcher is enabled."
                    />
                  </div>
                  <SettingRow
                    title="Placement"
                    description={
                      v.placement === "page" ? "Apps get their own page in the sidebar." : "Apps are shown as a strip at the top of the Overview."
                    }
                    className={v.enabled ? undefined : "opacity-60"}
                  >
                    <Segmented
                      label="Launcher placement"
                      value={v.placement}
                      onChange={(placement) => update({ placement })}
                      options={[
                        { value: "overview", label: "Overview strip" },
                        { value: "page", label: "Apps page" },
                      ]}
                    />
                  </SettingRow>
                </>
              )
            )}
          </CardBody>
        </Card>
        <Card>
          <CardHeader title="Apps" />
          <CardBody>
            <TileEditor />
          </CardBody>
        </Card>
      </div>
    </>
  );
}
