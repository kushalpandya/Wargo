// SPDX-License-Identifier: AGPL-3.0-only
// Torrents: Prowlarr search over a few made-up public indexers (only legal
// content: Linux ISOs, open movies, public-domain films, CC music, open
// datasets) and a Transmission whose downloads progress live.
import { delay, type RequestHandler, type WebSocketHandler } from "msw";
import type { Category, IndexerError, IndexerRef, SearchResponse, SearchResult, Torrent, TorrentStatus, TransmissionPayload } from "@/features/torrents/api";
import { api, badRequest, body, fail, json, query } from "../core/http";
import { refresh, topic } from "../core/bus";
import { persisted } from "../core/store";
import { DAY, GB, MB, MIN, TB, Walk, seeded } from "../core/rand";
import { prowlarrStatus, transmissionStatus } from "./integrations";

// ---------------------------------------------------------------------------
// Hashing helpers

function hash32(s: string, seed = 0x811c9dc5): number {
  let h = seed >>> 0;
  for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), 0x01000193) >>> 0;
  return h;
}

/** Stable 40-hex info hash for a title. */
function infoHash(title: string): string {
  let out = "";
  for (let i = 0; out.length < 40; i++) out += hash32(title, 0x9e3779b9 + i * 7919).toString(16).padStart(8, "0");
  return out.slice(0, 40);
}

// ---------------------------------------------------------------------------
// Indexers and categories

interface Indexer extends IndexerRef {
  /** Seeder multiplier. */
  weight: number;
  torrentFile: boolean;
  grabs: boolean;
  tracker: string;
}

const INDEXERS: Indexer[] = [
  { id: 1, name: "Internet Archive", weight: 0.5, torrentFile: true, grabs: false, tracker: "http://bt1.archive.org:6969/announce" },
  { id: 2, name: "LinuxTracker", weight: 1.2, torrentFile: true, grabs: true, tracker: "http://linuxtracker.org:2710/announce" },
  { id: 3, name: "Academic Torrents", weight: 0.35, torrentFile: true, grabs: true, tracker: "udp://tracker.academictorrents.com:6969/announce" },
  { id: 4, name: "Public Domain Torrents", weight: 0.6, torrentFile: false, grabs: false, tracker: "udp://tracker.opentrackr.org:1337/announce" },
  { id: 5, name: "Legit Torrents", weight: 0.7, torrentFile: true, grabs: true, tracker: "udp://tracker.opentrackr.org:1337/announce" },
  { id: 6, name: "FOSS Torrents", weight: 0.9, torrentFile: true, grabs: true, tracker: "udp://tracker.opentrackr.org:1337/announce" },
];
const indexerRefs = (): IndexerRef[] => [...INDEXERS].sort((a, b) => a.name.localeCompare(b.name)).map(({ id, name }) => ({ id, name }));

const CAT_NAMES: Record<number, string> = {
  2000: "Movies",
  2030: "Movies/SD",
  2040: "Movies/HD",
  2045: "Movies/UHD",
  3000: "Audio",
  3010: "Audio/MP3",
  3040: "Audio/Lossless",
  4000: "PC",
  4020: "PC/ISO",
  4030: "PC/Mac",
  5000: "TV",
  5080: "TV/Documentary",
  7000: "Books",
  7020: "Books/EBook",
  8000: "Other",
  8010: "Other/Misc",
};
const cats = (sub: number): Category[] => {
  const top = Math.floor(sub / 1000) * 1000;
  return top === sub ? [{ id: top, name: CAT_NAMES[top] }] : [{ id: top, name: CAT_NAMES[top] }, { id: sub, name: CAT_NAMES[sub] }];
};

// ---------------------------------------------------------------------------
// Catalog

type Kind = "distro" | "software" | "blender" | "film" | "doc" | "music" | "dataset" | "book";

const SITES: Record<Kind, number[]> = {
  distro: [2, 6, 5, 1],
  software: [6, 2, 5],
  blender: [1, 6, 5],
  film: [4, 1, 5],
  doc: [1, 4, 3],
  music: [1, 5],
  dataset: [3, 1],
  book: [1, 3, 5],
};

interface Item {
  title: string;
  size: number;
  cat: number;
  kind: Kind;
  /** 0..1, how popular it is. */
  pop: number;
  ageDays: number;
  keys: string;
}

const items: Item[] = [];
const add = (kind: Kind, cat: number, keys: string, list: [string, number, number?, number?][]) => {
  for (const [title, gb, pop = 0.5, ageDays = 90] of list) items.push({ title, size: Math.round(gb * GB), cat, kind, pop, ageDays, keys: `${keys} ${title}`.toLowerCase() });
};

