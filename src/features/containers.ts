// SPDX-License-Identifier: AGPL-3.0-only
// Containers: list, details, start/stop/restart, image update checks and
// updates (as jobs), logos, live stats and the logs WebSocket. Based on
// CONTAINERS in host.ts; changes persist in this browser.
import { delay, ws, type RequestHandler, type WebSocketHandler } from "msw";
import type { Container, ContainerDetails, ContainerStats, LogLine, Mount, Port, Probe, UpdateCheck, UpdatesStatus } from "@/features/containers/types";
import { api, badRequest, body, fail, json, notFound, query } from "../core/http";
import { persisted } from "../core/store";
import { refresh, topic } from "../core/bus";
import { startJob, type JobStep } from "../core/jobs";
import { requireApproval } from "../core/elevation";
import { DAY, GB, HOUR, MB, MIN, pick, seeded, Walk } from "../core/rand";
import { CONTAINERS, HOST, HOME, type DemoContainer } from "./host";
import { iconResponse } from "./icons";

// ---------- small helpers ----------

function hash(s: string): number {
  let h = 2166136261;
  for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), 16777619);
  return h >>> 0;
}

/** Deterministic hex string for a seed (container ids, digests). */
function hex(seed: string, n = 64): string {
  const r = seeded(hash(seed));
  let s = "";
  while (s.length < n) s += Math.floor(r() * 16).toString(16);
  return s;
}

const sha = (seed: string) => "sha256:" + hex(seed);
const short = (id: string) => id.replace(/^sha256:/, "").slice(0, 12);

/** Docker's RFC 3339 timestamps carry nanoseconds. */
function nanoTs(ms: number): string {
  const iso = new Date(ms).toISOString();
  return iso.replace(/Z$/, String(Math.floor(Math.random() * 1e6)).padStart(6, "0") + "Z");
}

/** "lscr.io/linuxserver/plex:latest" -> ["lscr.io/linuxserver/plex", "latest"] (like the Go splitRef). */
function splitRef(image: string): [string, string] {
  const at = image.indexOf("@");
  if (at >= 0) return [image.slice(0, at), ""];
  const slash = image.lastIndexOf("/");
  const colon = image.lastIndexOf(":");
  if (colon > slash) return [image.slice(0, colon), image.slice(colon + 1)];
  return [image, "latest"];
}

/** Docker's units.HumanDuration ("About an hour", "3 weeks"). */
function human(ms: number): string {
  const s = Math.floor(ms / 1000);
  if (s < 1) return "Less than a second";
  if (s === 1) return "1 second";
  if (s < 60) return `${s} seconds`;
  const m = Math.round(ms / MIN);
  if (m === 1) return "About a minute";
  if (ms < HOUR) return `${m} minutes`;
  const h = Math.round(ms / HOUR);
  if (h === 1) return "About an hour";
  if (ms < 48 * HOUR) return `${h} hours`;
  if (ms < 14 * DAY) return `${Math.floor(ms / DAY)} days`;
  if (ms < 90 * DAY) return `${Math.floor(ms / (7 * DAY))} weeks`;
  if (ms < 730 * DAY) return `${Math.floor(ms / (30 * DAY))} months`;
  return `${Math.floor(ms / (365 * DAY))} years`;
}

const p2 = (n: number) => String(n).padStart(2, "0");
const p3 = (n: number) => String(n).padStart(3, "0");
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
const d = (t: number) => new Date(t);
/** 2026-11-03 10:15:42 */
const ymdhms = (t: number) => d(t).toISOString().slice(0, 19).replace("T", " ");
const ymdhmsms = (t: number) => `${ymdhms(t)}.${p3(d(t).getUTCMilliseconds())}`;
const hms = (t: number) => d(t).toISOString().slice(11, 19);
const rnd = (lo: number, hi: number) => Math.floor(lo + Math.random() * (hi - lo + 1));
const LAN = ["192.168.1.34", "192.168.1.41", "192.168.1.52", "192.168.1.17", "100.101.7.22"];

// ---------- per-app profiles ----------

type Stream = LogLine["stream"];
type Gen = (t: number) => [Stream, string];

interface Health {
  test: string[];
  intervalS: number;
  timeoutS: number;
  retries: number;
  ok: string;
}

interface PortSpec {
  priv: number;
  pub?: number;
  type?: "tcp" | "udp";
}

interface Profile {
  service: string;
  entrypoint: string[] | null;
  cmd: string[] | null;
  ports: PortSpec[];
  hostNet?: boolean;
  env: string[];
  mounts: Mount[];
  imageLabels: Record<string, string>;
  health?: Health;
  imageSize: number;
  createdDaysAgo: number;
  /** Started this long ago (default: at boot). */
  startedAgo?: number;
  /** Stopped this long ago (exited containers). */
  finishedAgo?: number;
  restart: string;
  user?: string;
  workingDir?: string;
  privileged?: boolean;
  capAdd?: string[];
  devices?: number;
  memLimit?: number;
  nanoCpus?: number;
  pidsLimit?: number;
  dependsOn?: string;
  cpu: number;
  mem: number;
  rx: number;
  tx: number;
  probeHttp?: number;
  /** Exit code when stopped by the user. */
  stopCode?: number;
  /** Root-owned compose files (compose updates need approval). */
  rootCompose?: boolean;
  /** linuxserver.io image: version label carries the build. */
  lsio?: "dotnet" | "native";
  boot: (t: number) => [Stream, string][];
  line: Gen;
  stop?: (t: number) => [Stream, string][];
  error?: Gen;
}

