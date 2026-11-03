// SPDX-License-Identifier: AGPL-3.0-only
import { useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  AppWindow,
  ArrowLeftRight,
  Binary,
  Boxes,
  CircuitBoard,
  Clock,
  Container,
  Cpu,
  Globe,
  Gpu,
  Languages,
  Network,
  Package,
  Power,
  Server,
  SquareTerminal,
  Timer,
} from "lucide-react";
import { api } from "@/lib/api";
import { bytes, duration } from "@/lib/format";
import { cn } from "@/lib/cn";
import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/misc";
import { Logo } from "@/app/Logo";
import { useCapabilities } from "@/app/session";
import { usePublicIP, type Snapshot, type SystemInfo } from "@/features/system/useMetrics";
import { useContainers } from "@/features/containers/api";

export interface Desktop {
  name: string;
  version?: string;
  session?: string;
  wm?: string;
  user?: string;
}

export interface Specs {
  model?: string;
  board?: string;
  gpus?: string[];
  /** Per-GPU memory, same order as gpus. */
  gpuDetails?: { name: string; vram?: number; shared?: boolean }[];
  shell?: string;
  init?: string;
  timezone?: string;
  locale?: string;
  localIp?: string;
  packages?: { manager: string; count: number }[];
  desktop?: Desktop;
  logo: boolean;
  logoColor?: string;
}

export type SystemInfoWithSpecs = SystemInfo & { specs?: Specs };

const sessionLabel: Record<string, string> = { wayland: "Wayland", x11: "X11", tty: "TTY", mir: "Mir" };

function DistroLogo({ show, className }: { show: boolean; className?: string }) {
  const [failed, setFailed] = useState(false);
  if (!show || failed) return <Logo className={className} />;
  return <img src="/api/system/logo" alt="" onError={() => setFailed(true)} className={cn("object-contain", className)} />;
}

interface Item {
  icon: ReactNode;
  label: string;
  value?: ReactNode;
  title?: string;
}

function SpecItem({ icon, label, value, title }: Item) {
  return (
    <div className="flex min-w-0 items-start gap-2.5 py-1.5">
      <span className="mt-0.5 flex size-7 shrink-0 items-center justify-center rounded-md bg-surface-3 text-muted [&_svg]:size-3.5" aria-hidden="true">
        {icon}
      </span>
      <div className="min-w-0 flex-1">
        <div className="text-2xs font-medium uppercase tracking-wider text-subtle">{label}</div>
        <div className="num truncate text-ui text-fg" title={title ?? (typeof value === "string" ? value : undefined)}>
          {value}
        </div>
      </div>
    </div>
  );
}

function Section({ title, items }: { title: string; items: Item[] }) {
  const shown = items.filter((i) => i.value);
  if (!shown.length) return null;
  return (
    <section aria-label={title} className="min-w-0">
      <h3 className="mb-0.5 text-2xs font-semibold uppercase tracking-wider text-accent">{title}</h3>
      <div className="grid grid-cols-1 gap-x-4 @md:grid-cols-2">
        {shown.map((i) => (
          <SpecItem key={i.label} {...i} />
        ))}
      </div>
    </section>
  );
}

