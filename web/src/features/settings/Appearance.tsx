// SPDX-License-Identifier: AGPL-3.0-only
import type { ReactNode } from "react";
import { Check, Contrast, House, Moon, Paintbrush, Sun } from "lucide-react";
import { cn } from "@/lib/cn";
import { ACCENTS, setTheme, useAccent, useThemeMode, type Accent, type ThemeMode } from "@/lib/theme";
import { useSaveAccent } from "./accent";
import { Segmented, useRadioGroup } from "@/components/ui/form";
import { ErrorNote, Skeleton } from "@/components/ui/misc";
import { toast } from "@/components/ui/toast";
import { useLauncherSettings, useSaveLauncherSettings } from "@/features/launcher/api";
import type { TileSize } from "@/features/launcher/layout";
import { Card, CardBody, CardHeader } from "@/components/ui/card";
import { WeatherSettings } from "@/features/weather/WeatherSettings";
import { SettingRow, SettingsList, SettingsStack, StackedRow } from "./shared";

/** A tiny dashboard in the given palette (scoped with the `dark` class). */
function Preview({ dark }: { dark: boolean }) {
  return (
    <div className={cn(dark ? "dark" : "theme-light", "pointer-events-none")} aria-hidden="true">
      <div className="flex h-14 overflow-hidden rounded-md border border-border bg-bg">
        <div className="flex w-4 flex-col items-center gap-1 border-r border-border bg-surface py-1.5">
          <span className="size-2 rounded-[2px] bg-accent" />
          <span className="h-0.5 w-2 rounded-full bg-surface-3" />
          <span className="h-0.5 w-2 rounded-full bg-surface-3" />
        </div>
        <div className="flex flex-1 flex-col gap-1 p-1.5">
          <div className="h-1 w-8 rounded-full bg-fg/70" />
          <div className="grid flex-1 grid-cols-2 gap-1">
            {[0.6, 0.35].map((v, i) => (
              <div key={i} className="flex flex-col justify-end rounded-[3px] border border-border bg-surface p-1">
                <div className="h-0.5 overflow-hidden rounded-full bg-surface-3">
                  <div className={cn("h-full rounded-full", i ? "bg-frost" : "bg-ok")} style={{ width: `${v * 100}%` }} />
                </div>
              </div>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}

/** Light on the left, dark on the right: "Match system". */
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

// Same order as the switch at the bottom of the sidebar.
const modes: { value: ThemeMode; label: string; title: string; icon: ReactNode }[] = [
  { value: "light", label: "Light", title: "Light", icon: <Sun /> },
  { value: "auto", label: "Auto", title: "Match system", icon: <Contrast /> },
  { value: "dark", label: "Dark", title: "Dark", icon: <Moon /> },
];

function ColourMode() {
  const mode = useThemeMode();
  const radio = useRadioGroup(
    modes.map((m) => m.value),
    mode,
    setTheme,
  );
  return (
    <div {...radio.group} aria-label="Colour mode" className="grid max-w-md grid-cols-3 gap-2">
      {modes.map((m) => {
        const active = mode === m.value;
        return (
          <button
            key={m.value}
            type="button"
            {...radio.item(m.value)}
            onClick={() => setTheme(m.value)}
            aria-label={m.title}
            title={m.title}
            className={cn(
              "flex min-w-0 flex-col gap-1.5 rounded-lg border p-1.5 text-left transition-colors",
              active ? "border-accent bg-accent-soft/40 ring-1 ring-accent" : "border-border hover:border-border-strong hover:bg-surface-2",
            )}
          >
            {m.value === "auto" ? <SplitPreview /> : <Preview dark={m.value === "dark"} />}
            <span className="flex items-center gap-1.5 px-0.5 text-ui font-medium">
              <span className={cn("[&_svg]:size-3.5", active ? "text-accent" : "text-subtle")}>{m.icon}</span>
              {m.label}
            </span>
          </button>
        );
      })}
    </div>
  );
}

/** How Home looks: saved on the server, so it is the same in every browser. */
function HomeCard() {
  const { data, isLoading, error } = useLauncherSettings();
  const save = useSaveLauncherSettings();
  const size = save.isPending && save.variables ? save.variables.tileSize : data?.tileSize;
  return (
    <Card>
      <CardHeader title="Home" icon={<House />} tint="mint" />
      <CardBody>
        {isLoading ? (
          <Skeleton className="h-10" />
        ) : error ? (
          <ErrorNote error={error} />
        ) : (
          <SettingsList>
            <SettingRow title="App tiles" description="Large squares, or small tiles with the icon beside the name so more fit on a line.">
              <Segmented<TileSize>
                label="App tile size"
                size="sm"
                value={size ?? "large"}
                onChange={(tileSize) => save.mutate({ tileSize }, { onError: (e) => toast.error("Could not save tile size", e) })}
                options={[
                  { value: "large", label: "Large" },
                  { value: "small", label: "Small" },
                ]}
              />
            </SettingRow>
          </SettingsList>
        )}
      </CardBody>
    </Card>
  );
}

function AccentPicker() {
  const accent = useAccent();
  const save = useSaveAccent();
  const pick = (a: Accent) => save.mutate(a, { onError: (e) => toast.error("Could not save accent colour", e) });
  const radio = useRadioGroup(
    ACCENTS.map((a) => a.id),
    accent,
    pick,
  );
  return (
    <div {...radio.group} aria-label="Accent colour" className="flex flex-wrap gap-2">
      {ACCENTS.map((a) => {
        const active = accent === a.id;
        return (
          <button
            key={a.id}
            type="button"
            {...radio.item(a.id)}
            onClick={() => pick(a.id)}
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
  );
}

/** Colour mode and accent colour, together. */
function LookAndFeelCard() {
  return (
    <Card>
      <CardHeader title="Look & Feel" icon={<Paintbrush />} tint="iris" />
      <CardBody>
        <SettingsList>
          <StackedRow title="Colour mode">
            <ColourMode />
          </StackedRow>
          <StackedRow title="Accent colour">
            <AccentPicker />
          </StackedRow>
        </SettingsList>
      </CardBody>
    </Card>
  );
}

export default function Appearance() {
  return (
    <SettingsStack>
      <LookAndFeelCard />
      <HomeCard />
      <WeatherSettings />
    </SettingsStack>
  );
}
