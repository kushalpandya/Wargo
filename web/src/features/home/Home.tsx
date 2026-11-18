// SPDX-License-Identifier: AGPL-3.0-only
import { PageHeader } from "@/components/ui/misc";
import { useMetrics } from "@/features/system/useMetrics";
import { WeatherInline } from "@/features/weather/Weather";
import { LauncherStrip } from "./LauncherStrip";
import { SummaryTiles } from "@/features/system/Tiles";
import { Feed } from "./Feed";

function greeting(d = new Date()) {
  const h = d.getHours();
  return h < 5 ? "Good night" : h < 12 ? "Good morning" : h < 18 ? "Good afternoon" : "Good evening";
}

/** Greeting and the local weather, in the top bar. */
function Hero() {
  return <PageHeader title={greeting()} inline description={<WeatherInline />} />;
}

export default function Home() {
  const { latest, points } = useMetrics();
  return (
    // Apps (Files, Terminal and the user's), system tiles, then activity:
    // see .home-grid in styles/index.css. The host card is on About.
    <>
      <Hero />
      <div className="home-grid">
        <div className="min-w-0 [grid-area:apps]">
          <LauncherStrip />
        </div>
        <div className="min-w-0 [grid-area:tiles]">
          <SummaryTiles snap={latest} points={points} />
        </div>
        <div className="min-w-0 [grid-area:feed]">
          <Feed />
        </div>
      </div>
    </>
  );
}
