// SPDX-License-Identifier: AGPL-3.0-only
import { useState, type ReactNode } from "react";
import { Link } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Boxes, ChevronDown, Copy, OctagonX, Square } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { bytes, datetime, duration, num } from "@/lib/format";
import { cn } from "@/lib/cn";
import { copyWithToast } from "@/lib/clipboard";
import { Button } from "@/components/ui/button";
import { ConfirmDialog, Sheet } from "@/components/ui/dialog";
import { Menu, MenuItem, MenuLabel } from "@/components/ui/menu";
import { Badge, ErrorNote, Field, Notice, Skeleton } from "@/components/ui/misc";
import { toast } from "@/components/ui/toast";
import { cpuTone, memTone, stateNames, type ProcDetails, type ProcList, type SignalName } from "./types";

const signalInfo: Record<SignalName, { label: string; desc: string; danger?: boolean }> = {
  TERM: { label: "Terminate", desc: "Asks the process to exit cleanly (SIGTERM). Unsaved work in it may be lost.", danger: true },
  KILL: { label: "Force kill", desc: "Stops the process immediately (SIGKILL). It cannot clean up and data may be lost.", danger: true },
  HUP: { label: "Hang up / reload", desc: "Sends SIGHUP. Many daemons reload their configuration; others exit." },
  INT: { label: "Interrupt", desc: "Sends SIGINT, the same as pressing Ctrl+C.", danger: true },
  STOP: { label: "Pause", desc: "Freezes the process (SIGSTOP) until it is continued.", danger: true },
  CONT: { label: "Continue", desc: "Resumes a paused process (SIGCONT)." },
  USR1: { label: "User signal 1", desc: "Sends SIGUSR1; the meaning depends on the program." },
  USR2: { label: "User signal 2", desc: "Sends SIGUSR2; the meaning depends on the program." },
};

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="border-b border-border px-4 py-3 last:border-0">
      <h3 className="mb-1 text-2xs font-semibold uppercase tracking-wider text-subtle">{title}</h3>
      {children}
    </section>
  );
}

function Big({ label, value, tone }: { label: string; value: string; tone?: "warn" | "crit" }) {
  return (
    <div className="min-w-0 bg-surface px-3 py-2.5">
      <div className="text-2xs font-medium uppercase tracking-wider text-subtle">{label}</div>
      <div className={cn("num mt-0.5 truncate text-base font-medium", tone === "warn" && "text-warn", tone === "crit" && "text-crit")}>{value}</div>
    </div>
  );
}

