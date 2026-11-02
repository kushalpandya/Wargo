// SPDX-License-Identifier: AGPL-3.0-only
// Root pickers: the "Locations" sidebar (places + drives) and the breadcrumb
// with its location switcher.
import { useEffect, useRef } from "react";
import { ChevronDown, ChevronRight, Folder, HardDrive, Lock, Network, Usb } from "lucide-react";
import { bytes, level } from "@/lib/format";
import { cn } from "@/lib/cn";
import { Card } from "@/components/ui/card";
import { Menu, MenuItem, MenuLabel, MenuSeparator } from "@/components/ui/menu";
import { Meter } from "@/components/ui/misc";
import { segments, type Root, type RootId } from "./api";

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

function RootButton({ r, current, onPick }: { r: Root; current: boolean; onPick: (id: RootId) => void }) {
  const sub = r.kind === "drive" ? (r.path === "/" ? `/ · ${r.device ?? r.fstype}` : r.path) : r.path;
  return (
    <button
      type="button"
      onClick={() => onPick(r.id)}
      aria-current={current ? "true" : undefined}
      title={`${r.name} — ${kindLabel(r)}${r.device ? `, ${r.device}` : ""}`}
      className={cn("relative block w-full rounded-md px-2.5 py-2 text-left transition-colors", current ? "bg-surface-3" : "hover:bg-surface-2")}
    >
      {current && <span className="absolute inset-y-2 left-0 w-0.5 rounded-full bg-accent" aria-hidden="true" />}
      <div className="flex items-center gap-2">
        <RootIcon root={r} className={cn("size-4 shrink-0", current ? "text-accent" : "text-subtle")} />
        <span className="min-w-0 flex-1 truncate text-[0.8125rem] font-medium">{r.name}</span>
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
  );
}

export function RootsSidebar({ roots, current, onPick }: { roots: Root[]; current?: RootId; onPick: (id: RootId) => void }) {
  const places = roots.filter((r) => r.kind !== "drive");
  const drives = roots.filter((r) => r.kind === "drive");
  const section = (title: string, list: Root[], id: string) =>
    list.length > 0 && (
      <nav aria-labelledby={id} className="p-1.5">
        <h2 id={id} className="px-2.5 pb-1 pt-1.5 text-2xs font-semibold uppercase tracking-wider text-subtle">
          {title}
        </h2>
        {list.map((r) => (
          <RootButton key={r.id} r={r} current={r.id === current} onPick={onPick} />
        ))}
      </nav>
    );
  return (
    <aside className="hidden w-56 shrink-0 lg:block" aria-label="Locations">
      <Card className="scroll-area max-h-full">
        {section("Places", places, "files-places")}
        {places.length > 0 && drives.length > 0 && <div className="mx-3 h-px bg-border" />}
        {section("Drives", drives, "files-drives")}
      </Card>
    </aside>
  );
}

/** Menu items listing places and drives (used by the breadcrumb switcher). */
export function RootMenuItems({ roots, current, onPick }: { roots: Root[]; current?: RootId; onPick: (id: RootId) => void }) {
  const places = roots.filter((r) => r.kind !== "drive");
  const drives = roots.filter((r) => r.kind === "drive");
  const item = (r: Root) => (
    <MenuItem key={r.id} icon={<RootIcon root={r} />} onSelect={() => onPick(r.id)} shortcut={r.id === current ? "✓" : r.free !== undefined ? `${bytes(r.free)} free` : undefined}>
      <span className="flex min-w-0 flex-col py-0.5 leading-tight">
        <span className="truncate">
          {r.name}
          {r.readOnly && <span className="ml-1.5 text-2xs uppercase text-subtle">ro</span>}
        </span>
        {r.kind === "drive" && <span className="num truncate text-2xs text-subtle">{r.path}</span>}
      </span>
    </MenuItem>
  );
  return (
    <>
      {places.length > 0 && <MenuLabel>Places</MenuLabel>}
      {places.map(item)}
      {places.length > 0 && drives.length > 0 && <MenuSeparator />}
      {drives.length > 0 && <MenuLabel>Drives</MenuLabel>}
      {drives.map(item)}
    </>
  );
}

export function Breadcrumbs({
  roots,
  root,
  path,
  onNav,
  onRoot,
  alwaysSwitch,
}: {
  roots: Root[];
  root?: Root;
  path: string;
  onNav: (p: string) => void;
  onRoot: (id: RootId) => void;
  /** Show the location switcher on wide screens too (split view). */
  alwaysSwitch?: boolean;
}) {
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
      <ol ref={ref} className="flex min-w-0 items-center overflow-x-auto text-[0.8125rem] [scrollbar-width:none]">
        <li className="flex shrink-0 items-center">
          {roots.length > 1 ? (
            <div className="flex items-center">
              {rootBtn}
              <Menu
                align="start"
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
                <RootMenuItems roots={roots} current={root?.id} onPick={onRoot} />
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
