// SPDX-License-Identifier: AGPL-3.0-only
import type { ReactNode } from "react";
import { Check, Monitor, Moon, Sun } from "lucide-react";
import { cn } from "@/lib/cn";
import { setTheme, useIsDark, useThemeMode, type ThemeMode } from "@/lib/theme";
import { Card, CardBody, CardHeader } from "@/components/ui/card";
import { SettingsHeader } from "./shared";

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
  { value: "auto", label: "Match system", icon: <Monitor />, hint: "Follows your device's light or dark setting" },
  { value: "light", label: "Light", icon: <Sun />, hint: "Bright surfaces, best in daylight" },
  { value: "dark", label: "Dark", icon: <Moon />, hint: "Low-glare slate palette" },
];

export default function Appearance() {
  const mode = useThemeMode();
  const dark = useIsDark();
  return (
    <>
      <SettingsHeader title="Appearance" description="Theme is stored in this browser only." />
      <Card>
        <CardHeader title="Theme" meta={mode === "auto" ? `currently ${dark ? "dark" : "light"}` : undefined} />
        <CardBody>
          <div role="radiogroup" aria-label="Theme" className="grid gap-3 sm:grid-cols-3">
            {options.map((o) => {
              const active = mode === o.value;
              return (
                <button
                  key={o.value}
                  type="button"
                  role="radio"
                  aria-checked={active}
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
    </>
  );
}
