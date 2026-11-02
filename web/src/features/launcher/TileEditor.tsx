// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowDown, ArrowUp, Boxes, Download, GripVertical, ImageOff, LayoutGrid, Pencil, Plus, Trash2, Upload } from "lucide-react";
import { api } from "@/lib/api";
import { cn } from "@/lib/cn";
import { Button, IconButton } from "@/components/ui/button";
import { ConfirmDialog, Dialog } from "@/components/ui/dialog";
import { Checkbox, FormField, Input, Switch } from "@/components/ui/form";
import { Badge, EmptyState, ErrorNote, Notice, Skeleton, Spinner, StatusDot } from "@/components/ui/misc";
import { toast } from "@/components/ui/toast";
import { TileIcon, type Tile } from "./Tiles";
import { fetchIcon, slugify, tilesKey, uploadIcon, useTiles, type Suggestion, type TileInput } from "./api";

const emptyInput: TileInput = { name: "", url: "", icon: "", group: "", newTab: true, healthUrl: "", enabled: true };

function toInput(t: Tile): TileInput {
  return { name: t.name, url: t.url, icon: t.icon ?? "", group: t.group ?? "", newTab: t.newTab, healthUrl: t.healthUrl ?? "", enabled: t.enabled };
}

function validHttpUrl(v: string) {
  try {
    const u = new URL(v);
    return (u.protocol === "http:" || u.protocol === "https:") && !!u.host;
  } catch {
    return false;
  }
}

