// SPDX-License-Identifier: AGPL-3.0-only
import { useMemo, useState, type ReactNode } from "react";
import { ChevronRight } from "lucide-react";
import { bytes } from "@/lib/format";
import { cn } from "@/lib/cn";
import { Badge } from "@/components/ui/misc";
import type { PkgUpdate } from "./api";
import { VersionDiff } from "./VersionDiff";

const repoOrder = ["core", "system", "main", "base", "stable", "extra", "community", "multilib"];

function repoRank(r: string) {
  const i = repoOrder.indexOf(r.toLowerCase());
  return i < 0 ? repoOrder.length : i;
}

export function matches(u: PkgUpdate, q: string) {
  if (!q) return true;
  const s = q.toLowerCase();
  return u.name.toLowerCase().includes(s) || u.repo.toLowerCase().includes(s) || u.new.toLowerCase().includes(s);
}

/** Column template shared by header and rows (≥ md). */
const cols = "md:grid md:grid-cols-[minmax(10rem,1.1fr)_minmax(14rem,2fr)_7rem_5.5rem] md:items-center md:gap-4";

function Row({ u, showRepo, showSize = true }: { u: PkgUpdate; showRepo?: boolean; showSize?: boolean }) {
  return (
    <li className={cn("border-b border-border px-4 py-2 last:border-0 hover:bg-surface-2", cols)}>
      <div className="flex min-w-0 items-baseline justify-between gap-3">
        <span className="num truncate text-ui font-medium text-fg" title={u.name}>
          {u.name}
        </span>
        <span className="num shrink-0 text-xs text-muted md:hidden">
          {showRepo && u.repo ? `${u.repo} · ` : ""}
          {showSize && u.size > 0 ? bytes(u.size) : ""}
        </span>
      </div>
      <VersionDiff current={u.current} next={u.new} className="mt-0.5 md:mt-0" />
      <span className="num hidden truncate text-xs text-muted md:block">{u.repo || "—"}</span>
      <span className="num hidden text-right text-xs text-muted md:block">{showSize ? (u.size > 0 ? bytes(u.size) : "—") : ""}</span>
    </li>
  );
}

function ListHeader({ sizeLabel = "Download" }: { sizeLabel?: string }) {
  return (
    <div
      aria-hidden="true"
      className={cn(
        "hidden border-b border-border bg-surface-2 px-4 py-1.5 text-2xs font-semibold uppercase tracking-wider text-subtle",
        cols,
      )}
    >
      <span>Package</span>
      <span>Current → new</span>
      <span>Repository</span>
      <span className="text-right">{sizeLabel}</span>
    </div>
  );
}

function Group({ repo, items, defaultOpen }: { repo: string; items: PkgUpdate[]; defaultOpen: boolean }) {
  const [open, setOpen] = useState(defaultOpen);
  const size = items.reduce((a, u) => a + u.size, 0);
  const id = `grp-${repo.replace(/\W/g, "_")}`;
  return (
    <div className="border-b border-border last:border-0">
      <button
        type="button"
        aria-expanded={open}
        aria-controls={id}
        onClick={() => setOpen((o) => !o)}
        className="flex min-h-9 w-full items-center gap-2 bg-surface-2/60 px-4 py-1.5 text-left hover:bg-surface-3"
      >
        <ChevronRight className={cn("size-3.5 shrink-0 text-subtle transition-transform", open && "rotate-90")} />
        <span className="num text-ui font-semibold">{repo || "other"}</span>
        <Badge>{items.length}</Badge>
        {size > 0 && <span className="num ml-auto text-xs text-muted">{bytes(size)}</span>}
      </button>
      {open && (
        <ul id={id} aria-label={`${repo} updates`}>
          {items.map((u) => (
            <Row key={u.name} u={u} />
          ))}
        </ul>
      )}
    </div>
  );
}

/** Repo updates grouped by repository with collapsible groups. */
export function GroupedUpdates({ updates, query }: { updates: PkgUpdate[]; query: string }) {
  const groups = useMemo(() => {
    const m = new Map<string, PkgUpdate[]>();
    for (const u of updates) {
      if (!matches(u, query)) continue;
      const k = u.repo || "";
      if (!m.has(k)) m.set(k, []);
      m.get(k)!.push(u);
    }
    return [...m.entries()].sort((a, b) => repoRank(a[0]) - repoRank(b[0]) || a[0].localeCompare(b[0]));
  }, [updates, query]);

  if (groups.length === 0) {
    return <p className="px-4 py-6 text-center text-sm text-muted">No packages match “{query}”.</p>;
  }
  return (
    <div>
      <ListHeader />
      {groups.map(([repo, items]) => (
        <Group key={repo + (query ? "?" : "")} repo={repo} items={items} defaultOpen={groups.length <= 4 || !!query} />
      ))}
    </div>
  );
}

/** Flat read-only list (AUR, Flatpak). */
export function PlainList({ items, query, empty }: { items: PkgUpdate[]; query: string; empty?: ReactNode }) {
  const shown = items.filter((u) => matches(u, query));
  if (shown.length === 0) return <p className="px-4 py-4 text-center text-sm text-muted">{empty ?? "Nothing matches."}</p>;
  return (
    <div>
      <ListHeader sizeLabel="" />
      <ul>
        {shown.map((u) => (
          <Row key={u.name} u={u} showRepo showSize={false} />
        ))}
      </ul>
    </div>
  );
}
