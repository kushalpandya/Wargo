// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useState } from "react";
import { CloudSun, MapPin } from "lucide-react";
import { Card, CardBody, CardHeader } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { SearchInput, Segmented, Switch } from "@/components/ui/form";
import { ErrorNote, Skeleton, Spinner } from "@/components/ui/misc";
import { SettingRow, SettingsList, usePatchSave } from "@/features/settings/shared";
import { placeLabel, usePlaceSearch, useSaveWeatherSettings, useWeather, useWeatherSettings, type WeatherSettings as WS } from "./api";

/** Settings → Appearance: weather on Home (Open-Meteo), units and location. */
export function WeatherSettings() {
  const { data, isLoading, error } = useWeatherSettings();
  const report = useWeather(!!data?.enabled);
  const save = useSaveWeatherSettings();
  const [q, setQ] = useState("");
  const [term, setTerm] = useState("");
  useEffect(() => {
    const t = setTimeout(() => setTerm(q.trim()), 350);
    return () => clearTimeout(t);
  }, [q]);
  const results = usePlaceSearch(term);
  const [v, update] = usePatchSave<WS>(data, save);
  const where = report.data?.location;

  return (
    <Card>
      <CardHeader title="Weather" icon={<CloudSun />} tint="sky" meta="from Open-Meteo" />
      <CardBody>
        {isLoading ? (
          <Skeleton className="h-24" />
        ) : error ? (
          <ErrorNote error={error} />
        ) : v ? (
          <SettingsList>
            <SettingRow title="Show the weather on Home" description="Looked up from open-meteo.com while Home is open, at most every 15 minutes. No account or key.">
              <Switch checked={v.enabled} onCheckedChange={(enabled) => update({ enabled })} aria-label="Show the weather on Home" />
            </SettingRow>
            {v.enabled && (
              <SettingRow title="Units">
                <Segmented
                  label="Units"
                  value={v.units}
                  onChange={(units) => update({ units })}
                  options={[
                    { value: "metric", label: "°C, km/h" },
                    { value: "imperial", label: "°F, mph" },
                  ]}
                />
              </SettingRow>
            )}
            {v.enabled && (
              <div className="flex flex-col gap-2 pt-3">
                <SettingRow
                  className="pb-0"
                  title="Location"
                  description={
                    <span className="block truncate">
                      {where ? placeLabel(where) : report.isLoading ? "…" : "Not set"}
                      {v.location ? "" : " · from the server's timezone"}
                    </span>
                  }
                >
                  {v.location && (
                    <Button size="sm" variant="ghost" onClick={() => update({ location: null })}>
                      Use the server's timezone
                    </Button>
                  )}
                </SettingRow>
                <SearchInput value={q} onChange={setQ} placeholder="Search for a city…" label="Search for a city" />
                {term.length >= 2 && (
                  <div className="rounded-md border border-border" aria-live="polite">
                    {results.isLoading ? (
                      <div className="flex items-center gap-2 p-3 text-xs text-muted">
                        <Spinner className="size-3.5" /> Searching…
                      </div>
                    ) : results.error ? (
                      <div className="p-3">
                        <ErrorNote error={results.error} />
                      </div>
                    ) : !results.data?.length ? (
                      <p className="p-3 text-xs text-muted">No places match “{term}”.</p>
                    ) : (
                      <ul className="max-h-56 divide-y divide-border overflow-y-auto" aria-label="Places">
                        {results.data.map((p) => (
                          <li key={`${p.lat},${p.lon}`}>
                            <button
                              type="button"
                              onClick={() => {
                                update({ location: p });
                                setQ("");
                                setTerm("");
                              }}
                              className="flex w-full items-center gap-2 px-3 py-2 text-left text-ui hover:bg-surface-2 focus-visible:bg-surface-2 focus-visible:outline-none"
                            >
                              <MapPin className="size-3.5 shrink-0 text-subtle" aria-hidden="true" />
                              <span className="truncate">{placeLabel(p)}</span>
                            </button>
                          </li>
                        ))}
                      </ul>
                    )}
                  </div>
                )}
              </div>
            )}
          </SettingsList>
        ) : null}
      </CardBody>
    </Card>
  );
}