add("distro", 4020, "ubuntu linux iso canonical lts", [
  ["ubuntu-24.04.1-desktop-amd64.iso", 5.69, 1, 75],
  ["ubuntu-24.04.1-live-server-amd64.iso", 2.61, 0.8, 75],
  ["ubuntu-24.10-desktop-amd64.iso", 5.77, 0.85, 26],
  ["ubuntu-24.10-live-server-amd64.iso", 2.69, 0.6, 26],
  ["ubuntu-22.04.5-desktop-amd64.iso", 4.69, 0.7, 63],
  ["ubuntu-22.04.5-live-server-amd64.iso", 1.98, 0.55, 63],
  ["ubuntu-24.04.1-preinstalled-server-arm64+raspi.img.xz", 1.18, 0.35, 75],
  ["ubuntu-20.04.6-live-server-amd64.iso", 1.42, 0.25, 600],
  ["kubuntu-24.04.1-desktop-amd64.iso", 4.12, 0.4, 75],
  ["xubuntu-24.04.1-desktop-amd64.iso", 3.94, 0.35, 75],
  ["lubuntu-24.04.1-desktop-amd64.iso", 3.08, 0.25, 75],
  ["ubuntu-mate-24.04.1-desktop-amd64.iso", 4.21, 0.2, 75],
  ["ubuntu-budgie-24.04.1-desktop-amd64.iso", 4.03, 0.15, 75],
  ["ubuntustudio-24.04.1-dvd-amd64.iso", 6.11, 0.15, 75],
]);
add("distro", 4020, "debian linux iso", [
  ["debian-12.8.0-amd64-netinst.iso", 0.63, 0.9, 20],
  ["debian-12.8.0-amd64-DVD-1.iso", 3.72, 0.6, 20],
  ["debian-live-12.8.0-amd64-gnome.iso", 3.19, 0.5, 20],
  ["debian-live-12.8.0-amd64-kde.iso", 3.41, 0.4, 20],
  ["debian-live-12.8.0-amd64-xfce.iso", 2.88, 0.35, 20],
  ["debian-12.8.0-arm64-netinst.iso", 0.53, 0.3, 20],
  ["debian-11.11.0-amd64-netinst.iso", 0.39, 0.2, 120],
]);
add("distro", 4020, "fedora linux iso redhat", [
  ["Fedora-Workstation-Live-x86_64-41-1.4.iso", 2.24, 0.8, 9],
  ["Fedora-Server-dvd-x86_64-41-1.4.iso", 2.61, 0.4, 9],
  ["Fedora-KDE-Live-x86_64-41-1.4.iso", 2.63, 0.45, 9],
  ["Fedora-Everything-netinst-x86_64-41-1.4.iso", 0.81, 0.25, 9],
  ["Fedora-Workstation-Live-x86_64-40-1.14.iso", 2.13, 0.3, 200],
]);
add("distro", 4020, "arch linux archlinux iso", [
  ["archlinux-2024.11.01-x86_64.iso", 1.17, 0.85, 2],
  ["archlinux-2024.10.01-x86_64.iso", 1.15, 0.3, 33],
  ["archlinux-2024.09.01-x86_64.iso", 1.14, 0.15, 63],
]);
add("distro", 4020, "linux mint linuxmint iso cinnamon", [
  ["linuxmint-22-cinnamon-64bit.iso", 2.84, 0.85, 110],
  ["linuxmint-22-mate-64bit.iso", 2.83, 0.35, 110],
  ["linuxmint-22-xfce-64bit.iso", 2.78, 0.35, 110],
  ["linuxmint-21.3-cinnamon-64bit.iso", 2.92, 0.3, 300],
  ["lmde-6-cinnamon-64bit.iso", 2.68, 0.25, 400],
]);
add("distro", 4020, "kali linux iso security pentest", [
  ["kali-linux-2024.3-installer-amd64.iso", 4.04, 0.75, 75],
  ["kali-linux-2024.3-live-amd64.iso", 4.12, 0.6, 75],
  ["kali-linux-2024.3-installer-netinst-amd64.iso", 0.48, 0.3, 75],
  ["kali-linux-2024.3-raspberry-pi-arm64.img.xz", 2.1, 0.2, 75],
]);
add("distro", 4020, "opensuse suse linux iso", [
  ["openSUSE-Leap-15.6-DVD-x86_64-Media.iso", 4.31, 0.4, 160],
  ["openSUSE-Tumbleweed-DVD-x86_64-Current.iso", 4.42, 0.45, 3],
]);
add("distro", 4020, "manjaro linux iso", [
  ["manjaro-kde-24.1.2-241104-linux610.iso", 4.02, 0.45, 5],
  ["manjaro-gnome-24.1.2-241104-linux610.iso", 3.88, 0.35, 5],
  ["manjaro-xfce-24.1.2-241104-linux610.iso", 3.61, 0.3, 5],
]);
add("distro", 4020, "rocky almalinux alma centos rhel linux iso enterprise", [
  ["Rocky-9.5-x86_64-dvd.iso", 10.6, 0.4, 14],
  ["Rocky-9.5-x86_64-minimal.iso", 1.81, 0.35, 14],
  ["AlmaLinux-9.5-x86_64-dvd.iso", 10.8, 0.35, 16],
  ["AlmaLinux-9.5-x86_64-minimal.iso", 1.92, 0.3, 16],
  ["CentOS-Stream-9-latest-x86_64-dvd1.iso", 11.2, 0.2, 7],
]);
add("distro", 4020, "freebsd bsd iso", [
  ["FreeBSD-14.1-RELEASE-amd64-dvd1.iso", 4.58, 0.3, 150],
  ["FreeBSD-14.1-RELEASE-amd64-disc1.iso", 1.18, 0.3, 150],
  ["FreeBSD-14.1-RELEASE-amd64-memstick.img", 1.41, 0.2, 150],
]);
add("distro", 4020, "linux iso nixos popos pop os proxmox truenas tails raspberry pi alpine elementary zorin qubes endeavouros", [
  ["nixos-gnome-24.05.6463.d51c28603def-x86_64-linux.iso", 2.61, 0.35, 40],
  ["nixos-minimal-24.05.6463.d51c28603def-x86_64-linux.iso", 1.08, 0.3, 40],
  ["pop-os_22.04_amd64_intel_44.iso", 2.63, 0.45, 80],
  ["pop-os_22.04_amd64_nvidia_44.iso", 3.29, 0.4, 80],
  ["proxmox-ve_8.2-2.iso", 1.33, 0.6, 180],
  ["TrueNAS-SCALE-24.10.0.iso", 1.62, 0.5, 34],
  ["tails-amd64-6.9.img", 1.44, 0.5, 12],
  ["2024-11-19-raspios-bookworm-arm64.img.xz", 1.19, 0.55, 14],
  ["alpine-standard-3.20.3-x86_64.iso", 0.22, 0.3, 60],
  ["alpine-extended-3.20.3-x86_64.iso", 1.01, 0.2, 60],
  ["elementaryos-7.1-stable.20230926rc.iso", 2.97, 0.3, 400],
  ["Zorin-OS-17.2-Core-64-bit.iso", 3.61, 0.35, 70],
  ["Qubes-R4.2.3-x86_64.iso", 6.41, 0.2, 90],
  ["EndeavourOS_Endeavour_neo-2024.09.22.iso", 2.92, 0.3, 42],
]);
add("software", 4000, "software app open source", [
  ["LibreOffice_24.8.3_Linux_x86-64_deb.tar.gz", 0.21, 0.4, 9],
  ["blender-4.2.3-linux-x64.tar.xz", 0.33, 0.45, 30],
  ["krita-5.2.6-x86_64.appimage", 0.18, 0.2, 140],
  ["Godot_v4.3-stable_linux.x86_64.zip", 0.06, 0.25, 95],
  ["openmediavault_7.4.6-amd64.iso", 0.97, 0.3, 50],
  ["gimp-2.10.38-setup.exe", 0.31, 0.25, 210],
]);
add("blender", 2045, "blender open movie animation short film cc by creative commons", [
  ["Big Buck Bunny (2008) [2160p 60fps] [Blender Open Movie]", 0.64, 0.8, 1300],
  ["Big Buck Bunny (2008) [1080p] [Blender Open Movie]", 0.69, 0.7, 1300],
  ["Big Buck Bunny (2008) 3D Stereo [1080p]", 0.83, 0.2, 1300],
  ["Sintel (2010) [2160p] [Blender Open Movie]", 5.18, 0.6, 1200],
  ["Sintel (2010) [1080p] [Blender Open Movie]", 1.31, 0.6, 1200],
  ["Tears of Steel (2012) [2160p] [Blender Open Movie]", 6.27, 0.5, 1100],
  ["Tears of Steel (2012) [1080p] [Blender Open Movie]", 1.29, 0.45, 1100],
  ["Tears of Steel - Mango Project Raw Footage (CC BY 3.0)", 122.4, 0.1, 1500],
  ["Elephants Dream (2006) [1080p] [Blender Open Movie]", 0.82, 0.3, 1400],
  ["Cosmos Laundromat (2015) [1080p] [Blender Open Movie]", 1.07, 0.35, 900],
  ["Spring (2019) [2160p] [Blender Open Movie]", 1.62, 0.35, 700],
  ["Agent 327 Operation Barbershop (2017) [1080p]", 0.47, 0.3, 800],
  ["Caminandes 3 Llamigos (2016) [1080p]", 0.18, 0.25, 850],
  ["Sprite Fright (2021) [2160p] [Blender Open Movie]", 2.43, 0.35, 400],
  ["Charge (2022) [2160p] [Blender Open Movie]", 1.12, 0.25, 300],
  ["Wing It! (2023) [2160p] [Blender Studio]", 0.94, 0.25, 200],
  ["Blender Open Movies Collection 2006-2023 [1080p]", 14.6, 0.3, 180],
]);
add("film", 2040, "public domain classic film movie old silent", [
  ["Night of the Living Dead (1968) [1080p Restored] [Public Domain]", 1.62, 0.6, 500],
  ["Nosferatu (1922) [720p] [Public Domain]", 1.12, 0.5, 700],
  ["Metropolis (1927) [1080p] [Public Domain]", 2.31, 0.45, 600],
  ["The General (1926) Buster Keaton [720p]", 1.02, 0.35, 650],
  ["Sherlock Jr. (1924) Buster Keaton [720p]", 0.61, 0.3, 650],
  ["The Kid (1921) Charlie Chaplin [720p]", 0.71, 0.35, 700],
  ["The Gold Rush (1925) Charlie Chaplin [1080p]", 1.48, 0.35, 400],
  ["His Girl Friday (1940) [720p]", 1.39, 0.4, 800],
  ["Charade (1963) [1080p]", 2.06, 0.45, 750],
  ["Plan 9 from Outer Space (1959) [720p]", 0.94, 0.3, 900],
  ["Detour (1945) [720p] [Film Noir]", 0.81, 0.25, 900],
  ["D.O.A. (1949) [720p] [Film Noir]", 0.97, 0.25, 900],
  ["Carnival of Souls (1962) [1080p]", 1.36, 0.3, 850],
  ["The Last Man on Earth (1964) Vincent Price [720p]", 1.04, 0.3, 850],
  ["The Little Shop of Horrors (1960) [720p]", 0.79, 0.3, 900],
  ["The Cabinet of Dr. Caligari (1920) [1080p]", 1.21, 0.3, 700],
  ["A Trip to the Moon (1902) [Hand Colored] [1080p]", 0.24, 0.3, 500],
  ["Steamboat Willie (1928) [1080p] [Public Domain 2024]", 0.12, 0.5, 300],
  ["The Phantom of the Opera (1925) [720p]", 1.18, 0.25, 800],
  ["Safety Last! (1923) Harold Lloyd [720p]", 0.88, 0.25, 800],
]);
add("doc", 5080, "documentary nature wildlife animals ocean earth planet science public domain", [
  ["NASA - Earth Views from the ISS [2160p] [Public Domain]", 18.4, 0.5, 120],
  ["NASA - Apollo 11 Mission Footage Collection (1969) [Public Domain]", 24.7, 0.45, 400],
  ["Prelinger Archives - Nature and Wildlife Films 1930-1970", 31.2, 0.3, 900],
  ["National Park Service - Yellowstone B-Roll [2160p] [Public Domain]", 42.8, 0.3, 300],
  ["USFWS - Wildlife Footage Collection [1080p] [Public Domain]", 26.1, 0.25, 500],
  ["NOAA Ocean Exploration - Deep Sea Expeditions 2016-2023 [1080p]", 58.3, 0.35, 200],
  ["Nanook of the North (1922) [Documentary] [720p]", 1.06, 0.3, 700],
  ["Man with a Movie Camera (1929) [Documentary] [1080p]", 1.42, 0.3, 600],
  ["The River (1938) Pare Lorentz [Documentary] [Public Domain]", 0.58, 0.2, 800],
  ["The Plow That Broke the Plains (1936) [Documentary] [Public Domain]", 0.46, 0.15, 800],
  ["Wikimedia Commons - Featured Nature Videos 2024 (CC BY-SA)", 9.7, 0.2, 60],
  ["Ocean Wildlife Stock Footage [2160p] (CC BY 4.0)", 12.3, 0.25, 140],
  ["NASA - Hubble Ultra Deep Field Flythrough [2160p]", 3.4, 0.3, 250],
  ["ESA - Rosetta Mission Imagery Archive (CC BY-SA 3.0 IGO)", 15.9, 0.15, 700],
]);
add("music", 3040, "music audio album creative commons cc flac mp3 free", [
  ["Nine Inch Nails - Ghosts I-IV (2008) [FLAC] (CC BY-NC-SA)", 1.12, 0.6, 1500],
  ["Nine Inch Nails - The Slip (2008) [FLAC 24bit] (CC BY-NC-SA)", 0.86, 0.5, 1500],
  ["Kevin MacLeod - Incompetech Complete Collection [MP3 320] (CC BY 4.0)", 11.8, 0.45, 300],
  ["Jonathan Coulton - Thing a Week (2006) [FLAC] (CC BY-NC)", 1.38, 0.3, 1400],
  ["Musopen - Chopin Complete Works [FLAC] [Public Domain]", 6.9, 0.4, 600],
  ["Musopen - Beethoven Symphonies 1-9 [FLAC] [Public Domain]", 3.8, 0.4, 600],
  ["Josh Woodward - Complete Discography [MP3 320] (CC BY)", 2.7, 0.2, 400],
  ["Brad Sucks - Out of It (2012) [FLAC] (CC BY-SA)", 0.38, 0.2, 1300],
  ["Grateful Dead - 1977-05-08 Barton Hall Cornell (AUD) [FLAC]", 1.94, 0.35, 900],
]);
add("dataset", 8010, "dataset data open research machine learning dump archive", [
  ["enwiki-20241101-pages-articles-multistream.xml.bz2", 22.4, 0.5, 2],
  ["planet-241104.osm.pbf (OpenStreetMap)", 76.3, 0.45, 2],
  ["COCO 2017 Train and Val Images", 19.3, 0.4, 900],
  ["Mozilla Common Voice Corpus 19.0 English (CC0)", 85.2, 0.3, 40],
  ["Stack Exchange Data Dump 2024-09-30", 92.1, 0.35, 34],
  ["LibriSpeech ASR Corpus (CC BY 4.0)", 59.6, 0.3, 1100],
  ["NYC TLC Trip Record Data 2009-2024 (Parquet)", 160.4, 0.2, 60],
  ["MNIST Handwritten Digits (Original IDX)", 0.05, 0.25, 1600],
]);
add("book", 7020, "books ebook epub public domain gutenberg library", [
  ["Standard Ebooks - Complete Collection 2024-10 [EPUB] [Public Domain]", 3.1, 0.4, 30],
  ["Project Gutenberg - Top 1000 eBooks [EPUB]", 0.94, 0.35, 120],
  ["Project Gutenberg DVD August 2024 [ISO]", 8.2, 0.25, 90],
  ["LibriVox - Sherlock Holmes Complete Audiobooks [MP3] [Public Domain]", 4.6, 0.3, 400],
]);

