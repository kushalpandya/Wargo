// SPDX-License-Identifier: AGPL-3.0-only
import { LayoutGrid } from "lucide-react";
import { Card, CardBody, CardHeader } from "@/components/ui/card";
import { Segmented } from "@/components/ui/form";
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
          <CardBody>
            {isLoading ? (
              <Skeleton className="h-16" />
            ) : error ? (
              <ErrorNote error={error} />
            ) : (
              v && (
                <SettingRow
                  title="Show apps"
                  description={
                    !v.enabled
                      ? "Apps are hidden and their online checks are paused."
                      : v.placement === "page"
                        ? "Apps get their own page in the sidebar. Online checks run every minute."
                        : "Apps are shown at the top of Home. Online checks run every minute."
                  }
                >
                  <Segmented
                    label="Where to show apps"
                    value={!v.enabled ? "off" : v.placement === "page" ? "page" : "overview"}
                    onChange={(choice) => update(choice === "off" ? { enabled: false } : { enabled: true, placement: choice })}
                    options={[
                      { value: "overview", label: "On Home" },
                      { value: "page", label: "Apps page" },
                      { value: "off", label: "Hidden" },
                    ]}
                  />
                </SettingRow>
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
