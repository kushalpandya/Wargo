// SPDX-License-Identifier: AGPL-3.0-only
// Bookmarks and drive visibility: shared state for every pane (sidebar,
// location menus, toolbar toggle) plus the manage / rename dialogs.
import { createContext, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ArrowDown, ArrowUp, Folder, Home, Pencil, Star, Trash2 } from "lucide-react";
import { ApiError } from "@/lib/api";
import { cn } from "@/lib/cn";
import { Button, IconButton } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { FormField, Input, Switch } from "@/components/ui/form";
import { EmptyState, ErrorNote } from "@/components/ui/misc";
import { toast } from "@/components/ui/toast";
import { absPath, baseName, bookmarkApi, locationsKey, type Bookmark, type Loc, type Locations, type Root } from "./api";
import { RootIcon } from "./Locations";

export const MAX_BOOKMARK_NAME = 64;

export interface LocationsApi {
  roots: Root[];
  /** All detected drives, hidden ones included. */
  drives: Root[];
  bookmarks: Bookmark[];
  hiddenCount: number;
  home?: string;
  /** Data loaded from the server (bookmarks are unknown before). */
  ready: boolean;
  findBookmark: (l: Loc) => Bookmark | undefined;
  toggleBookmark: (l: Loc) => void;
  removeBookmark: (b: Bookmark) => void;
  renameBookmark: (b: Bookmark) => void;
  moveBookmark: (b: Bookmark, delta: -1 | 1) => void;
  reorderBookmarks: (ids: string[]) => void;
  setDriveHidden: (r: Root, hidden: boolean) => void;
  manageDrives: () => void;
  manageBookmarks: () => void;
}

const Ctx = createContext<LocationsApi | null>(null);

export function useLocationsApi(): LocationsApi {
  const v = useContext(Ctx);
  if (!v) throw new Error("useLocationsApi outside LocationsProvider");
  return v;
}

/** "~/Downloads" for paths in the admin's home. */
export function shortPath(abs: string, home?: string) {
  if (home && home !== "/") {
    if (abs === home) return "~";
    if (abs.startsWith(home + "/")) return "~" + abs.slice(home.length);
  }
  return abs;
}

export function BookmarkIcon({ b, className }: { b: Bookmark; className?: string }) {
  const Icon = b.home ? Home : b.kind === "place" && b.path === "." ? Folder : Star;
  return <Icon className={className} aria-hidden="true" />;
}

