// SPDX-License-Identifier: AGPL-3.0-only
import type { ReactNode } from "react";
import { Check, Contrast, House, Moon, Paintbrush, Sun } from "lucide-react";
import { cn } from "@/lib/cn";
import { ACCENTS, setAccent, setTheme, useAccent, useIsDark, useThemeMode, type ThemeMode } from "@/lib/theme";
import { usePref } from "@/lib/prefs";
import { Switch, useRadioGroup } from "@/components/ui/form";
import { Card, CardBody, CardHeader } from "@/components/ui/card";
import { WeatherSettings } from "@/features/weather/WeatherSettings";
import { SettingsStack } from "./shared";

/** Miniature dashboard drawn in the given palette (scoped with the `dark` class). */
function Preview({ dark }: { dark: boolean }) {
  return (
    <div className={cn(dark ? "dark" : "theme-light", "pointer-events-none")} aria-hidden="true">
      <div className="flex h-24 overflow-hidden rounded-md border border-border bg-bg text-fg">
        <div className="flex w-7 flex-col items-center gap-1.5 border-r border-border bg-surface py-2">
          <span className="size-3 rounded-sm bg-accent" />
          <span className="h-1 w-3.5 rounded-full bg-surface-3" />
          <span className="h-1 w-3.5 rounded-full bg-surface-3" />
          <span className="h-1 w-3.5 rounded-full bg-surface-3" />
        </div>
        <div className="flex flex-1 flex-col gap-1.5 p-2">
          <div className="h-1.5 w-12 rounded-full bg-fg/70" />
          <div className="grid flex-1 grid-cols-3 gap-1.5">
            {[0.62, 0.35, 0.8].map((v, i) => (
              <div key={i} className="flex flex-col justify-between rounded border border-border bg-surface p-1.5">
                <div className="h-1 w-6 rounded-full bg-muted/60" />
                <div className="h-1 overflow-hidden rounded-full bg-surface-3">
                  <div className={cn("h-full rounded-full", i === 2 ? "bg-warn" : i === 1 ? "bg-frost" : "bg-ok")} style={{ width: `${v * 100}%` }} />
                </div>
              </div>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}

function SplitPreview() {
  return (
    <div className="relative">
      <Preview dark={false} />
      <div className="absolute inset-0 [clip-path:polygon(55%_0,100%_0,100%_100%,45%_100%)]">
        <Preview dark />
      </div>
    </div>
  );
}

const options: { value: ThemeMode; label: string; icon: ReactNode; hint: string }[] = [
  { value: "auto", label: "Match system", icon: <Contrast />, hint: "Follows your device's light or dark setting" },
  { value: "light", label: "Light", icon: <Sun />, hint: "Bright surfaces, best in daylight" },
  { value: "dark", label: "Dark", icon: <Moon />, hint: "Deep, low-glare colours for night" },
];

function AccentCard() {
  const accent = useAccent();
  const radio = useRadioGroup(
    ACCENTS.map((a) => a.id),
    accent,
    setAccent,
  );
  return (
    <Card>
      <CardHeader title="Accent colour" icon={<Paintbrush />} tint="iris" meta="this browser" />
      <CardBody>
        <div {...radio.group} aria-label="Accent colour" className="flex flex-wrap gap-2">
          {ACCENTS.map((a) => {
            const active = accent === a.id;
            return (
              <button
                key={a.id}
                type="button"
                {...radio.item(a.id)}
                onClick={() => setAccent(a.id)}
                className={cn(
                  "inline-flex h-9 items-center gap-2 rounded-md border px-3 text-ui font-medium transition-colors",
                  active ? "border-accent bg-accent-soft text-fg" : "border-border text-muted hover:border-border-strong hover:text-fg",
                )}
              >
                <span className="size-4 rounded-full ring-1 ring-black/10" style={{ background: a.swatch }} aria-hidden="true" />
                {a.label}
                {active && <Check className="size-3.5 text-accent" aria-hidden="true" />}
              </button>
            );
          })}
        </div>
      </CardBody>
    </Card>
  );
}

/** What Home shows (per browser). */
function HomeCard() {
  const [feed, setFeed] = usePref("wargo.home.feed", "on", ["on", "off"] as const);
  return (
    <Card>
      <CardHeader title="Home" icon={<House />} tint="mint" meta="this browser" />
      <CardBody>
        <Switch
          checked={feed === "on"}
          onCheckedChange={(on) => setFeed(on ? "on" : "off")}
          label="Show the feed"
          description="Updates, containers needing attention, downloads, recent files and system errors, beside the rest of Home."
        />
      </CardBody>
    </Card>
  );
}

export default function Appearance() {
  const mode = useThemeMode();
  const dark = useIsDark();
  const radio = useRadioGroup(
    options.map((o) => o.value),
    mode,
    setTheme,
  );
  return (
    <SettingsStack>
      <Card>
        <CardHeader title="Theme" meta={mode === "auto" ? `currently ${dark ? "dark" : "light"}` : undefined} />
        <CardBody>
          <div {...radio.group} aria-label="Theme" className="grid gap-3 sm:grid-cols-3">
            {options.map((o) => {
              const active = mode === o.value;
              return (
                <button
                  key={o.value}
                  type="button"
                  {...radio.item(o.value)}
                  onClick={() => setTheme(o.value)}
                  className={cn(
                    "flex flex-col gap-2.5 rounded-lg border p-2.5 text-left transition-colors",
                    active ? "border-accent bg-accent-soft/40 ring-1 ring-accent" : "border-border hover:border-border-strong hover:bg-surface-2",
                  )}
                >
                  {o.value === "auto" ? <SplitPreview /> : <Preview dark={o.value === "dark"} />}
                  <div className="flex items-start gap-2 px-0.5">
                    <span className={cn("mt-0.5 [&_svg]:size-4", active ? "text-accent" : "text-subtle")}>{o.icon}</span>
                    <div className="min-w-0 flex-1">
                      <div className="text-sm font-medium">{o.label}</div>
                      <div className="text-xs text-muted">{o.hint}</div>
                    </div>
                    {active && <Check className="size-4 shrink-0 text-accent" aria-hidden="true" />}
                  </div>
                </button>
              );
            })}
          </div>
        </CardBody>
      </Card>
      <AccentCard />
      <WeatherSettings />
      <HomeCard />
    </SettingsStack>
  );
}
