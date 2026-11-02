// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState, type ReactNode } from "react";
import { Popover } from "radix-ui";
import { Link } from "react-router";
import { Check, ChevronDown } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { cn } from "@/lib/cn";
import { Dialog } from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/form";
import { Tooltip } from "@/components/ui/menu";
import { toast } from "@/components/ui/toast";
import { copyText, errorMessage, type IndexerRef } from "./api";

/** Readonly, pre-selected magnet for manual copying on insecure origins. */
export function MagnetDialog({ magnet, title, onClose }: { magnet: string | null; title?: string; onClose: () => void }) {
  const ref = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    if (!magnet) return;
    const t = window.setTimeout(() => {
      ref.current?.focus();
      ref.current?.select();
    }, 50);
    return () => window.clearTimeout(t);
  }, [magnet]);
  return (
    <Dialog
      open={!!magnet}
      onOpenChange={(o) => !o && onClose()}
      title="Copy magnet link"
      description={
        <>
          Automatic copying needs HTTPS. The link is selected — press <kbd className="num">Ctrl+C</kbd> (or long-press on mobile).
        </>
      }
      footer={<Button onClick={onClose}>Done</Button>}
    >
      {title && <p className="mb-2 break-words text-sm font-medium">{title}</p>}
      <label htmlFor="magnet-text" className="sr-only">
        Magnet link
      </label>
      <textarea
        id="magnet-text"
        ref={ref}
        readOnly
        value={magnet ?? ""}
        onFocus={(e) => e.currentTarget.select()}
        rows={5}
        className="num w-full resize-none break-all rounded-md border border-border bg-surface-2 p-2 text-xs text-fg focus:border-focus focus:outline-none focus:ring-2 focus:ring-focus/25"
      />
    </Dialog>
  );
}

export type RowAction = "magnet" | "torrent" | "queue";

/** Per-result actions with per-row busy state; shared by table and cards. */
export function useResultActions() {
  const [busy, setBusy] = useState<Record<string, RowAction | undefined>>({});
  const [magnetOnly, setMagnetOnly] = useState<Record<string, boolean>>({});
  const [dialog, setDialog] = useState<{
    magnet: string;
    title: string;
  } | null>(null);

  const run = async (id: string, kind: RowAction, fn: () => Promise<void>) => {
    setBusy((b) => ({ ...b, [id]: kind }));
    try {
      await fn();
    } finally {
      setBusy((b) => ({ ...b, [id]: undefined }));
    }
  };

  const copyMagnet = (id: string, title: string) =>
    run(id, "magnet", async () => {
      try {
        const { magnet } = await api.get<{ magnet: string }>(`/api/prowlarr/results/${encodeURIComponent(id)}/magnet`);
        if (await copyText(magnet)) toast.ok("Magnet link copied");
        else setDialog({ magnet, title });
      } catch (e) {
        toast.error("Could not get magnet link", errorMessage(e));
      }
    });

  const downloadTorrent = (id: string, title: string) =>
    run(id, "torrent", async () => {
      try {
        const res = await api.request<Response>(`/api/prowlarr/results/${encodeURIComponent(id)}/torrent`, { response: true });
        const blob = await res.blob();
        const cd = res.headers.get("Content-Disposition") ?? "";
        const name = /filename="([^"]+)"/.exec(cd)?.[1] ?? "download.torrent";
        const url = URL.createObjectURL(blob);
        const a = document.createElement("a");
        a.href = url;
        a.download = name;
        document.body.appendChild(a);
        a.click();
        a.remove();
        window.setTimeout(() => URL.revokeObjectURL(url), 10_000);
      } catch (e) {
        if (e instanceof ApiError && e.code === "magnet_only") {
          setMagnetOnly((m) => ({ ...m, [id]: true }));
          toast.info("No .torrent file available", `"${title}" only provides a magnet link — use Copy magnet instead.`);
        } else toast.error("Download failed", errorMessage(e));
      }
    });

  const queue = (id: string, title: string) =>
    run(id, "queue", async () => {
      try {
        const r = await api.post<{ via: "prowlarr" | "transmission" }>(`/api/prowlarr/results/${encodeURIComponent(id)}/queue`);
        toast.ok("Queued in Transmission", r.via === "prowlarr" ? `Sent via Prowlarr: ${title}` : title);
      } catch (e) {
        toast.error("Could not queue torrent", errorMessage(e));
      }
    });

  return {
    busy,
    magnetOnly,
    copyMagnet,
    downloadTorrent,
    queue,
    dialog: <MagnetDialog magnet={dialog?.magnet ?? null} title={dialog?.title} onClose={() => setDialog(null)} />,
  };
}