/** Add/edit dialog for a single tile. */
export function TileDialog({
  open,
  onOpenChange,
  tile,
  groups,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  tile?: Tile | null;
  groups: string[];
}) {
  const qc = useQueryClient();
  const [v, setV] = useState<TileInput>(emptyInput);
  const [slug, setSlug] = useState("");
  const [slugTouched, setSlugTouched] = useState(false);
  const [touched, setTouched] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!open) return;
    setV(tile ? toInput(tile) : emptyInput);
    setSlug(tile ? slugify(tile.name) : "");
    setSlugTouched(false);
    setTouched(false);
  }, [open, tile]);

  const set = <K extends keyof TileInput>(k: K, val: TileInput[K]) => setV((p) => ({ ...p, [k]: val }));

  const upload = useMutation({
    mutationFn: uploadIcon,
    onSuccess: (icon) => set("icon", icon),
    onError: (e) => toast.error("Upload failed", e),
  });
  const fetchSlug = useMutation({
    mutationFn: () => fetchIcon(slug.trim()),
    onSuccess: (icon) => set("icon", icon),
  });
  const save = useMutation({
    mutationFn: (body: TileInput) => (tile ? api.put<Tile>(`/api/launcher/tiles/${tile.id}`, body) : api.post<Tile>("/api/launcher/tiles", body)),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: tilesKey });
      toast.ok(tile ? "App updated" : "App added");
      onOpenChange(false);
    },
  });

  const nameErr = touched && !v.name.trim() ? "Name is required" : undefined;
  const urlErr = touched && !validHttpUrl(v.url) ? "Enter a full http:// or https:// URL" : undefined;
  const healthErr = touched && v.healthUrl && !validHttpUrl(v.healthUrl) ? "Enter a full http:// or https:// URL" : undefined;
  const slugOk = /^[a-z0-9-]{1,64}$/.test(slug.trim());

  const submit = () => {
    setTouched(true);
    if (!v.name.trim() || !validHttpUrl(v.url) || (v.healthUrl && !validHttpUrl(v.healthUrl))) return;
    save.mutate({ ...v, name: v.name.trim(), group: v.group?.trim() });
  };

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={tile ? `Edit ${tile.name}` : "Add app"}
      size="md"
      footer={
        <>
          <Button onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button variant="primary" loading={save.isPending} onClick={submit}>
            {tile ? "Save" : "Add app"}
          </Button>
        </>
      }
    >
      <form
        className="flex flex-col gap-3.5"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <div className="grid gap-3.5 sm:grid-cols-2">
          <FormField label="Name" htmlFor="tile-name" error={nameErr}>
            <Input
              id="tile-name"
              value={v.name}
              maxLength={64}
              autoFocus
              onChange={(e) => {
                set("name", e.target.value);
                if (!slugTouched) setSlug(slugify(e.target.value));
              }}
            />
          </FormField>
          <FormField label="Group" htmlFor="tile-group" hint="Optional heading tiles are grouped under.">
            <Input id="tile-group" list="tile-groups" value={v.group} maxLength={48} onChange={(e) => set("group", e.target.value)} />
            <datalist id="tile-groups">
              {groups.map((g) => (
                <option key={g} value={g} />
              ))}
            </datalist>
          </FormField>
        </div>
        <FormField label="URL" htmlFor="tile-url" error={urlErr}>
          <Input id="tile-url" className="num" inputMode="url" placeholder="http://nas.lan:8096" value={v.url} onChange={(e) => set("url", e.target.value)} />
        </FormField>

        <fieldset className="rounded-md border border-border p-3">
          <legend className="px-1 text-[0.8125rem] font-medium">Icon</legend>
          <div className="flex flex-wrap items-start gap-3">
            <div className="flex size-14 shrink-0 items-center justify-center rounded-md border border-border bg-surface-2">
              <TileIcon tile={{ name: v.name || "?", icon: v.icon }} className="size-10" />
            </div>
            <div className="flex min-w-0 flex-1 flex-col gap-2">
              <div className="flex gap-2">
                <div className="min-w-0 flex-1">
                  <label htmlFor="tile-slug" className="sr-only">
                    dashboard-icons name
                  </label>
                  <Input
                    id="tile-slug"
                    className="num"
                    placeholder="dashboard-icons name, e.g. jellyfin"
                    value={slug}
                    onChange={(e) => {
                      setSlug(e.target.value.toLowerCase());
                      setSlugTouched(true);
                    }}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") {
                        e.preventDefault();
                        if (slugOk) fetchSlug.mutate();
                      }
                    }}
                  />
                </div>
                <Button icon={<Download />} loading={fetchSlug.isPending} disabled={!slugOk} onClick={() => fetchSlug.mutate()}>
                  Fetch
                </Button>
              </div>
              <div className="flex flex-wrap gap-2">
                <Button size="sm" icon={<Upload />} loading={upload.isPending} onClick={() => fileRef.current?.click()}>
                  Upload…
                </Button>
                {v.icon && (
                  <Button size="sm" variant="ghost" icon={<ImageOff />} onClick={() => set("icon", "")}>
                    Use letter
                  </Button>
                )}
                <input
                  ref={fileRef}
                  type="file"
                  accept="image/png,image/svg+xml,image/webp,image/jpeg"
                  className="hidden"
                  aria-label="Upload icon file"
                  onChange={(e) => {
                    const f = e.target.files?.[0];
                    e.target.value = "";
                    if (!f) return;
                    if (f.size > 512 * 1024) return toast.error("Icon is larger than 512 KB");
                    upload.mutate(f);
                  }}
                />
              </div>
              {fetchSlug.error ? (
                <p className="text-xs text-crit">{fetchSlug.error.message}</p>
              ) : (
                <p className="text-xs text-muted">Icons from dashboard-icons are downloaded once and served by Wargo. PNG, SVG, WebP or JPEG up to 512 KB.</p>
              )}
            </div>
          </div>
        </fieldset>

        <FormField
          label="Health check URL"
          htmlFor="tile-health"
          error={healthErr}
          hint={
            <>
              Optional. Checked every minute; any answer below HTTP 500 counts as online.{" "}
              {v.url && v.healthUrl !== v.url && (
                <button type="button" className="font-medium text-accent underline underline-offset-2" onClick={() => set("healthUrl", v.url)}>
                  Use app URL
                </button>
              )}
            </>
          }
        >
          <Input id="tile-health" className="num" inputMode="url" value={v.healthUrl} onChange={(e) => set("healthUrl", e.target.value)} />
        </FormField>
        <div className="flex flex-col gap-3 border-t border-border pt-3">
          <Switch checked={v.newTab} onCheckedChange={(b) => set("newTab", b)} label="Open in a new tab" />
          <Switch checked={v.enabled} onCheckedChange={(b) => set("enabled", b)} label="Show this app" description="Hidden apps stay in the editor but not on the launcher." />
        </div>
        {save.error && <ErrorNote error={save.error} />}
        <button type="submit" className="hidden" aria-hidden="true" tabIndex={-1} />
      </form>
    </Dialog>
  );
}

