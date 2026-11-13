// SPDX-License-Identifier: AGPL-3.0-only
// Root pickers: the "Locations" sidebar (drives, then bookmarks) and the
// breadcrumb with its location switcher.
import { useEffect, useRef, useState, type ReactNode } from "react";
import { ArrowDown, ArrowUp, ChevronDown, ChevronRight, EyeOff, Folder, GripVertical, HardDrive, Lock, MoreHorizontal, Network, Pencil, Settings2, Star, StarOff, Usb } from "lucide-react";
import { bytes, level } from "@/lib/format";
import { cn } from "@/lib/cn";
import { Card } from "@/components/ui/card";
import { IconButton } from "@/components/ui/button";
import { Menu, MenuItem, MenuLabel, MenuSeparator, Tooltip } from "@/components/ui/menu";
import { Meter } from "@/components/ui/misc";
import { absPath, segments, type Bookmark, type Loc, type Root } from "./api";
import { BookmarkIcon, shortPath, useLocationsApi, type LocationsApi } from "./bookmarks";

export function RootIcon({ root, className }: { root?: Root; className?: string }) {
  const Icon = !root ? HardDrive : root.kind === "place" ? Folder : root.network ? Network : root.removable ? Usb : HardDrive;
  return <Icon className={className} aria-hidden="true" />;
}

function kindLabel(r: Root) {
  if (r.kind === "place") return "Folder";
  if (r.network) return `Network share (${r.fstype})`;
  if (r.removable) return `Removable drive (${r.fstype})`;
  return `Drive (${r.fstype})`;
}

function RootUsage({ r }: { r: Root }) {
  if (!r.total) return null;
  const used = r.used ?? r.total - (r.free ?? 0);
  const p = (used / r.total) * 100;
  return (
    <>
      <Meter value={p} tone={level(p, 80, 92)} className="mt-1.5 h-1" label={`${r.name} usage`} />
      <div className="num mt-1 text-2xs text-muted">
        {bytes(r.free)} free of {bytes(r.total)}
      </div>
    </>
  );
}

const rowCls = (current: boolean, disabled?: boolean) =>
  cn("relative block w-full rounded-md py-2 pl-2.5 pr-8 text-left transition-colors", current ? "bg-surface-3" : !disabled && "hover:bg-surface-2", disabled && "cursor-not-allowed");

const rowMenuBtn =
  "absolute right-1 top-1.5 flex size-6 items-center justify-center rounded text-subtle opacity-0 transition-opacity hover:bg-surface-3 hover:text-fg focus-visible:opacity-100 group-hover:opacity-100 group-focus-within:opacity-100 data-[state=open]:opacity-100 pointer-coarse:opacity-100";

function CurrentBar() {
  return <span className="absolute inset-y-2 left-0 w-0.5 rounded-full bg-accent" aria-hidden="true" />;
}

function DriveRow({ r, current, onPick }: { r: Root; current: boolean; onPick: (l: Loc) => void }) {
  const { setDriveHidden, manageDrives } = useLocationsApi();
  const sub = r.path === "/" ? `/ · ${r.device ?? r.fstype}` : r.path;
  return (
    <div className="group relative">
      <button
        type="button"
        onClick={() => onPick({ root: r.id, path: "." })}
        aria-current={current ? "true" : undefined}
        title={`${r.name} — ${kindLabel(r)}${r.device ? `, ${r.device}` : ""}`}
        className={rowCls(current)}
      >
        {current && <CurrentBar />}
        <div className="flex items-center gap-2">
          <RootIcon root={r} className={cn("size-4 shrink-0", current ? "text-accent" : "text-subtle")} />
          <span className="min-w-0 flex-1 truncate text-ui font-medium">{r.name}</span>
          {r.readOnly && (
            <span className="flex h-4 shrink-0 items-center gap-0.5 rounded border border-border px-1 text-[0.625rem] font-medium uppercase tracking-wide text-muted" title="Read-only">
              <Lock className="size-2.5" aria-hidden="true" />
              RO
              <span className="sr-only"> (read-only)</span>
            </span>
          )}
        </div>
        <div className="num mt-0.5 truncate pl-6 text-2xs text-subtle">{sub}</div>
        <div className="pl-6">
          <RootUsage r={r} />
        </div>
      </button>
      <Menu
        trigger={
          <button type="button" aria-label={`${r.name} options`} title="Options" className={rowMenuBtn}>
            <MoreHorizontal className="size-3.5" />
          </button>
        }
      >
        <MenuItem icon={<EyeOff />} onSelect={() => setDriveHidden(r, true)}>
          Hide from Locations
        </MenuItem>
        <MenuItem icon={<Settings2 />} onSelect={manageDrives}>
          Manage drives…
        </MenuItem>
      </Menu>
    </div>
  );
}