export function ProcessSheet({
  pid,
  onClose,
  onOpen,
  list,
  live,
}: {
  pid: number | null;
  onClose: () => void;
  onOpen: (pid: number) => void;
  list?: ProcList;
  live: boolean;
}) {
  const qc = useQueryClient();
  const [confirm, setConfirm] = useState<SignalName | null>(null);
  const q = useQuery({
    queryKey: ["procs", pid],
    queryFn: () => api.get<ProcDetails>(`/api/procs/${pid}`),
    enabled: pid !== null,
    refetchInterval: live ? 3000 : false,
    retry: false,
  });
  const d = q.data && q.data.pid === pid ? q.data : undefined;
  const gone = q.error instanceof ApiError && q.error.status === 404;
  const sig = useMutation({
    mutationFn: (s: SignalName) => api.post(`/api/procs/${pid}/signal`, { signal: s }),
    onSuccess: (_, s) => {
      toast.ok(`Sent SIG${s} to ${d?.name ?? pid}`);
      setConfirm(null);
      qc.invalidateQueries({ queryKey: ["procs"] });
    },
    onError: (e) => {
      setConfirm(null);
      if (e instanceof ApiError && e.code === "elevation_canceled") return;
      toast.error("Could not send signal", e);
    },
  });
  const protectedPid = pid !== null && (pid <= 1 || pid === list?.self);
  const foreign = d && list && d.uid !== list.uid;
  const names = new Map(list?.procs.map((p) => [p.pid, p.name]));
  const now = list?.sampledAt ?? Date.now();

  const actions = d && !protectedPid && (
    <div className="flex flex-wrap items-center gap-2">
      <Button size="sm" variant="outline-danger" icon={<Square />} onClick={() => setConfirm("TERM")}>
        Terminate
      </Button>
      <Button size="sm" variant="outline-danger" icon={<OctagonX />} onClick={() => setConfirm("KILL")}>
        Kill
      </Button>
      <Menu
        align="start"
        trigger={
          <Button size="sm" variant="secondary">
            More signals <ChevronDown />
          </Button>
        }
      >
        <MenuLabel>Send signal</MenuLabel>
        {(["HUP", "INT", "STOP", "CONT", "USR1", "USR2"] as SignalName[]).map((s) => (
          <MenuItem key={s} onSelect={() => setConfirm(s)} shortcut={`SIG${s}`}>
            {signalInfo[s].label}
          </MenuItem>
        ))}
      </Menu>
    </div>
  );

  return (
    <>
      <Sheet
        open={pid !== null}
        onOpenChange={(o) => !o && onClose()}
        title={d ? d.name : pid !== null ? `PID ${pid}` : ""}
        description={d ? `PID ${d.pid} · ${d.user}${d.containerName ? ` · container ${d.containerName}` : ""}` : undefined}
      >
        {gone ? (
          <div className="p-4">
            <Notice tone="neutral">This process has exited.</Notice>
          </div>
        ) : q.error ? (
          <div className="p-4">
            <ErrorNote error={q.error} />
          </div>
        ) : !d ? (
          <div className="space-y-3 p-4">
            <Skeleton className="h-16" />
            <Skeleton className="h-40" />
          </div>
        ) : (
          <div>
            <div className="grid grid-cols-2 gap-px border-b border-border bg-border sm:grid-cols-4">
              <Big label="CPU" value={`${d.cpu.toFixed(1)}%`} tone={cpuTone(d.cpu)} />
              <Big label="Memory" value={bytes(d.memRSS)} tone={memTone(d.memPct)} />
              <Big label="Threads" value={String(d.threads)} />
              <Big label="State" value={stateNames[d.state] ?? d.state} />
            </div>
            <div className="border-b border-border px-4 py-3">
              {protectedPid ? (
                <Notice tone="neutral">{pid === list?.self ? "This is Wargo itself; it cannot be signalled from here." : "PID 1 cannot be signalled."}</Notice>
              ) : (
                <>
                  {actions}
                  {foreign && (
                    <p className="mt-2 text-xs text-muted">Owned by {d.user}: signalling it needs administrator approval.</p>
                  )}
                </>
              )}
            </div>
            <Section title="Command">
              <div className="group relative mt-1 rounded-md border border-border bg-surface-2 p-2.5 pr-9">
                <code className="num block whitespace-pre-wrap break-all text-xs text-fg">{d.cmdline || `[${d.name}]`}</code>
                {d.cmdline && (
                  <button
                    type="button"
                    aria-label="Copy command"
                    title="Copy command"
                    onClick={() => void copyWithToast(d.cmdline, "Command copied")}
                    className="absolute right-1 top-1 inline-flex size-8 items-center justify-center rounded text-subtle hover:bg-surface-3 hover:text-fg"
                  >
                    <Copy className="size-3.5" />
                  </button>
                )}
              </div>
            </Section>
            <Section title="Process">
              <dl className="divide-y divide-border text-sm">
                <Field label="PID" mono>{d.pid}</Field>
                <Field label="Parent" mono>
                  {d.ppid > 0 ? (
                    <button type="button" className="text-frost hover:underline" onClick={() => onOpen(d.ppid)}>
                      {d.ppid} {d.status.parentName && `(${d.status.parentName})`}
                    </button>
                  ) : (
                    "—"
                  )}
                </Field>
                <Field label="User">
                  {d.user} <span className="num text-subtle">({d.uid})</span>
                </Field>
                <Field label="Nice" mono>{d.nice}</Field>
                <Field label="Started">
                  <span title={datetime(d.startTime)}>{duration((now - d.startTime) / 1000)} ago</span>
                </Field>
                {d.exe && <Field label="Executable" mono><span title={d.exe}>{d.exe}</span></Field>}
                {d.cwd && <Field label="Working dir" mono><span title={d.cwd}>{d.cwd}</span></Field>}
                <Field label="Open files" mono>{d.fdCount ?? <span className="text-subtle">not readable</span>}</Field>
                <Field label="Environment" mono>{d.envCount !== undefined ? `${d.envCount} variables` : <span className="text-subtle">not readable</span>}</Field>
                <Field label="Context switches" mono>
                  {num(d.status.volCtxSwitches)} / {num(d.status.invCtxSwitches)}
                </Field>
                {d.containerId && (
                  <Field label="Container">
                    <Link to={`/containers/${d.containerId.slice(0, 12)}`} className="inline-flex items-center gap-1 text-frost hover:underline">
                      <Boxes className="size-3.5" />
                      {d.containerName || d.containerId.slice(0, 12)}
                    </Link>
                  </Field>
                )}
              </dl>
            </Section>
            <Section title="Memory & IO">
              <dl className="divide-y divide-border text-sm">
                <Field label="Resident" mono>{bytes(d.mem.rss)} <span className="text-subtle">({d.memPct.toFixed(2)}%)</span></Field>
                <Field label="Virtual" mono>{bytes(d.mem.vms)}</Field>
                <Field label="Shared" mono>{bytes(d.mem.shared)}</Field>
                <Field label="Swap" mono>{bytes(d.mem.swap)}</Field>
                <Field label="Disk read" mono>{d.io ? bytes(d.io.read) : <span className="text-subtle">not readable</span>}</Field>
                <Field label="Disk written" mono>{d.io ? bytes(d.io.write) : <span className="text-subtle">not readable</span>}</Field>
              </dl>
            </Section>
            <Section title={`Children (${d.children.length})`}>
              {d.children.length === 0 ? (
                <p className="py-1 text-sm text-muted">No child processes.</p>
              ) : (
                <div className="flex flex-wrap gap-1.5 pt-1">
                  {d.children.slice(0, 100).map((c) => (
                    <button
                      key={c}
                      type="button"
                      onClick={() => onOpen(c)}
                      className="inline-flex min-h-8 items-center gap-1.5 rounded-md border border-border bg-surface-2 px-2 text-xs hover:border-border-strong hover:bg-surface-3"
                    >
                      <span className="num text-muted">{c}</span>
                      <span className="max-w-40 truncate">{names.get(c) ?? ""}</span>
                    </button>
                  ))}
                  {d.children.length > 100 && <Badge>+{d.children.length - 100}</Badge>}
                </div>
              )}
            </Section>
            {d.cgroup && (
              <Section title="Control group">
                <code className="num block whitespace-pre-wrap break-all py-1 text-xs text-muted">{d.cgroup}</code>
              </Section>
            )}
          </div>
        )}
      </Sheet>
      {confirm && (
        <ConfirmDialog
          open
          onOpenChange={(o) => !o && setConfirm(null)}
          title={`${signalInfo[confirm].label} ${d?.name ?? `PID ${pid}`}?`}
          description={signalInfo[confirm].desc}
          confirmLabel={`Send SIG${confirm}`}
          danger={signalInfo[confirm].danger}
          loading={sig.isPending}
          onConfirm={() => sig.mutate(confirm)}
        >
          <p className="num text-xs text-muted">
            PID {pid} · {d?.user}
            {foreign ? " · requires administrator approval" : ""}
          </p>
        </ConfirmDialog>
      )}
    </>
  );
}
