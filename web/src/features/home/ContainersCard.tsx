// SPDX-License-Identifier: AGPL-3.0-only
import { memo, useMemo } from "react";
import { Link } from "react-router";
import { ArrowUpCircle, Boxes, ChevronRight } from "lucide-react";
import { cn } from "@/lib/cn";
import { Card, CardHeader } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge, EmptyState, Skeleton, StatusDot } from "@/components/ui/misc";
import { Tooltip } from "@/components/ui/menu";
import type { Container } from "@/features/containers/types";
import { useContainers } from "@/features/containers/api";
import { ContainerIcon } from "@/features/containers/ContainerIcon";
import { containerTone } from "@/features/containers/util";

function statusLabel(c: Container): string {
  if (c.state !== "running") return c.state === "exited" ? "Stopped" : c.state[0].toUpperCase() + c.state.slice(1);
  if (c.health === "unhealthy") return "Unhealthy";
  if (c.health === "starting") return "Starting";
  if (c.health === "healthy") return "Healthy";
  return "Running";
}

function rank(c: Container): number {
  if (c.health === "unhealthy") return 0;
  if (c.state === "restarting") return 1;
  if (c.updateAvailable) return 2;
  if (c.state === "running") return 3;
  return 4;
}

function versionOf(c: Container): string {
  if (c.version) return c.version;
  const tag = c.tag ?? (c.image.includes(":") ? c.image.slice(c.image.lastIndexOf(":") + 1) : "latest");
  return tag;
}

// Memoised: parents re-render on every 1 s metrics tick.
export const ContainersCard = memo(function ContainersCard() {
  const { data, isLoading, error } = useContainers();

  const list = useMemo(() => [...(data ?? [])].sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name)), [data]);
  const running = list.filter((c) => c.state === "running").length;
  const unhealthy = list.filter((c) => c.health === "unhealthy").length;
  const updates = list.filter((c) => c.updateAvailable).length;

  return (
    <Card className="flex h-full min-h-0 flex-col">
      <CardHeader
        title="Containers"
        icon={<Boxes />}
        meta={data ? `${running}/${list.length} running${unhealthy ? ` · ${unhealthy} unhealthy` : ""}${updates ? ` · ${updates} update${updates > 1 ? "s" : ""}` : ""}` : undefined}
        actions={
          <Button asChild size="xs" variant="ghost">
            <Link to="/containers">
              Manage <ChevronRight />
            </Link>
          </Button>
        }
      />
      {isLoading ? (
        <div className="space-y-2 p-3.5">
          {Array.from({ length: 6 }, (_, i) => (
            <Skeleton key={i} className="h-9" />
          ))}
        </div>
      ) : error ? (
        <EmptyState icon={<Boxes />} title="Docker isn't reachable">
          {error instanceof Error ? error.message : String(error)}
        </EmptyState>
      ) : list.length === 0 ? (
        <EmptyState icon={<Boxes />} title="No containers" />
      ) : (
        <ul className="scroll-area max-h-[34rem] flex-1 divide-y divide-border" aria-label="Containers">
          {list.map((c) => {
            const tone = containerTone(c);
            return (
              <li key={c.id}>
                <Link
                  to={`/containers/${c.id}`}
                  className="flex min-w-0 items-center gap-3 px-3.5 py-2 transition-colors hover:bg-surface-2 focus-visible:bg-surface-2"
                >
                  <ContainerIcon c={c} />
                  <div className="min-w-0 flex-1">
                    <div className="flex min-w-0 items-center gap-2">
                      <span className="truncate text-[0.8125rem] font-medium">{c.name}</span>
                      {c.updateAvailable && (
                        <Tooltip content="A newer image is available — see Updates">
                          <span className="inline-flex shrink-0">
                            <Badge tone="accent">
                              <ArrowUpCircle className="size-3" aria-hidden="true" />
                              Update
                            </Badge>
                          </span>
                        </Tooltip>
                      )}
                    </div>
                    <div className="num truncate text-xs text-subtle" title={c.image}>
                      {versionOf(c)}
                      <span className="max-sm:hidden"> · {c.image.replace(/:[^:/]+$/, "")}</span>
                    </div>
                  </div>
                  <div className="flex shrink-0 items-center gap-1.5 text-xs">
                    <StatusDot tone={tone} />
                    <span className={cn(tone === "crit" ? "text-crit" : tone === "warn" ? "text-warn" : tone === "ok" ? "text-fg" : "text-muted")}>{statusLabel(c)}</span>
                  </div>
                </Link>
              </li>
            );
          })}
        </ul>
      )}
    </Card>
  );
});
