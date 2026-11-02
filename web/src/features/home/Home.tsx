// SPDX-License-Identifier: AGPL-3.0-only
import { useCapabilities } from "@/app/session";
import { useMetrics } from "@/features/overview/useMetrics";
import { LauncherStrip } from "@/features/overview/LauncherStrip";
import { SummaryTiles } from "@/features/system/Tiles";
import { SpecsCard } from "./SpecsCard";
import { ContainersCard } from "./ContainersCard";

export default function Home() {
  const { latest, points } = useMetrics();
  const { data: caps } = useCapabilities();
  return (
    <div className="mx-auto max-w-[1600px]">
      <h1 className="sr-only">Home</h1>
      {caps?.launcher?.enabled && caps.launcher.placement === "overview" && <LauncherStrip />}
      <SummaryTiles snap={latest} points={points} link />
      <div className="mt-3 grid grid-cols-1 gap-3 lg:grid-cols-2">
        <SpecsCard snap={latest} />
        {caps?.docker?.available !== false ? (
          <ContainersCard />
        ) : null}
      </div>
    </div>
  );
}