// ---------------------------------------------------------------------------
// Results for words not in the catalog: built from the query.

const titleCase = (s: string) => s.replace(/\b\w/g, (c) => c.toUpperCase());

function generated(q: string, r: () => number): Item[] {
  const T = titleCase(q);
  const slug = q.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
  const year = 1925 + Math.floor(r() * 40);
  const yr2 = 1930 + Math.floor(r() * 35);
  const tpl: [Kind, number, string, number][] = [
    ["doc", 8010, `${T} - Internet Archive Collection [Public Domain]`, 4 + r() * 40],
    ["film", 2040, `${T} (${year}) [720p] [Public Domain]`, 0.7 + r() * 1.2],
    ["doc", 5080, `${T} (${yr2}) [Documentary] [Prelinger Archives]`, 0.3 + r() * 0.9],
    ["dataset", 8010, `${slug}-dataset-2024.tar.zst`, 2 + r() * 60],
    ["music", 3040, `${T} - Field Recordings [FLAC] (CC BY 4.0)`, 0.4 + r() * 2],
    ["music", 3010, `${T} - Free Music Archive Compilation [MP3 320] (CC BY-NC)`, 0.2 + r() * 0.8],
    ["doc", 5080, `${T} Lecture Series - MIT OpenCourseWare [720p] (CC BY-NC-SA)`, 3 + r() * 12],
    ["book", 7020, `${T} - Project Gutenberg eBooks [EPUB] [Public Domain]`, 0.02 + r() * 0.3],
    ["doc", 2045, `${T} - NASA Image and Video Library [2160p] [Public Domain]`, 2 + r() * 20],
    ["blender", 2040, `${T} (2023) Blender Community Short Film [1080p] (CC BY)`, 0.3 + r() * 1.2],
    ["doc", 8010, `${T} - Wikimedia Commons Media Pack 2024 (CC BY-SA)`, 1 + r() * 9],
    ["book", 3010, `${T} - LibriVox Audiobook [MP3] [Public Domain]`, 0.1 + r() * 0.6],
  ];
  return tpl
    .filter(() => r() < 0.75)
    .map(([kind, cat, title, gb]) => ({ title, size: Math.round(gb * GB), cat, kind, pop: 0.05 + r() * 0.3, ageDays: 20 + r() * 900, keys: title.toLowerCase() }));
}

