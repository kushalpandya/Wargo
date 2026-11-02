// SPDX-License-Identifier: AGPL-3.0-only
import { useState } from "react";
import { useSearchParams } from "react-router";
import { Columns2, Download, Search as SearchIcon, Square } from "lucide-react";
import { useCapabilities } from "@/app/session";
import { useTopicValue } from "@/lib/ws";
import { useMedia } from "@/lib/hooks";
import { PageHeader } from "@/components/ui/misc";
import { Segmented } from "@/components/ui/form";
import SearchPage from "./Search";
import Downloads from "./Downloads";

type Tab = "search" | "downloads";
type Layout = "tabs" | "split";

const LAYOUT_KEY = "wargo.torrents.layout";

/**
 * Search (Prowlarr) and Downloads (Transmission) in one place: side by side
 * on wide screens, tabs elsewhere or when the user prefers.
 */
export default function Torrents() {
  const { data: caps } = useCapabilities();
  const [params, setParams] = useSearchParams();
  const wide = useMedia("(min-width: 1280px)");
  const [layout, setLayoutState] = useState<Layout>(() => (localStorage.getItem(LAYOUT_KEY) === "tabs" ? "tabs" : "split"));
  const setLayout = (l: Layout) => {
    localStorage.setItem(LAYOUT_KEY, l);
    setLayoutState(l);
  };

  const hasSearch = !!caps?.prowlarr?.available;
  const hasDownloads = !!caps?.transmission?.available;
  const requested = params.get("tab") === "downloads" ? "downloads" : "search";
  const tab: Tab = !hasSearch ? "downloads" : !hasDownloads ? "search" : requested;
  const split = wide && layout === "split" && hasSearch && hasDownloads;

  const live = useTopicValue<{ stats?: { activeTorrentCount?: number } }>(hasDownloads ? "transmission" : null);
  const active = live?.stats?.activeTorrentCount;

  const setTab = (t: Tab) => {
    const next = new URLSearchParams(params);
    if (t === "search") next.delete("tab");
    else next.set("tab", t);
    setParams(next, { replace: true });
  };

  return (
    <div className="mx-auto max-w-[1600px]">
      <PageHeader
        title="Torrents"
        description={
          hasSearch && hasDownloads
            ? "Search every Prowlarr indexer and manage Transmission downloads."
            : hasSearch
              ? "Search every Prowlarr indexer."
              : "Manage Transmission downloads."
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
          <section aria-label="Search">
            <SearchPage embedded />
          </section>
          <section aria-label="Downloads" className="sticky top-16">
            <h2 className="mb-2 flex items-center gap-1.5 text-2xs font-semibold uppercase tracking-wider text-subtle">
              <Download className="size-3.5" aria-hidden="true" /> Downloads
            </h2>
            <Downloads embedded />
          </section>
        </div>
      ) : tab === "search" && hasSearch ? (
        <SearchPage embedded />
      ) : (
        <Downloads embedded />
      )}
    </div>
  );
}
