// SPDX-License-Identifier: AGPL-3.0-only
import { useMemo, useState, type ReactNode } from "react";
import { ChevronRight } from "lucide-react";
import { bytes } from "@/lib/format";
import { cn } from "@/lib/cn";
import { Badge } from "@/components/ui/misc";
import { Card, CardBody, CardHeader } from "@/components/ui/card";
import { SearchInput } from "@/components/ui/form";
import type { PkgUpdate } from "./api";
import { VersionDiff } from "./VersionDiff";

const repoOrder = ["core", "system", "main", "base", "stable", "extra", "community", "multilib"];

function repoRank(r: string) {
  const i = repoOrder.indexOf(r.toLowerCase());
  return i < 0 ? repoOrder.length : i;
}

/** Homebrew lists are grouped by kind rather than by tap. */
const kindLabel: Record<string, string> = { formula: "Formulae", cask: "Casks", pinned: "Pinned" };
const kindOrder = ["formula", "cask", "pinned"];

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
        <span className="flex min-w-0 items-center gap-1.5">
          <span className="num truncate text-ui font-medium text-fg" title={u.name}>
            {u.name}
          </span>
          {u.needsPassword && (
            <Badge tone="warn" title="Its installer runs with administrator rights, so Update all asks for your password">
              password
            </Badge>
          )}
        </span>
        <span className="num shrink-0 text-xs text-muted md:hidden">
          {[showRepo && u.repo, showSize && u.size > 0 && bytes(u.size)].filter(Boolean).join(" · ")}
        </span>
      </div>
      <VersionDiff current={u.current} next={u.new} className="mt-0.5 md:mt-0" />
      <span className="num hidden truncate text-xs text-muted md:block">{u.repo || "—"}</span>
      <span className="num hidden text-right text-xs text-muted md:block">{showSize ? (u.size > 0 ? bytes(u.size) : "—") : ""}</span>
    </li>
  );
}

function ListHeader({ sizeLabel = "Download", repoLabel = "Repository" }: { sizeLabel?: string; repoLabel?: string }) {
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
      <span>{repoLabel}</span>
      <span className="text-right">{sizeLabel}</span>
    </div>
  );
}

function Group({
  repo,
  label,
  items,
  defaultOpen,
  showSize,
}: {
  repo: string;
  label: string;
  items: PkgUpdate[];
  defaultOpen: boolean;
  showSize: boolean;
}) {
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
        <span className="num text-ui font-semibold">{label}</span>
        <Badge>{items.length}</Badge>
        {size > 0 && <span className="num ml-auto text-xs text-muted">{bytes(size)}</span>}
      </button>
      {open && (
        <ul id={id} aria-label={`${repo} updates`}>
          {items.map((u) => (
            <Row key={u.name} u={u} showSize={showSize} />
          ))}
        </ul>
      )}
    </div>
  );
}

/**
 * Updates in collapsible groups: by repository for OS packages, by kind
 * (formulae, casks) for Homebrew, whose sizes aren't known up front.
 */
export function GroupedUpdates({ updates, query, by = "repo" }: { updates: PkgUpdate[]; query: string; by?: "repo" | "kind" }) {
  const groups = useMemo(() => {
    const m = new Map<string, PkgUpdate[]>();
    for (const u of updates) {
      if (!matches(u, query)) continue;
      const k = by === "kind" ? u.source : u.repo || "";
      if (!m.has(k)) m.set(k, []);
      m.get(k)!.push(u);
    }
    const rank = by === "kind" ? (k: string) => kindOrder.indexOf(k) : repoRank;
    return [...m.entries()].sort((a, b) => rank(a[0]) - rank(b[0]) || a[0].localeCompare(b[0]));
  }, [updates, query, by]);
  const kind = by === "kind";

  if (groups.length === 0) {
    return <p className="px-4 py-6 text-center text-sm text-muted">No packages match “{query}”.</p>;
  }
  return (
    <div>
      <ListHeader sizeLabel={kind ? "" : "Download"} repoLabel={kind ? "Tap" : "Repository"} />
      {groups.map(([repo, items]) => (
        <Group
          key={repo + (query ? "?" : "")}
          repo={repo}
          label={kind ? (kindLabel[repo] ?? repo) : repo || "other"}
          items={items}
          showSize={!kind}
          defaultOpen={groups.length <= 4 || !!query}
        />
      ))}
    </div>
  );
}

/** Flat read-only list (AUR, Flatpak). */
export function PlainList({ items, query, empty, repoLabel }: { items: PkgUpdate[]; query: string; empty?: ReactNode; repoLabel?: string }) {
  const shown = items.filter((u) => matches(u, query));
  if (shown.length === 0) return <p className="px-4 py-4 text-center text-sm text-muted">{empty ?? "Nothing matches."}</p>;
  return (
    <div>
      <ListHeader sizeLabel="" repoLabel={repoLabel} />
      <ul>
        {shown.map((u) => (
          <Row key={u.name} u={u} showRepo showSize={false} />
        ))}
      </ul>
    </div>
  );
}

/** The pending-updates card of a package source, with a filter box. */
export function PackageListCard({
  title,
  icon,
  updates,
  query,
  onQuery,
  by,
  footer,
}: {
  title: string;
  icon: ReactNode;
  updates: PkgUpdate[];
  query: string;
  onQuery: (q: string) => void;
  by?: "repo" | "kind";
  footer?: ReactNode;
}) {
  const shown = query ? updates.filter((u) => matches(u, query)).length : updates.length;
  return (
    <Card>
      <CardHeader
        title={title}
        icon={icon}
        meta={query ? `${shown} of ${updates.length}` : `${updates.length}`}
        actions={<SearchInput value={query} onChange={onQuery} placeholder="Filter packages" className="w-40 sm:w-56" />}
      />
      <GroupedUpdates updates={updates} query={query} by={by} />
      {footer}
    </Card>
  );
}

/** Updates Wargo lists but doesn't install (AUR, Flatpak, pinned formulae). */
export function ReadOnlyCard({
  title,
  icon,
  items,
  query,
  badge = "read-only",
  repoLabel,
  children,
}: {
  title: string;
  icon: ReactNode;
  items: PkgUpdate[];
  query: string;
  badge?: string;
  repoLabel?: string;
  children: ReactNode;
}) {
  return (
    <Card className="mt-4">
      <CardHeader title={title} icon={icon} meta={`${items.length}`} actions={<Badge>{badge}</Badge>} />
      <CardBody className="border-b border-border py-2.5 text-xs text-muted">{children}</CardBody>
      <PlainList items={items} query={query} repoLabel={repoLabel} />
    </Card>
  );
}