// ---------------------------------------------------------------------------
// Search

interface Entry {
  result: SearchResult;
  hash: string;
  tracker: string;
}
/** Result id -> entry; like the server's cache, it is lost on reload. */
const entries = new Map<string, Entry>();

const tokens = (q: string) =>
  q
    .toLowerCase()
    .split(/[^a-z0-9.+]+/)
    .filter((w) => w.length > 1);

function search(q: string, wantCats: number[], wantIdx: number[]): Omit<SearchResponse, "tookMs"> {
  const r = seeded(hash32(q.toLowerCase()));
  const words = tokens(q);
  const score = (it: Item) => words.filter((w) => it.keys.includes(w)).length;
  let found = items.filter((it) => words.length > 0 && score(it) === words.length);
  if (found.length < 6 && words.length > 1) found = found.concat(items.filter((it) => score(it) > 0 && score(it) < words.length && r() < 0.5).slice(0, 10));
  // Archive-style results for the exact words, fewer when the catalog has plenty.
  const extra = found.length >= 6 ? [] : generated(q.trim(), r);
  found = found.concat(extra);

  // Indexers that are currently failing.
  const errors: IndexerError[] = [];
  const down = new Set<number>();
  const SLOT = 7 * 60_000;
  const slot = Math.floor(Date.now() / SLOT);
  const flaky: [number, number][] = [
    [5, 0.45],
    [4, 0.15],
  ];
  for (const [id, p] of flaky) {
    if (seeded(slot * 31 + id)() >= p) continue;
    down.add(id);
    if (wantIdx.length && !wantIdx.includes(id)) continue;
    const until = new Date((slot + 1) * SLOT + 3 * 60_000);
    const hhmm = `${String(until.getHours()).padStart(2, "0")}:${String(until.getMinutes()).padStart(2, "0")}`;
    errors.push({ indexerId: id, indexer: INDEXERS.find((i) => i.id === id)!.name, message: `Temporarily disabled after failures until ${hhmm}` });
  }

  const results: SearchResult[] = [];
  for (const it of found) {
    const sites = SITES[it.kind];
    const hash = infoHash(it.title);
    sites.forEach((id, n) => {
      const rr = seeded(hash32(it.title + "@" + id));
      // The main site always lists it; others sometimes.
      if (n > 0 && rr() > 0.55 - n * 0.1) return;
      if (down.has(id) || (wantIdx.length && !wantIdx.includes(id))) return;
      const idx = INDEXERS.find((i) => i.id === id)!;
      const base = Math.pow(it.pop, 1.6) * idx.weight * 1800 * (0.5 + rr());
      const seeders = Math.max(0, Math.round(base + rr() * 4));
      const leechers = Math.round(seeders * (0.02 + rr() * 0.25) + rr() * 3);
      const age = new Date(Date.now() - (it.ageDays + rr() * 6) * DAY - rr() * DAY).toISOString();
      const id8 = (hash32(`${id}:${it.title}`).toString(16) + hash32(`${it.title}:${id}`).toString(16)).padStart(16, "0");
      const res: SearchResult = {
        id: id8,
        title: it.title,
        size: it.size + Math.round(rr() * 4096),
        seeders: id === 1 && rr() < 0.08 ? null : seeders,
        leechers: id === 1 && rr() < 0.08 ? null : leechers,
        ...(idx.grabs ? { grabs: Math.round(seeders * (3 + rr() * 30)) } : {}),
        indexer: idx.name,
        indexerId: id,
        age,
        categories: cats(it.cat),
        infoHash: hash,
        hasMagnet: true,
        hasTorrent: idx.torrentFile,
      };
      entries.set(res.id, { result: res, hash, tracker: idx.tracker });
      results.push(res);
    });
  }
  const catOk = (res: SearchResult) => !wantCats.length || res.categories.some((c) => wantCats.includes(c.id) || wantCats.includes(Math.floor(c.id / 1000) * 1000));
  const shown = wantIdx.length ? indexerRefs().filter((i) => wantIdx.includes(i.id)) : indexerRefs();
  return { results: results.filter(catOk), indexers: indexerRefs(), errors: errors.filter((e) => shown.some((i) => i.id === e.indexerId)) };
}

