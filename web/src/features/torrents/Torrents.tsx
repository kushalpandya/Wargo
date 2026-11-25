// SPDX-License-Identifier: AGPL-3.0-only
import { readPref, setPref } from "@/lib/prefs";
import { useState, type ReactNode } from "react";
import { useSearchParams } from "react-router";
import { Columns2, Download, Plus, Search as SearchIcon, Square } from "lucide-react";
import { useCapabilities } from "@/app/session";
import { useTopicValue } from "@/lib/ws";
import { useHiddenFor, useMedia } from "@/lib/hooks";
import { PageHeader } from "@/components/ui/misc";
import { Segmented } from "@/components/ui/form";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import SearchPage from "./Search";
import Downloads from "./Downloads";
import { AddDialog } from "./AddDialog";
import { RemoveFinishedSwitch } from "./RemoveFinished";
import { HIDDEN_TAB_DELAY_MS } from "./api";

type Tab = "search" | "downloads";
type Layout = "tabs" | "split";

const LAYOUT_KEY = "wargo.torrents.layout";

/**
 * Heading above a column's card, with that column's actions on the right.
 * Both split-view columns have the same minimum height so their cards line up
 * (the split view's Downloads column is wide enough that its actions don't wrap).
 */
function ColumnHeading({ id, icon, children, actions }: { id: string; icon: ReactNode; children: ReactNode; actions?: ReactNode }) {
  return (
    <div className="mb-2 flex min-h-8 flex-wrap items-center justify-between gap-2">
      <h2 id={id} className="flex items-center gap-1.5 text-2xs font-semibold uppercase tracking-wider text-subtle [&_svg]:size-3.5">
        <span aria-hidden="true">{icon}</span>
        {children}
      </h2>
      {/* ml-auto keeps them on the right when a narrow screen wraps them below the title. */}
      {actions && <div className="ml-auto flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  );
}

/**
 * Search (Prowlarr) and Downloads (Transmission) in one place: side by side
 * on wide screens, tabs elsewhere or when the user prefers.
 */
export default function Torrents() {
  const { data: caps } = useCapabilities();
  const [params, setParams] = useSearchParams();
  const wide = useMedia("(min-width: 1280px)");
  const [layout, setLayoutState] = useState<Layout>(() => (readPref(LAYOUT_KEY) === "tabs" ? "tabs" : "split"));
  const setLayout = (l: Layout) => {
    setPref(LAYOUT_KEY, l);
    setLayoutState(l);
  };

  const hasSearch = !!caps?.prowlarr?.available;
  const hasDownloads = !!caps?.transmission?.available;
  const requested = params.get("tab") === "downloads" ? "downloads" : "search";
  const tab: Tab = !hasSearch ? "downloads" : !hasDownloads ? "search" : requested;
  const split = wide && layout === "split" && hasSearch && hasDownloads;

  const hidden = useHiddenFor(HIDDEN_TAB_DELAY_MS);
  const live = useTopicValue<{ stats?: { activeTorrentCount?: number } }>(hasDownloads && !hidden ? "transmission" : null);
  const active = live?.stats?.activeTorrentCount;

  // One Add dialog for the header button and for .torrent files dropped on Downloads.
  const [adding, setAdding] = useState(false);
  const [dropped, setDropped] = useState<File[]>([]);
  const openAdd = (files: File[]) => {
    setDropped(files);
    setAdding(true);
  };

  // Downloads' own actions, on its heading row above the card.
  const downloadActions = caps?.transmission?.configured ? (
    <>
      <RemoveFinishedSwitch available={hasDownloads} compact />
      <Button size="sm" variant="primary" icon={<Plus />} disabled={!hasDownloads} onClick={() => openAdd([])}>
        Add
      </Button>
    </>
  ) : undefined;
  const downloads = (
    <section aria-labelledby="torrents-downloads" className={cn(split && "sticky top-16")}>
      <ColumnHeading id="torrents-downloads" icon={<Download />} actions={downloadActions}>
        Downloads
      </ColumnHeading>
      <Downloads embedded onAddFiles={openAdd} />
    </section>
  );

  const setTab = (t: Tab) => {
    const next = new URLSearchParams(params);
    if (t === "search") next.delete("tab");
    else next.set("tab", t);
    setParams(next, { replace: true });
  };

  return (
    <div className="min-w-0">
      <PageHeader
        title="Torrents"
        description={
          hasSearch && hasDownloads
            ? "Search every Prowlarr indexer and manage Transmission downloads."
            : hasSearch
              ? "Search every Prowlarr indexer."
              : "Add and manage Transmission downloads."
        }
        actions={
          <>
            {hasSearch && hasDownloads && !split && (
              <Segmented<Tab>
                label="Section"
                value={tab}
                onChange={setTab}
                options={[
                  { value: "search", label: <><SearchIcon /> Search</> },
                  {
                    value: "downloads",
                    label: (
                      <>
                        <Download /> Downloads
                        {active ? <span className="num rounded bg-frost-soft px-1 text-2xs text-frost">{active}</span> : null}
                      </>
                    ),
                  },
                ]}
              />
            )}
            {wide && hasSearch && hasDownloads && (
              <Segmented<Layout>
                label="Layout"
                value={layout}
                onChange={setLayout}
                options={[
                  { value: "split", label: <Columns2 />, title: "Side by side" },
                  { value: "tabs", label: <Square />, title: "One at a time" },
                ]}
              />
            )}
          </>
        }
      />

      {split ? (
        <div className="grid grid-cols-[minmax(0,3fr)_minmax(0,2fr)] items-start gap-4">
          <section aria-labelledby="torrents-search">
            <ColumnHeading id="torrents-search" icon={<SearchIcon />}>
              Search
            </ColumnHeading>
            <SearchPage embedded />
          </section>
          {downloads}
        </div>
      ) : tab === "search" && hasSearch ? (
        <SearchPage embedded />
      ) : (
        downloads
      )}
      <AddDialog open={adding} onOpenChange={setAdding} initialFiles={dropped} />
    </div>
  );
}