const LSIO_ENV = ["HOME", "LANG", "LANGUAGE", "LSIO_FIRST_PARTY", "PATH", "PGID", "PUID", "S6_CMD_WAIT_FOR_SERVICES_MAXTIME", "S6_STAGE2_HOOK", "S6_VERBOSITY", "TERM", "TZ", "UMASK", "VIRTUAL_ENV", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME"];

function lsioLabels(title: string, src: string): Record<string, string> {
  return {
    maintainer: "thespad",
    "org.opencontainers.image.authors": "linuxserver.io",
    "org.opencontainers.image.description": `${title} container image by linuxserver.io`,
    "org.opencontainers.image.documentation": `https://docs.linuxserver.io/images/docker-${src}`,
    "org.opencontainers.image.licenses": "GPL-3.0-only",
    "org.opencontainers.image.source": `https://github.com/linuxserver/docker-${src}`,
    "org.opencontainers.image.title": title,
    "org.opencontainers.image.url": `https://github.com/linuxserver/docker-${src}/packages`,
    "org.opencontainers.image.vendor": "linuxserver.io",
  };
}

const lsioBoot = (app: string): ((t: number) => [Stream, string][]) => () => [
  ["out", "[migrations] started"],
  ["out", "[migrations] no migrations found"],
  ["out", "───────────────────────────────────────"],
  ["out", "      ██╗     ███████╗██╗ ██████╗"],
  ["out", "      ██║     ██╔════╝██║██╔═══██╗"],
  ["out", "      ██║     ███████╗██║██║   ██║"],
  ["out", "      ██║     ╚════██║██║██║   ██║"],
  ["out", "      ███████╗███████║██║╚██████╔╝"],
  ["out", "      ╚══════╝╚══════╝╚═╝ ╚═════╝"],
  ["out", "   Brought to you by linuxserver.io"],
  ["out", "───────────────────────────────────────"],
  ["out", "User UID:    1000"],
  ["out", "User GID:    1000"],
  ["out", "───────────────────────────────────────"],
  ["out", "[custom-init] No custom files found, skipping..."],
  ["out", `[ls.io-init] done.`],
  ...(app ? ([["out", app]] as [Stream, string][]) : []),
];

const lsioStop = (): [Stream, string][] => [
  ["out", "s6-rc: info: service legacy-services: stopping"],
  ["out", "s6-rc: info: service legacy-services successfully stopped"],
  ["out", "s6-rc: info: service init-mods: stopping"],
  ["out", "s6-rc: info: service s6rc-oneshot-runner: stopping"],
  ["out", "s6-rc: info: service s6rc-oneshot-runner successfully stopped"],
];

const bind = (source: string, destination: string, rw = true): Mount => ({ type: "bind", source, destination, mode: rw ? "rw" : "ro", rw });
const vol = (name: string, destination: string): Mount => ({ type: "volume", name, source: `/var/lib/docker/volumes/${name}/_data`, destination, mode: "z", rw: true });
const stack = (p: string, sub: string) => `${HOME}/stacks/${p}/${sub}`;

const SHOWS = ["Severance", "The Bear", "Shogun", "Slow Horses", "Andor", "Silo", "The Last of Us", "Fallout", "Blue Eye Samurai"];
const MOVIES = ["Dune: Part Two (2024)", "Oppenheimer (2023)", "Past Lives (2023)", "The Holdovers (2023)", "Civil War (2024)", "Furiosa (2024)", "Perfect Days (2023)", "Arrival (2016)"];
const ISOS = ["debian-12.8.0-amd64-netinst.iso", "ubuntu-24.04.1-desktop-amd64.iso", "Fedora-Workstation-Live-x86_64-41-1.4.iso", "archlinux-2024.11.01-x86_64.iso", "linuxmint-22-cinnamon-64bit.iso", "proxmox-ve_8.3-1.iso"];
const INDEXERS = ["1337x", "Nyaa.si", "TorrentGalaxy", "LimeTorrents", "EZTV", "YTS"];
const DOMAINS = ["api.github.com", "www.youtube.com", "i.ytimg.com", "time.cloudflare.com", "connectivitycheck.gstatic.com", "graph.facebook.com", "dns.msftncsi.com", "www.netflix.com", "app-measurement.com", "ocsp.digicert.com", "registry-1.docker.io", "ghcr.io", "doubleclick.net", "api.spotify.com"];
const ep = () => `S0${rnd(1, 3)}E${p2(rnd(1, 10))}`;

const jfTs = (t: number) => `[${ymdhmsms(t)} +00:00]`;
const arr = (lvl: string, src: string, msg: string): [Stream, string] => ["out", `[${lvl}] ${src}: ${msg}`];
const nest = (t: number, lvl: string, ctx: string, msg: string): [Stream, string] => {
  const dt = d(t);
  const h = dt.getUTCHours();
  return ["out", `[Nest] 7  - ${p2(dt.getUTCMonth() + 1)}/${p2(dt.getUTCDate())}/${dt.getUTCFullYear()}, ${((h + 11) % 12) + 1}:${p2(dt.getUTCMinutes())}:${p2(dt.getUTCSeconds())} ${h < 12 ? "AM" : "PM"}     ${lvl.padStart(4)} [${ctx}] ${msg}`];
};
const pg = (t: number, msg: string): [Stream, string] => ["err", `${ymdhmsms(t)} UTC [${rnd(60, 4000)}] ${msg}`];
const redisTs = (t: number) => {
  const dt = d(t);
  return `${p2(dt.getUTCDate())} ${MONTHS[dt.getUTCMonth()]} ${dt.getUTCFullYear()} ${hms(t)}.${p3(dt.getUTCMilliseconds())}`;
};
const apacheTs = (t: number) => {
  const dt = d(t);
  return `${p2(dt.getUTCDate())}/${MONTHS[dt.getUTCMonth()]}/${dt.getUTCFullYear()}:${hms(t)} +0000`;
};
const ha = (t: number, lvl: string, src: string, msg: string): [Stream, string] => [lvl === "INFO" ? "out" : "err", `${ymdhmsms(t)} ${lvl} (MainThread) [${src}] ${msg}`];
const vw = (t: number, msg: string): [Stream, string] => ["out", `[${ymdhmsms(t)}]${msg}`];
const kuma = (t: number, lvl: string, msg: string): [Stream, string] => ["out", `${d(t).toISOString().slice(0, 19)}+00:00 [MONITOR] ${lvl}: ${msg}`];
const ppl = (t: number, lvl: string, src: string, msg: string): [Stream, string] => ["out", `[${ymdhms(t)},${p3(d(t).getUTCMilliseconds())}] [${lvl}] [${src}] ${msg}`];

const PROFILES: Record<string, Profile> = {
  jellyfin: {
    service: "jellyfin",
    entrypoint: ["/jellyfin/jellyfin"],
    cmd: null,
    ports: [{ priv: 8096, pub: 8096 }, { priv: 8920 }, { priv: 1900, pub: 1900, type: "udp" }, { priv: 7359, pub: 7359, type: "udp" }],
    env: ["HEALTHCHECK_URL", "JELLYFIN_CACHE_DIR", "JELLYFIN_CONFIG_DIR", "JELLYFIN_DATA_DIR", "JELLYFIN_FFMPEG", "JELLYFIN_LOG_DIR", "JELLYFIN_PublishedServerUrl", "JELLYFIN_WEB_DIR", "LANG", "LANGUAGE", "LC_ALL", "MALLOC_TRIM_THRESHOLD_", "NVIDIA_DRIVER_CAPABILITIES", "NVIDIA_VISIBLE_DEVICES", "PATH", "TZ"],
    mounts: [bind(stack("media", "jellyfin/cache"), "/cache"), bind(stack("media", "jellyfin/config"), "/config"), bind("/DATA/media", "/media", false)],
    imageLabels: { "org.opencontainers.image.title": "Jellyfin", "org.opencontainers.image.source": "https://github.com/jellyfin/jellyfin-packaging", "org.opencontainers.image.vendor": "Jellyfin Project", "org.opencontainers.image.description": "The Free Software Media System" },
    health: { test: ["CMD-SHELL", 'curl -Lk -fsS "${HEALTHCHECK_URL:-http://localhost:8096/health}" || exit 1'], intervalS: 30, timeoutS: 30, retries: 3, ok: "Healthy" },
    imageSize: 1.42 * GB,
    createdDaysAgo: 19,
    startedAgo: 19 * DAY - 40 * MIN,
    restart: "unless-stopped",
    user: "1000:1000",
    devices: 2,
    cpu: 1.8,
    mem: 840 * MB,
    rx: 60_000,
    tx: 1_400_000,
    boot: (t) => [
      ["out", `${jfTs(t)} [INF] [1] Main: Jellyfin version: 10.10.3`],
      ["out", `${jfTs(t)} [INF] [1] Main: Environment Variables: ["[JELLYFIN_CONFIG_DIR, /config/config]", "[JELLYFIN_DATA_DIR, /config]", "[JELLYFIN_CACHE_DIR, /cache]", "[JELLYFIN_LOG_DIR, /config/log]"]`],
      ["out", `${jfTs(t)} [INF] [1] Main: Operating system: Debian GNU/Linux 12 (bookworm)`],
      ["out", `${jfTs(t)} [INF] [1] Main: Architecture: X64`],
      ["out", `${jfTs(t)} [INF] [1] Main: Processor count: 16`],
      ["out", `${jfTs(t)} [INF] [1] Main: Program data path: /config`],
      ["out", `${jfTs(t)} [INF] [1] Main: Web resources path: /jellyfin/jellyfin-web`],
      ["out", `${jfTs(t)} [INF] [1] MediaBrowser.MediaEncoding.Encoder.MediaEncoder: Available hwaccel types: ["cuda", "vaapi", "qsv", "drm", "opencl", "vulkan"]`],
      ["out", `${jfTs(t)} [INF] [1] Emby.Server.Implementations.ApplicationHost: Kestrel is listening on 0.0.0.0`],
      ["out", `${jfTs(t)} [INF] [1] Main: Startup complete 0:00:04.8125513`],
    ],
    line: (t) =>
      pick<[Stream, string]>([
        ["out", `${jfTs(t)} [INF] [${rnd(5, 60)}] Emby.Server.Implementations.Session.SessionManager: Playback stopped reported by app "Jellyfin Android" "2.6.2" playing "${pick(SHOWS)} ${ep()}". Stopped at "${rnd(100000, 3200000)}" ms`],
        ["out", `${jfTs(t)} [INF] [${rnd(5, 60)}] Jellyfin.Api.Helpers.MediaInfoHelper: User policy for demo. EnablePlaybackRemuxing: True EnableVideoPlaybackTranscoding: True EnableAudioPlaybackTranscoding: True`],
        ["out", `${jfTs(t)} [INF] [${rnd(5, 60)}] Emby.Server.Implementations.HttpServer.WebSocketManager: WS "${pick(LAN)}" request`],
        ["out", `${jfTs(t)} [INF] [${rnd(5, 60)}] Emby.Server.Implementations.ScheduledTasks.TaskManager: Scan Media Library Completed after 1 minute(s) and ${rnd(2, 58)} seconds`],
        ["out", `${jfTs(t)} [INF] [${rnd(5, 60)}] MediaBrowser.MediaEncoding.Transcoding.TranscodeManager: /usr/lib/jellyfin-ffmpeg/ffmpeg -analyzeduration 200M -init_hw_device vaapi=va:/dev/dri/renderD128 -i "/media/tv/${pick(SHOWS)}/Season 01/${ep()}.mkv" -map_metadata -1 -codec:v:0 h264_vaapi -b:v 7616000 -f hls`],
        ["out", `${jfTs(t)} [WRN] [${rnd(5, 60)}] Jellyfin.Server.Middleware.ResponseTimeMiddleware: Slow HTTP Response from http://localhost:8096/health to 127.0.0.1 in 0:00:${rnd(12, 29)}.${rnd(100, 999)} with Status Code 503`],
      ]),
    error: (t) => ["out", `${jfTs(t)} [ERR] [${rnd(5, 60)}] Microsoft.AspNetCore.Server.Kestrel: Connection id "0HN8${hex(String(t), 8).toUpperCase()}": An unhandled exception was thrown by the application. System.Threading.Tasks.TaskCanceledException: A task was canceled.`],
    stop: (t) => [["out", `${jfTs(t)} [INF] [1] Main: Received a SIGTERM signal. Shutting down.`], ["out", `${jfTs(t)} [INF] [1] Emby.Server.Implementations.ApplicationHost: Disposing CoreAppHost`]],
  },
  plex: {
    service: "plex",
    entrypoint: ["/init"],
    cmd: null,
    ports: [],
    hostNet: true,
    env: ["HOME", "LANG", "LANGUAGE", "NVIDIA_DRIVER_CAPABILITIES", "PATH", "PGID", "PLEX_DOWNLOAD", "PUID", "S6_CMD_WAIT_FOR_SERVICES_MAXTIME", "TERM", "TZ", "VERSION"],
    mounts: [bind(stack("media", "plex/config"), "/config"), bind("/DATA/media", "/media", false), bind("/tmp/plex-transcode", "/transcode")],
    imageLabels: { "org.opencontainers.image.title": "Plex", "org.opencontainers.image.source": "https://github.com/linuxserver/docker-plex", "org.opencontainers.image.vendor": "linuxserver.io", "build_version": "Linuxserver.io version:- 1.41.3.9314-a0bfb8370-ls250" },
    imageSize: 0.38 * GB,
    createdDaysAgo: 12,
    startedAgo: 12 * DAY - 20 * MIN,
    restart: "unless-stopped",
    devices: 1,
    cpu: 2.4,
    mem: 410 * MB,
    rx: 40_000,
    tx: 2_100_000,
    boot: () => [
      ["out", "[migrations] started"],
      ["out", "[custom-init] No custom files found, skipping..."],
      ["out", "Starting Plex Media Server. . . (you can ignore the libusb_init error)"],
      ["out", "Connection to localhost (::1) 32400 port [tcp/*] succeeded!"],
      ["out", "[ls.io-init] done."],
    ],
    line: (t) =>
      pick<[Stream, string]>([
        ["out", `Plex Media Server: Library scan complete for "TV Shows" (${rnd(2, 40)} new items) at ${hms(t)}`],
        ["out", `Plex Media Server: Transcoder started for "${pick(SHOWS)} ${ep()}" (1080p H.264 → 720p) at ${hms(t)}`],
        ["out", `Plex Media Server: Direct play started for "${pick(MOVIES)}" at ${hms(t)}`],
      ]),
    stop: () => [["out", "Stopping Plex Media Server."]],
  },
  sonarr: {
    service: "sonarr",
    entrypoint: ["/init"],
    cmd: null,
    ports: [{ priv: 8989, pub: 8989 }],
    env: [...LSIO_ENV, "SONARR_BRANCH", "SONARR_CHANNEL"],
    mounts: [bind(stack("media", "sonarr"), "/config"), bind("/DATA/downloads", "/downloads"), bind("/DATA/media/tv", "/tv")],
    imageLabels: lsioLabels("Sonarr", "sonarr"),
    imageSize: 203 * MB,
    createdDaysAgo: 11,
    startedAgo: 11 * DAY - 3 * HOUR,
    restart: "unless-stopped",
    lsio: "dotnet",
    cpu: 0.35,
    mem: 312 * MB,
    rx: 8_000,
    tx: 3_000,
    probeHttp: 200,
    boot: (t) => [...lsioBoot("")(t), arr("Info", "Bootstrap", "Starting Sonarr - /app/sonarr/bin/Sonarr - Version 4.0.11.2680"), arr("Info", "Microsoft.Hosting.Lifetime", "Now listening on: http://[::]:8989")],
    line: () =>
      pick([
        arr("Info", "RssSyncService", "Starting RSS Sync"),
        arr("Info", "DownloadDecisionMaker", `Processing ${rnd(40, 120)} releases`),
        arr("Info", "RssSyncService", `RSS Sync Completed. Reports found: ${rnd(40, 120)}, Reports grabbed: 0`),
        arr("Info", "RefreshSeriesService", `Updating info for ${pick(SHOWS)}`),
        arr("Info", "DiskScanService", `Scanning disk for ${pick(SHOWS)}`),
        arr("Info", "DiskScanService", `Completed scanning disk for ${pick(SHOWS)}`),
        arr("Info", "EpisodeSearchService", `Searching indexers for [${pick(SHOWS)} : ${ep()}]`),
      ]),
    stop: lsioStop,
  },
  radarr: {
    service: "radarr",
    entrypoint: ["/init"],
    cmd: null,
    ports: [{ priv: 7878, pub: 7878 }],
    env: [...LSIO_ENV, "RADARR_BRANCH"],
    mounts: [bind(stack("media", "radarr"), "/config"), bind("/DATA/downloads", "/downloads"), bind("/DATA/media/movies", "/movies")],
    imageLabels: lsioLabels("Radarr", "radarr"),
    imageSize: 211 * MB,
    createdDaysAgo: 19,
    restart: "unless-stopped",
    lsio: "dotnet",
    cpu: 0.3,
    mem: 287 * MB,
    rx: 6_000,
    tx: 2_500,
    probeHttp: 200,
    boot: (t) => [...lsioBoot("")(t), arr("Info", "Bootstrap", "Starting Radarr - /app/radarr/bin/Radarr - Version 5.15.1.9463"), arr("Info", "Microsoft.Hosting.Lifetime", "Now listening on: http://[::]:7878")],
    line: () =>
      pick([
        arr("Info", "RssSyncService", "Starting RSS Sync"),
        arr("Info", "DownloadDecisionMaker", `Processing ${rnd(30, 90)} releases`),
        arr("Info", "RssSyncService", `RSS Sync Completed. Reports found: ${rnd(30, 90)}, Reports grabbed: 0`),
        arr("Info", "RefreshMovieService", `Updating info for ${pick(MOVIES)}`),
        arr("Info", "DiskScanService", `Completed scanning disk for ${pick(MOVIES)}`),
        arr("Info", "MovieSearchService", `Searching indexers for [${pick(MOVIES)}]. ${rnd(1, 6)} active indexers`),
      ]),
    stop: lsioStop,
  },
  prowlarr: {
    service: "prowlarr",
    entrypoint: ["/init"],
    cmd: null,
    ports: [{ priv: 9696, pub: 9696 }],
    env: [...LSIO_ENV, "PROWLARR_BRANCH"],
    mounts: [bind(stack("media", "prowlarr"), "/config")],
    imageLabels: lsioLabels("Prowlarr", "prowlarr"),
    imageSize: 178 * MB,
    createdDaysAgo: 19,
    restart: "unless-stopped",
    lsio: "dotnet",
    cpu: 0.2,
    mem: 176 * MB,
    rx: 12_000,
    tx: 4_000,
    probeHttp: 200,
    boot: (t) => [...lsioBoot("")(t), arr("Info", "Bootstrap", "Starting Prowlarr - /app/prowlarr/bin/Prowlarr - Version 1.28.2.4885"), arr("Info", "Microsoft.Hosting.Lifetime", "Now listening on: http://[::]:9696")],
    line: () =>
      pick([
        arr("Info", "ReleaseSearchService", `Searching indexer(s): [${INDEXERS.slice(0, rnd(2, 5)).join(", ")}] for Term: [${pick(SHOWS)}], Offset: 0, Limit: 100, Categories: [5000, 5030, 5040]`),
        arr("Info", "ApplicationService", `Syncing indexers to ${pick(["Sonarr", "Radarr"])}`),
        arr("Info", "HistoryService", `Release from ${pick(INDEXERS)} grabbed by Sonarr`),
        arr("Warn", "Cardigann", `${pick(INDEXERS)}: Request to indexer failed, 429 Too Many Requests. Retry in 5 minutes`),
      ]),
    stop: lsioStop,
  },
  transmission: {
    service: "transmission",
    entrypoint: ["/init"],
    cmd: null,
    ports: [{ priv: 9091, pub: 9091 }, { priv: 51413, pub: 51413 }, { priv: 51413, pub: 51413, type: "udp" }],
    env: [...LSIO_ENV, "PEERPORT", "TRANSMISSION_WEB_HOME", "USER"],
    mounts: [bind(stack("media", "transmission"), "/config"), bind("/DATA/downloads", "/downloads"), bind("/DATA/downloads/watch", "/watch")],
    imageLabels: lsioLabels("Transmission", "transmission"),
    imageSize: 96 * MB,
    createdDaysAgo: 34,
    restart: "unless-stopped",
    lsio: "native",
    cpu: 0.25,
    mem: 94 * MB,
    rx: 1_900_000,
    tx: 420_000,
    probeHttp: 301,
    boot: (t) => [...lsioBoot("")(t), ["out", `[${ymdhmsms(t)}] inf Transmission 4.0.6 (38c164933e) started (session.cc:1376)`], ["out", `[${ymdhmsms(t)}] inf RPC Server Serving RPC and Web requests on 0.0.0.0:9091/transmission/ (rpc-server.cc:890)`]],
    line: (t) =>
      pick<[Stream, string]>([
        ["out", `[${ymdhmsms(t)}] inf ${pick(ISOS)} Announcing to tracker (announcer.cc:1085)`],
        ["out", `[${ymdhmsms(t)}] inf ${pick(ISOS)} Got ${rnd(10, 80)} peers from tracker (announcer.cc:1095)`],
        ["out", `[${ymdhmsms(t)}] inf ${pick(ISOS)} Verified piece ${rnd(10, 2400)} (torrent.cc:1612)`],
        ["out", `[${ymdhmsms(t)}] inf Port Forwarding State changed from 'Not forwarded' to 'Forwarded' (port-forwarding.cc:107)`],
      ]),
    stop: lsioStop,
  },
  qbittorrent: {
    service: "qbittorrent",
    entrypoint: ["/init"],
    cmd: null,
    ports: [{ priv: 8085, pub: 8085 }, { priv: 6881, pub: 6881 }, { priv: 6881, pub: 6881, type: "udp" }],
    env: [...LSIO_ENV, "QBT_VERSION", "WEBUI_PORT", "TORRENTING_PORT"],
    mounts: [bind(stack("media", "qbittorrent"), "/config"), bind("/DATA/downloads", "/downloads")],
    imageLabels: lsioLabels("Qbittorrent", "qbittorrent"),
    imageSize: 187 * MB,
    createdDaysAgo: 34,
    startedAgo: 23 * DAY,
    finishedAgo: 3800 * MIN,
    restart: "unless-stopped",
    lsio: "native",
    stopCode: 143,
    cpu: 0.4,
    mem: 220 * MB,
    rx: 900_000,
    tx: 300_000,
    probeHttp: 200,
    boot: (t) => [...lsioBoot("")(t), ["out", "WebUI will be started shortly after internal preparations. Please wait..."], ["out", "******** Information ********"], ["out", "To control qBittorrent, access the WebUI at: http://localhost:8085"]],
    line: (t) => ["out", `(N) ${ymdhms(t)} - Added new torrent. Torrent: "${pick(ISOS)}"`],
    stop: () => [["out", "Catching signal: SIGTERM"], ["out", "Exiting cleanly"], ...lsioStop()],
  },
  "immich-server": {
    service: "immich-server",
    entrypoint: ["tini", "--", "/bin/bash", "start.sh"],
    cmd: null,
    ports: [{ priv: 2283, pub: 2283 }],
    env: ["DB_DATABASE_NAME", "DB_HOSTNAME", "DB_PASSWORD", "DB_USERNAME", "IMMICH_BUILD", "IMMICH_BUILD_IMAGE", "IMMICH_BUILD_IMAGE_URL", "IMMICH_BUILD_URL", "IMMICH_ENV", "IMMICH_REPOSITORY", "IMMICH_REPOSITORY_URL", "IMMICH_SOURCE_COMMIT", "IMMICH_SOURCE_REF", "IMMICH_VERSION", "NODE_ENV", "NVIDIA_DRIVER_CAPABILITIES", "NVIDIA_VISIBLE_DEVICES", "PATH", "REDIS_HOSTNAME", "TZ", "UPLOAD_LOCATION", "YARN_VERSION"],
    mounts: [bind("/etc/localtime", "/etc/localtime", false), bind("/DATA/photos/library", "/usr/src/app/upload")],
    imageLabels: { "org.opencontainers.image.source": "https://github.com/immich-app/immich", "org.opencontainers.image.title": "immich-server", "org.opencontainers.image.licenses": "AGPL-3.0" },
    health: { test: ["CMD", "immich-healthcheck"], intervalS: 30, timeoutS: 30, retries: 3, ok: "" },
    imageSize: 1.63 * GB,
    createdDaysAgo: 6,
    startedAgo: 6 * DAY - 2 * HOUR,
    restart: "always",
    dependsOn: "database:service_started:false,redis:service_started:false",
    cpu: 0.9,
    mem: 1.12 * GB,
    rx: 45_000,
    tx: 90_000,
    workingDir: "/usr/src/app",
    boot: (t) => [
      ["out", "Initializing Immich v1.122.0"],
      ["out", "Detected CPU Cores: 16"],
      ["out", "Starting api worker"],
      ["out", "Starting microservices worker"],
      nest(t, "LOG", "Microservices:EventRepository", "Initialized websocket server"),
      nest(t, "LOG", "Api:NestFactory", "Starting Nest application..."),
      nest(t, "LOG", "Api:Bootstrap", "Immich Server is listening on http://[::1]:2283 [v1.122.0] [production]"),
    ],
    line: (t) =>
      pick([
        nest(t, "LOG", "Api:EventRepository", `Websocket Connect:    ${hex(String(t + 1), 20)}`),
        nest(t, "LOG", "Api:EventRepository", `Websocket Disconnect: ${hex(String(t + 2), 20)}`),
        nest(t, "LOG", "Microservices:MetadataService", `Extracting metadata for asset ${hex(String(t), 8)}-${hex(String(t + 3), 4)}-4${hex(String(t + 4), 3)}`),
        nest(t, "LOG", "Microservices:PersonService", `Detected ${rnd(1, 4)} new faces in asset ${hex(String(t + 5), 8)}`),
        nest(t, "LOG", "Api:LibraryService", "Starting to scan library uploads"),
        nest(t, "WARN", "Microservices:MediaService", `Could not extract thumbnail from IMG_${rnd(1000, 9999)}.HEIC, falling back to preview`),
      ]),
  },
  "immich-postgres": {
    service: "database",
    entrypoint: ["docker-entrypoint.sh"],
    cmd: ["postgres", "-c", "shared_preload_libraries=vectors.so", "-c", 'search_path="$user", public, vectors', "-c", "logging_collector=on", "-c", "max_wal_size=2GB", "-c", "shared_buffers=512MB", "-c", "wal_compression=on"],
    ports: [{ priv: 5432 }],
    env: ["GOSU_VERSION", "LANG", "PATH", "PGDATA", "PG_MAJOR", "PG_VERSION", "POSTGRES_DB", "POSTGRES_INITDB_ARGS", "POSTGRES_PASSWORD", "POSTGRES_USER"],
    mounts: [bind(stack("immich", "postgres"), "/var/lib/postgresql/data")],
    imageLabels: {},
    health: { test: ["CMD-SHELL", "pg_isready --dbname='immich' --username='postgres' || exit 1"], intervalS: 300, timeoutS: 30, retries: 3, ok: "/var/run/postgresql:5432 - accepting connections" },
    imageSize: 452 * MB,
    createdDaysAgo: 62,
    restart: "always",
    cpu: 0.25,
    mem: 386 * MB,
    rx: 30_000,
    tx: 40_000,
    boot: (t) => [pg(t, "LOG:  starting PostgreSQL 14.10 (Debian 14.10-1.pgdg120+1) on x86_64-pc-linux-gnu"), pg(t, 'LOG:  listening on IPv4 address "0.0.0.0", port 5432'), pg(t, "LOG:  database system is ready to accept connections")],
    line: (t) =>
      pick([
        pg(t, "LOG:  checkpoint starting: time"),
        pg(t, `LOG:  checkpoint complete: wrote ${rnd(20, 900)} buffers (${(Math.random() * 3).toFixed(1)}%); 0 WAL file(s) added, 0 removed, ${rnd(0, 2)} recycled; write=${rnd(10, 60)}.${rnd(100, 999)} s, sync=0.0${rnd(10, 99)} s, total=${rnd(10, 60)}.${rnd(100, 999)} s`),
        pg(t, `LOG:  automatic vacuum of table "immich.public.asset_job_status": index scans: 0`),
      ]),
    stop: (t) => [pg(t, "LOG:  received fast shutdown request"), pg(t, "LOG:  database system is shut down")],
  },
  "immich-redis": {
    service: "redis",
    entrypoint: ["docker-entrypoint.sh"],
    cmd: ["redis-server"],
    ports: [{ priv: 6379 }],
    env: ["GOSU_VERSION", "PATH", "REDIS_DOWNLOAD_SHA", "REDIS_DOWNLOAD_URL", "REDIS_VERSION"],
    mounts: [vol(hex("immich-redis-anon"), "/data")],
    imageLabels: {},
    health: { test: ["CMD-SHELL", "redis-cli ping || exit 1"], intervalS: 30, timeoutS: 30, retries: 3, ok: "PONG" },
    imageSize: 41 * MB,
    createdDaysAgo: 62,
    restart: "always",
    workingDir: "/data",
    cpu: 0.08,
    mem: 14 * MB,
    rx: 20_000,
    tx: 25_000,
    boot: (t) => [["out", `1:C ${redisTs(t)} # oO0OoO0OoO0Oo Redis is starting oO0OoO0OoO0Oo`], ["out", `1:M ${redisTs(t)} * Server initialized`], ["out", `1:M ${redisTs(t)} * Ready to accept connections tcp`]],
    line: (t) => {
      const pid = rnd(100, 900);
      return pick<[Stream, string]>([
        ["out", `1:M ${redisTs(t)} * 100 changes in 300 seconds. Saving...`],
        ["out", `1:M ${redisTs(t)} * Background saving started by pid ${pid}`],
        ["out", `${pid}:C ${redisTs(t)} * DB saved on disk`],
        ["out", `${pid}:C ${redisTs(t)} * Fork CoW for RDB: current 0 MB, peak 0 MB, average 0 MB`],
        ["out", `1:M ${redisTs(t)} * Background saving terminated with success`],
      ]);
    },
    stop: (t) => [["out", `1:signal-handler (${Math.floor(t / 1000)}) Received SIGTERM scheduling shutdown...`], ["out", `1:M ${redisTs(t)} # Redis is now ready to exit, bye bye...`]],
  },
  nextcloud: {
    service: "app",
    entrypoint: ["/entrypoint.sh"],
    cmd: ["apache2-foreground"],
    ports: [{ priv: 80, pub: 8080 }],
    env: ["APACHE_CONFDIR", "APACHE_ENVVARS", "MYSQL_DATABASE", "MYSQL_HOST", "MYSQL_PASSWORD", "MYSQL_USER", "NEXTCLOUD_TRUSTED_DOMAINS", "NEXTCLOUD_VERSION", "PATH", "PHP_INI_DIR", "PHP_MEMORY_LIMIT", "PHP_UPLOAD_LIMIT", "PHP_VERSION", "REDIS_HOST", "TZ"],
    mounts: [vol("nextcloud_nextcloud", "/var/www/html"), bind("/DATA/nextcloud", "/var/www/html/data")],
    imageLabels: {},
    imageSize: 1.21 * GB,
    createdDaysAgo: 27,
    restart: "unless-stopped",
    workingDir: "/var/www/html",
    cpu: 0.3,
    mem: 246 * MB,
    rx: 150_000,
    tx: 210_000,
    probeHttp: 302,
    boot: () => [["out", "Configuring Redis as session handler"], ["out", "=> Searching for hook scripts (*.sh) to run, located in the folder \"/docker-entrypoint-hooks.d/before-starting\""], ["err", "AH00558: apache2: Could not reliably determine the server's fully qualified domain name, using 172.20.0.2. Set the 'ServerName' directive globally to suppress this message"], ["err", "[core:notice] [pid 1] AH00094: Command line: 'apache2 -D FOREGROUND'"]],
    line: (t) => {
      const ip = pick(LAN);
      const req = pick([
        `"PROPFIND /remote.php/dav/files/demo/ HTTP/1.1" 207 ${rnd(800, 4000)} "-" "Mozilla/5.0 (Linux) mirall/3.14.3 (Nextcloud, debian-6.1.0-28-amd64 ClientArchitecture: x86_64 OsArchitecture: x86_64)"`,
        `"GET /ocs/v2.php/apps/notifications/api/v2/notifications?format=json HTTP/1.1" 200 ${rnd(300, 900)} "-" "Mozilla/5.0 (Android) Nextcloud-android/3.30.4"`,
        `"GET /status.php HTTP/1.1" 200 ${rnd(150, 200)} "-" "Mozilla/5.0 (Linux) mirall/3.14.3"`,
        `"PUT /remote.php/dav/uploads/demo/web-file-upload-${hex(String(t), 16)}/1 HTTP/1.1" 201 ${rnd(0, 20)} "-" "Mozilla/5.0 (Android) Nextcloud-android/3.30.4"`,
        `"POST /index.php/apps/text/session/1834/sync HTTP/1.1" 200 ${rnd(100, 600)} "https://cloud.homelab.lan/" "Mozilla/5.0 (X11; Linux x86_64; rv:132.0) Gecko/20100101 Firefox/132.0"`,
      ]);
      return ["out", `${ip} - demo [${apacheTs(t)}] ${req}`];
    },
    stop: (t) => [["err", `[${d(t).toUTCString()}] [mpm_prefork:notice] [pid 1] AH00169: caught SIGTERM, shutting down`]],
  },
  "nextcloud-cron": {
    service: "cron",
    entrypoint: ["/cron.sh"],
    cmd: null,
    ports: [{ priv: 80 }],
    env: ["APACHE_CONFDIR", "APACHE_ENVVARS", "NEXTCLOUD_VERSION", "PATH", "PHP_INI_DIR", "PHP_MEMORY_LIMIT", "PHP_VERSION", "TZ"],
    mounts: [vol("nextcloud_nextcloud", "/var/www/html"), bind("/DATA/nextcloud", "/var/www/html/data")],
    imageLabels: {},
    imageSize: 1.21 * GB,
    createdDaysAgo: 27,
    startedAgo: 130 * MIN,
    finishedAgo: 125 * MIN,
    restart: "no",
    workingDir: "/var/www/html",
    stopCode: 1,
    dependsOn: "app:service_started:false",
    cpu: 0.05,
    mem: 32 * MB,
    rx: 100,
    tx: 100,
    boot: () => [["out", "crond: crond (busybox 1.36.1) started, log level 8"]],
    line: (t) => ["out", `crond: USER www-data pid ${rnd(20, 400)} cmd php -f /var/www/html/cron.php (${hms(t)})`],
    stop: () => [["err", "Console has to be executed with the user that owns the file config/config.php"], ["err", "Current user id: 0"], ["err", "Owner id of config.php: 33"], ["err", "Try adding 'sudo -u #33' to the beginning of the command (without the single quotes)"]],
  },
  "home-assistant": {
    service: "homeassistant",
    entrypoint: ["/init"],
    cmd: null,
    ports: [],
    hostNet: true,
    env: ["LANG", "PATH", "PIP_BREAK_SYSTEM_PACKAGES", "PIP_DISABLE_PIP_VERSION_CHECK", "PIP_PREFER_BINARY", "PYTHONPATH", "S6_BEHAVIOUR_IF_STAGE2_FAILS", "S6_CMD_WAIT_FOR_SERVICES_MAXTIME", "S6_SERVICES_GRACETIME", "TZ", "UV_EXTRA_INDEX_URL", "UV_SYSTEM_PYTHON"],
    mounts: [bind(stack("home", "home-assistant"), "/config"), bind("/etc/localtime", "/etc/localtime", false), bind("/run/dbus", "/run/dbus", false)],
    imageLabels: { "io.hass.type": "core", "io.hass.arch": "amd64", "org.opencontainers.image.title": "Home Assistant", "org.opencontainers.image.source": "https://github.com/home-assistant/core", "org.opencontainers.image.licenses": "Apache-2.0" },
    imageSize: 1.84 * GB,
    createdDaysAgo: 14,
    startedAgo: 14 * DAY - 25 * MIN,
    restart: "unless-stopped",
    privileged: true,
    devices: 1,
    workingDir: "/config",
    cpu: 0.9,
    mem: 612 * MB,
    rx: 0,
    tx: 0,
    probeHttp: 200,
    boot: (t) => [["out", "s6-rc: info: service legacy-services successfully started"], ha(t, "INFO", "homeassistant.bootstrap", "Home Assistant initialized in 6.41s"), ha(t, "INFO", "homeassistant.core", "Starting Home Assistant")],
    line: (t) =>
      pick([
        ha(t, "INFO", "homeassistant.components.automation.hallway_motion_light", "Hallway motion light: Running automation actions"),
        ha(t, "INFO", "homeassistant.components.automation.hallway_motion_light", "Hallway motion light: Executing step call service"),
        ha(t, "INFO", "homeassistant.components.recorder.core", "Recorder purge completed"),
        ha(t, "WARNING", "homeassistant.components.zha.core.cluster_handlers", "[0x5B21:1:0x0006]: async_initialize: all attempts have failed"),
        ha(t, "ERROR", "homeassistant.components.tplink.coordinator", "Unable to connect to the device 192.168.1.57: Unable to query the device: timed out"),
        ha(t, "INFO", "homeassistant.components.automation.sunset_lights", "Sunset lights: Running automation actions"),
      ]),
    stop: (t) => [ha(t, "INFO", "homeassistant.core", "Stopping Home Assistant"), ["out", "s6-rc: info: service legacy-services: stopping"]],
  },
  pihole: {
    service: "pihole",
    entrypoint: ["/s6-init"],
    cmd: null,
    ports: [{ priv: 53, pub: 53 }, { priv: 53, pub: 53, type: "udp" }, { priv: 67, type: "udp" }, { priv: 80, pub: 8053 }, { priv: 443 }],
    env: ["DNSMASQ_USER", "FTLCONF_LOCAL_IPV4", "FTL_CMD", "PATH", "PHP_ERROR_LOG", "PIHOLE_DOCKER_TAG", "PIHOLE_DNS_", "S6_BEHAVIOUR_IF_STAGE2_FAILS", "S6_CMD_WAIT_FOR_SERVICES_MAXTIME", "S6_KEEP_ENV", "S6_LOGGING", "TZ", "WEBPASSWORD", "phpver"],
    mounts: [bind("/opt/stacks/network/etc-dnsmasq.d", "/etc/dnsmasq.d"), bind("/opt/stacks/network/etc-pihole", "/etc/pihole")],
    imageLabels: { "image.authors": "dev@pi-hole.net", "org.opencontainers.image.source": "https://github.com/pi-hole/docker-pi-hole" },
    health: { test: ["CMD-SHELL", "dig +short +norecurse +retry=0 @127.0.0.1 pi.hole || exit 1"], intervalS: 30, timeoutS: 30, retries: 3, ok: HOST.lanIp },
    imageSize: 312 * MB,
    createdDaysAgo: 48,
    restart: "unless-stopped",
    capAdd: ["NET_ADMIN"],
    rootCompose: true,
    cpu: 0.12,
    mem: 74 * MB,
    rx: 25_000,
    tx: 30_000,
    boot: () => [["out", "  [i] Setting up user & group for the pihole user"], ["out", "  [i] Installing configs from /etc/.pihole..."], ["out", "  [✓] Installed /etc/dnsmasq.d/01-pihole.conf"], ["out", "  [i] Pi-hole blocking is enabled"], ["out", "  [✓] Gravity database is up to date (174,632 domains)"], ["out", "  [i] Starting pihole-FTL (no-daemon) as pihole"]],
    line: (t) => {
      const dom = pick(DOMAINS);
      return pick<[Stream, string]>([
        ["out", `${MONTHS[d(t).getUTCMonth()]} ${p2(d(t).getUTCDate())} ${hms(t)} dnsmasq[${rnd(200, 260)}]: query[A] ${dom} from ${pick(LAN)}`],
        ["out", `${MONTHS[d(t).getUTCMonth()]} ${p2(d(t).getUTCDate())} ${hms(t)} dnsmasq[${rnd(200, 260)}]: cached ${dom} is ${rnd(100, 200)}.${rnd(1, 254)}.${rnd(1, 254)}.${rnd(1, 254)}`],
        ["out", `${MONTHS[d(t).getUTCMonth()]} ${p2(d(t).getUTCDate())} ${hms(t)} dnsmasq[${rnd(200, 260)}]: gravity blocked doubleclick.net is 0.0.0.0`],
        ["out", `${MONTHS[d(t).getUTCMonth()]} ${p2(d(t).getUTCDate())} ${hms(t)} dnsmasq[${rnd(200, 260)}]: forwarded ${dom} to 9.9.9.9`],
      ]);
    },
  },
  vaultwarden: {
    service: "vaultwarden",
    entrypoint: ["/start.sh"],
    cmd: null,
    ports: [{ priv: 80, pub: 8222 }],
    env: ["ADMIN_TOKEN", "DOMAIN", "PATH", "ROCKET_ADDRESS", "ROCKET_PORT", "ROCKET_PROFILE", "SIGNUPS_ALLOWED", "SSL_CERT_DIR", "TZ", "WEBSOCKET_ENABLED"],
    mounts: [bind(stack("tools", "vaultwarden"), "/data")],
    imageLabels: { "org.opencontainers.image.source": "https://github.com/dani-garcia/vaultwarden", "org.opencontainers.image.licenses": "AGPL-3.0-only", "org.opencontainers.image.title": "vaultwarden" },
    health: { test: ["CMD", "/healthcheck.sh"], intervalS: 60, timeoutS: 10, retries: 3, ok: "" },
    imageSize: 247 * MB,
    createdDaysAgo: 21,
    restart: "unless-stopped",
    memLimit: 512 * MB,
    pidsLimit: 200,
    workingDir: "/",
    cpu: 0.02,
    mem: 29 * MB,
    rx: 2_000,
    tx: 3_000,
    boot: () => [["out", "/--------------------------------------------------------------------\\"], ["out", "|                        Starting Vaultwarden                        |"], ["out", "|                           Version 1.32.5                           |"], ["out", "\\--------------------------------------------------------------------/"], ["out", "[INFO] Rocket has launched from http://0.0.0.0:80"]],
    line: (t) =>
      pick([
        vw(t, "[request][INFO] GET /api/sync"),
        vw(t, "[response][INFO] (sync) GET /api/sync => 200 OK"),
        vw(t, "[request][INFO] POST /identity/connect/token"),
        vw(t, "[response][INFO] (login) POST /identity/connect/token => 200 OK"),
        vw(t, "[vaultwarden::api::notifications][INFO] Accepting Rocket WS connection from " + pick(LAN)),
        vw(t, "[response][INFO] (get_icon) GET /icons/<domain>/icon.png => 200 OK"),
      ]),
  },
  "uptime-kuma": {
    service: "uptime-kuma",
    entrypoint: ["/usr/bin/dumb-init", "--"],
    cmd: ["extra/entrypoint.sh", "node", "server/server.js"],
    ports: [{ priv: 3001, pub: 3001 }],
    env: ["NODE_VERSION", "PATH", "TZ", "UPTIME_KUMA_IS_CONTAINER", "YARN_VERSION"],
    mounts: [vol("tools_uptime-kuma", "/app/data"), bind("/var/run/docker.sock", "/var/run/docker.sock", false)],
    imageLabels: {},
    health: { test: ["CMD", "extra/healthcheck"], intervalS: 60, timeoutS: 30, retries: 5, ok: "" },
    imageSize: 446 * MB,
    createdDaysAgo: 40,
    restart: "unless-stopped",
    workingDir: "/app",
    cpu: 0.4,
    mem: 138 * MB,
    rx: 30_000,
    tx: 22_000,
    boot: (t) => [["out", "==> Performing startup jobs and maintenance tasks"], ["out", "==> Starting application with user 0 group 0"], ["out", "Welcome to Uptime Kuma"], ["out", `${d(t).toISOString().slice(0, 19)}+00:00 [SERVER] INFO: Listening on 3001`]],
    line: (t) =>
      pick([
        kuma(t, "WARN", `Monitor #4 'Nextcloud': Pending: timeout of 48000ms exceeded | Max retries: 1 | Retry: 1 | Retry Interval: 60 seconds | Type: http`),
        kuma(t, "INFO", `Monitor #7 'Router': Successful Response: ${rnd(1, 9)} ms | Interval: 60 seconds | Type: ping`),
        kuma(t, "INFO", `Monitor #2 'Jellyfin': Successful Response: ${rnd(10, 90)} ms | Interval: 60 seconds | Type: http`),
        kuma(t, "INFO", `Monitor #9 'Pi-hole DNS': Successful Response: ${rnd(1, 6)} ms | Interval: 60 seconds | Type: dns`),
      ]),
  },
  paperless: {
    service: "webserver",
    entrypoint: ["/sbin/docker-entrypoint.sh"],
    cmd: ["/usr/local/bin/paperless_cmd.sh"],
    ports: [{ priv: 8000, pub: 8010 }],
    env: ["PAPERLESS_CONSUMER_POLLING", "PAPERLESS_DBENGINE", "PAPERLESS_OCR_LANGUAGE", "PAPERLESS_REDIS", "PAPERLESS_SECRET_KEY", "PAPERLESS_TIME_ZONE", "PAPERLESS_URL", "PATH", "PYTHONDONTWRITEBYTECODE", "PYTHONUNBUFFERED", "PYTHONWARNINGS", "S6_BEHAVIOUR_IF_STAGE2_FAILS", "USERMAP_GID", "USERMAP_UID"],
    mounts: [bind("/DATA/scans", "/usr/src/paperless/consume"), bind(stack("tools", "paperless/data"), "/usr/src/paperless/data"), bind(stack("tools", "paperless/media"), "/usr/src/paperless/media")],
    imageLabels: { "org.opencontainers.image.source": "https://github.com/paperless-ngx/paperless-ngx", "org.opencontainers.image.title": "paperless-ngx", "org.opencontainers.image.licenses": "GPL-3.0-only" },
    imageSize: 1.38 * GB,
    createdDaysAgo: 30,
    restart: "unless-stopped",
    workingDir: "/usr/src/paperless/src",
    cpu: 0.15,
    mem: 410 * MB,
    rx: 3_000,
    tx: 2_000,
    probeHttp: 302,
    boot: (t) => [["out", "Paperless-ngx docker container starting..."], ["out", "Apply database migrations..."], ["out", "No migrations to apply."], ppl(t, "INFO", "paperless.management.consumer", "Polling directory for changes: /usr/src/paperless/consume"), ["out", "[INFO] Starting granian (main PID: 1)"]],
    line: (t) =>
      pick([
        ppl(t, "INFO", "celery.worker.strategy", `Task documents.tasks.consume_file[${hex(String(t), 8)}-${hex(String(t + 1), 4)}] received`),
        ppl(t, "INFO", "paperless.consumer", `Consuming scan_${ymdhms(t).slice(0, 10)}_${rnd(100, 999)}.pdf`),
        ppl(t, "INFO", "paperless.consumer", `Document ${pick(["ACME invoice", "Electricity bill", "Bank statement", "Insurance letter", "Payslip"])} consumption finished`),
        ppl(t, "INFO", "celery.app.trace", `Task paperless_mail.tasks.process_mail_accounts succeeded in ${(Math.random() * 2).toFixed(3)}s: 'No new documents were added.'`),
      ]),
  },
};

// ---------- state ----------

interface CState {
  name: string;
  image: string;
  state: "running" | "exited" | "paused";
  exitCode: number;
  created: number;
  startedAt: number;
  finishedAt: number;
  restartCount: number;
  /** Health check result once past the start period. */
  health?: "healthy" | "unhealthy";
  unhealthySince?: number;
  /** Newer version tag, if any. */
  update: string | null;
  checkError?: string;
}

interface State {
  list: CState[];
  checkedAt: number;
}

const bootMs = HOST.bootTime * 1000;

function initial(): State {
  const now = Date.now();
  return {
    checkedAt: now - 2 * HOUR - 13 * MIN,
    list: CONTAINERS.map((c: DemoContainer) => {
      const p = PROFILES[c.name];
      const created = now - (p?.createdDaysAgo ?? 20) * DAY;
      const startedAt = Math.max(created + 3000, p?.startedAgo ? now - p.startedAgo : bootMs + 20_000 + (hash(c.name) % 30_000));
      return {
        name: c.name,
        image: c.image,
        state: c.state,
        exitCode: c.exitCode ?? 0,
        created,
        startedAt,
        finishedAt: c.state === "exited" ? now - (p?.finishedAgo ?? HOUR) : 0,
        restartCount: 0,
        health: c.health === "unhealthy" ? "unhealthy" : c.health ? "healthy" : undefined,
        unhealthySince: c.health === "unhealthy" ? now - 95 * MIN : undefined,
        update: c.update ?? null,
        checkError: c.image.startsWith("tensorchord/") ? "Registry rate limit reached; will retry on the next check" : undefined,
      };
    }),
  };
}

const store = persisted<State>("containers", initial, 1);
let checking: Promise<void> | null = null;
const updating = new Set<string>();

const INTERVAL = 6 * HOUR;
const START_PERIOD = 25_000;
const PROBE_DELAY = 40_000;

const PROJECT_ORDER = ["media", "immich", "nextcloud", "home", "network", "tools"];
const fullId = (name: string) => hex("container:" + name);
const profile = (name: string): Profile => PROFILES[name] ?? PROFILES.sonarr;
const def = (name: string) => CONTAINERS.find((c) => c.name === name);

/** Last full check time, rolled forward as if background checks ran. */
function lastCheck(st: State): number {
  const now = Date.now();
  let t = st.checkedAt;
  if (now - t > INTERVAL) t = now - ((now - t) % INTERVAL);
  return t;
}

function composeDir(project: string, p: Profile) {
  return p.rootCompose ? `/opt/stacks/${project}` : `${HOME}/stacks/${project}`;
}

function versionOf(c: CState, p: Profile, tag: string): string {
  if (p.lsio === "dotnet") return `${tag}.${1000 + (hash(c.image) % 9000)}-ls${100 + (hash(c.image + "ls") % 180)}`;
  if (p.lsio === "native") return `${tag}-r0-ls${100 + (hash(c.image + "ls") % 280)}`;
  return tag;
}

function healthOf(c: CState, p: Profile): Container["health"] {
  if (!p.health || c.state !== "running") return undefined;
  if (Date.now() - c.startedAt < START_PERIOD) return "starting";
  return c.health ?? "healthy";
}

function statusOf(c: CState, h: Container["health"]): string {
  const now = Date.now();
  if (c.state === "exited") return `Exited (${c.exitCode}) ${human(now - c.finishedAt)} ago`;
  const up = `Up ${human(now - c.startedAt)}`;
  if (c.state === "paused") return `${up} (Paused)`;
  if (h === "starting") return `${up} (health: starting)`;
  return h ? `${up} (${h})` : up;
}

function portsOf(c: CState, p: Profile): Port[] {
  if (p.hostNet) return [];
  const live = c.state !== "exited";
  // Stopped containers list no ports at all.
  if (!live) return [];
  const out: Port[] = p.ports.map((s) => (s.pub ? { private: s.priv, public: s.pub, type: s.type ?? "tcp" } : { private: s.priv, type: s.type ?? "tcp" }));
  // Like normalizePorts: published first, then by private port.
  return out.sort((a, b) => Number(!!b.public) - Number(!!a.public) || a.private - b.private || a.type.localeCompare(b.type));
}

function probeOf(c: CState, p: Profile, h: Container["health"]): Probe | undefined {
  if (c.state !== "running" || h || p.probeHttp === undefined) return undefined;
  const now = Date.now();
  if (now - c.startedAt < PROBE_DELAY) return undefined;
  const port = p.hostNet ? 8123 : (p.ports.find((x) => x.pub && (x.type ?? "tcp") === "tcp")?.pub ?? 0);
  if (!port) return undefined;
  const period = 300_000;
  const offset = hash(c.name) % period;
  const checkedAt = Math.max(c.startedAt + PROBE_DELAY, now - ((now + offset) % period));
  return { status: "up", port, httpStatus: p.probeHttp, latencyMs: 2 + (hash(c.name + Math.floor(checkedAt / period)) % 38), checkedAt, intervalS: 300 };
}

function project(c: CState) {
  return def(c.name)?.group ?? "tools";
}

function toContainer(c: CState, st: State): Container {
  const p = profile(c.name);
  const id = fullId(c.name);
  const [ref, tag] = splitRef(c.image);
  const h = healthOf(c, p);
  const proj = project(c);
  const dir = composeDir(proj, p);
  const checked = lastCheck(st);
  const out: Container = {
    id: short(id),
    fullId: id,
    name: c.name,
    image: c.image,
    imageId: sha("image:" + c.image),
    command: [...(p.entrypoint ?? []), ...(p.cmd ?? [])].join(" "),
    state: c.state,
    status: statusOf(c, h),
    ...(h ? { health: h } : {}),
    created: c.created,
    ports: portsOf(c, p),
    networkMode: p.hostNet ? "host" : `${proj}_default`,
    compose: { project: proj, service: p.service, workingDir: dir, configFiles: `${dir}/compose.yaml` },
    casaos: false,
    updateAvailable: c.update !== null,
    updateCheckedAt: Math.max(checked, c.created),
    version: versionOf(c, p, tag),
    imageRef: ref,
    tag,
    icon: def(c.name)?.icon ? `/api/containers/${short(id)}/icon` : undefined,
  };
  const pr = probeOf(c, p, h);
  if (pr) out.probe = pr;
  return out;
}

function list(): Container[] {
  const st = store.get();
  return st.list.map((c) => toContainer(c, st)).sort((a, b) => a.name.localeCompare(b.name));
}

function find(ref: string): CState | undefined {
  const st = store.get();
  return st.list.find((c) => {
    const id = fullId(c.name);
    return c.name === ref || id === ref || (ref.length >= 4 && id.startsWith(ref));
  });
}

function digest(c: CState) {
  return sha("digest:" + c.image);
}

function updatesStatus(): UpdatesStatus {
  const st = store.get();
  const checkedAt = lastCheck(st);
  const results: Record<string, UpdateCheck> = {};
  for (const c of st.list) {
    const r: UpdateCheck = {
      image: c.image,
      updateAvailable: c.update !== null,
      localDigest: digest(c),
      remoteDigest: c.update ? sha("digest:" + splitRef(c.image)[0] + ":" + c.update) : digest(c),
      checkedAt: Math.max(checkedAt, c.created),
    };
    if (c.checkError) {
      r.error = c.checkError;
      delete r.remoteDigest;
    }
    results[short(fullId(c.name))] = r;
  }
  return { checkedAt, checking: !!checking, nextCheckAt: checkedAt + INTERVAL, intervalMs: INTERVAL, results };
}

// ---------- details ----------

function details(c: CState): ContainerDetails {
  const st = store.get();
  const base = toContainer(c, st);
  const p = profile(c.name);
  const proj = project(c);
  const running = c.state !== "exited";
  const members = st.list.filter((x) => project(x) === proj);
  const subnet = 18 + Math.max(0, PROJECT_ORDER.indexOf(proj));
  const host = 2 + members.indexOf(c);
  const tag = base.tag;
  const labels: Record<string, string> = {
    ...p.imageLabels,
    "com.docker.compose.config-hash": hex("cfg:" + c.name + c.image),
    "com.docker.compose.container-number": "1",
    "com.docker.compose.depends_on": p.dependsOn ?? "",
    "com.docker.compose.image": base.imageId,
    "com.docker.compose.oneoff": "False",
    "com.docker.compose.project": proj,
    "com.docker.compose.project.config_files": base.compose!.configFiles!,
    "com.docker.compose.project.working_dir": base.compose!.workingDir!,
    "com.docker.compose.service": p.service,
    "com.docker.compose.version": "2.29.7",
  };
  if (p.lsio) {
    labels.build_version = `Linuxserver.io version:- ${base.version} Build-date:- ${new Date(c.created - 2 * DAY).toISOString().slice(0, 19)}+00:00`;
    labels["org.opencontainers.image.version"] = base.version;
    labels["org.opencontainers.image.ref.name"] = hex("rev:" + c.image, 40);
    labels["org.opencontainers.image.revision"] = hex("rev:" + c.image, 40);
    labels["org.opencontainers.image.created"] = new Date(c.created - 2 * DAY).toISOString().slice(0, 19) + "+00:00";
  } else if (p.imageLabels["org.opencontainers.image.source"]) {
    labels["org.opencontainers.image.version"] = tag;
    labels["org.opencontainers.image.revision"] = hex("rev:" + c.image, 40);
  }
  const all = [...(p.entrypoint ?? []), ...(p.cmd ?? [])];
  const h = base.health;
  let healthInfo: ContainerDetails["healthInfo"];
  if (p.health) {
    const hc = p.health;
    const now = Date.now();
    const bad = h === "unhealthy";
    const n = c.state === "running" ? Math.min(5, Math.floor((now - c.startedAt) / (hc.intervalS * 1000))) : 5;
    const lastAt = c.state === "running" ? now - ((now - c.startedAt) % (hc.intervalS * 1000)) : c.finishedAt;
    const log = Array.from({ length: n }, (_, i) => {
      const start = lastAt - (n - 1 - i) * hc.intervalS * 1000;
      return bad
        ? { start: nanoTs(start), end: nanoTs(start + hc.timeoutS * 1000), exitCode: -1, output: `Health check exceeded timeout (${hc.timeoutS}s)` }
        : { start: nanoTs(start), end: nanoTs(start + 40 + (hash(String(start)) % 300)), exitCode: 0, output: hc.ok };
    });
    const streak = bad && c.unhealthySince ? Math.floor((now - c.unhealthySince) / (hc.intervalS * 1000)) : 0;
    healthInfo = {
      status: c.state === "running" ? (h ?? "starting") : "unhealthy",
      failingStreak: streak,
      test: hc.test,
      intervalNs: hc.intervalS * 1e9,
      timeoutNs: hc.timeoutS * 1e9,
      retries: hc.retries,
      log,
    };
  }
  const n = PROJECT_ORDER.indexOf(proj);
  return {
    ...base,
    createdAt: nanoTs(c.created),
    path: all[0] ?? "",
    args: all.slice(1),
    entrypoint: p.entrypoint,
    cmd: p.cmd,
    ...(p.workingDir ? { workingDir: p.workingDir } : {}),
    ...(p.user ? { user: p.user } : {}),
    hostname: p.hostNet ? HOST.hostname : short(fullId(c.name)),
    tty: false,
    env: [...p.env].sort(),
    stateInfo: {
      status: c.state,
      running,
      paused: c.state === "paused",
      restarting: false,
      oomKilled: false,
      pid: running ? 1200 + (hash(c.name + c.startedAt) % 60000) : 0,
      exitCode: c.state === "exited" ? c.exitCode : 0,
      startedAt: nanoTs(c.startedAt),
      ...(c.finishedAt ? { finishedAt: nanoTs(c.finishedAt) } : {}),
      restartCount: c.restartCount,
    },
    mounts: [...p.mounts].sort((a, b) => a.destination.localeCompare(b.destination)),
    networks: p.hostNet
      ? []
      : [
          {
            name: `${proj}_default`,
            ...(running ? { ip: `172.${subnet}.0.${host}`, prefix: 16, gateway: `172.${subnet}.0.1`, mac: `02:42:ac:${subnet.toString(16)}:00:${p2(host)}` } : {}),
            aliases: [c.name, p.service],
            dnsNames: [c.name, p.service, short(fullId(c.name))],
          },
        ],
    restartPolicy: { name: p.restart, maxRetry: 0 },
    labels,
    limits: { nanoCpus: p.nanoCpus ?? 0, cpuShares: 0, memory: p.memLimit ?? 0, memorySwap: p.memLimit ? p.memLimit * 2 : 0, pidsLimit: p.pidsLimit ?? 0 },
    ...(healthInfo ? { healthInfo } : {}),
    imageInfo: {
      id: base.imageId,
      tags: [c.image],
      digests: [`${base.imageRef}@${digest(c)}`],
      created: nanoTs(c.created - (2 + n) * DAY),
      size: Math.round(p.imageSize),
      architecture: "amd64",
      os: "linux",
    },
    security: { privileged: !!p.privileged, ...(p.capAdd ? { capAdd: p.capAdd } : {}), devices: p.devices ?? 0 },
    logDriver: "json-file",
    platform: "linux",
  };
}

// ---------- live topics ----------

const walks = new Map<string, { cpu: Walk; mem: Walk; rx: Walk; tx: Walk }>();
const coresPct = 100; // stats are percent of total host capacity

function statsFor(c: CState): ContainerStats {
  const p = profile(c.name);
  let w = walks.get(c.name);
  if (!w) {
    w = {
      cpu: new Walk(p.cpu, p.cpu, p.cpu * 0.6, 0, coresPct, 0.04),
      mem: new Walk(p.mem, p.mem, p.mem * 0.02, p.mem * 0.85, p.mem * 1.25, 0.01),
      rx: new Walk(p.rx, p.rx, p.rx * 0.5 + 200, 0, p.rx * 4 + 2000, 0.05),
      tx: new Walk(p.tx, p.tx, p.tx * 0.5 + 200, 0, p.tx * 4 + 2000, 0.05),
    };
    walks.set(c.name, w);
  }
  const fresh = Date.now() - c.startedAt < 20_000;
  return {
    cpu: w.cpu.next() * (fresh ? 4 : 1),
    mem: Math.round(w.mem.next() * (fresh ? 0.6 : 1)),
    memLimit: p.memLimit ?? HOST.memTotal,
    netRx: w.rx.next(),
    netTx: w.tx.next(),
  };
}

function stats(): Record<string, ContainerStats> {
  const out: Record<string, ContainerStats> = {};
  for (const c of store.get().list) if (c.state === "running") out[short(fullId(c.name))] = statsFor(c);
  return out;
}

topic("containers", { snapshot: list, tick: list, every: 15_000 });
topic("containers.stats", { snapshot: stats, tick: stats, every: 3000 });

/** Push the list now and again when health checks and probes settle. */
function changed() {
  refresh("containers");
  setTimeout(() => refresh("containers"), START_PERIOD + 500);
  setTimeout(() => refresh("containers"), PROBE_DELAY + 500);
}

// ---------- actions ----------

function startC(c: CState) {
  const now = Date.now();
  if (c.state === "running") return;
  if (c.state === "paused") {
    c.state = "running";
    return;
  }
  c.state = "running";
  c.startedAt = now;
  c.exitCode = 0;
  // A fresh start clears a stuck health check.
  if (profile(c.name).health) {
    c.health = "healthy";
    c.unhealthySince = undefined;
  }
}

function stopC(c: CState) {
  if (c.state === "exited") return;
  c.state = "exited";
  c.finishedAt = Date.now();
  c.exitCode = profile(c.name).stopCode ?? 0;
}

// ---------- update jobs ----------

function layers(seed: string, n: number) {
  return Array.from({ length: n }, (_, i) => hex(seed + i, 12));
}

function updateSteps(c: CState, strategy: "compose" | "recreate", elevated: boolean, dir: string, proj: string): { steps: JobStep[]; result: Record<string, unknown> } {
  const p = profile(c.name);
  const svc = p.service;
  const oldImageId = sha("image:" + c.image);
  const newImage = c.update ? `${splitRef(c.image)[0]}:${c.update}` : c.image;
  const newImageId = sha("image:" + newImage);
  const ls = layers(newImage, 4 + (hash(c.name) % 4));
  const base = `docker compose -p ${proj} -f ${dir}/compose.yaml`;
  if (strategy === "compose" && elevated) {
    return {
      steps: [
        { ms: 900, stage: "Updating via docker compose (elevated)" },
        {
          ms: c.update ? 4200 : 1500,
          log: c.update
            ? [` ${svc} Pulling`, ...ls.map((l) => ` ${l} Pull complete`), ` ${svc} Pulled`, ` Container ${c.name}  Recreate`, ` Container ${c.name}  Recreated`, ` Container ${c.name}  Starting`, ` Container ${c.name}  Started`]
            : [` ${svc} Pulling`, ` ${svc} Pulled`, ` Container ${c.name}  Running`],
        },
      ],
      result: { strategy: "compose" },
    };
  }
  if (strategy === "compose") {
    return {
      steps: [
        { ms: 400, stage: `docker pull ${svc}`, log: [`$ ${base} pull ${svc}`, ` ${svc} Pulling`] },
        ...(c.update
          ? [
              { ms: 700, log: ls.map((l) => ` ${l} Pulling fs layer`) },
              { ms: 2600, log: ls.map((l) => ` ${l} Downloading`) },
              { ms: 900, log: [...ls.map((l) => ` ${l} Pull complete`), ` ${svc} Pulled`] },
            ]
          : [{ ms: 900, log: [` ${svc} Pulled`] }]),
        { ms: 400, stage: `docker up -d ${svc}`, log: [`$ ${base} up -d ${svc}`] },
        ...(c.update
          ? [
              { ms: 900, log: [` Container ${c.name}  Recreate`] },
              { ms: 1300, log: [` Container ${c.name}  Recreated`, ` Container ${c.name}  Starting`] },
              { ms: 600, log: [` Container ${c.name}  Started`] },
            ]
          : [{ ms: 500, log: [` Container ${c.name}  Running`] }]),
      ],
      result: { strategy: "compose" },
    };
  }
  const newId = hex("container:" + c.name + ":" + newImage);
  if (!c.update) {
    return {
      steps: [
        { ms: 500, stage: `Pulling ${c.image}`, log: [`Pulling ${c.image}`] },
        { ms: 1200, log: [`Digest: ${digest(c)}`, `Status: Image is up to date for ${c.image}`, "Image unchanged: already up to date"] },
      ],
      result: { oldImage: oldImageId, newImage: oldImageId, updated: false, message: "already up to date" },
    };
  }
  return {
    steps: [
      { ms: 500, stage: `Pulling ${newImage}`, log: [`Pulling ${newImage}`, `${splitRef(newImage)[1]}: Pulling from ${splitRef(newImage)[0]}`, ...ls.map((l) => `${l}: Pulling fs layer`)] },
      { ms: 2800, stage: `Pulling ${newImage}`, current: `${ls.length} layer(s) downloading` },
      { ms: 800, log: [...ls.map((l) => `${l}: Pull complete`), `Digest: ${sha("digest:" + newImage)}`, `Status: Downloaded newer image for ${newImage}`, `New image ${short(newImageId)} (was ${short(oldImageId)})`] },
      { ms: 600, stage: "Recreating container", current: c.name, log: [`Renamed ${c.name} to ${c.name}-wargo-old`, "Stopping old container"] },
      { ms: 1200, log: [`Created ${short(newId)}`, "Started new container, checking it stays up"] },
      { ms: 3000, log: ["Removed old container", "Done"] },
    ],
    result: { oldImage: oldImageId, newImage: newImageId, updated: true, newId },
  };
}

// ---------- logs ----------

interface LogSource {
  history: LogLine[];
  next?: () => LogLine;
}

const mk = (t: number, [stream, text]: [Stream, string]): LogLine => ({ ts: nanoTs(t), stream, text });

function lineAt(c: CState, p: Profile, t: number): [Stream, string] {
  if (p.error && c.health === "unhealthy" && Math.random() < 0.25) return p.error(t);
  return p.line(t);
}

function logSource(c: CState, tail: number): LogSource {
  const p = profile(c.name);
  const now = Date.now();
  const end = c.state === "exited" ? c.finishedAt : now;
  const out: LogLine[] = [];
  const want = Math.max(0, Math.min(tail, 400));
  // Steady lines going back in time, then boot lines if the start is in range.
  const steady: LogLine[] = [];
  let t = end - 4000;
  const quiet = c.name === "nextcloud-cron" ? 60_000 : 10_000;
  while (steady.length < want && t > c.startedAt + 8000) {
    steady.push(mk(t, lineAt(c, p, t)));
    t -= quiet + Math.random() * quiet * 5;
  }
  steady.reverse();
  if (steady.length < want) p.boot(c.startedAt).forEach((l, i) => out.push(mk(c.startedAt + 200 + i * 120, l)));
  out.push(...steady);
  if (c.state === "exited") (p.stop?.(end) ?? []).forEach((l, i) => out.push(mk(end - 300 + i * 60, l)));
  const history = want ? out.slice(-want) : [];
  return { history, next: c.state === "running" ? () => mk(Date.now(), lineAt(c, p, Date.now())) : undefined };
}

const logsSocket = ws.link(/\/ws\/containers\/[^/]+\/logs/);

const logsHandler = logsSocket.addEventListener("connection", ({ client }) => {
  const url = new URL(client.url);
  const ref = decodeURIComponent(url.pathname.split("/").slice(-2)[0] ?? "");
  const tail = Math.min(Math.max(Number(url.searchParams.get("tail") ?? 200) || 0, 0), 5000);
  const c = find(ref);
  if (!c) {
    client.send(JSON.stringify({ error: `Error response from daemon: No such container: ${ref}` }));
    client.close(1000);
    return;
  }
  const src = logSource(c, tail);
  let timer = 0;
  const startedAt = c.startedAt;
  const finish = () => {
    clearTimeout(timer);
    try {
      client.send(JSON.stringify({ eof: true }));
      client.close(1000);
    } catch {
      /* already closed */
    }
  };
  setTimeout(() => {
    for (let i = 0; i < src.history.length; i += 500) client.send(JSON.stringify({ lines: src.history.slice(i, i + 500) }));
    if (c.state === "exited") return finish();
    const loop = () => {
      const cur = find(ref);
      // Docker ends a follow when the container stops or is recreated.
      if (!cur || cur.state === "exited" || cur.startedAt !== startedAt) {
        const p = profile(c.name);
        if (cur?.state === "exited") client.send(JSON.stringify({ lines: (p.stop?.(Date.now()) ?? []).map((l) => mk(Date.now(), l)) }));
        return finish();
      }
      if (cur.state === "running" && src.next) client.send(JSON.stringify({ lines: [src.next()] }));
      timer = window.setTimeout(loop, 900 + Math.random() * 1600);
    };
    timer = window.setTimeout(loop, 800);
  }, 120);
  client.addEventListener("close", () => clearTimeout(timer));
});

// ---------- handlers ----------

const unknown = (ref: string) => notFound(`No such container: ${ref}`);
const validRef = (s: string) => /^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,254}$/.test(s);