function magnetOf(e: Entry): string {
  const dn = encodeURIComponent(e.result.title).replace(/%20/g, "+");
  return `magnet:?xt=urn:btih:${e.hash}&dn=${dn}&xl=${e.result.size}&tr=${encodeURIComponent(e.tracker)}&tr=${encodeURIComponent("udp://tracker.opentrackr.org:1337/announce")}`;
}

/** A small but well-formed .torrent (bencoded) for a result. */
function torrentFile(e: Entry): Uint8Array<ArrayBuffer> {
  const enc = new TextEncoder();
  const parts: Uint8Array[] = [];
  const str = (s: string | Uint8Array) => {
    const b = typeof s === "string" ? enc.encode(s) : s;
    parts.push(enc.encode(`${b.length}:`), b);
  };
  const raw = (s: string) => parts.push(enc.encode(s));
  const size = e.result.size;
  let plen = 256 * 1024;
  while (size / plen > 4000) plen *= 2;
  const n = Math.ceil(size / plen);
  const pieces = new Uint8Array(n * 20);
  const r = seeded(hash32(e.hash));
  for (let i = 0; i < pieces.length; i++) pieces[i] = Math.floor(r() * 256);
  raw("d");
  str("announce");
  str(e.tracker);
  str("created by");
  str("mktorrent 1.1");
  str("creation date");
  raw(`i${Math.floor(new Date(e.result.age ?? Date.now()).getTime() / 1000)}e`);
  str("info");
  raw("d");
  str("length");
  raw(`i${size}e`);
  str("name");
  str(e.result.title);
  str("piece length");
  raw(`i${plen}e`);
  str("pieces");
  str(pieces);
  raw("ee");
  const total = parts.reduce((s, p) => s + p.length, 0);
  const out = new Uint8Array(total);
  let off = 0;
  for (const p of parts) {
    out.set(p, off);
    off += p.length;
  }
  return out;
}

