// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronRight, Copy, CornerLeftUp, Folder, FolderOpen, HardDrive, Trash2 } from "lucide-react";
import { api, ApiError, qs } from "@/lib/api";
import { bytes, datetime } from "@/lib/format";
import { cn } from "@/lib/cn";
import { copyWithToast } from "@/lib/clipboard";
import { Dialog, Sheet } from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Checkbox, FormField, Input, Segmented, Switch } from "@/components/ui/form";
import { ErrorNote, Notice, Spinner } from "@/components/ui/misc";
import { toast } from "@/components/ui/toast";
import { absPath, baseName, fileApi, joinPath, parentPath, segments, type Entry, type Listing, type Root, type RootId } from "./api";
import { FileIcon, typeLabel } from "./kinds";

function errMsg(e: unknown) {
  return e instanceof Error ? e.message : String(e);
}

// ---- name prompt (new folder / new file / rename) ------------------------------

export function NameDialog({
  open,
  onOpenChange,
  title,
  label,
  initial = "",
  confirmLabel,
  onSubmit,
  selectStem,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  title: string;
  label: string;
  initial?: string;
  confirmLabel: string;
  onSubmit: (name: string) => Promise<unknown>;
  selectStem?: boolean;
}) {
  const [name, setName] = useState(initial);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (!open) return;
    setName(initial);
    setError(null);
    const t = window.setTimeout(() => {
      const el = ref.current;
      if (!el) return;
      el.focus();
      const dot = initial.lastIndexOf(".");
      if (selectStem && dot > 0) el.setSelectionRange(0, dot);
      else el.select();
    }, 30);
    return () => window.clearTimeout(t);
  }, [open, initial, selectStem]);
  const invalid = !name.trim() || name.includes("/") || name === "." || name === "..";
  const submit = async () => {
    if (invalid) return;
    setBusy(true);
    setError(null);
    try {
      await onSubmit(name.trim());
      onOpenChange(false);
    } catch (e) {
      if (!(e instanceof ApiError && e.code === "elevation_canceled")) setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={title}
      size="sm"
      footer={
        <>
          <Button onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button variant="primary" loading={busy} disabled={invalid} onClick={submit}>
            {confirmLabel}
          </Button>
        </>
      }
    >
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
        className="space-y-3"
      >
        <FormField label={label} htmlFor="files-name" error={name.includes("/") ? 'Names cannot contain "/"' : undefined}>
          <Input id="files-name" ref={ref} value={name} onChange={(e) => setName(e.target.value)} autoComplete="off" spellCheck={false} className="num" />
        </FormField>
        <ErrorNote error={error} />
      </form>
    </Dialog>
  );
}

// ---- delete ------------------------------------------------------------------

export function DeleteDialog({
  open,
  onOpenChange,
  root,
  paths,
  trashAvailable,
  onDone,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  root: RootId;
  paths: string[];
  trashAvailable: boolean;
  onDone: () => void;
}) {
  const [trash, setTrash] = useState(trashAvailable);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [noTrash, setNoTrash] = useState<string | null>(null);
  useEffect(() => {
    if (open) {
      setTrash(trashAvailable);
      setError(null);
      setNoTrash(null);
    }
  }, [open, trashAvailable]);
  const run = async (useTrash: boolean) => {
    setBusy(true);
    setError(null);
    try {
      const r = await fileApi.remove(root, paths, useTrash);
      toast.ok(useTrash ? "Moved to trash" : "Deleted", `${r.deleted} item${r.deleted === 1 ? "" : "s"}`);
      onOpenChange(false);
      onDone();
    } catch (e) {
      if (e instanceof ApiError && e.code === "trash_unavailable") {
        setNoTrash(e.message);
        setTrash(false);
      } else if (!(e instanceof ApiError && e.code === "elevation_canceled")) setError(e);
    } finally {
      setBusy(false);
    }
  };
  const label = paths.length === 1 ? `“${baseName(paths[0])}”` : `${paths.length} items`;
  const permanent = !trash || !!noTrash;
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={permanent ? `Delete ${label} permanently?` : `Move ${label} to trash?`}
      description={permanent ? "This cannot be undone." : "Items are moved to Wargo's trash folder on the same disk."}
      size="sm"
      footer={
        <>
          <Button autoFocus onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button variant="danger" loading={busy} onClick={() => run(!permanent)} icon={<Trash2 />}>
            {permanent ? "Delete permanently" : "Move to trash"}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <ul className="num max-h-40 space-y-0.5 overflow-auto rounded-md border border-border bg-surface-2 px-2.5 py-2 text-xs text-muted">
          {paths.slice(0, 50).map((p) => (
            <li key={p} className="truncate">
              {p}
            </li>
          ))}
          {paths.length > 50 && <li>… and {paths.length - 50} more</li>}
        </ul>
        {noTrash ? (
          <Notice tone="warn">{noTrash}</Notice>
        ) : (
          trashAvailable && <Checkbox checked={trash} onCheckedChange={setTrash} label="Move to trash instead of deleting" />
        )}
        <ErrorNote error={error} />
      </div>
    </Dialog>
  );
}