interface RowDrag {
  armed: boolean;
  dragging: boolean;
  arm: (on: boolean) => void;
  start: () => void;
  over: () => void;
  end: () => void;
}

function BookmarkRow({ b, index, count, current, onPick, drag }: { b: Bookmark; index: number; count: number; current: boolean; onPick: (l: Loc) => void; drag: RowDrag }) {
  const { home, renameBookmark, removeBookmark, moveBookmark } = useLocationsApi();
  const disabled = !b.available;
  const button = (
    <button
      type="button"
      onClick={() => !disabled && onPick({ root: b.root, path: b.path })}
      aria-current={current ? "true" : undefined}
      aria-disabled={disabled || undefined}
      aria-keyshortcuts="Alt+ArrowUp Alt+ArrowDown F2"
      title={disabled ? undefined : `${b.name} — ${b.abs} (Alt+↑/↓ to reorder, F2 to rename)`}
      onKeyDown={(e) => {
        if (e.altKey && (e.key === "ArrowUp" || e.key === "ArrowDown")) {
          e.preventDefault();
          const d = e.key === "ArrowUp" ? -1 : 1;
          if (index + d >= 0 && index + d < count) moveBookmark(b, d);
        } else if (e.key === "F2") {
          e.preventDefault();
          renameBookmark(b);
        }
      }}
      className={rowCls(current, disabled)}
    >
      {current && <CurrentBar />}
      <div className={cn("flex items-center gap-2", disabled && "opacity-50")}>
        <BookmarkIcon
          b={b}
          className={cn(
            "size-4 shrink-0 transition-opacity",
            current ? "text-accent" : "text-subtle",
            // The drag grip takes the icon's place on hover (fine pointers).
            count > 1 && "pointer-fine:group-hover:opacity-0",
          )}
        />
        <span className="min-w-0 flex-1 truncate text-ui font-medium">{b.name}</span>
      </div>
      <div className={cn("num mt-0.5 truncate pl-6 text-2xs text-subtle", disabled && "opacity-70")}>{disabled ? (b.reason ?? "Unavailable") : shortPath(b.abs, home)}</div>
    </button>
  );
  return (
    <li
      className={cn("group relative", drag.dragging && "opacity-60")}
      draggable={drag.armed}
      onDragStart={(e) => {
        e.dataTransfer.effectAllowed = "move";
        e.dataTransfer.setData("text/plain", b.name);
        drag.start();
      }}
      onDragOver={(e) => {
        e.preventDefault();
        drag.over();
      }}
      onDrop={(e) => e.preventDefault()}
      onDragEnd={drag.end}
    >
      {disabled ? (
        <Tooltip content={b.reason ?? "Unavailable"} side="right">
          {button}
        </Tooltip>
      ) : (
        button
      )}
      {count > 1 && (
        <span
          className="absolute left-2.5 top-2.5 flex size-4 cursor-grab items-center justify-center text-subtle opacity-0 transition-opacity hover:text-fg active:cursor-grabbing group-hover:opacity-100 pointer-coarse:hidden"
          onPointerDown={() => drag.arm(true)}
          onPointerUp={() => drag.arm(false)}
          title="Drag to reorder (or Alt+↑/↓)"
          aria-hidden="true"
        >
          <GripVertical className="size-4" />
        </span>
      )}
      <Menu
        trigger={
          <button type="button" aria-label={`${b.name} bookmark options`} title="Options" className={rowMenuBtn}>
            <MoreHorizontal className="size-3.5" />
          </button>
        }
      >
        <MenuItem icon={<Pencil />} shortcut="F2" onSelect={() => renameBookmark(b)}>
          Rename…
        </MenuItem>
        <MenuItem icon={<ArrowUp />} shortcut="Alt+↑" disabled={index === 0} onSelect={() => moveBookmark(b, -1)}>
          Move up
        </MenuItem>
        <MenuItem icon={<ArrowDown />} shortcut="Alt+↓" disabled={index === count - 1} onSelect={() => moveBookmark(b, 1)}>
          Move down
        </MenuItem>
        <MenuSeparator />
        <MenuItem icon={<StarOff />} danger onSelect={() => removeBookmark(b)}>
          Remove bookmark
        </MenuItem>
      </Menu>
    </li>
  );
}

/** Which row represents a pane's folder: an exact bookmark, else its
 * (visible) drive, else the deepest bookmark containing it. */