const safeFilename = (t: string) => t.replace(/[^A-Za-z0-9 ._()[\]+-]+/g, "_").split(/\s+/).join(" ").replace(/^[ ._]+|[ ._]+$/g, "").slice(0, 150) || "download";

// ---------------------------------------------------------------------------
// Transmission

interface Stored {
  id: number;
  hash: string;
  name: string;
  status: TorrentStatus;
  size: number;
  done: number;
  uploaded: number;
  error: number;
  errorString: string;
  addedDate: number;
  dir: string;
  /** Typical download speed for this torrent (bytes/s). */
  speed: number;
  /** Seeding popularity 0..1. */
  demand: number;
}

interface TrState {
  torrents: Stored[];
  nextId: number;
  last: number;
  /** Bytes written to /DATA since the demo started. */
  used: number;
}

const DIR = "/DATA/Downloads/complete";
const FREE0 = 2.3 * TB;
const sec = (ms: number) => Math.floor((Date.now() - ms) / 1000);

function initial(): TrState {
  const t = (id: number, name: string, size: number, frac: number, status: TorrentStatus, ageMs: number, ratio: number, extra: Partial<Stored> = {}): Stored => ({
    id,
    hash: infoHash(name),
    name,
    status,
    size: Math.round(size),
    done: Math.round(size * frac),
    uploaded: Math.round(size * ratio),
    error: 0,
    errorString: "",
    addedDate: sec(ageMs),
    dir: DIR,
    speed: 12 * MB,
    demand: 0.4,
    ...extra,
  });
  const torrents = [
    t(1, "ubuntu-24.04.1-desktop-amd64.iso", 5.69 * GB, 1, "seeding", 41 * DAY, 3.42, { demand: 0.9 }),
    t(2, "Big Buck Bunny (2008) [2160p 60fps] [Blender Open Movie]", 0.64 * GB, 1, "seeding", 18 * DAY, 7.81, { demand: 0.5 }),
    t(3, "archlinux-2024.11.01-x86_64.iso", 1.17 * GB, 1, "seeding", 2 * DAY, 1.27, { demand: 0.7 }),
    t(4, "debian-12.8.0-amd64-DVD-1.iso", 3.72 * GB, 0.63, "downloading", 25 * MIN, 0.04, { speed: 5.5 * MB }),
    t(5, "Tears of Steel (2012) [2160p] [Blender Open Movie]", 6.27 * GB, 0.22, "downloading", 12 * MIN, 0.01, { speed: 14 * MB }),
    t(6, "enwiki-20241101-pages-articles-multistream.xml.bz2", 22.4 * GB, 0.08, "downloading", 6 * MIN, 0, { speed: 21 * MB }),
    t(7, "Sintel (2010) [2160p] [Blender Open Movie]", 5.18 * GB, 0.41, "stopped", 3 * DAY, 0.12),
    t(8, "Prelinger Archives - Nature and Wildlife Films 1930-1970", 31.2 * GB, 0.37, "downloading", 6 * DAY, 0.05, {
      error: 2,
      errorString: "Tracker gave HTTP response code 404 (Not Found)",
      speed: 0,
    }),
    t(9, "Fedora-Workstation-Live-x86_64-41-1.4.iso", 2.24 * GB, 0, "checking", 9 * DAY, 2.1, { demand: 0.6 }),
    t(10, "Night of the Living Dead (1968) [1080p Restored] [Public Domain]", 1.62 * GB, 1, "seeding", 64 * DAY, 4.9, { demand: 0.25 }),
  ];
  return { torrents, nextId: 11, last: Date.now(), used: 0 };
}

const tr = persisted<TrState>("transmission", initial);

/** Live speeds per torrent (not saved). */
const down = new Map<number, Walk>();
const up = new Map<number, Walk>();
const rates = new Map<number, { d: number; u: number; peers: number }>();

function walkFor(m: Map<number, Walk>, id: number, target: number, max: number) {
  let w = m.get(id);
  if (!w) m.set(id, (w = new Walk(target * 0.7, target, target * 0.25, 0, max, 0.02)));
  return w;
}

/** Advances downloads since the last call (at most 30 s at a time). */
function advance() {
  tr.update((s) => {
    const now = Date.now();
    const dt = Math.min(30_000, Math.max(0, now - s.last)) / 1000;
    s.last = now;
    for (const t of s.torrents) {
      let d = 0;
      let u = 0;
      let peers = 0;
      if (t.status === "downloading" && !t.error && t.done < t.size) {
        d = walkFor(down, t.id, t.speed, t.speed * 1.8).next();
        u = walkFor(up, t.id, 180 * 1024, 900 * 1024).next();
        peers = 18 + Math.round((d / MB) * 2.2 + Math.random() * 6);
        const got = Math.min(t.size - t.done, Math.round(d * dt));
        t.done += got;
        s.used += got;
        if (t.done >= t.size) {
          t.status = "seeding";
          down.delete(t.id);
        }
      } else if (t.status === "downloading" && t.error) {
        peers = 0;
      } else if (t.status === "checking") {
        // Verifies about 40 MB/s, then seeds.
        t.done = Math.min(t.size, t.done + Math.round(40 * MB * dt));
        if (t.done >= t.size) t.status = "seeding";
      } else if (t.status === "seeding") {
        const target = t.demand * 1.6 * MB;
        u = walkFor(up, t.id, target, target * 3 + 1).next();
        if (u < 20 * 1024) u = 0;
        peers = u ? 1 + Math.round((u / MB) * 3 + Math.random() * 3) : 0;
      }
      t.uploaded += Math.round(u * dt);
      rates.set(t.id, { d: Math.round(d), u: Math.round(u), peers });
    }
  });
}

