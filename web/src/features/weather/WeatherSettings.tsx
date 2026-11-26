// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useState } from "react";
import { CloudSun, MapPin } from "lucide-react";
import { Card, CardBody, CardHeader } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { SearchInput, Segmented } from "@/components/ui/form";
import { ErrorNote, Spinner } from "@/components/ui/misc";
import { enabledToggle } from "@/features/settings/save";
import { SettingRow, SettingsList } from "@/features/settings/shared";
import { placeLabel, usePlaceSearch, useSaveWeatherSettings, useWeather, useWeatherSettings } from "./api";

/** Settings → Appearance: weather on Home (Open-Meteo), units and location. */
export function WeatherSettings() {
  // Shows a change as soon as it is made (see useSettingsSave).
  const { data: v, error } = useWeatherSettings();
  const report = useWeather(!!v?.enabled);
  const update = useSaveWeatherSettings();
  const [q, setQ] = useState("");
  const [term, setTerm] = useState("");
  useEffect(() => {
    const t = setTimeout(() => setTerm(q.trim()), 350);
    return () => clearTimeout(t);
  }, [q]);
  const results = usePlaceSearch(term);
  const where = report.data?.location;

  return (
    <Card>
      <CardHeader
        title="Weather"
        icon={<CloudSun />}
        tint="sky"
        meta={v && !v.enabled ? "Forecast on Home, from open-meteo.com" : undefined}
        toggle={enabledToggle("Show the weather on Home", v, update, error)}
      />
      {error && !v ? (
        <CardBody>
          <ErrorNote error={error} />
        </CardBody>
      ) : (
        v?.enabled && (
          <CardBody>
            <SettingsList>
              <SettingRow title="Units">
                <Segmented
                  label="Units"
                  size="sm"
                  value={v.units}
                  onChange={(units) => update({ units })}
                  options={[
                    { value: "metric", label: "°C, km/h" },
                    { value: "imperial", label: "°F, mph" },
                  ]}
                />
              </SettingRow>
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
            </SettingsList>
          </CardBody>
        )
      )}
    </Card>
  );
}
