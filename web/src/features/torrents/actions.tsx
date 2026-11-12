// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState, type ReactNode } from "react";
import { api, ApiError } from "@/lib/api";
import { cn } from "@/lib/cn";
import { Dialog } from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Tooltip } from "@/components/ui/menu";
import { toast } from "@/components/ui/toast";
import { copyText } from "@/lib/clipboard";
import { downloadBlob } from "@/lib/download";
import { errorMessage } from "./api";

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

// Results known to only offer magnets; kept for the page's lifetime so
// the hint survives navigating away and back.
const magnetOnlyIds: Record<string, boolean> = {};

/**
 * Per-result actions with per-row busy state; shared by table and cards.
 * Result IDs live on the server for a limited time; when one has expired
 * `refresh` re-runs the search (IDs are stable) and the action is retried
 * once.
 */
export function useResultActions(refresh?: () => Promise<unknown>) {
  const [busy, setBusy] = useState<Record<string, RowAction | undefined>>({});
  const [magnetOnly, setMagnetOnlyState] = useState<Record<string, boolean>>(() => ({ ...magnetOnlyIds }));
  const setMagnetOnly = (fn: (m: Record<string, boolean>) => Record<string, boolean>) =>
    setMagnetOnlyState((m) => {
      const next = fn(m);
      Object.assign(magnetOnlyIds, next);
      return next;
    });
  const retrying = async <T,>(call: () => Promise<T>): Promise<T> => {
    try {
      return await call();
    } catch (e) {
      if (!(e instanceof ApiError && e.code === "result_expired") || !refresh) throw e;
      await refresh();
      return call();
    }
  };
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
        const { magnet } = await retrying(() => api.get<{ magnet: string }>(`/api/prowlarr/results/${encodeURIComponent(id)}/magnet`));
        if (await copyText(magnet)) toast.ok("Magnet link copied");
        else setDialog({ magnet, title });
      } catch (e) {
        toast.error("Could not get magnet link", errorMessage(e));
      }
    });

  const downloadTorrent = (id: string, title: string) =>
    run(id, "torrent", async () => {
      try {
        const res = await retrying(() => api.request<Response>(`/api/prowlarr/results/${encodeURIComponent(id)}/torrent`, { response: true }));
        const blob = await res.blob();
        const cd = res.headers.get("Content-Disposition") ?? "";
        const name = /filename="([^"]+)"/.exec(cd)?.[1] ?? "download.torrent";
        downloadBlob(blob, name);
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
        const r = await retrying(() => api.post<{ via: "prowlarr" | "transmission" }>(`/api/prowlarr/results/${encodeURIComponent(id)}/queue`));
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
