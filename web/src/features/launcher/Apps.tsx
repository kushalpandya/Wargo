// SPDX-License-Identifier: AGPL-3.0-only
import { useState } from "react";
import { Link } from "react-router";
import { Check, LayoutGrid, Pencil, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { EmptyState, ErrorNote, PageHeader, Skeleton } from "@/components/ui/misc";
import { LauncherDisabledNotice, LauncherGrid, TileEditor } from "./TileEditor";
import { useLauncherSettings, useTiles } from "./api";

export default function Apps() {
  const [edit, setEdit] = useState(false);
  const { data: tiles, isLoading, error } = useTiles();
  const { data: settings } = useLauncherSettings();
  const visible = (tiles ?? []).filter((t) => t.enabled);
  const up = visible.filter((t) => t.status === "up").length;
  const checked = visible.filter((t) => t.status).length;

  return (
    <>
      <PageHeader
        title="Apps"
        description={
          tiles?.length
            ? `${visible.length} app${visible.length === 1 ? "" : "s"}${checked ? ` · ${up} of ${checked} online` : ""}`
            : "Shortcuts to the services running in your homelab."
        }
        actions={
          <Button variant={edit ? "primary" : "secondary"} size="sm" icon={edit ? <Check /> : <Pencil />} onClick={() => setEdit((e) => !e)} aria-pressed={edit}>
            {edit ? "Done" : "Edit"}
          </Button>
        }
      />
      {settings && !settings.enabled && (
        <div className="mb-4">
          <LauncherDisabledNotice
            action={
              <Link to="/settings/general" className="inline-flex min-h-8 items-center text-sm font-medium text-accent underline underline-offset-2">
                Launcher settings
              </Link>
            }
          />
        </div>
      )}
      {edit ? (
        <TileEditor />
      ) : isLoading ? (
        <div className="grid grid-cols-[repeat(auto-fill,minmax(7rem,1fr))] gap-2 sm:grid-cols-[repeat(auto-fill,minmax(8.5rem,1fr))] sm:gap-3">
          {Array.from({ length: 8 }, (_, i) => (
            <Skeleton key={i} className="aspect-square rounded-xl" />
          ))}
        </div>
      ) : error ? (
        <ErrorNote error={error} />
      ) : visible.length === 0 ? (
        <div className="rounded-lg border border-dashed border-border-strong">
          <EmptyState
            icon={<LayoutGrid />}
            title="No apps yet"
            action={
              <Button variant="primary" size="sm" icon={<Plus />} onClick={() => setEdit(true)}>
                Add apps
              </Button>
            }
          >
            Add links to Jellyfin, Nextcloud, your router… or import them from running containers.
          </EmptyState>
        </div>
      ) : (
        <LauncherGrid tiles={visible} />
      )}
    </>
  );
}