/** Wraps a possibly-disabled control so its tooltip still works. */
export function WithReason({ reason, children, className }: { reason?: string; children: ReactNode; className?: string }) {
  if (!reason) return <>{children}</>;
  return (
    <Tooltip content={reason}>
      <span tabIndex={0} aria-label={reason} className={cn("inline-flex rounded-md focus-visible:outline-2 focus-visible:outline-focus", className)}>
        {children}
      </span>
    </Tooltip>
  );
}

/** Indexer multi-select; empty selection means "all". */
export function IndexerPicker({ indexers, value, onChange }: { indexers: IndexerRef[]; value: number[]; onChange: (v: number[]) => void }) {
  const all = value.length === 0;
  const label = all ? "All indexers" : value.length === 1 ? (indexers.find((i) => i.id === value[0])?.name ?? "1 indexer") : `${value.length} indexers`;
  const toggle = (id: number, on: boolean) => {
    const base = all ? indexers.map((i) => i.id) : value;
    let next = on ? [...new Set([...base, id])] : base.filter((x) => x !== id);
    if (next.length === indexers.length) next = [];
    if (next.length === 0 && !on) return; // keep at least one
    onChange(next);
  };
  return (
    <Popover.Root>
      <Popover.Trigger asChild>
        <button
          type="button"
          aria-label={`Indexers: ${label}`}
          className="inline-flex h-8 min-w-0 items-center gap-1.5 rounded-md border border-border bg-surface-2 px-2.5 text-[0.8125rem] text-fg hover:border-border-strong focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus/25"
        >
          <span className="truncate">{label}</span>
          <ChevronDown className="size-3.5 shrink-0 text-subtle" />
        </button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content align="end" sideOffset={4} className="z-50 w-64 rounded-md border border-border-strong bg-surface p-1 shadow-xl focus:outline-none">
          <button
            type="button"
            onClick={() => onChange([])}
            className="flex h-8 w-full items-center gap-2 rounded px-2 text-left text-[0.8125rem] hover:bg-surface-3"
          >
            <Check className={cn("size-3.5", all ? "text-accent" : "invisible")} />
            All enabled indexers
          </button>
          <div className="my-1 h-px bg-border" />
          <div className="max-h-72 overflow-y-auto">
            {indexers.map((i) => (
              <div key={i.id} className="flex h-8 items-center rounded px-2 hover:bg-surface-3">
                <Checkbox checked={all || value.includes(i.id)} onCheckedChange={(on) => toggle(i.id, on)} label={i.name} className="w-full" />
              </div>
            ))}
            {indexers.length === 0 && <p className="px-2 py-1.5 text-xs text-muted">No enabled torrent indexers.</p>}
          </div>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

/** Router link styled as a small secondary button. */
export function LinkButton({ to, icon, children }: { to: string; icon?: ReactNode; children: ReactNode }) {
  return (
    <Link
      to={to}
      className="inline-flex h-7 items-center gap-1.5 rounded-md border border-border bg-surface-2 px-2.5 text-[0.8125rem] text-fg transition-colors hover:border-border-strong hover:bg-surface-3 max-md:h-8 [&_svg]:size-[1.1em]"
    >
      {icon}
      {children}
    </Link>
  );
}
