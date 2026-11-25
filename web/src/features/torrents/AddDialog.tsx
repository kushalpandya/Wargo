// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState, type DragEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Upload, X } from "lucide-react";
import { cn } from "@/lib/cn";
import { bytes } from "@/lib/format";
import { Button, IconButton } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { FormField, Input } from "@/components/ui/form";
import { toast } from "@/components/ui/toast";
import { addTorrent, errorMessage, torrentsKey } from "./api";
import { isMagnet, toBase64, torrentFileError } from "./add";

interface Picked {
  file: File;
  /** Set after a failed attempt to add it. */
  failure?: string;
}

const accept = ".torrent,application/x-bittorrent";

export function AddDialog({
  open,
  onOpenChange,
  initialFiles,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  initialFiles?: File[];
}) {
  const qc = useQueryClient();
  const [magnet, setMagnet] = useState("");
  const [magnetFailure, setMagnetFailure] = useState<string | null>(null);
  const [picked, setPicked] = useState<Picked[]>([]);
  const [busy, setBusy] = useState(false);
  const [over, setOver] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);

  // Start fresh each time the dialog opens, with any files that were dropped on the page.
  useEffect(() => {
    if (!open) return;
    setMagnet("");
    setMagnetFailure(null);
    setPicked((initialFiles ?? []).map((file) => ({ file })));
    setBusy(false);
    setOver(false);
  }, [open, initialFiles]);

  const addFiles = (list: Iterable<File>) => {
    const next = Array.from(list).map((file) => ({ file }));
    if (next.length) setPicked((p) => [...p, ...next]);
  };

  const onDrop = (e: DragEvent) => {
    e.preventDefault();
    setOver(false);
    addFiles(e.dataTransfer.files);
  };

  const trimmed = magnet.trim();
  const magnetBad = trimmed !== "" && !isMagnet(trimmed);
  const valid = picked.filter((p) => !torrentFileError(p.file));
  const invalid = picked.length - valid.length;
  const canAdd = !busy && !magnetBad && (trimmed !== "" || valid.length > 0);

  const submit = async () => {
    setBusy(true);
    setMagnetFailure(null);
    for (const p of picked) {
      const reason = torrentFileError(p.file);
      if (reason) toast.error("Could not add", `${p.file.name}: ${reason}`);
    }
    const jobs: { run: () => Promise<{ name: string; duplicate: boolean }>; file?: File }[] = [];
    if (trimmed) jobs.push({ run: () => addTorrent({ magnet: trimmed }) });
    for (const p of valid) {
      jobs.push({ file: p.file, run: async () => addTorrent({ torrentBase64: toBase64(await p.file.arrayBuffer()) }) });
    }
    const results = await Promise.allSettled(jobs.map((j) => j.run()));
    let failed = 0;
    const failures = new Map<File, string>();
    results.forEach((r, i) => {
      if (r.status === "fulfilled") {
        toast.ok(r.value.duplicate ? "Already in Transmission" : "Added", r.value.name);
      } else {
        failed++;
        const message = errorMessage(r.reason);
        toast.error("Could not add", message);
        const f = jobs[i].file;
        if (f) failures.set(f, message);
        else setMagnetFailure(message);
      }
    });
    qc.invalidateQueries({ queryKey: torrentsKey });
    setBusy(false);
    if (failed === 0 && invalid === 0) {
      onOpenChange(false);
      return;
    }
    // Keep what still needs attention: failed files and files that were never valid.
    setPicked((all) =>
      all
        .filter((p) => failures.has(p.file) || torrentFileError(p.file))
        .map((p) => (failures.has(p.file) ? { ...p, failure: failures.get(p.file) } : p)),
    );
    if (trimmed && results[0].status === "fulfilled") setMagnet("");
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => !busy && onOpenChange(o)}
      title="Add torrent"
      description="Paste a magnet link, choose .torrent files, or both."
      footer={
        <>
          <Button onClick={() => onOpenChange(false)} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" loading={busy} disabled={!canAdd} onClick={submit}>
            Add
          </Button>
        </>
      }
    >
      <form
        className="flex flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (canAdd) submit();
        }}
      >
        <FormField
          label="Magnet link"
          htmlFor="add-torrent-magnet"
          error={magnetBad ? "This is not a magnet link. It should start with magnet:?" : magnetFailure}
        >
          <Input
            id="add-torrent-magnet"
            value={magnet}
            onChange={(e) => setMagnet(e.target.value)}
            placeholder="magnet:?xt=urn:btih:…"
            autoComplete="off"
            spellCheck={false}
            aria-invalid={magnetBad || !!magnetFailure}
            disabled={busy}
          />
        </FormField>

        <div className="flex flex-col gap-2">
          <span className="text-ui font-medium text-fg" id="add-torrent-files-label">
            Torrent files
          </span>
          <button
            type="button"
            aria-labelledby="add-torrent-files-label"
            aria-describedby="add-torrent-files-hint"
            disabled={busy}
            onClick={() => fileInput.current?.click()}
            onDragOver={(e) => {
              e.preventDefault();
              setOver(true);
            }}
            onDragLeave={() => setOver(false)}
            onDrop={onDrop}
            className={cn(
              "flex min-h-20 w-full flex-col items-center justify-center gap-1 rounded-md border border-dashed border-border-strong px-3 py-4 text-center text-sm text-muted transition-colors",
              "hover:border-accent hover:text-fg focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus disabled:opacity-50",
              over && "border-accent bg-surface-2 text-fg",
            )}
          >
            <Upload className="size-5" aria-hidden />
            <span id="add-torrent-files-hint">Drop .torrent files here or choose files</span>
          </button>
          <input
            ref={fileInput}
            type="file"
            accept={accept}
            multiple
            hidden
            tabIndex={-1}
            onChange={(e) => {
              addFiles(e.target.files ?? []);
              e.target.value = "";
            }}
          />
          {picked.length > 0 && (
            <ul className="flex flex-col gap-1" aria-label="Chosen files">
              {picked.map((p, i) => {
                const problem = torrentFileError(p.file) ?? p.failure;
                return (
                  <li key={`${p.file.name}-${i}`} className="flex items-start gap-2 rounded-md border border-border bg-surface-2 px-3 py-1.5">
                    <div className="min-w-0 flex-1">
                      <p className="break-words text-sm [overflow-wrap:anywhere]">{p.file.name}</p>
                      <p className={cn("text-xs", problem ? "text-crit" : "text-muted")}>{problem ?? bytes(p.file.size)}</p>
                    </div>
                    <IconButton
                      size="sm"
                      label={`Remove ${p.file.name}`}
                      disabled={busy}
                      onClick={() => setPicked((all) => all.filter((_, j) => j !== i))}
                    >
                      <X />
                    </IconButton>
                  </li>
                );
              })}
            </ul>
          )}
        </div>
        {/* Lets Enter in the magnet field submit the form. */}
        <button type="submit" hidden tabIndex={-1} aria-hidden />
      </form>
    </Dialog>
  );
}