/** Lists running containers with published ports and adds the selected ones. */
export function SuggestDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["launcher", "suggest"],
    queryFn: () => api.get<Suggestion[]>("/api/launcher/suggest"),
    enabled: open,
    staleTime: 0,
  });
  const [sel, setSel] = useState<Set<string>>(new Set());
  const [progress, setProgress] = useState<string | null>(null);
  const list = q.data ?? [];
  const keyOf = (s: Suggestion) => s.container + s.url;

  useEffect(() => {
    if (open && q.data) setSel(new Set(q.data.filter((s) => !s.exists).map(keyOf)));
  }, [open, q.data]);

  const add = useMutation({
    mutationFn: async () => {
      const chosen = list.filter((s) => sel.has(keyOf(s)));
      let n = 0;
      for (const s of chosen) {
        setProgress(`Adding ${s.name} (${++n}/${chosen.length})…`);
        let icon = "";
        if (s.iconSlug) icon = await fetchIcon(s.iconSlug).catch(() => "");
        await api.post("/api/launcher/tiles", { name: s.name, url: s.url, group: s.group, icon, newTab: true, healthUrl: s.url, enabled: true });
      }
      return chosen.length;
    },
    onSuccess: (n) => {
      toast.ok(`Added ${n} app${n === 1 ? "" : "s"}`);
      onOpenChange(false);
    },
    onSettled: () => {
      setProgress(null);
      qc.invalidateQueries({ queryKey: tilesKey });
    },
  });

  const selectable = list.filter((s) => !s.exists);
  const allChecked = selectable.length > 0 && selectable.every((s) => sel.has(keyOf(s)));

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => !add.isPending && onOpenChange(o)}
      title="Add from containers"
      description="Running containers with published ports. URLs use the hostname you opened Wargo with."
      size="lg"
      footer={
        <>
          {progress && (
            <span className="mr-auto flex items-center gap-2 text-xs text-muted" role="status">
              <Spinner className="size-3.5" /> {progress}
            </span>
          )}
          <Button onClick={() => onOpenChange(false)} disabled={add.isPending}>
            Cancel
          </Button>
          <Button variant="primary" loading={add.isPending} disabled={sel.size === 0} onClick={() => add.mutate()}>
            Add {sel.size || ""} app{sel.size === 1 ? "" : "s"}
          </Button>
        </>
      }
    >
      {q.isLoading ? (
        <div className="flex flex-col gap-2">
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} className="h-12" />
          ))}
        </div>
      ) : q.error ? (
        <ErrorNote error={q.error} />
      ) : list.length === 0 ? (
        <EmptyState icon={<Boxes />} title="No containers with published ports">
          Containers that publish a TCP port on the host show up here.
        </EmptyState>
      ) : (
        <div className="flex flex-col gap-2">
          <div className="flex items-center justify-between px-1">
            <Checkbox
              checked={allChecked ? true : sel.size > 0 ? "indeterminate" : false}
              onCheckedChange={(b) => setSel(b ? new Set(selectable.map(keyOf)) : new Set())}
              label={<span className="text-xs text-muted">Select all</span>}
            />
            <span className="text-xs text-muted">{list.length} found</span>
          </div>
          <ul className="divide-y divide-border rounded-md border border-border">
            {list.map((s) => {
              const k = keyOf(s);
              return (
                <li key={k} className={cn("flex items-center gap-3 px-3 py-2.5", s.exists && "opacity-60")}>
                  <Checkbox
                    checked={sel.has(k)}
                    aria-label={`Add ${s.name}`}
                    onCheckedChange={(b) =>
                      setSel((p) => {
                        const n = new Set(p);
                        if (b) n.add(k);
                        else n.delete(k);
                        return n;
                      })
                    }
                  />
                  <TileIcon tile={{ name: s.name }} />
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
                      <span className="truncate text-[0.8125rem] font-medium">{s.name}</span>
                      {s.exists && <Badge>Already added</Badge>}
                    </div>
                    <div className="num truncate text-2xs text-muted">
                      {s.url.replace(/^https?:\/\//, "")} · {s.image}
                    </div>
                  </div>
                  {s.iconSlug && <span className="num hidden text-2xs text-muted sm:inline">icon: {s.iconSlug}</span>}
                </li>
              );
            })}
          </ul>
        </div>
      )}
      {add.error && <ErrorNote error={add.error} className="mt-3" />}
    </Dialog>
  );
}

