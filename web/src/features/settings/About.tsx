// SPDX-License-Identifier: AGPL-3.0-only
import { useQuery } from "@tanstack/react-query";
import { ExternalLink, Scale, Server } from "lucide-react";
import { api } from "@/lib/api";
import { bytes, duration } from "@/lib/format";
import { Logo } from "@/app/Logo";
import { Card, CardBody, CardHeader, Stat } from "@/components/ui/card";
import { ErrorNote, Field, Skeleton } from "@/components/ui/misc";
import { AuditLog } from "./Audit";

interface Self {
  version: string;
  goVersion: string;
  pid: number;
  rssBytes: number;
  heapBytes: number;
  goroutines: number;
  uptimeSec: number;
  dataDir: string;
  user: string;
  container?: boolean;
  hostRoot?: string;
  containerId?: string;
}

interface Info {
  hostname: string;
  os: string;
  kernel: string;
  arch: string;
  cpuModel: string;
  cores: number;
  threads: number;
  memTotal: number;
  bootTime: number;
  virtualization?: string;
}

const REPO = "https://github.com/kushalpandya/wargo";
const LICENSE = "https://www.gnu.org/licenses/agpl-3.0.html";

/** GitHub's mark (lucide has no brand icons). */
function GitHubMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 16 16" fill="currentColor" className={className} aria-hidden="true">
      <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0016 8c0-4.42-3.58-8-8-8z" />
    </svg>
  );
}

const linkClass = "inline-flex min-h-8 items-center gap-1.5 text-sm text-accent underline-offset-2 hover:underline [&_svg]:size-4";

/** Wargo itself: version, the running process, and where the project lives. */
function WargoCard({ s, error }: { s?: Self; error: unknown }) {
  return (
    <Card className="flex flex-col">
      <CardBody className="flex items-center gap-4 border-b border-border">
        <Logo className="size-12" />
        <div className="min-w-0 flex-1">
          <div className="text-lg font-bold tracking-tight">Wargo</div>
          <div className="text-sm text-muted">Self-hosted homelab dashboard</div>
        </div>
        <div className="text-right">
          <div className="text-2xs font-medium uppercase tracking-wider text-muted">Version</div>
          <div className="num text-sm font-medium">{s?.version ?? "…"}</div>
        </div>
      </CardBody>
      <CardBody className="flex-1">
        {error ? (
          <ErrorNote error={error} />
        ) : !s ? (
          <Skeleton className="h-32" />
        ) : (
          <>
            <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
              <Stat label="Memory (RSS)" value={bytes(s.rssBytes).split(" ")[0]} unit={bytes(s.rssBytes).split(" ")[1]} sub={`heap ${bytes(s.heapBytes)}`} />
              <Stat label="Goroutines" value={s.goroutines} />
              <Stat label="Uptime" value={duration(s.uptimeSec)} />
              <Stat label="Runtime" value={s.goVersion.replace(/^go/, "")} sub="Go" />
            </div>
            <dl className="mt-4 divide-y divide-border border-t border-border text-sm">
              <Field label="Mode">
                {s.container ? (
                  <span title={s.hostRoot ? `Host filesystem at ${s.hostRoot}` : undefined}>
                    Container mode{s.containerId ? <span className="num text-muted"> · {s.containerId}</span> : null}
                  </span>
                ) : (
                  "Native"
                )}
              </Field>
              <Field label="Process" mono>
                pid {s.pid}
              </Field>
              <Field label={s.container ? "Acts as" : "Runs as"} mono>
                {s.user || "—"}
              </Field>
              <Field label="Data directory" mono>
                <span title={s.dataDir}>{s.dataDir}</span>
              </Field>
            </dl>
          </>
        )}
      </CardBody>
      <CardBody className="flex flex-wrap items-center gap-x-5 gap-y-1 border-t border-border py-2.5">
        <a href={REPO} target="_blank" rel="noopener noreferrer" className={linkClass}>
          <GitHubMark /> GitHub <ExternalLink className="!size-3.5" aria-hidden="true" />
        </a>
        <a
          href={LICENSE}
          target="_blank"
          rel="noopener noreferrer"
          className={linkClass}
          title="Wargo is free software under the AGPL-3.0: if you run a modified version for others over a network, you must offer them its source."
        >
          <Scale aria-hidden="true" /> GNU AGPL v3.0 <ExternalLink className="!size-3.5" aria-hidden="true" />
        </a>
      </CardBody>
    </Card>
  );
}

export default function About() {
  const self = useQuery({ queryKey: ["system", "self"], queryFn: () => api.get<Self>("/api/system/self"), refetchInterval: 5000 });
  const info = useQuery({ queryKey: ["system", "info"], queryFn: () => api.get<Info>("/api/system/info"), staleTime: Infinity });
  const i = info.data;
  return (
    <div className="about-fit max-w-[calc(3*42rem+2*1rem)]">
      {/* Two cards side by side on wide screens, then the audit log. */}
      <div className="grid gap-4 lg:grid-cols-2">
        <WargoCard s={self.data} error={self.error} />
        <Card>
          <CardHeader title="Host" icon={<Server />} />
          <CardBody>
            {info.error ? (
              <ErrorNote error={info.error} />
            ) : !i ? (
              <Skeleton className="h-24" />
            ) : (
              <dl className="divide-y divide-border text-sm">
                <Field label="Hostname" mono>
                  {i.hostname}
                </Field>
                <Field label="Operating system">{i.os}</Field>
                <Field label="Kernel" mono>
                  {i.kernel} ({i.arch})
                </Field>
                <Field label="CPU">
                  <span title={i.cpuModel}>
                    {i.cpuModel} · {i.cores}C/{i.threads}T
                  </span>
                </Field>
                <Field label="Memory" mono>
                  {bytes(i.memTotal)}
                </Field>
                {i.virtualization && <Field label="Virtualization">{i.virtualization}</Field>}
              </dl>
            )}
          </CardBody>
        </Card>
      </div>
      <div className="audit-slot mt-6">
        <AuditLog />
      </div>
    </div>
  );
}
