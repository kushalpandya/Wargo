// SPDX-License-Identifier: AGPL-3.0-only
import { useQuery } from "@tanstack/react-query";
import { BookOpen, Cpu, ExternalLink, Scale, Server } from "lucide-react";
import { api } from "@/lib/api";
import { bytes, duration } from "@/lib/format";
import { Logo } from "@/app/Logo";
import { Card, CardBody, CardHeader, Stat } from "@/components/ui/card";
import { ErrorNote, Field, Skeleton } from "@/components/ui/misc";
import { SettingsGrid } from "./shared";
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

export default function About() {
  const self = useQuery({ queryKey: ["system", "self"], queryFn: () => api.get<Self>("/api/system/self"), refetchInterval: 5000 });
  const info = useQuery({ queryKey: ["system", "info"], queryFn: () => api.get<Info>("/api/system/info"), staleTime: Infinity });
  const s = self.data;
  const i = info.data;
  return (
    <>
      <SettingsGrid>
        <Card>
          <CardBody className="flex flex-wrap items-center gap-4">
            <Logo className="size-10" />
            <div className="min-w-0 flex-1">
              <div className="text-base font-semibold tracking-tight">Wargo</div>
              <div className="text-sm text-muted">Self-hosted homelab dashboard</div>
            </div>
            <div className="text-right">
              <div className="text-2xs font-medium uppercase tracking-wider text-muted">Version</div>
              <div className="num text-sm">{s?.version ?? "…"}</div>
            </div>
          </CardBody>
        </Card>

        <Card>
          <CardHeader title="Wargo process" icon={<Cpu />} meta={s ? `pid ${s.pid}` : undefined} />
          <CardBody>
            {self.error ? (
              <ErrorNote error={self.error} />
            ) : !s ? (
              <Skeleton className="h-12" />
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
        </Card>

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

        <Card>
          <CardHeader title="Project" icon={<BookOpen />} />
          <CardBody className="flex flex-col gap-2 text-sm">
            <a href={REPO} target="_blank" rel="noopener noreferrer" className="inline-flex min-h-8 items-center gap-2 self-start text-accent underline-offset-2 hover:underline">
              Source code <ExternalLink className="size-3.5" aria-hidden="true" />
            </a>
            <a
              href="https://www.gnu.org/licenses/agpl-3.0.html"
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex min-h-8 items-center gap-2 self-start text-accent underline-offset-2 hover:underline"
            >
              <Scale className="size-3.5" aria-hidden="true" /> Licensed under the GNU AGPL v3.0 <ExternalLink className="size-3.5" aria-hidden="true" />
            </a>
            <p className="text-xs text-muted">
              Wargo is free software: you can redistribute and modify it under the terms of the AGPL-3.0. If you run a modified version for others over a network,
              you must offer them its source.
            </p>
          </CardBody>
        </Card>
      </SettingsGrid>
      <div className="mt-6 max-w-[calc(3*42rem+2*1rem)]">
        <AuditLog />
      </div>
    </>
  );
}