// ---- permissions -------------------------------------------------------------

const who = ["Owner", "Group", "Others"] as const;
const bitsLabel = ["Read", "Write", "Execute"] as const;

function parseOctal(s: string): number | null {
  if (!/^[0-7]{3,4}$/.test(s)) return null;
  return parseInt(s, 8);
}

function toOctal(v: number) {
  return v.toString(8).padStart(4, "0");
}

export function ChmodDialog({ open, onOpenChange, root, entries, dir, onDone }: { open: boolean; onOpenChange: (o: boolean) => void; root: RootId; entries: Entry[]; dir: string; onDone: () => void }) {
  const first = entries[0];
  const [text, setText] = useState("0644");
  const [recursive, setRecursive] = useState(false);
  const [dirText, setDirText] = useState("0755");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const hasDir = entries.some((e) => e.type === "dir");
  useEffect(() => {
    if (open && first) {
      setText(first.octal);
      setRecursive(false);
      setError(null);
      const v = parseInt(first.octal, 8);
      setDirText(toOctal(first.type === "dir" ? v : v | ((v & 0o444) >> 2)));
    }
  }, [open, first]);
  const value = parseOctal(text);
  const toggle = (bit: number, on: boolean) => {
    if (value === null) return;
    setText(toOctal(on ? value | bit : value & ~bit));
  };
  const submit = async () => {
    if (value === null) return;
    setBusy(true);
    setError(null);
    try {
      await fileApi.chmod(
        root,
        entries.map((e) => joinPath(dir, e.name)),
        text,
        recursive,
        recursive ? dirText : undefined,
      );
      toast.ok("Permissions updated");
      onOpenChange(false);
      onDone();
    } catch (e) {
      if (!(e instanceof ApiError && e.code === "elevation_canceled")) setError(e);
    } finally {
      setBusy(false);
    }
  };
  if (!first) return null;
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title="Permissions"
      description={entries.length === 1 ? first.name : `${entries.length} items`}
      size="sm"
      footer={
        <>
          <Button onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button variant="primary" loading={busy} disabled={value === null || (recursive && parseOctal(dirText) === null)} onClick={submit}>
            Apply
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <table className="w-full text-[0.8125rem]">
          <thead>
            <tr>
              <th className="sr-only">Class</th>
              {bitsLabel.map((b) => (
                <th key={b} className="pb-1.5 text-center text-2xs font-semibold uppercase tracking-wider text-subtle">
                  {b}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {who.map((w, wi) => (
              <tr key={w} className="border-t border-border">
                <th scope="row" className="py-2 text-left font-medium">
                  {w}
                </th>
                {bitsLabel.map((b, bi) => {
                  const bit = 1 << (8 - (wi * 3 + bi));
                  return (
                    <td key={b} className="py-2 text-center">
                      <Checkbox checked={value !== null && (value & bit) !== 0} onCheckedChange={(v) => toggle(bit, v)} aria-label={`${w} ${b}`} className="justify-center" />
                    </td>
                  );
                })}
              </tr>
            ))}
          </tbody>
        </table>
        <div className="grid grid-cols-2 gap-3">
          <FormField label="Octal" htmlFor="chmod-octal" error={value === null ? "3–4 octal digits" : undefined}>
            <Input id="chmod-octal" value={text} onChange={(e) => setText(e.target.value.trim())} className="num" inputMode="numeric" maxLength={4} />
          </FormField>
          <div className="flex flex-col justify-end gap-1 pb-1.5 text-xs text-muted">
            <span className="num">{value !== null ? symbolic(value) : "—"}</span>
          </div>
        </div>
        {hasDir && <Switch checked={recursive} onCheckedChange={setRecursive} label="Apply to everything inside" description="Symlinks are left untouched." />}
        {recursive && (
          <FormField label="Mode for folders" htmlFor="chmod-dir" hint="Folders usually need execute (x) to be opened." error={parseOctal(dirText) === null ? "3–4 octal digits" : undefined}>
            <Input id="chmod-dir" value={dirText} onChange={(e) => setDirText(e.target.value.trim())} className="num" maxLength={4} />
          </FormField>
        )}
        <ErrorNote error={error} />
      </div>
    </Dialog>
  );
}

function symbolic(v: number) {
  const r = "rwxrwxrwx";
  let s = "";
  for (let i = 0; i < 9; i++) s += v & (1 << (8 - i)) ? r[i] : "-";
  return s;
}

export function ChownDialog({ open, onOpenChange, root, entries, dir, onDone }: { open: boolean; onOpenChange: (o: boolean) => void; root: RootId; entries: Entry[]; dir: string; onDone: () => void }) {
  const first = entries[0];
  const [owner, setOwner] = useState("");
  const [recursive, setRecursive] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    if (open && first) {
      setOwner(`${first.owner}:${first.group}`);
      setRecursive(false);
      setError(null);
    }
  }, [open, first]);
  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      await fileApi.chown(
        root,
        entries.map((e) => joinPath(dir, e.name)),
        owner.trim(),
        recursive,
      );
      toast.ok("Owner updated");
      onOpenChange(false);
      onDone();
    } catch (e) {
      if (!(e instanceof ApiError && e.code === "elevation_canceled")) setError(e);
    } finally {
      setBusy(false);
    }
  };
  if (!first) return null;
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title="Change owner"
      description={entries.length === 1 ? first.name : `${entries.length} items`}
      size="sm"
      footer={
        <>
          <Button onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button variant="primary" loading={busy} disabled={!owner.trim()} onClick={submit}>
            Apply
          </Button>
        </>
      }
    >
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        <FormField label="Owner" htmlFor="chown-owner" hint="user, user:group or :group — names or numeric ids.">
          <Input id="chown-owner" value={owner} onChange={(e) => setOwner(e.target.value)} className="num" autoComplete="off" spellCheck={false} />
        </FormField>
        {entries.some((e) => e.type === "dir") && <Switch checked={recursive} onCheckedChange={setRecursive} label="Apply to everything inside" />}
        <Notice tone="frost">Changing ownership usually requires administrator approval.</Notice>
        <ErrorNote error={error} />
      </form>
    </Dialog>
  );
}

