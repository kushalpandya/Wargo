// SPDX-License-Identifier: AGPL-3.0-only
import type { ReactNode } from "react";
import { Link, useNavigate } from "react-router";
import { AlertTriangle, ArrowDownToLine, Bell, CircleAlert, Clapperboard, HardDrive, PackageCheck, Settings, X } from "lucide-react";
import { Button, IconButton } from "@/components/ui/button";
import { IconChip, type Tint } from "@/components/ui/card";
import { EmptyState, ErrorNote, Meter, Skeleton } from "@/components/ui/misc";
import { ago } from "@/lib/format";
import { cn } from "@/lib/cn";
import { useClearAll, useClearNotification, type Active, type Category, type Item, type Snapshot } from "./api";
import { sortActive } from "./format";

const look: Record<Category, [ReactNode, Tint]> = {
  "download.started": [<ArrowDownToLine key="i" />, "sky"],
  "download.finished": [<ArrowDownToLine key="i" />, "sky"],
  "download.error": [<CircleAlert key="i" />, "rose"],
  "stream.started": [<Clapperboard key="i" />, "butter"],
  "container.unhealthy": [<AlertTriangle key="i" />, "peach"],
  "container.failed": [<CircleAlert key="i" />, "rose"],
  "updates.found": [<PackageCheck key="i" />, "mint"],
  "disk.full": [<HardDrive key="i" />, "peach"],
};

function CategoryIcon({ category, muted }: { category: Category; muted?: boolean }) {
  const [icon, tint] = look[category] ?? [<Bell key="i" />, "iris"];
  return (
    <IconChip tint={muted ? "neutral" : tint} className="mt-0.5 size-7 rounded-md [&_svg]:size-3.5">
      {icon}
    </IconChip>
  );
}

const rowClass = "flex min-w-0 flex-1 items-start gap-2.5 rounded-md px-2 py-2";
const linkClass = "transition-colors hover:bg-surface-3/70 focus-visible:bg-surface-3/70 focus-visible:outline-none";

/** A row's body, a link into Wargo when it has one (closing the panel on the way). */
function Target({ to, onNavigate, children }: { to: string; onNavigate: () => void; children: ReactNode }) {
  if (!to) return <div className={rowClass}>{children}</div>;
  return (
    <Link to={to} onClick={onNavigate} className={cn(rowClass, linkClass)}>
      {children}
    </Link>
  );
}

function ActiveRow({ a, onNavigate }: { a: Active; onNavigate: () => void }) {
  const pct = Math.max(0, Math.min(100, a.progress * 100));
  return (
    <li className="flex">
      <Target to={a.link} onNavigate={onNavigate}>
        <CategoryIcon category={a.category} />
        <span className="min-w-0 flex-1">
          <span className="flex items-baseline gap-2">
            <span className="min-w-0 flex-1 truncate text-ui text-fg">{a.title}</span>
            <span className="num shrink-0 text-xs text-subtle">{Math.round(pct)}%</span>
          </span>
          <Meter value={pct} tone={a.category === "stream.started" ? "accent" : "frost"} className="mt-1" label={`${a.title} progress`} />
          {a.detail && <span className="num mt-0.5 block truncate text-2xs text-muted">{a.detail}</span>}
        </span>
      </Target>
    </li>
  );
}

function RecentRow({
  item,
  onNavigate,
  onClear,
  clearing,
}: {
  item: Item;
  onNavigate: () => void;
  onClear: () => void;
  clearing: boolean;
}) {
  const resolved = !!item.resolvedAt;
  return (
    <li className={cn("flex items-start gap-1", resolved && "opacity-60")}>
      <Target to={item.link} onNavigate={onNavigate}>
        <CategoryIcon category={item.category} muted={resolved} />
        <span className="min-w-0 flex-1">
          <span className="flex items-baseline gap-2">
            <span className="min-w-0 flex-1 truncate text-ui text-fg">{item.title}</span>
            <span className="num shrink-0 text-xs text-subtle">{ago(item.createdAt)}</span>
          </span>
          {item.body && <span className="block text-xs text-muted [overflow-wrap:anywhere]">{item.body}</span>}
          {resolved && <span className="block text-2xs text-subtle">Resolved {ago(item.resolvedAt)}</span>}
        </span>
      </Target>
      <IconButton label={`Clear ${item.title}`} size="xs" className="mt-2" disabled={clearing} onClick={onClear}>
        <X />
      </IconButton>
    </li>
  );
}

function SectionTitle({ children, action }: { children: ReactNode; action?: ReactNode }) {
  return (
    <div className="flex min-h-7 items-center justify-between gap-2 px-2 pb-1">
      <h3 className="text-2xs font-semibold uppercase tracking-wider text-subtle">{children}</h3>
      {action}
    </div>
  );
}

/** The notifications panel's contents: header, then Active and Recent. */
export function Panel({ data, loading, error, onClose }: { data?: Snapshot; loading: boolean; error: unknown; onClose: () => void }) {
  const navigate = useNavigate();
  const clearOne = useClearNotification();
  const clearAll = useClearAll();
  const active = sortActive(data?.active ?? []);
  const recent = data?.recent ?? [];

  return (
    <>
      <div className="flex shrink-0 items-center gap-1 border-b border-border px-3 py-2">
        <h2 id="notifications-title" className="flex-1 text-md font-semibold tracking-tight">
          Notifications
        </h2>
        <IconButton
          label="Notification settings"
          size="sm"
          onClick={() => {
            onClose();
            navigate("/settings/general#notifications");
          }}
        >
          <Settings />
        </IconButton>
        <IconButton label="Close" size="sm" onClick={onClose}>
          <X />
        </IconButton>
      </div>
      <div className="scroll-area min-h-0 flex-1 overscroll-contain">
        {loading ? (
          <div className="space-y-2 p-3">
            {Array.from({ length: 3 }, (_, i) => (
              <Skeleton key={i} className="h-10" />
            ))}
          </div>
        ) : error ? (
          <ErrorNote error={error} className="m-3" />
        ) : !active.length && !recent.length ? (
          <EmptyState icon={<Bell />} title="Nothing to show" className="py-10">
            Downloads, streams, problems and updates will show up here.
          </EmptyState>
        ) : (
          <div className="flex flex-col gap-3 p-2">
            {active.length > 0 && (
              <section>
                <SectionTitle>Active</SectionTitle>
                <ul>
                  {active.map((a) => (
                    <ActiveRow key={a.id} a={a} onNavigate={onClose} />
                  ))}
                </ul>
              </section>
            )}
            {recent.length > 0 && (
              <section>
                <SectionTitle
                  action={
                    <Button variant="ghost" size="xs" loading={clearAll.isPending} onClick={() => clearAll.mutate()}>
                      Clear all
                    </Button>
                  }
                >
                  Recent
                </SectionTitle>
                <ul>
                  {recent.map((item) => (
                    <RecentRow
                      key={item.id}
                      item={item}
                      onNavigate={onClose}
                      clearing={clearOne.isPending && clearOne.variables === item.id}
                      onClear={() => clearOne.mutate(item.id)}
                    />
                  ))}
                </ul>
              </section>
            )}
            {(clearOne.error || clearAll.error) && <ErrorNote error={clearOne.error || clearAll.error} />}
          </div>
        )}
      </div>
    </>
  );
}
