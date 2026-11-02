// SPDX-License-Identifier: AGPL-3.0-only
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { MonitorCog } from "lucide-react";
import { api } from "@/lib/api";
import { bytes, duration, pct } from "@/lib/format";
import { Card, CardHeader } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/misc";
import { Logo } from "@/app/Logo";
import { useCapabilities, useMe } from "@/app/session";
import type { Snapshot, SystemInfo } from "@/features/system/useMetrics";
import { useStorage } from "@/features/system/Storage";

export interface Specs {
  model?: string;
  board?: string;
  gpus?: string[];
  shell?: string;
  init?: string;
  timezone?: string;
  locale?: string;
  localIp?: string;
  packages?: { manager: string; count: number }[];
  logo: boolean;
  logoColor?: string;
}

export type SystemInfoWithSpecs = SystemInfo & { specs?: Specs };

function DistroLogo({ show }: { show: boolean }) {
  const [failed, setFailed] = useState(false);
  if (!show || failed) return <Logo className="size-20 sm:size-24" />;
  return (
    <img
      src="/api/system/logo"
      alt=""
      width={96}
      height={96}
      onError={() => setFailed(true)}
      className="size-20 object-contain sm:size-24"
    />
  );
}

/** Fastfetch-style machine summary. */
export function SpecsCard({ snap }: { snap: Snapshot | null }) {
  const { data: info } = useQuery({
    queryKey: ["system", "info"],
    queryFn: () => api.get<SystemInfoWithSpecs>("/api/system/info"),
    staleTime: 5 * 60_000,
  });
  const { data: me } = useMe();
  const { data: caps } = useCapabilities();
  const storage = useStorage();
  const sp = info?.specs;
  const rootFs = storage.data?.filesystems.find((f) => f.mount === "/");

  const rows: [string, string | undefined][] = info
    ? [
        ["OS", `${info.os} ${info.arch === "amd64" ? "x86_64" : info.arch === "arm64" ? "aarch64" : info.arch}`],
        ["Host", sp?.model],
        ["Board", sp?.board && sp.board !== sp.model ? sp.board : undefined],
        ["Kernel", info.kernel],
        ["Uptime", snap ? duration(snap.uptime) : undefined],
        ["Packages", sp?.packages?.length ? sp.packages.map((p) => `${p.count.toLocaleString()} (${p.manager})`).join(", ") : undefined],
        ["Shell", sp?.shell],
        ["Init", sp?.init],
        ["CPU", `${info.cpuModel} (${info.threads})${snap?.cpu.freqMHz ? ` @ ${(snap.cpu.freqMHz / 1000).toFixed(2)} GHz` : ""}`],
        ...((sp?.gpus ?? []).map((g, i) => [sp!.gpus!.length > 1 ? `GPU ${i + 1}` : "GPU", g]) as [string, string][]),
        ["Memory", snap ? `${bytes(snap.memory.used)} / ${bytes(snap.memory.total)} (${pct(snap.memory.usedPct)})` : bytes(info.memTotal)],
        ["Swap", snap && snap.memory.swapTotal ? `${bytes(snap.memory.swapUsed)} / ${bytes(snap.memory.swapTotal)}` : snap ? "Disabled" : undefined],
        ["Disk (/)", rootFs ? `${bytes(rootFs.used)} / ${bytes(rootFs.total)} (${pct(rootFs.usedPct)}) · ${rootFs.fstype}` : undefined],
        ["Local IP", sp?.localIp],
        ["Locale", sp?.locale],
        ["Timezone", sp?.timezone],
        ["Docker", caps?.docker?.available ? (caps.docker as { version?: string }).version : undefined],
        ["Wargo", caps?.version],
      ]
    : [];

  return (
    <Card className="flex h-full flex-col">
      <CardHeader title="System" icon={<MonitorCog />} meta={info?.virtualization ? `virtualised · ${info.virtualization}` : undefined} />
      <div className="flex flex-1 flex-col gap-4 p-4 sm:flex-row sm:gap-6">
        <div className="flex shrink-0 justify-center sm:block sm:pt-1">
          <DistroLogo show={!!sp?.logo} />
        </div>
        <div className="min-w-0 flex-1">
          {info ? (
            <>
              <div className="num text-[0.9375rem] font-semibold">
                <span className="text-accent">{me?.user ?? "user"}</span>
                <span className="text-subtle">@</span>
                <span className="text-accent">{info.hostname}</span>
              </div>
              <div className="my-1.5 h-px w-full max-w-64 bg-border" aria-hidden="true" />
              <dl className="num grid grid-cols-[max-content_minmax(0,1fr)] gap-x-3 gap-y-1 text-[0.8125rem]">
                {rows
                  .filter(([, v]) => v)
                  .map(([k, v]) => (
                    <div key={k} className="contents">
                      <dt className="font-medium text-frost">{k}</dt>
                      <dd className="min-w-0 break-words text-fg">{v}</dd>
                    </div>
                  ))}
              </dl>
              <div className="mt-3 flex gap-1" aria-hidden="true">
                {["bg-fg", "bg-crit", "bg-ok", "bg-warn", "bg-frost", "bg-accent", "bg-muted", "bg-subtle"].map((c) => (
                  <span key={c} className={`h-3 w-5 rounded-sm ${c}`} />
                ))}
              </div>
            </>
          ) : (
            <div className="space-y-2">
              {Array.from({ length: 10 }, (_, i) => (
                <Skeleton key={i} className="h-3.5 w-full max-w-80" />
              ))}
            </div>
          )}
        </div>
      </div>
    </Card>
  );
}
