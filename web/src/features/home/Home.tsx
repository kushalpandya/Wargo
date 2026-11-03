// SPDX-License-Identifier: AGPL-3.0-only
import { useCallback } from "react";
import { Link } from "react-router";
import { FolderTree, PanelRightOpen, SquareTerminal } from "lucide-react";
import { usePref } from "@/lib/prefs";
import { useCapabilities } from "@/app/session";
import { Button } from "@/components/ui/button";
import { PageHeader } from "@/components/ui/misc";
import { useMetrics } from "@/features/system/useMetrics";
import { WeatherInline } from "@/features/weather/Weather";
import { LauncherStrip } from "./LauncherStrip";
import { SummaryTiles } from "@/features/system/Tiles";
import { SpecsCard } from "./SpecsCard";
import { Feed } from "./Feed";

function greeting(d = new Date()) {
  const h = d.getHours();
  return h < 5 ? "Good night" : h < 12 ? "Good morning" : h < 18 ? "Good afternoon" : "Good evening";
}

/** Greeting and the local weather, in the top bar. */
function Hero({ onShowFeed }: { onShowFeed?: () => void }) {
  return (
    <PageHeader
      title={greeting()}
      inline
      description={<WeatherInline />}
      actions={
        <>
          {onShowFeed && (
            <Button size="sm" variant="ghost" icon={<PanelRightOpen />} onClick={onShowFeed}>
              Feed
            </Button>
          )}
          <Button asChild size="sm">
            <Link to="/files">
              <FolderTree /> Files
            </Link>
          </Button>
          <Button asChild size="sm">
            <Link to="/terminal">
              <SquareTerminal /> Terminal
            </Link>
          </Button>
        </>
      }
    />
  );
}

export default function Home() {
  const { latest, points } = useMetrics();
  const { data: caps } = useCapabilities();
  const apps = !!caps?.launcher?.enabled && caps.launcher.placement === "overview";
  const [feedPref] = usePref("wargo.home.feed", "on", ["on", "off"] as const);
  const [collapsed, setCollapsed] = usePref("wargo.home.feedCollapsed", "no", ["no", "yes"] as const);
  const feed = feedPref === "on" && collapsed === "no";
  const collapse = useCallback(() => setCollapsed("yes"), [setCollapsed]);
  return (
    // A bento grid that follows the space available (container queries on
    // <main>), not the window: see .home-grid in styles/index.css.
    <>
      <Hero onShowFeed={feedPref === "on" && collapsed === "yes" ? () => setCollapsed("no") : undefined} />
      <div className="home-grid" data-apps={apps || undefined} data-feed={feed || undefined}>
        <div className="min-w-0 [grid-area:tiles]">
          <SummaryTiles snap={latest} points={points} link />
        </div>
        {apps && (
          <div className="min-w-0 [grid-area:apps]">
            <LauncherStrip />
          </div>
        )}
        <div className="min-w-0 [grid-area:specs]">
          <SpecsCard snap={latest} />
        </div>
        {feed && (
          <div className="home-feed min-w-0 [grid-area:feed]">
            <Feed onCollapse={collapse} />
          </div>
        )}
      </div>
    </>
  );
}