export function currentLocation(api: LocationsApi, l?: Loc): string | undefined {
  if (!l) return undefined;
  const exact = api.findBookmark(l);
  if (exact) return `b:${exact.id}`;
  const r = api.roots.find((x) => x.id === l.root);
  if (!r) return undefined;
  if (r.kind === "drive" && !r.hidden) return `d:${r.id}`;
  const abs = absPath(r, l.path);
  let best: Bookmark | undefined;
  for (const b of api.bookmarks) {
    if (b.available && (abs === b.abs || abs.startsWith(b.abs.replace(/\/$/, "") + "/")) && (!best || b.abs.length > best.abs.length)) best = b;
  }
  return best ? `b:${best.id}` : undefined;
}

function SectionHeader({ id, title, children }: { id: string; title: string; children?: ReactNode }) {
  return (
    <div className="flex h-7 items-center gap-1 pl-2.5 pr-1">
      <h2 id={id} className="flex-1 text-2xs font-semibold uppercase tracking-wider text-subtle">
        {title}
      </h2>
      {children}
    </div>
  );
}

export function RootsSidebar({ current, onPick }: { current?: Loc; onPick: (l: Loc) => void }) {
  const api = useLocationsApi();
  const { drives, bookmarks, manageDrives, manageBookmarks, reorderBookmarks } = api;
  const visibleDrives = drives.filter((d) => !d.hidden);
  const cur = currentLocation(api, current);
  const [order, setOrder] = useState<Bookmark[] | null>(null);
  const [dragId, setDragId] = useState<string | null>(null);
  const [armed, setArmed] = useState<string | null>(null);
  const list = order ?? bookmarks;
  const dragFor = (b: Bookmark, i: number): RowDrag => ({
    armed: armed === b.id,
    dragging: dragId === b.id,
    arm: (on) => setArmed(on ? b.id : null),
    start: () => setDragId(b.id),
    over: () => {
      if (!dragId || dragId === b.id) return;
      const from = list.findIndex((x) => x.id === dragId);
      if (from < 0) return;
      const next = [...list];
      const [m] = next.splice(from, 1);
      next.splice(i, 0, m);
      setOrder(next);
    },
    end: () => {
      if (dragId && order && order.some((x, k) => x.id !== bookmarks[k]?.id)) reorderBookmarks(order.map((x) => x.id));
      setOrder(null);
      setDragId(null);
      setArmed(null);
    },
  });
  return (
    <aside className="hidden w-56 shrink-0 lg:block" aria-label="Locations">
      <Card className="scroll-area max-h-full">
        {drives.length > 0 && (
          <>
            <nav aria-labelledby="files-drives" className="p-1.5">
              <SectionHeader id="files-drives" title="Drives">
                <IconButton label="Manage drives" size="xs" onClick={manageDrives} className="text-subtle [&_svg]:size-3.5">
                  <Settings2 />
                </IconButton>
              </SectionHeader>
              {visibleDrives.map((r) => (
                <DriveRow key={r.id} r={r} current={cur === `d:${r.id}`} onPick={onPick} />
              ))}
              {visibleDrives.length === 0 && (
                <button type="button" onClick={manageDrives} className="w-full rounded-md px-2.5 py-2 text-left text-2xs text-muted hover:bg-surface-2">
                  All drives are hidden. Show drives…
                </button>
              )}
            </nav>
            <div className="mx-3 h-px bg-border" />
          </>
        )}
        <nav aria-labelledby="files-bookmarks" className="p-1.5">
          <SectionHeader id="files-bookmarks" title="Bookmarks">
            {bookmarks.length > 0 && (
              <IconButton label="Manage bookmarks" size="xs" onClick={manageBookmarks} className="text-subtle [&_svg]:size-3.5">
                <Settings2 />
              </IconButton>
            )}
          </SectionHeader>
          {list.length > 0 ? (
            <ul aria-label="Bookmarks">
              {list.map((b, i) => (
                <BookmarkRow key={b.id} b={b} index={i} count={list.length} current={cur === `b:${b.id}`} onPick={onPick} drag={dragFor(b, i)} />
              ))}
            </ul>
          ) : (
            api.ready && (
              <p className="px-2.5 py-2 text-2xs leading-relaxed text-muted">
                No bookmarks yet. Open a folder and press the star or <kbd className="font-sans">Ctrl+D</kbd>.
              </p>
            )
          )}
        </nav>
      </Card>
    </aside>
  );
}

/** Menu items listing drives, then bookmarks (the breadcrumb switcher on
 * phones, tablets and in split view). */