export function LocationsProvider({ roots, data, children }: { roots: Root[]; data?: Locations; children: ReactNode }) {
  const qc = useQueryClient();
  const [manage, setManage] = useState<"drives" | "bookmarks" | null>(null);
  const [renaming, setRenaming] = useState<Bookmark | null>(null);
  const bookmarks = useMemo(() => data?.bookmarks ?? [], [data]);
  const drives = useMemo(() => roots.filter((r) => r.kind === "drive"), [roots]);
  const busy = useRef(new Set<string>());

  const patch = (fn: (d: Locations) => Locations) => qc.setQueryData<Locations>(locationsKey, (d) => (d ? fn(d) : d));
  const reload = () => qc.invalidateQueries({ queryKey: locationsKey });

  const api = useMemo<LocationsApi>(() => {
    const rootOf = (id: string) => roots.find((r) => r.id === id);
    const findBookmark = (l: Loc) => {
      const r = rootOf(l.root);
      if (!r) return undefined;
      const abs = absPath(r, l.path);
      return bookmarks.find((b) => b.abs === abs);
    };
    const removeBookmark = (b: Bookmark) => {
      patch((d) => ({
        ...d,
        bookmarks: d.bookmarks.filter((x) => x.id !== b.id),
      }));
      bookmarkApi.remove(b.id).then(
        () => {
          toast.ok("Bookmark removed", b.name);
          reload();
        },
        (e) => {
          toast.error("Could not remove bookmark", e);
          reload();
        },
      );
    };
    const addBookmark = (l: Loc) => {
      const key = `${l.root}:${l.path}`;
      if (busy.current.has(key)) return;
      busy.current.add(key);
      bookmarkApi.add(l.root, l.path).then(
        (b) => {
          patch((d) => (d.bookmarks.some((x) => x.id === b.id) ? d : { ...d, bookmarks: [...d.bookmarks, b] }));
          toast.ok("Bookmarked", b.name);
          busy.current.delete(key);
          reload();
        },
        (e) => {
          busy.current.delete(key);
          if (e instanceof ApiError && e.code === "exists") toast.info("Already bookmarked", e.message);
          else toast.error("Could not bookmark this folder", e);
          reload();
        },
      );
    };
    const reorderBookmarks = (ids: string[]) => {
      patch((d) => ({
        ...d,
        bookmarks: ids.map((id) => d.bookmarks.find((b) => b.id === id)).filter((b): b is Bookmark => !!b),
      }));
      bookmarkApi.reorder(ids).then(reload, (e) => {
        toast.error("Could not save bookmark order", e);
        reload();
      });
    };
    return {
      roots,
      drives,
      bookmarks,
      hiddenCount: drives.filter((d) => d.hidden).length,
      home: data?.home,
      ready: !!data,
      findBookmark,
      toggleBookmark: (l) => {
        const b = findBookmark(l);
        if (b) removeBookmark(b);
        else addBookmark(l);
      },
      removeBookmark,
      renameBookmark: setRenaming,
      moveBookmark: (b, delta) => {
        const i = bookmarks.findIndex((x) => x.id === b.id);
        const j = i + delta;
        if (i < 0 || j < 0 || j >= bookmarks.length) return;
        const ids = bookmarks.map((x) => x.id);
        [ids[i], ids[j]] = [ids[j], ids[i]];
        reorderBookmarks(ids);
      },
      reorderBookmarks,
      setDriveHidden: (r, hidden) => {
        patch((d) => ({
          ...d,
          roots: d.roots.map((x) => (x.id === r.id ? { ...x, hidden } : x)),
          hiddenDrives: Math.max(0, d.hiddenDrives + (hidden ? 1 : -1)),
        }));
        bookmarkApi.setDriveHidden(r.id, hidden).then(
          () => {
            qc.invalidateQueries({ queryKey: ["capabilities"] });
            reload();
          },
          (e) => {
            toast.error(hidden ? "Could not hide drive" : "Could not show drive", e);
            reload();
          },
        );
      },
      manageDrives: () => setManage("drives"),
      manageBookmarks: () => setManage("bookmarks"),
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [roots, drives, bookmarks, data]);

  return (
    <Ctx.Provider value={api}>
      {children}
      <ManageDrivesDialog open={manage === "drives"} onOpenChange={(o) => !o && setManage(null)} />
      <ManageBookmarksDialog open={manage === "bookmarks"} onOpenChange={(o) => !o && setManage(null)} />
      <RenameBookmarkDialog
        bookmark={renaming}
        onClose={() => setRenaming(null)}
        onSubmit={async (b, name) => {
          const nb = await bookmarkApi.rename(b.id, name);
          patch((d) => ({
            ...d,
            bookmarks: d.bookmarks.map((x) => (x.id === nb.id ? nb : x)),
          }));
          reload();
        }}
      />
    </Ctx.Provider>
  );
}

// ---- dialogs -----------------------------------------------------------------

function ManageDrivesDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const { drives, setDriveHidden, hiddenCount } = useLocationsApi();
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title="Manage drives"
      description="Choose which drives appear in Locations. Hidden drives stay reachable through bookmarks and links."
      footer={
        <>
          {hiddenCount > 0 && (
            <Button variant="ghost" className="mr-auto" onClick={() => drives.filter((d) => d.hidden).forEach((d) => setDriveHidden(d, false))}>
              Show all
            </Button>
          )}
          <Button variant="primary" onClick={() => onOpenChange(false)}>
            Done
          </Button>
        </>
      }
    >
      {drives.length === 0 ? (
        <EmptyState icon={<RootIcon />} title="No drives detected" />
      ) : (
        <ul className="-mx-1 divide-y divide-border" aria-label="Drives">
          {drives.map((d) => (
            <li key={d.id} className="flex items-center gap-3 px-1 py-2.5">
              <RootIcon root={d} className={cn("size-4 shrink-0", d.hidden ? "text-subtle" : "text-accent")} />
              <div className="min-w-0 flex-1">
                <div className="truncate text-[0.8125rem] font-medium">{d.name}</div>
                <div className="num truncate text-2xs text-muted">
                  {d.path}
                  {d.device ? ` · ${d.device}` : ""}
                  {d.fstype ? ` · ${d.fstype}` : ""}
                </div>
              </div>
              <SwitchLabelled checked={!d.hidden} label={`Show ${d.name} (${d.path}) in Locations`} onChange={(v) => setDriveHidden(d, !v)} />
            </li>
          ))}
        </ul>
      )}
    </Dialog>
  );
}

/** Switch with an accessible name but a terse visible label. */
function SwitchLabelled({ checked, label, onChange }: { checked: boolean; label: string; onChange: (v: boolean) => void }) {
  return (
    <label className="flex shrink-0 cursor-pointer items-center gap-2">
      <span className="sr-only">{label}</span>
      <span className="w-10 text-right text-2xs text-muted max-sm:hidden" aria-hidden="true">
        {checked ? "Shown" : "Hidden"}
      </span>
      <Switch checked={checked} onCheckedChange={onChange} />
    </label>
  );
}

function ManageBookmarksDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const { bookmarks, home, moveBookmark, renameBookmark, removeBookmark } = useLocationsApi();
  const [announce, setAnnounce] = useState("");
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title="Manage bookmarks"
      description="Rename, reorder or remove bookmarked folders."
      footer={
        <Button variant="primary" onClick={() => onOpenChange(false)}>
          Done
        </Button>
      }
    >
      <p className="sr-only" aria-live="polite">
        {announce}
      </p>
      {bookmarks.length === 0 ? (
        <EmptyState icon={<Star />} title="No bookmarks yet">
          Open a folder and press the star (or Ctrl+D) to bookmark it.
        </EmptyState>
      ) : (
        <ul className="-mx-1 divide-y divide-border" aria-label="Bookmarks">
          {bookmarks.map((b, i) => (
            <li key={b.id} className="flex items-center gap-2 px-1 py-2">
              <BookmarkIcon b={b} className={cn("size-4 shrink-0", b.available ? "text-accent" : "text-subtle")} />
              <div className="min-w-0 flex-1">
                <div className="truncate text-[0.8125rem] font-medium">{b.name}</div>
                <div className="num truncate text-2xs text-muted">
                  {shortPath(b.abs, home)}
                  {!b.available && <span className="text-warn"> · {b.reason ?? "Unavailable"}</span>}
                </div>
              </div>
              <div className="flex shrink-0 items-center">
                <IconButton
                  label={`Move ${b.name} up`}
                  size="sm"
                  className="max-sm:size-8"
                  disabled={i === 0}
                  onClick={() => {
                    moveBookmark(b, -1);
                    setAnnounce(`${b.name} moved to position ${i} of ${bookmarks.length}`);
                  }}
                >
                  <ArrowUp />
                </IconButton>
                <IconButton
                  label={`Move ${b.name} down`}
                  size="sm"
                  className="max-sm:size-8"
                  disabled={i === bookmarks.length - 1}
                  onClick={() => {
                    moveBookmark(b, 1);
                    setAnnounce(`${b.name} moved to position ${i + 2} of ${bookmarks.length}`);
                  }}
                >
                  <ArrowDown />
                </IconButton>
                <IconButton label={`Rename ${b.name}`} size="sm" className="max-sm:size-8" onClick={() => renameBookmark(b)}>
                  <Pencil />
                </IconButton>
                <IconButton label={`Remove bookmark ${b.name}`} size="sm" className="hover:text-crit max-sm:size-8" onClick={() => removeBookmark(b)}>
                  <Trash2 />
                </IconButton>
              </div>
            </li>
          ))}
        </ul>
      )}
    </Dialog>
  );
}

function RenameBookmarkDialog({ bookmark, onClose, onSubmit }: { bookmark: Bookmark | null; onClose: () => void; onSubmit: (b: Bookmark, name: string) => Promise<void> }) {
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [last, setLast] = useState<Bookmark | null>(null);
  const ref = useRef<HTMLInputElement>(null);
  if (bookmark && bookmark !== last) {
    setLast(bookmark);
    setName(bookmark.name);
    setError(null);
  }
  useEffect(() => {
    if (!bookmark) return;
    const t = window.setTimeout(() => ref.current?.select(), 30);
    return () => window.clearTimeout(t);
  }, [bookmark]);
  const trimmed = name.trim();
  const tooLong = [...trimmed].length > MAX_BOOKMARK_NAME;
  const invalid = !trimmed || tooLong;
  const shown = bookmark ?? last;
  const submit = async () => {
    if (invalid || !bookmark) return;
    setBusy(true);
    setError(null);
    try {
      await onSubmit(bookmark, trimmed);
      onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={!!bookmark}
      onOpenChange={(o) => !o && onClose()}
      title="Rename bookmark"
      size="sm"
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" loading={busy} disabled={invalid} onClick={submit}>
            Rename
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
        <FormField
          label="Name"
          htmlFor="bookmark-name"
          error={tooLong ? `At most ${MAX_BOOKMARK_NAME} characters` : undefined}
          hint={shown ? <span className="num">{shown.abs || baseName(shown.path)}</span> : undefined}
        >
          <Input id="bookmark-name" ref={ref} value={name} onChange={(e) => setName(e.target.value)} autoComplete="off" maxLength={MAX_BOOKMARK_NAME * 2} />
        </FormField>
        <ErrorNote error={error} />
      </form>
    </Dialog>
  );
}