// ---- compress / extract ------------------------------------------------------

export function CompressDialog({
  open,
  onOpenChange,
  root,
  dir,
  entries,
  onJob,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  root: RootId;
  dir: string;
  entries: Entry[];
  onJob: (id: string) => void;
}) {
  const [format, setFormat] = useState<"zip" | "tar.gz">("zip");
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const stem = entries.length === 1 ? entries[0].name : dir === "." ? "Archive" : baseName(dir);
  useEffect(() => {
    if (open) {
      setName(stem);
      setError(null);
    }
  }, [open, stem]);
  const suffix = format === "zip" ? ".zip" : ".tar.gz";
  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      const r = await fileApi.archive(
        root,
        entries.map((e) => joinPath(dir, e.name)),
        joinPath(dir, name.trim() + suffix),
        format,
      );
      onJob(r.jobId);
      onOpenChange(false);
    } catch (e) {
      if (!(e instanceof ApiError && e.code === "elevation_canceled")) setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title="Compress"
      description={entries.length === 1 ? entries[0].name : `${entries.length} items`}
      size="sm"
      footer={
        <>
          <Button onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button variant="primary" loading={busy} disabled={!name.trim() || name.includes("/")} onClick={submit}>
            Create archive
          </Button>
        </>
      }
    >
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        <div className="flex flex-col gap-1">
          <span className="text-[0.8125rem] font-medium">Format</span>
          <Segmented
            label="Archive format"
            value={format}
            onChange={setFormat}
            options={[
              { value: "zip", label: "ZIP" },
              { value: "tar.gz", label: "TAR.GZ" },
            ]}
          />
        </div>
        <FormField label="Archive name" htmlFor="archive-name">
          <div className="flex items-center gap-2">
            <Input id="archive-name" value={name} onChange={(e) => setName(e.target.value)} className="num" autoComplete="off" />
            <span className="num shrink-0 text-sm text-muted">{suffix}</span>
          </div>
        </FormField>
        <ErrorNote error={error} />
      </form>
    </Dialog>
  );
}