/** Editable list of tiles: add, edit, delete and reorder. Reused by Settings → Launcher. */
export function TileEditor({ className }: { className?: string }) {
  const qc = useQueryClient();
  const { data, isLoading, error } = useTiles();
  const [order, setOrder] = useState<Tile[] | null>(null);
  const [editing, setEditing] = useState<Tile | null>(null);
  const [dialog, setDialog] = useState(false);
  const [suggest, setSuggest] = useState(false);
  const [deleting, setDeleting] = useState<Tile | null>(null);
  const [dragId, setDragId] = useState<string | null>(null);
  const [armed, setArmed] = useState<string | null>(null);
  const [announce, setAnnounce] = useState("");

  const tiles = useMemo(() => order ?? [...(data ?? [])].sort((a, b) => a.sort - b.sort || a.name.localeCompare(b.name)), [order, data]);
  const groups = useMemo(() => [...new Set((data ?? []).map((t) => t.group).filter(Boolean) as string[])].sort(), [data]);

  const reorder = useMutation({
    mutationFn: (ids: string[]) => api.post("/api/launcher/tiles/reorder", { ids }),
    onSettled: async () => {
      await qc.invalidateQueries({ queryKey: tilesKey });
      setOrder(null);
    },
    onError: (e) => toast.error("Could not save order", e),
  });
  const del = useMutation({
    mutationFn: (t: Tile) => api.del(`/api/launcher/tiles/${t.id}`),
    onSuccess: (_, t) => {
      toast.ok(`Removed ${t.name}`);
      setDeleting(null);
      qc.invalidateQueries({ queryKey: tilesKey });
    },
  });

  const commit = (list: Tile[]) => {
    setOrder(list);
    reorder.mutate(list.map((t) => t.id));
  };
  const move = (i: number, d: -1 | 1) => {
    const j = i + d;
    if (j < 0 || j >= tiles.length) return;
    const list = [...tiles];
    [list[i], list[j]] = [list[j], list[i]];
    setAnnounce(`${list[j].name} moved to position ${j + 1} of ${list.length}`);
    commit(list);
  };

  const openNew = () => {
    setEditing(null);
    setDialog(true);
  };

  return (
    <div className={className}>
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <Button variant="primary" size="sm" icon={<Plus />} onClick={openNew}>
          Add app
        </Button>
        <Button size="sm" icon={<Boxes />} onClick={() => setSuggest(true)}>
          Add from containers
        </Button>
        {tiles.length > 1 && <span className="ml-auto hidden text-xs text-muted sm:inline">Drag the handle or use the arrows to reorder.</span>}
      </div>
      <p className="sr-only" aria-live="polite">
        {announce}
      </p>

      {isLoading ? (
        <div className="flex flex-col gap-2">
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} className="h-12" />
          ))}
        </div>
      ) : error ? (
        <ErrorNote error={error} />
      ) : tiles.length === 0 ? (
        <div className="rounded-md border border-dashed border-border-strong">
          <EmptyState icon={<LayoutGrid />} title="No apps yet">
            Add links to the services you run, or import them from your running containers.
          </EmptyState>
        </div>
      ) : (
        <ul className="divide-y divide-border rounded-md border border-border bg-surface" aria-label="Apps">
          {tiles.map((t, i) => (
            <li
              key={t.id}
              draggable={armed === t.id}
              onDragStart={(e) => {
                setDragId(t.id);
                e.dataTransfer.effectAllowed = "move";
                e.dataTransfer.setData("text/plain", t.id);
              }}
              onDragOver={(e) => {
                if (!dragId || dragId === t.id) return;
                e.preventDefault();
                const from = tiles.findIndex((x) => x.id === dragId);
                if (from < 0) return;
                const list = [...tiles];
                const [m] = list.splice(from, 1);
                list.splice(i, 0, m);
                setOrder(list);
              }}
              onDrop={(e) => e.preventDefault()}
              onDragEnd={() => {
                if (dragId && order) commit(order);
                setDragId(null);
                setArmed(null);
              }}
              className={cn("flex items-center gap-2 px-2 py-2 sm:gap-3 sm:px-3", dragId === t.id && "bg-surface-2 opacity-70")}
            >
              <span
                className="hidden cursor-grab touch-none rounded p-1 text-subtle hover:bg-surface-3 hover:text-fg active:cursor-grabbing sm:inline-flex"
                onPointerDown={() => setArmed(t.id)}
                onPointerUp={() => !dragId && setArmed(null)}
                aria-hidden="true"
                title="Drag to reorder"
              >
                <GripVertical className="size-4" />
              </span>
              <TileIcon tile={t} />
              <div className="min-w-0 flex-1">
                <div className="flex min-w-0 items-center gap-2">
                  <span className="truncate text-[0.8125rem] font-medium">{t.name}</span>
                  {t.status && <StatusDot tone={t.status === "up" ? "ok" : "crit"} label={t.status === "up" ? "Online" : "Offline"} />}
                  {t.group && <Badge className="hidden sm:inline-flex">{t.group}</Badge>}
                  {!t.enabled && <Badge tone="warn">Hidden</Badge>}
                </div>
                <div className="num truncate text-2xs text-muted">{t.url.replace(/^https?:\/\//, "")}</div>
              </div>
              <div className="flex shrink-0 items-center">
                <IconButton label={`Move ${t.name} up`} size="sm" className="max-sm:size-8" disabled={i === 0 || reorder.isPending} onClick={() => move(i, -1)}>
                  <ArrowUp />
                </IconButton>
                <IconButton label={`Move ${t.name} down`} size="sm" className="max-sm:size-8" disabled={i === tiles.length - 1 || reorder.isPending} onClick={() => move(i, 1)}>
                  <ArrowDown />
                </IconButton>
                <IconButton
                  label={`Edit ${t.name}`}
                  size="sm"
                  className="max-sm:size-8"
                  onClick={() => {
                    setEditing(t);
                    setDialog(true);
                  }}
                >
                  <Pencil />
                </IconButton>
                <IconButton label={`Delete ${t.name}`} size="sm" className="hover:text-crit max-sm:size-8" onClick={() => setDeleting(t)}>
                  <Trash2 />
                </IconButton>
              </div>
            </li>
          ))}
        </ul>
      )}

      <TileDialog open={dialog} onOpenChange={setDialog} tile={editing} groups={groups} />
      <SuggestDialog open={suggest} onOpenChange={setSuggest} />
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Delete ${deleting?.name ?? "app"}?`}
        description="The tile is removed from the launcher. The app itself is not affected."
        confirmLabel="Delete"
        danger
        loading={del.isPending}
        onConfirm={() => deleting && del.mutate(deleting)}
      >
        {del.error && <ErrorNote error={del.error} />}
      </ConfirmDialog>
    </div>
  );
}

export function LauncherDisabledNotice({ action }: { action?: React.ReactNode }) {
  return (
    <Notice tone="frost">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span>The app launcher is turned off. Enable it to show these apps on the Overview or in the sidebar.</span>
        {action}
      </div>
    </Notice>
  );
}