export const handlers: (RequestHandler | WebSocketHandler)[] = [
  api.get("/api/containers", () => json(list())),

  api.get("/api/containers/updates", () => json(updatesStatus())),

  api.post("/api/containers/check-updates", async () => {
    if (!checking) {
      checking = (async () => {
        refresh("containers");
        await delay(2500 + Math.random() * 2000);
        store.update((s) => {
          s.checkedAt = Date.now();
        });
        checking = null;
        refresh("containers");
      })();
    }
    await checking;
    return json(updatesStatus());
  }),

  api.get("/api/containers/:id/icon", ({ params }) => {
    const c = find(String(params.id));
    const slug = c && def(c.name)?.icon;
    return slug ? iconResponse(slug) : unknown(String(params.id));
  }),

  api.get("/api/containers/:id/logs", ({ request, params }) => {
    const c = find(String(params.id));
    if (!c) return unknown(String(params.id));
    const tail = Math.min(Math.max(Number(query(request).get("tail") ?? 500) || 500, 1), 10000);
    return json({ lines: logSource(c, tail).history, truncated: false });
  }),

  api.get("/api/containers/:id", ({ params }) => {
    const ref = String(params.id);
    if (!validRef(ref)) return badRequest("invalid container id");
    const c = find(ref);
    return c ? json(details(c)) : unknown(ref);
  }),

  api.post("/api/containers/:id/:action", async ({ request, params }) => {
    const ref = String(params.id);
    if (!validRef(ref)) return badRequest("invalid container id");
    const action = String(params.action);
    if (!["start", "stop", "restart", "update"].includes(action)) return notFound();
    const c = find(ref);
    if (!c) return unknown(ref);

    if (action === "update") {
      const req = await body<{ strategy?: string }>(request);
      let strategy = !req.strategy || req.strategy === "api" ? "auto" : req.strategy;
      if (!["auto", "recreate", "compose"].includes(strategy)) return badRequest("strategy must be auto, recreate or compose");
      const p = profile(c.name);
      const proj = project(c);
      const dir = composeDir(proj, p);
      let elevated = false;
      if (strategy === "auto") strategy = p.rootCompose ? "recreate" : "compose";
      if (strategy === "compose" && p.rootCompose) {
        elevated = true;
        const need = requireApproval(request, "containers.update", `docker compose pull && up -d (project ${proj})`);
        if (need) return need;
      }
      const id = fullId(c.name);
      if (updating.has(id)) return fail(409, "busy", "An update for this container is already running");
      updating.add(id);
      const { steps, result } = updateSteps(c, strategy as "compose" | "recreate", elevated, dir, proj);
      const name = c.name;
      const job = startJob({
        kind: "containers.update",
        title: `Update ${name}`,
        steps,
        result,
        onDone: () => {
          updating.delete(id);
          store.update((s) => {
            const cur = s.list.find((x) => x.name === name);
            if (!cur || !cur.update) return;
            const wasRunning = cur.state !== "exited";
            cur.image = `${splitRef(cur.image)[0]}:${cur.update}`;
            cur.update = null;
            cur.checkError = undefined;
            cur.created = Date.now();
            if (wasRunning) {
              cur.state = "running";
              cur.startedAt = Date.now();
              cur.restartCount = 0;
              if (profile(name).health) {
                cur.health = "healthy";
                cur.unhealthySince = undefined;
              }
            }
          });
          changed();
        },
      });
      // A canceled job never reaches onDone.
      setTimeout(() => updating.delete(id), steps.reduce((a, s) => a + s.ms, 0) + 5000);
      return json({ jobId: job.id, strategy });
    }

    await delay(action === "start" ? 500 : 1100 + Math.random() * 800);
    store.update((s) => {
      const cur = s.list.find((x) => x.name === c.name)!;
      if (action === "start") startC(cur);
      else if (action === "stop") stopC(cur);
      else {
        stopC(cur);
        startC(cur);
      }
    });
    changed();
    return json({ ok: true });
  }),

  logsHandler,
];