export function ExtractDialog({ open, onOpenChange, root, dir, entry, onJob }: { open: boolean; onOpenChange: (o: boolean) => void; root: RootId; dir: string; entry?: Entry; onJob: (id: string) => void }) {
  const [name, setName] = useState("");
  const [here, setHere] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const stem = entry ? entry.name.replace(/(\.tar\.gz|\.tgz|\.tar|\.zip)$/i, "") : "";
  useEffect(() => {
    if (open) {
      setName(stem);
      setHere(false);
      setError(null);
    }
  }, [open, stem]);
  if (!entry) return null;
  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      const r = await fileApi.extract(root, joinPath(dir, entry.name), here ? dir : joinPath(dir, name.trim()));
      onJob(r.jobId);
      onOpenChange(false);
    } catch (e) {
      if (!(e instanceof ApiError && e.code === "elevation_canceled")) setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title="Extract archive"
      description={entry.name}
      size="sm"
      footer={
        <>
          <Button onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button variant="primary" loading={busy} disabled={!here && (!name.trim() || name.includes("/"))} onClick={submit}>
            Extract
          </Button>
        </>
      }
    >
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        <Switch checked={here} onCheckedChange={setHere} label="Extract into the current folder" description="Existing files are never overwritten." />
        {!here && (
          <FormField label="Into new folder" htmlFor="extract-name">
            <Input id="extract-name" value={name} onChange={(e) => setName(e.target.value)} className="num" autoComplete="off" />
          </FormField>
        )}
        <p className="text-xs text-muted">Links and unsafe paths inside the archive are skipped. Extraction stops at 20 GB.</p>
        <ErrorNote error={error} />
      </form>
    </Dialog>
  );
}

// ---- folder picker (copy / move) ---------------------------------------------