/** Machine summary in Wargo's own style: identity banner + grouped facts. */
export function SpecsCard({ snap }: { snap: Snapshot | null }) {
  const { data: info } = useQuery({
    queryKey: ["system", "info"],
    queryFn: () => api.get<SystemInfoWithSpecs>("/api/system/info"),
    staleTime: 2 * 60_000,
  });
  const { data: caps } = useCapabilities();
  const sp = info?.specs;
  const de = sp?.desktop;
  const arch = info ? (info.arch === "amd64" ? "x86_64" : info.arch === "arm64" ? "aarch64" : info.arch) : "";
  const gpuList = sp?.gpuDetails ?? (sp?.gpus ?? []).map((name) => ({ name, vram: undefined, shared: undefined }));
  // Same interval as the feed's: one poll for both (the topic keeps it live).
  const { data: containers } = useContainers({ enabled: !!caps?.docker?.available, refetchInterval: 60_000 });
  const publicIp = usePublicIP();

  const hardware: Item[] = [
    { icon: <Server />, label: "Host", value: sp?.model },
    { icon: <CircuitBoard />, label: "Board", value: sp?.board && sp.board !== sp.model ? sp.board : undefined },
    {
      icon: <Cpu />,
      label: "Processor",
      value: info ? `${info.cpuModel}${info.cpuMaxMHz ? ` @ ${(info.cpuMaxMHz / 1000).toFixed(1)} GHz` : ""}` : undefined,
      title: info ? `${info.cpuModel} · ${info.cores} cores / ${info.threads} threads` : undefined,
    },
    ...gpuList.map((g, i) => ({
      icon: <Gpu />,
      label: gpuList.length > 1 ? `Graphics ${i + 1}` : "Graphics",
      value: `${g.name}${g.vram ? ` · ${bytes(g.vram)}` : g.shared ? " · shared memory" : ""}`,
    })),
    {
      icon: <ArrowLeftRight />,
      label: "Swap",
      value: snap ? (snap.memory.swapTotal ? `${bytes(snap.memory.swapUsed)} of ${bytes(snap.memory.swapTotal)}` : "Disabled") : undefined,
    },
  ];

  const software: Item[] = [
    { icon: <Binary />, label: "Kernel", value: info?.kernel },
    {
      icon: <AppWindow />,
      label: "Desktop",
      value: de
        ? [de.name, de.version].filter(Boolean).join(" ") + (de.session && sessionLabel[de.session] ? ` · ${sessionLabel[de.session]}` : "")
        : info
          ? "None (headless)"
          : undefined,
      title: de ? `${de.name} ${de.version ?? ""}${de.wm ? ` · ${de.wm}` : ""}${de.user ? ` · session of ${de.user}` : ""}` : undefined,
    },
    {
      icon: <Package />,
      label: "Packages",
      value: sp?.packages?.length ? sp.packages.map((p) => `${p.count.toLocaleString()} ${p.manager}`).join(" · ") : undefined,
    },
    { icon: <SquareTerminal />, label: "Shell", value: sp?.shell },
    { icon: <Power />, label: "Init", value: sp?.init },
    {
      icon: <Container />,
      label: "Docker",
      value: caps?.docker?.available ? (caps.docker as { version?: string }).version : undefined,
    },
  ];

  const network: Item[] = [
    { icon: <Network />, label: "Local IP", value: sp?.localIp },
    {
      icon: <Globe />,
      label: "Public IP",
      value: publicIp.data?.ip ?? (publicIp.isLoading ? "…" : publicIp.data?.error ? "Unavailable" : undefined),
      title: publicIp.data?.source ? `Looked up via ${publicIp.data.source}` : undefined,
    },
    { icon: <Clock />, label: "Timezone", value: sp?.timezone },
    { icon: <Languages />, label: "Locale", value: sp?.locale },
  ];

  return (
    <Card className="@container flex h-full flex-col overflow-hidden">
      {/* Identity banner */}
      <div className="relative flex items-center gap-4 border-b border-border bg-surface-2 px-4 py-4">
        <div
          className="pointer-events-none absolute inset-y-0 left-0 w-1"
          style={{ background: sp?.logoColor ?? "var(--accent)" }}
          aria-hidden="true"
        />
        <div className="flex size-14 shrink-0 items-center justify-center rounded-xl border border-border bg-surface p-2">
          {info ? <DistroLogo show={!!sp?.logo} className="size-full" /> : <Skeleton className="size-full" />}
        </div>
        <div className="min-w-0 flex-1">
          {info ? (
            <>
              <h2 className="truncate text-lg font-semibold leading-tight tracking-tight">{info.hostname}</h2>
              <p className="truncate text-sm text-muted">
                {info.os} <span className="num text-subtle">· {arch}</span>
              </p>
            </>
          ) : (
            <>
              <Skeleton className="h-5 w-32" />
              <Skeleton className="mt-1.5 h-4 w-48" />
            </>
          )}
        </div>
        <div className="hidden shrink-0 flex-col items-end gap-1 text-xs @sm:flex">
          {snap && (
            <span className="num inline-flex items-center gap-1 text-fg" title="Uptime">
              <Timer className="size-3.5 text-subtle" aria-hidden="true" />
              up {duration(snap.uptime)}
            </span>
          )}
          {containers && (
            <span className="num inline-flex items-center gap-1 text-subtle" title="Containers installed and running">
              <Boxes className="size-3.5" aria-hidden="true" />
              {containers.length} installed · {containers.filter((c) => c.state === "running").length} running
            </span>
          )}
        </div>
      </div>

      <div className="flex flex-1 flex-col gap-4 p-4">
        {info ? (
          <>
            <Section title="Hardware" items={hardware} />
            <Section title="Software" items={software} />
            <Section title="Network & locale" items={network} />
          </>
        ) : (
          <div className="grid gap-3 @md:grid-cols-2">
            {Array.from({ length: 8 }, (_, i) => (
              <Skeleton key={i} className="h-9" />
            ))}
          </div>
        )}
      </div>
    </Card>
  );
}
