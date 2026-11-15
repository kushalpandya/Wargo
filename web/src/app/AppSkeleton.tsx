// SPDX-License-Identifier: AGPL-3.0-only
import { useLocation } from "react-router";
import { readPref } from "@/lib/prefs";
import { cn } from "@/lib/cn";
import { Skeleton } from "@/components/ui/misc";
import { Logo } from "./Logo";
import { TilePlaceholders, lastTileSize } from "@/features/launcher/layout";

/*
 * Placeholders built from the same markup as the real Home cards (app
 * tiles in features/launcher, system tiles in features/system/Tiles.tsx,
 * the Activity card in features/home/Feed.tsx), so each
 * one is exactly as tall as the card it stands in for at every breakpoint.
 * A text line keeps its real font size and line height; a grey bar sits in
 * it instead of the text. Keep these in step when those cards change.
 */

const pulse = "animate-pulse rounded bg-surface-3";

/** One line of text at the given text classes, shown as a bar of width w. */
function Line({ className, w }: { className?: string; w: string }) {
  return (
    <div className={className}>
      <span className={cn("inline-block h-[0.75em] align-middle", pulse, w)} />
    </div>
  );
}

/** Mirrors Tile: header, value, caption, then the 44 px sparkline. */
function TileSkeleton() {
  return (
    <section className="relative flex h-full min-w-0 flex-col overflow-hidden rounded-xl border border-border/60 bg-surface shadow-card">
      <div className="flex items-start gap-3 p-3 pb-1 sm:p-4 sm:pb-1">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 text-ui font-semibold">
            <span className={cn("size-7 shrink-0 rounded-lg", pulse)} />
            <Line w="w-16" />
          </div>
          {/* Value and unit share a baseline, as in Tile; the unit's line adds to the row's height. */}
          <div className="mt-3 flex min-w-0 items-baseline gap-1.5">
            <span className="num relative text-3xl font-bold leading-none tracking-tight sm:text-4xl">
              <span className="invisible">00</span>
              <span className={cn("absolute inset-x-0 top-[12%] bottom-[12%]", pulse)} />
            </span>
            <span className="num invisible text-xs font-medium sm:text-ui">%</span>
          </div>
          <Line className="mt-2 text-xs" w="w-4/5" />
        </div>
        <div className="max-sm:hidden">
          <span className={cn("block size-[54px] rounded-full", pulse)} />
        </div>
      </div>
      <div className="mt-auto pt-2">
        {/* The sparkline is an inline <svg>, so it sits on a text line like this one. */}
        <svg className="w-full" style={{ height: 44 }} aria-hidden="true" />
      </div>
    </section>
  );
}

/** Mirrors the feed card in its usual "All quiet" state. */
function FeedSkeleton() {
  return (
    <section className="flex h-full min-h-0 min-w-0 flex-col rounded-xl border border-border bg-surface shadow-card">
      <header className="flex min-h-14 items-center gap-3 border-b border-border/70 px-4 py-2.5">
        <span className={cn("size-8 shrink-0 rounded-lg", pulse)} />
        <Line className="text-md font-semibold" w="w-16" />
      </header>
      <div className="flex flex-col items-center gap-2 px-6 py-12 text-center">
        <span className={cn("size-7 rounded-full", pulse)} />
        <Line className="font-medium" w="w-20" />
        <Line className="text-sm" w="w-56 max-w-full" />
      </div>
    </section>
  );
}

/** Home's layout (apps, tiles, activity) as placeholders, while the page or its data loads. */
export function HomeSkeleton() {
  return (
    <div aria-hidden="true">
      <div className="home-grid">
        {/* Files and Terminal are always there, at the size this browser last saw; other apps load in after them. */}
        <div className="min-w-0 [grid-area:apps]">
          <TilePlaceholders size={lastTileSize()} />
        </div>
        <div className="min-w-0 [grid-area:tiles]">
          <div className="grid grid-cols-2 gap-3 sm:gap-4 xl:grid-cols-4">
            {[0, 1, 2, 3].map((i) => (
              <TileSkeleton key={i} />
            ))}
          </div>
        </div>
        <div className="min-w-0 [grid-area:feed]">
          <FeedSkeleton />
        </div>
      </div>
    </div>
  );
}

/** Any other page: a heading row and a card. */
function PageSkeleton() {
  return (
    <div className="space-y-4" aria-hidden="true">
      <Skeleton className="h-10 w-full rounded-lg" />
      <Skeleton className="h-80 w-full rounded-xl" />
    </div>
  );
}

/**
 * The app's frame (sidebar or phone top bar) with the current page as
 * placeholders, shown while the app checks the session at launch. It mirrors
 * Layout's sizes so nothing jumps when the real page replaces it.
 */
export function AppSkeleton() {
  const { pathname } = useLocation();
  const pref = readPref("wargo.sidebar");
  const collapsed = pref === "collapsed" || (pref !== "expanded" && !window.matchMedia("(min-width: 1024px)").matches);
  return (
    <div className="wargo-canvas flex min-h-dvh" role="status" aria-label="Loading Wargo">
      <aside
        className={cn(
          "sticky top-0 hidden h-dvh shrink-0 flex-col border-r border-border bg-surface pt-[env(safe-area-inset-top)] md:flex",
          collapsed ? "w-[4.25rem]" : "w-60",
        )}
      >
        <div className={cn("flex items-center gap-2.5", collapsed ? "justify-center py-3" : "h-16 px-4")}>
          <Logo className="size-8 shrink-0" />
          {!collapsed && <span className="text-lg font-bold tracking-tight">Wargo</span>}
        </div>
        <div className="flex flex-col gap-2 px-3 py-2">
          {[0, 1, 2, 3, 4, 5].map((i) => (
            <Skeleton key={i} className="h-8 rounded-md" />
          ))}
        </div>
      </aside>
      <div className="flex min-w-0 flex-1 flex-col">
        <div className="flex h-[calc(4rem+env(safe-area-inset-top))] shrink-0 items-center gap-3 px-3 pt-[env(safe-area-inset-top)] sm:px-5 lg:px-7">
          <Skeleton className="size-9 rounded-md md:hidden" />
          <div className="flex min-w-0 flex-col gap-1.5 md:flex-row md:items-center md:gap-2.5">
            <Line className="text-lg font-bold leading-tight" w="w-40" />
            {pathname === "/" && <Line className="text-xs md:text-sm" w="w-32" />}
          </div>
        </div>
        <div className="@container/main min-w-0 flex-1 px-3 pb-8 pt-1 sm:px-5 lg:px-7">
          {pathname === "/" ? <HomeSkeleton /> : <PageSkeleton />}
        </div>
      </div>
    </div>
  );
}