export function FolderPicker({
  open,
  onOpenChange,
  root,
  rootInfo,
  start,
  title,
  confirmLabel,
  sources,
  onPick,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  root: RootId;
  rootInfo?: Root;
  start: string;
  title: string;
  confirmLabel: string;
  sources: string[];
  onPick: (dest: string) => Promise<void>;
}) {
  const [cwd, setCwd] = useState(start);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    if (open) {
      setCwd(start);
      setError(null);
    }
  }, [open, start]);
  const q = useQuery({
    queryKey: ["files", "list", root, cwd, false, "dirs"],
    queryFn: ({ signal }) => api.get<Listing>(`/api/files/list${qs({ root, path: cwd, dirsOnly: 1 })}`, signal),
    enabled: open,
  });
  const blocked = (p: string) => sources.some((s) => p === s || p.startsWith(s + "/"));
  const invalid = blocked(cwd);
  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      await onPick(cwd);
      onOpenChange(false);
    } catch (e) {
      if (!(e instanceof ApiError && e.code === "elevation_canceled")) setError(e);
    } finally {
      setBusy(false);
    }
  };
  const segs = segments(cwd);
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={title}
      description={`Choose a destination in ${rootInfo?.name ?? "this root"}`}
      footer={
        <>
          <span className="num mr-auto min-w-0 truncate text-xs text-muted">{absPath(rootInfo, cwd)}</span>
          <Button onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button variant="primary" loading={busy} disabled={invalid} onClick={submit}>
            {confirmLabel}
          </Button>
        </>
      }
    >
      <nav aria-label="Destination path" className="mb-2 flex flex-wrap items-center gap-0.5 text-[0.8125rem]">
        <button type="button" className="flex h-7 items-center gap-1 rounded px-1.5 font-medium hover:bg-surface-3" onClick={() => setCwd(".")}>
          <HardDrive className="size-3.5 text-subtle" />
          {rootInfo?.name ?? "Root"}
        </button>
        {segs.map((s, i) => (
          <span key={i} className="flex items-center">
            <ChevronRight className="size-3.5 text-subtle" />
            <button type="button" className="h-7 max-w-[12rem] truncate rounded px-1.5 hover:bg-surface-3" onClick={() => setCwd(segs.slice(0, i + 1).join("/"))}>
              {s}
            </button>
          </span>
        ))}
      </nav>
      <div className="min-h-48 rounded-md border border-border">
        {cwd !== "." && (
          <button type="button" onClick={() => setCwd(parentPath(cwd))} className="flex h-9 w-full items-center gap-2 border-b border-border px-3 text-left text-[0.8125rem] text-muted hover:bg-surface-2">
            <CornerLeftUp className="size-4" /> Up one level
          </button>
        )}
        {q.isLoading && (
          <div className="flex h-32 items-center justify-center text-muted">
            <Spinner />
          </div>
        )}
        {q.error && <ErrorNote error={q.error} className="m-2" />}
        {q.data?.entries.length === 0 && <p className="px-3 py-6 text-center text-sm text-muted">No folders here</p>}
        <ul>
          {q.data?.entries.map((e) => {
            const p = joinPath(cwd, e.name);
            const dis = blocked(p);
            return (
              <li key={e.name}>
                <button
                  type="button"
                  disabled={dis}
                  onClick={() => setCwd(p)}
                  className="flex h-9 w-full items-center gap-2 px-3 text-left text-[0.8125rem] hover:bg-surface-2 disabled:opacity-40"
                >
                  <Folder className="size-4 text-frost" fill="currentColor" fillOpacity={0.16} />
                  <span className="min-w-0 flex-1 truncate">{e.name}</span>
                  <ChevronRight className="size-3.5 text-subtle" />
                </button>
              </li>
            );
          })}
        </ul>
      </div>
      {invalid && <p className="mt-2 text-xs text-crit">A folder cannot be copied or moved into itself.</p>}
      <ErrorNote error={error} className="mt-2" />
    </Dialog>
  );
}

// ---- conflicts ---------------------------------------------------------------

export function ConflictDialog({ state, onResolve }: { state: { count: number; names: string[] } | null; onResolve: (choice: "rename" | "skip" | null) => void }) {
  return (
    <Dialog
      open={!!state}
      onOpenChange={(o) => !o && onResolve(null)}
      title="Items already exist"
      description={state ? `${state.count} item${state.count === 1 ? "" : "s"} with the same name already exist in the destination.` : undefined}
      size="sm"
      footer={
        <>
          <Button autoFocus onClick={() => onResolve(null)}>
            Cancel
          </Button>
          <Button onClick={() => onResolve("skip")}>Skip existing</Button>
          <Button variant="primary" onClick={() => onResolve("rename")}>
            Keep both
          </Button>
        </>
      }
    >
      <ul className="num max-h-40 space-y-0.5 overflow-auto text-xs text-muted">
        {state?.names.slice(0, 20).map((n) => (
          <li key={n} className="truncate">
            {n}
          </li>
        ))}
      </ul>
    </Dialog>
  );
}

// ---- properties --------------------------------------------------------------