function view(t: Stored): Torrent {
  const r = rates.get(t.id) ?? { d: 0, u: 0, peers: 0 };
  const running = t.status !== "stopped";
  const left = t.size - t.done;
  return {
    id: t.id,
    hash: t.hash,
    name: t.name,
    status: t.status,
    percentDone: t.size ? Math.min(1, t.done / t.size) : 0,
    rateDownload: running ? r.d : 0,
    rateUpload: running ? r.u : 0,
    eta: t.status === "downloading" && r.d > 0 ? Math.round(left / r.d) : -1,
    sizeWhenDone: t.size,
    leftUntilDone: left,
    uploadRatio: t.uploaded === 0 && t.done === 0 ? -1 : Math.round((t.uploaded / Math.max(t.status === "checking" ? t.size : t.done, 1)) * 100) / 100,
    error: t.error,
    errorString: t.errorString,
    addedDate: t.addedDate,
    peersConnected: running ? r.peers : 0,
    downloadDir: t.dir,
  };
}

function payload(): TransmissionPayload {
  const st = transmissionStatus();
  if (!st.available) return { torrents: [], stats: { downloadSpeed: 0, uploadSpeed: 0, activeTorrentCount: 0, pausedTorrentCount: 0 }, error: st.error ?? "Transmission is not configured", at: new Date().toISOString() };
  advance();
  const s = tr.get();
  const torrents = s.torrents.map(view);
  return {
    torrents,
    stats: {
      downloadSpeed: torrents.reduce((a, t) => a + t.rateDownload, 0),
      uploadSpeed: torrents.reduce((a, t) => a + t.rateUpload, 0),
      activeTorrentCount: torrents.filter((t) => t.rateDownload > 0 || t.rateUpload > 0 || t.status === "checking").length,
      pausedTorrentCount: torrents.filter((t) => t.status === "stopped").length,
      freeSpace: Math.max(0, Math.round(FREE0 - s.used)),
    },
    at: new Date().toISOString(),
  };
}

topic("transmission", { snapshot: payload, tick: payload, every: 2000 });

/** Adds a torrent like torrent-add (duplicates by info hash). */
function addTorrent(name: string, hash: string, size: number) {
  let out = { id: 0, name, hashString: hash, duplicate: false };
  tr.update((s) => {
    const dup = s.torrents.find((t) => t.hash === hash);
    if (dup) {
      out = { id: dup.id, name: dup.name, hashString: dup.hash, duplicate: true };
      return;
    }
    const id = s.nextId++;
    s.torrents.push({
      id,
      hash,
      name,
      status: "downloading",
      size,
      done: 0,
      uploaded: 0,
      error: 0,
      errorString: "",
      addedDate: Math.floor(Date.now() / 1000),
      dir: DIR,
      // Smaller torrents finish within a minute or two in the demo.
      speed: Math.min(28 * MB, Math.max(6 * MB, size / 90)),
      demand: 0.3 + Math.random() * 0.4,
    });
    out.id = id;
  });
  refresh("transmission");
  return out;
}

const trNotConfigured = () => fail(409, "not_configured", "Transmission is not configured");
const pwNotConfigured = () => fail(409, "not_configured", "Prowlarr is not configured");
const expired = () => fail(404, "result_expired", "This search result has expired, search again");

/** null when Prowlarr works, else the error response. */
function prowlarrDown(): Response | null {
  const st = prowlarrStatus();
  if (!st.configured) return pwNotConfigured();
  if (!st.available) return fail(502, "upstream", st.error ?? "Prowlarr is not reachable");
  return null;
}
function transmissionDown(): Response | null {
  const st = transmissionStatus();
  if (!st.configured) return trNotConfigured();
  if (!st.available) return fail(502, "upstream", st.error ?? "Transmission is not reachable");
  return null;
}

function findTorrent(raw: string): Stored | Response {
  const list = tr.get().torrents;
  if (/^[0-9a-fA-F]{40}$/.test(raw)) {
    const t = list.find((x) => x.hash === raw.toLowerCase());
    return t ?? json({ ok: true });
  }
  const n = Number(raw);
  if (!Number.isInteger(n) || n <= 0) return badRequest("invalid torrent id");
  // Transmission ignores unknown ids.
  return list.find((x) => x.id === n) ?? json({ ok: true });
}

function parseInts(raw: string | null, max: number): number[] | Response {
  const out: number[] = [];
  for (const p of (raw ?? "").split(",")) {
    const s = p.trim();
    if (!s) continue;
    const n = Number(s);
    if (!/^\d+$/.test(s) || n > 1_000_000) return badRequest("invalid id list");
    out.push(n);
    if (out.length > max) return badRequest("too many ids");
  }
  return out;
}

/** Reads the info name and total length out of a .torrent. */
function parseTorrent(b: Uint8Array): { name: string; size: number } | null {
  const s = new TextDecoder("latin1").decode(b);
  if (!s.startsWith("d")) return null;
  const m = /4:name(\d+):/.exec(s);
  if (!m) return null;
  const name = s.substr(m.index + m[0].length, Number(m[1]));
  const lens = [...s.matchAll(/6:lengthi(\d+)e/g)].reduce((a, x) => a + Number(x[1]), 0);
  return { name: new TextDecoder().decode(Uint8Array.from(name, (c) => c.charCodeAt(0))), size: lens || 700 * MB };
}

