// SPDX-License-Identifier: AGPL-3.0-only
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, qs } from "@/lib/api";
import { useSettingsSave } from "@/features/settings/save";

export interface Place {
  name: string;
  region?: string;
  country?: string;
  lat: number;
  lon: number;
}

export interface WeatherReport {
  enabled: boolean;
  units: "metric" | "imperial";
  location?: Place;
  /** The location was guessed from the server's timezone. */
  auto?: boolean;
  current?: { temp: number; feels: number; code: number; isDay: boolean; humidity: number; wind: number };
  today?: { max: number; min: number };
  updatedAt?: number;
  error?: string;
}

export interface WeatherSettings {
  enabled: boolean;
  location?: Place | null;
  units: "metric" | "imperial";
}

export const weatherKey = ["weather"] as const;

/** Current weather; the server caches Open-Meteo for 15 minutes. */
export function useWeather(enabled = true) {
  return useQuery({
    queryKey: weatherKey,
    queryFn: () => api.get<WeatherReport>("/api/weather"),
    enabled,
    staleTime: 10 * 60_000,
    refetchInterval: 15 * 60_000,
  });
}

const settingsKey = ["weather", "settings"] as const;

export function useWeatherSettings() {
  return useQuery({ queryKey: settingsKey, queryFn: () => api.get<WeatherSettings>("/api/weather/settings") });
}

/** Saves changes to the weather settings, shown at once (see useSettingsSave). */
export function useSaveWeatherSettings() {
  const qc = useQueryClient();
  return useSettingsSave<WeatherSettings>({
    queryKey: settingsKey,
    scope: "weather-settings",
    put: (v) => api.put<WeatherSettings>("/api/weather/settings", v),
    onSaved: () => qc.invalidateQueries({ queryKey: weatherKey, exact: true }),
  });
}

/** Places matching a search term (at least 2 characters). */
export function usePlaceSearch(term: string) {
  return useQuery({
    queryKey: ["weather-search", term],
    queryFn: () => api.get<Place[]>(`/api/weather/search${qs({ q: term })}`),
    enabled: term.length >= 2,
    staleTime: 5 * 60_000,
  });
}

export const placeLabel = (p: Place) => [p.name, p.region, p.country].filter(Boolean).join(", ");