function Row({ label, children, mono = true }: { label: string; children: React.ReactNode; mono?: boolean }) {
  return (
    <div className="grid grid-cols-[7rem_1fr] gap-3 border-b border-border px-4 py-2.5 text-[0.8125rem] last:border-0">
      <dt className="text-muted">{label}</dt>
      <dd className={cn("min-w-0 break-all", mono && "num")}>{children}</dd>
    </div>
  );
}

export function PropertiesSheet({ open, onOpenChange, root, rootInfo, dir, entry }: { open: boolean; onOpenChange: (o: boolean) => void; root: RootId; rootInfo?: Root; dir: string; entry?: Entry }) {
  const p = entry ? (entry.name === "." ? dir : joinPath(dir, entry.name)) : dir;
  const isDir = entry?.type === "dir" || entry?.targetType === "dir";
  const size = useQuery({
    queryKey: ["files", "size", root, p],
    queryFn: () => fileApi.size(root, p),
    enabled: open && !!entry && isDir,
    staleTime: 30_000,
    retry: false,
  });
  const full = absPath(rootInfo, p);
  const copy = (s: string) => void copyWithToast(s);
  const rows = useMemo(() => entry, [entry]);
  if (!rows) return null;
  const e = rows;
  return (
    <Sheet open={open} onOpenChange={onOpenChange} title={e.name} description={typeLabel(e)} width="max-w-md">
      <div className="flex items-center gap-3 border-b border-border px-4 py-4">
        <div className="flex size-11 items-center justify-center rounded-md border border-border bg-surface-2">
          <FileIcon entry={e} className="size-6" />
        </div>
        <div className="min-w-0">
          <div className="truncate font-medium">{e.name}</div>
          <div className="num text-xs text-muted">{isDir ? (size.data ? bytes(size.data.size) : "Folder") : bytes(e.size)}</div>
        </div>
      </div>
      <dl>
        <Row label="Location">
          <span className="flex items-start gap-2">
            <span className="min-w-0 flex-1">{full}</span>
            <button type="button" onClick={() => copy(full)} aria-label="Copy full path" title="Copy full path" className="-m-1 shrink-0 rounded p-1 text-subtle hover:bg-surface-3 hover:text-fg">
              <Copy className="size-3.5" />
            </button>
          </span>
        </Row>
        <Row label="Size">
          {isDir ? (
            size.isLoading ? (
              <span className="inline-flex items-center gap-2 text-muted">
                <Spinner className="size-3" /> Calculating…
              </span>
            ) : size.data ? (
              <>
                {bytes(size.data.size)} <span className="text-muted">({size.data.size.toLocaleString()} B)</span> · {size.data.files.toLocaleString()} files
                {size.data.partial && <span className="text-warn"> · partial (timed out)</span>}
              </>
            ) : (
              <span className="text-crit">{size.error ? errMsg(size.error) : "—"}</span>
            )
          ) : (
            <>
              {bytes(e.size)} <span className="text-muted">({e.size.toLocaleString()} B)</span>
            </>
          )}
        </Row>
        <Row label="Modified">{datetime(e.mtime)}</Row>
        <Row label="Permissions">
          {e.mode} <span className="text-muted">({e.octal})</span>
        </Row>
        <Row label="Owner">
          {e.owner}:{e.group} <span className="text-muted">({e.uid}:{e.gid})</span>
        </Row>
        <Row label="Access" mono={false}>
          {e.readable ? "Readable" : "Not readable"} · {e.writable ? "writable" : "read-only"} for you
        </Row>
        {e.type === "symlink" && (
          <Row label="Link target">
            {e.target}
            {e.targetType === "outside" && <span className="mt-1 block font-sans text-xs text-warn">Points outside this root — not followed.</span>}
            {e.targetType === "broken" && <span className="mt-1 block font-sans text-xs text-crit">Target does not exist.</span>}
          </Row>
        )}
        {e.mime && <Row label="Type">{e.mime}</Row>}
      </dl>
      {isDir && (
        <div className="px-4 py-3">
          <Button size="sm" icon={<FolderOpen />} onClick={() => size.refetch()} loading={size.isFetching}>
            Recalculate size
          </Button>
        </div>
      )}
    </Sheet>
  );
}
