// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useId, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Box } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { Dialog } from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { FormField, Input, SearchInput } from "@/components/ui/form";
import { EmptyState, Spinner, StatusDot } from "@/components/ui/misc";

interface ContainerLite {
  id: string;
  name: string;
  image?: string;
  state: string;
  status?: string;
}

const NAME_RE = /^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$/;

/** Picks a running container to exec into (or accepts a typed name). */
export function ContainerPicker({
  open,
  onOpenChange,
  onPick,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  onPick: (target: string) => void;
}) {
  const [q, setQ] = useState("");
  const [manual, setManual] = useState("");
  const manualId = useId();
  const { data, error, isLoading } = useQuery({
    queryKey: ["containers"],
    queryFn: () => api.get<ContainerLite[]>("/api/containers"),
    enabled: open,
    retry: false,
    staleTime: 5_000,
  });
  useEffect(() => {
    if (open) {
      setQ("");
      setManual("");
    }
  }, [open]);
  const running = useMemo(() => {
    const list = Array.isArray(data) ? data.filter((c) => c.state === "running") : [];
    const f = q.trim().toLowerCase();
    return list
      .filter((c) => !f || c.name.toLowerCase().includes(f) || c.image?.toLowerCase().includes(f) || c.id.startsWith(f))
      .sort((a, b) => a.name.localeCompare(b.name));
  }, [data, q]);
  const unavailable = error instanceof ApiError && (error.status === 404 || error.status === 503);
  const manualOk = NAME_RE.test(manual);

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title="Container shell"
      description="Opens bash (or sh) inside a running container with docker exec."
      size="md"
    >
      <div className="flex flex-col gap-3">
        {!unavailable && !error && <SearchInput value={q} onChange={setQ} placeholder="Filter containers…" autoFocus />}
        <div className="-mx-1 max-h-[min(50dvh,22rem)] overflow-auto">
          {isLoading ? (
            <div className="flex h-24 items-center justify-center text-muted">
              <Spinner />
            </div>
          ) : error ? (
            <p className="px-1 py-3 text-sm text-muted">
              {unavailable ? "Container listing is not available." : `Could not list containers: ${(error as Error).message}`} You can still type a
              container name below.
            </p>
          ) : running.length === 0 ? (
            <EmptyState icon={<Box />} title={q ? "No matches" : "No running containers"} className="py-6" />
          ) : (
            <ul className="flex flex-col" aria-label="Running containers">
              {running.map((c) => (
                <li key={c.id}>
                  <button
                    type="button"
                    onClick={() => onPick(c.name || c.id)}
                    className="flex w-full items-center gap-2.5 rounded-md px-2 py-2 text-left hover:bg-surface-3 focus-visible:bg-surface-3"
                  >
                    <StatusDot tone="ok" />
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-sm font-medium">{c.name}</span>
                      <span className="num block truncate text-xs text-muted">{c.image ?? c.id.slice(0, 12)}</span>
                    </span>
                    <span className="num shrink-0 text-2xs text-subtle max-sm:hidden">{c.id.slice(0, 12)}</span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
        <form
          className="flex items-end gap-2 border-t border-border pt-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (manualOk) onPick(manual);
          }}
        >
          <FormField
            label="Name or ID"
            htmlFor={manualId}
            className="flex-1"
            error={manual && !manualOk ? "Letters, digits, '_', '.', '-' only" : undefined}
          >
            <Input id={manualId} value={manual} onChange={(e) => setManual(e.target.value.trim())} placeholder="e.g. jellyfin" className="num" autoComplete="off" spellCheck={false} />
          </FormField>
          <Button type="submit" variant="primary" disabled={!manualOk} className={manual && !manualOk ? "mb-5" : undefined}>
            Open
          </Button>
        </form>
      </div>
    </Dialog>
  );
}

export function RenameDialog({
  open,
  onOpenChange,
  initial,
  onSave,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  initial: string;
  onSave: (name: string) => void;
}) {
  const [v, setV] = useState(initial);
  const id = useId();
  useEffect(() => {
    if (open) setV(initial);
  }, [open, initial]);
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title="Rename tab"
      size="sm"
      footer={
        <>
          <Button onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button variant="primary" type="submit" form={id + "f"}>
            Save
          </Button>
        </>
      }
    >
      <form
        id={id + "f"}
        onSubmit={(e) => {
          e.preventDefault();
          onSave(v.trim().slice(0, 60));
        }}
      >
        <FormField label="Name" htmlFor={id} hint="Leave empty to use the shell's title.">
          <Input id={id} value={v} onChange={(e) => setV(e.target.value)} autoFocus maxLength={60} />
        </FormField>
      </form>
    </Dialog>
  );
}

/** Fallback when the async clipboard API is unavailable (e.g. plain HTTP). */
export function PasteDialog({
  open,
  onOpenChange,
  onPaste,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  onPaste: (text: string) => void;
}) {
  const [v, setV] = useState("");
  const id = useId();
  useEffect(() => {
    if (open) setV("");
  }, [open]);
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title="Paste into terminal"
      description="Clipboard access isn't available here. Paste the text below (Ctrl+V or long-press)."
      size="md"
      footer={
        <>
          <Button onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button variant="primary" disabled={!v} onClick={() => onPaste(v)}>
            Send to terminal
          </Button>
        </>
      }
    >
      <label htmlFor={id} className="sr-only">
        Text to paste
      </label>
      <textarea
        id={id}
        autoFocus
        value={v}
        onChange={(e) => setV(e.target.value)}
        rows={6}
        spellCheck={false}
        className="num w-full resize-y rounded-md border border-border bg-surface-2 p-2.5 text-ui text-fg focus:border-focus focus:outline-none focus:ring-2 focus:ring-focus/25"
      />
    </Dialog>
  );
}