export const handlers: (RequestHandler | WebSocketHandler)[] = [
  // --- Prowlarr ---
  api.get("/api/prowlarr/indexers", () => prowlarrDown() ?? json({ indexers: indexerRefs() })),

  api.get("/api/prowlarr/search", async ({ request }) => {
    const bad = prowlarrDown();
    if (bad) return bad;
    const p = query(request);
    const q = (p.get("q") ?? "").trim();
    if (!q) return badRequest("query is required");
    if (q.length > 200) return badRequest("query is too long");
    const c = parseInts(p.get("categories"), 20);
    if (c instanceof Response) return c;
    const ids = parseInts(p.get("indexers"), 200);
    if (ids instanceof Response) return ids;
    const start = performance.now();
    // Indexers answer at different speeds.
    await delay(1000 + Math.random() * 1900);
    const res = search(q, c, ids);
    return json<SearchResponse>({ ...res, tookMs: Math.round(performance.now() - start) });
  }),

  api.get("/api/prowlarr/results/:id/magnet", ({ params }) => {
    const bad = prowlarrDown();
    if (bad) return bad;
    const e = entries.get(String(params.id));
    return e ? json({ magnet: magnetOf(e) }) : expired();
  }),

  api.get("/api/prowlarr/results/:id/torrent", ({ params }) => {
    const bad = prowlarrDown();
    if (bad) return bad;
    const e = entries.get(String(params.id));
    if (!e) return expired();
    if (!e.result.hasTorrent) return fail(404, "magnet_only", "This result only provides a magnet link");
    const b = torrentFile(e);
    const name = safeFilename(e.result.title) + ".torrent";
    return new Response(b, {
      headers: {
        "Content-Type": "application/x-bittorrent",
        "Content-Disposition": `attachment; filename="${name}"; filename*=UTF-8''${encodeURIComponent(name)}`,
        "Content-Length": String(b.length),
        "Cache-Control": "no-store",
      },
    });
  }),

  api.post("/api/prowlarr/results/:id/queue", async ({ params }) => {
    const bad = prowlarrDown();
    if (bad) return bad;
    const e = entries.get(String(params.id));
    if (!e) return expired();
    const via = prowlarrStatus().hasTransmissionClient ? "prowlarr" : transmissionStatus().available ? "transmission" : "";
    if (!via) return fail(409, "no_download_client", "Neither Prowlarr nor Wargo has a working Transmission connection");
    await delay(300 + Math.random() * 500);
    addTorrent(e.result.title, e.hash, e.result.size);
    return json({ via });
  }),

  // --- Transmission ---
  api.get("/api/transmission/torrents", () => transmissionDown() ?? json(payload())),

  api.post("/api/transmission/torrents", async ({ request }) => {
    const bad = transmissionDown();
    if (bad) return bad;
    let inp: { magnet?: string; torrentBase64?: string };
    try {
      inp = await body(request);
    } catch {
      return badRequest("invalid JSON body (a .torrent must be at most 10 MB)");
    }
    if (inp.magnet && !inp.torrentBase64) {
      if (!inp.magnet.startsWith("magnet:?") || inp.magnet.length > 16 << 10) return badRequest("magnet must start with magnet:?");
      const u = new URLSearchParams(inp.magnet.slice(8));
      const xt = /urn:btih:([0-9a-fA-F]{40})/.exec(u.get("xt") ?? "");
      const hash = xt ? xt[1].toLowerCase() : infoHash(inp.magnet);
      const name = u.get("dn") || hash;
      const size = Number(u.get("xl")) || Math.round((0.5 + Math.random() * 4) * GB);
      return json(addTorrent(name, hash, size));
    }
    if (inp.torrentBase64 && !inp.magnet) {
      let b: Uint8Array;
      try {
        b = Uint8Array.from(atob(inp.torrentBase64), (c) => c.charCodeAt(0));
      } catch {
        return badRequest("torrentBase64 is not valid base64");
      }
      const m = parseTorrent(b);
      if (!m) return badRequest("not a valid .torrent file: missing info name");
      return json(addTorrent(m.name, infoHash(m.name), m.size));
    }
    return badRequest("provide exactly one of magnet or torrentBase64");
  }),

  api.post("/api/transmission/torrents/:id/:action", ({ params }) => {
    const bad = transmissionDown();
    if (bad) return bad;
    const action = String(params.action);
    if (action !== "start" && action !== "stop") return fail(404, "not_found", "Not found");
    const t = findTorrent(String(params.id));
    if (t instanceof Response) return t;
    tr.update((s) => {
      const x = s.torrents.find((y) => y.id === t.id)!;
      if (action === "stop") x.status = "stopped";
      else {
        // A restart re-announces; the demo's tracker answers this time.
        if (x.error) {
          x.error = 0;
          x.errorString = "";
          if (!x.speed) x.speed = 7 * MB;
        }
        x.status = x.done >= x.size ? "seeding" : "downloading";
      }
    });
    down.delete(t.id);
    up.delete(t.id);
    advance();
    refresh("transmission");
    return json({ ok: true });
  }),

  api.del("/api/transmission/torrents/:id", ({ request, params }) => {
    const bad = transmissionDown();
    if (bad) return bad;
    const del = query(request).get("deleteData") ?? "";
    if (!["", "false", "0", "true", "1"].includes(del)) return badRequest("deleteData must be true or false");
    const t = findTorrent(String(params.id));
    if (t instanceof Response) return t;
    tr.update((s) => {
      s.torrents = s.torrents.filter((x) => x.id !== t.id);
      if (del === "true" || del === "1") s.used -= t.done;
    });
    rates.delete(t.id);
    refresh("transmission");
    return json({ ok: true });
  }),
];
