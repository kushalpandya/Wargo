// SPDX-License-Identifier: AGPL-3.0-only
import { ApiError } from "@/lib/api";

export interface Category {
  id: number;
  name: string;
}

export interface SearchResult {
  id: string;
  title: string;
  size: number;
  seeders: number | null;
  leechers: number | null;
  grabs?: number | null;
  indexer: string;
  indexerId: number;
  age?: string;
  categories: Category[];
  infoHash?: string;
  hasMagnet: boolean;
  hasTorrent: boolean;
  infoUrl?: string;
}

export interface IndexerRef {
  id: number;
  name: string;
}

export interface IndexerError {
  indexerId: number;
  indexer: string;
  message: string;
}

export interface SearchResponse {
  results: SearchResult[];
  indexers: IndexerRef[];
  tookMs: number;
  errors: IndexerError[];
}

export type TorrentStatus = "stopped" | "check-wait" | "checking" | "download-wait" | "downloading" | "seed-wait" | "seeding";

export interface Torrent {
  id: number;
  hash: string;
  name: string;
  status: TorrentStatus;
  percentDone: number;
  rateDownload: number;
  rateUpload: number;
  eta: number;
  sizeWhenDone: number;
  leftUntilDone: number;
  uploadRatio: number;
  error: number;
  errorString: string;
  addedDate: number;
  peersConnected: number;
  downloadDir: string;
}

export interface TransmissionPayload {
  torrents: Torrent[];
  stats: {
    downloadSpeed: number;
    uploadSpeed: number;
    activeTorrentCount: number;
    pausedTorrentCount: number;
    freeSpace?: number;
  };
  error?: string;
  at: string;
}

export interface TransmissionClientHint {
  name: string;
  host: string;
  port: number;
  useSsl: boolean;
  urlBase: string;
  username: string;
}

export interface ProwlarrStatus {
  url: string;
  hasApiKey: boolean;
  configured: boolean;
  available: boolean;
  hasTransmissionClient: boolean;
  transmissionClient?: TransmissionClientHint;
  version?: string;
  error?: string;
}

export interface TransmissionStatus {
  url: string;
  username: string;
  hasPassword: boolean;
  configured: boolean;
  available: boolean;
  version?: string;
  error?: string;
}

export interface IntegrationsStatus {
  prowlarr: ProwlarrStatus;
  transmission: TransmissionStatus;
}

export interface Candidate {
  container: string;
  image: string;
  state: string;
  url?: string;
  networkVia?: string;
  note?: string;
  configPath?: string;
  configReadable?: boolean;
}

export interface Discovery {
  docker: { available: boolean; error?: string };
  prowlarr: Candidate[];
  transmission: Candidate[];
  transmissionFromProwlarr?: {
    name: string;
    url: string;
    host: string;
    port: number;
    username: string;
  };
}

export interface TestResult {
  ok: boolean;
  version?: string;
  error?: string;
  hasTransmissionClient?: boolean;
  transmissionClient?: TransmissionClientHint;
  url?: string;
}

/** Category filter presets (Newznab top-level ids). */
export const CATEGORIES: { id: number; label: string }[] = [
  { id: 2000, label: "Movies" },
  { id: 5000, label: "TV" },
  { id: 3000, label: "Audio" },
  { id: 7000, label: "Books" },
  { id: 4000, label: "PC / Software" },
  { id: 8000, label: "Other" },
];

export function errorMessage(e: unknown): string {
  if (e instanceof ApiError) return e.message;
  if (e instanceof Error) return e.message;
  return String(e);
}