export function RootMenuItems({ current, onPick }: { current?: Loc; onPick: (l: Loc) => void }) {
  const api = useLocationsApi();
  const drives = api.drives.filter((d) => !d.hidden);
  const cur = currentLocation(api, current);
  return (
    <>
      <div className="-mx-1 max-h-[min(28rem,calc(var(--radix-dropdown-menu-content-available-height,70dvh)-5rem))] overflow-y-auto px-1">
        {drives.length > 0 && <MenuLabel>Drives</MenuLabel>}
        {drives.map((r) => (
          <MenuItem key={r.id} icon={<RootIcon root={r} />} onSelect={() => onPick({ root: r.id, path: "." })} shortcut={cur === `d:${r.id}` ? "✓" : r.free !== undefined ? `${bytes(r.free)} free` : undefined}>
            <span className="flex min-w-0 flex-col py-0.5 leading-tight">
              <span className="truncate">
                {r.name}
                {r.readOnly && <span className="ml-1.5 text-2xs uppercase text-subtle">ro</span>}
              </span>
              <span className="num truncate text-2xs text-subtle">{r.path}</span>
            </span>
          </MenuItem>
        ))}
        {drives.length > 0 && api.bookmarks.length > 0 && <MenuSeparator />}
        {api.bookmarks.length > 0 && <MenuLabel>Bookmarks</MenuLabel>}
        {api.bookmarks.map((b) => (
          <MenuItem key={b.id} icon={<BookmarkIcon b={b} />} disabled={!b.available} onSelect={() => onPick({ root: b.root, path: b.path })} shortcut={cur === `b:${b.id}` ? "✓" : undefined}>
            <span className="flex min-w-0 flex-col py-0.5 leading-tight">
              <span className="truncate">{b.name}</span>
              <span className="num truncate text-2xs text-subtle">{b.available ? shortPath(b.abs, api.home) : (b.reason ?? "Unavailable")}</span>
            </span>
          </MenuItem>
        ))}
      </div>
      <MenuSeparator />
      {api.bookmarks.length > 0 && (
        <MenuItem icon={<Star />} onSelect={api.manageBookmarks}>
          Manage bookmarks…
        </MenuItem>
      )}
      {api.drives.length > 0 && (
        <MenuItem icon={<Settings2 />} onSelect={api.manageDrives}>
          Manage drives…
        </MenuItem>
      )}
    </>
  );
}

export function Breadcrumbs({
  root,
  path,
  onNav,
  onPick,
  alwaysSwitch,
}: {
  root?: Root;
  path: string;
  onNav: (p: string) => void;
  onPick: (l: Loc) => void;
  /** Show the location switcher on wide screens too (split view). */
  alwaysSwitch?: boolean;
}) {
  const api = useLocationsApi();
  const segs = segments(path);
  const ref = useRef<HTMLOListElement>(null);
  useEffect(() => {
    ref.current?.scrollTo({ left: ref.current.scrollWidth });
  }, [path]);
  const rootBtn = (
    <button type="button" onClick={() => onNav(".")} className="flex h-8 max-w-[10rem] shrink-0 items-center gap-1.5 rounded-md px-2 font-medium hover:bg-surface-3">
      <RootIcon root={root} className="size-4 shrink-0 text-accent" />
      <span className="truncate">{root?.name ?? "…"}</span>
    </button>
  );
  return (
    <nav aria-label="Breadcrumb" className="min-w-0 flex-1">
      <ol ref={ref} className="flex min-w-0 items-center overflow-x-auto text-ui [scrollbar-width:none]">
        <li className="flex shrink-0 items-center">
          {api.roots.length > 1 || api.bookmarks.length > 0 ? (
            <div className="flex items-center">
              {rootBtn}
              <Menu
                align="start"
                // Rows hold a name, a path and free space: phones get the screen width less 12 px a side, larger screens 22rem.
                className="w-[calc(100vw-1.5rem)] sm:w-[22rem]"
                trigger={
                  <button
                    type="button"
                    aria-label="Switch location"
                    className={cn("-ml-1 flex h-8 w-6 items-center justify-center rounded-md text-subtle hover:bg-surface-3 hover:text-fg", !alwaysSwitch && "lg:hidden")}
                  >
                    <ChevronDown className="size-3.5" />
                  </button>
                }
              >
                <RootMenuItems current={root ? { root: root.id, path } : undefined} onPick={onPick} />
              </Menu>
            </div>
          ) : (
            rootBtn
          )}
        </li>
        {segs.map((s, i) => {
          const last = i === segs.length - 1;
          return (
            <li key={i} className="flex shrink-0 items-center">
              <ChevronRight className="size-3.5 shrink-0 text-subtle" aria-hidden="true" />
              {last ? (
                <span aria-current="page" className="max-w-[16rem] truncate px-2 font-medium">
                  {s}
                </span>
              ) : (
                <button type="button" onClick={() => onNav(segs.slice(0, i + 1).join("/"))} className="h-8 max-w-[12rem] truncate rounded-md px-2 text-muted hover:bg-surface-3 hover:text-fg">
                  {s}
                </button>
              )}
            </li>
          );
        })}
      </ol>
    </nav>
  );
}
