// SPDX-License-Identifier: AGPL-3.0-only
import { Link } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { Boxes, ChevronRight } from "lucide-react";
import { api } from "@/lib/api";
import { Card, CardHeader } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Skeleton, StatusDot, type Tone } from "@/components/ui/misc";
import type { Container } from "@/features/containers/types";

export function containerTone(c: Pick<Container, "state" | "health">): Tone {
  if (c.state !== "running") return c.state === "restarting" ? "warn" : c.state === "paused" ? "frost" : "neutral";
  if (c.health === "unhealthy") return "crit";
  if (c.health === "starting") return "warn";
  return "ok";
}

export function ContainersSummary() {
  const { data, isLoading } = useQuery({
    queryKey: ["containers"],
    queryFn: () => api.get<Container[]>("/api/containers"),
    refetchInterval: 10_000,
  });
  const running = data?.filter((c) => c.state === "running").length ?? 0;
  const unhealthy = data?.filter((c) => c.health === "unhealthy").length ?? 0;
  const list = [...(data ?? [])].sort((a, b) => Number(b.state === "running") - Number(a.state === "running") || a.name.localeCompare(b.name));
  return (
    <Card>
      <CardHeader
        title="Containers"
        icon={<Boxes />}
        meta={data ? `${running} running · ${data.length - running} stopped${unhealthy ? ` · ${unhealthy} unhealthy` : ""}` : undefined}
        actions={
          <Button asChild size="xs" variant="ghost">
            <Link to="/containers">
              Manage <ChevronRight />
            </Link>
          </Button>
        }
      />
      {isLoading ? (
        <Skeleton className="m-3.5 h-16" />
      ) : (
        <div className="grid grid-cols-1 gap-px bg-border sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-4">
          {list.map((c) => (
            <Link
              key={c.id}
              to={`/containers/${c.id}`}
              className="flex min-w-0 items-center gap-2.5 bg-surface px-3.5 py-2.5 text-xs hover:bg-surface-2"
            >
              <StatusDot tone={containerTone(c)} label={c.health ?? c.state} />
              <div className="min-w-0 flex-1">
                <div className="truncate font-medium text-fg">{c.name}</div>
                <div className="num truncate text-subtle">{c.image}</div>
              </div>
              <span className="shrink-0 text-subtle">{c.state === "running" ? (c.health ?? "running") : c.state}</span>
            </Link>
          ))}
        </div>
      )}
    </Card>
  );
}
