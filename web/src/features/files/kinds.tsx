// SPDX-License-Identifier: AGPL-3.0-only
import {
  File,
  FileArchive,
  FileAudio,
  FileCode2,
  FileImage,
  FileQuestion,
  FileText,
  FileVideo,
  Folder,
  FolderSymlink,
  Link2Off,
  ShieldAlert,
  type LucideProps,
} from "lucide-react";
import { cn } from "@/lib/cn";
import type { Entry } from "./api";

export type Kind = "dir" | "image" | "svg" | "video" | "audio" | "pdf" | "text" | "code" | "archive" | "other";

const codeExt = new Set(
  "js jsx ts tsx mjs cjs go rs py rb php java kt c h cc cpp hpp cs swift sh bash zsh fish ps1 lua pl r sql html htm css scss less vue svelte json jsonc yaml yml toml ini conf cfg xml gradle dockerfile makefile mk nix tf hcl proto graphql gql zig dart ex exs erl hs ml clj scala groovy cmake service socket timer env properties".split(
    " ",
  ),
);
const textExt = new Set("txt md markdown rst log csv tsv nfo srt vtt sub ass diff patch rtf tex lock gitignore gitattributes editorconfig npmrc".split(" "));
const textNames = new Set(["dockerfile", "makefile", "license", "readme", "changelog", "authors", "copying", "caddyfile", "vagrantfile", "procfile", "jenkinsfile", ".bashrc", ".zshrc", ".profile", ".bash_profile", ".env", "hosts", "fstab", "crontab"]);
const imageExt = new Set("png jpg jpeg gif webp avif bmp ico".split(" "));
const videoExt = new Set("mp4 m4v webm mov ogv mkv".split(" "));
const audioExt = new Set("mp3 flac ogg opus wav m4a aac oga weba".split(" "));
const archiveExt = new Set("zip tar tgz gz bz2 xz zst 7z rar".split(" "));

export function ext(name: string): string {
  const i = name.lastIndexOf(".");
  return i <= 0 ? "" : name.slice(i + 1).toLowerCase();
}

export function kindOf(e: Pick<Entry, "name" | "type" | "targetType" | "mime">): Kind {
  if (e.type === "dir" || e.targetType === "dir") return "dir";
  const x = ext(e.name);
  const low = e.name.toLowerCase();
  if (x === "svg") return "svg";
  if (imageExt.has(x)) return "image";
  if (videoExt.has(x)) return "video";
  if (audioExt.has(x)) return "audio";
  if (x === "pdf") return "pdf";
  if (archiveExt.has(x)) return "archive";
  if (codeExt.has(x) || textNames.has(low) && low !== "license" && low !== "readme") return "code";
  if (textExt.has(x) || textNames.has(low)) return "text";
  if (e.mime?.startsWith("text/")) return "text";
  return "other";
}

/** Archives the backend can extract. */
export function isExtractable(name: string): boolean {
  const low = name.toLowerCase();
  return low.endsWith(".zip") || low.endsWith(".tar") || low.endsWith(".tar.gz") || low.endsWith(".tgz");
}

export function isPreviewable(e: Entry): boolean {
  if (e.type === "dir" || e.targetType === "outside" || e.targetType === "broken") return false;
  return kindOf(e) !== "archive";
}

export function isEditable(k: Kind) {
  return k === "text" || k === "code" || k === "svg";
}

const tone: Record<Kind, string> = {
  dir: "text-frost",
  image: "text-ok",
  svg: "text-ok",
  video: "text-accent",
  audio: "text-accent",
  pdf: "text-crit",
  text: "text-muted",
  code: "text-warn",
  archive: "text-warn",
  other: "text-subtle",
};

export function FileIcon({ entry, className, ...rest }: { entry: Entry; className?: string } & LucideProps) {
  const k = kindOf(entry);
  const cls = cn("shrink-0", tone[k], className);
  if (entry.type === "symlink" && entry.targetType === "outside") return <ShieldAlert className={cn(cls, "text-warn")} aria-label="Link outside root" {...rest} />;
  if (entry.type === "symlink" && entry.targetType === "broken") return <Link2Off className={cn(cls, "text-crit")} aria-label="Broken link" {...rest} />;
  if (entry.type === "symlink" && k === "dir") return <FolderSymlink className={cls} {...rest} />;
  switch (k) {
    case "dir":
      return <Folder className={cls} fill="currentColor" fillOpacity={0.16} {...rest} />;
    case "image":
    case "svg":
      return <FileImage className={cls} {...rest} />;
    case "video":
      return <FileVideo className={cls} {...rest} />;
    case "audio":
      return <FileAudio className={cls} {...rest} />;
    case "pdf":
      return <FileText className={cls} {...rest} />;
    case "archive":
      return <FileArchive className={cls} {...rest} />;
    case "code":
      return <FileCode2 className={cls} {...rest} />;
    case "text":
      return <FileText className={cls} {...rest} />;
  }
  return entry.type === "other" ? <FileQuestion className={cls} {...rest} /> : <File className={cls} {...rest} />;
}

export function typeLabel(e: Entry): string {
  if (e.type === "symlink") {
    if (e.targetType === "outside") return "Link (outside root)";
    if (e.targetType === "broken") return "Broken link";
    return e.targetType === "dir" ? "Link to folder" : "Link";
  }
  if (e.type === "dir") return "Folder";
  if (e.type === "other") return "Special file";
  const k = kindOf(e);
  const x = ext(e.name).toUpperCase();
  const names: Partial<Record<Kind, string>> = { image: "Image", svg: "SVG image", video: "Video", audio: "Audio", pdf: "PDF", archive: "Archive", code: "Code", text: "Text" };
  return names[k] ? `${names[k]}${x && k !== "pdf" && k !== "svg" ? ` · ${x}` : ""}` : x ? `${x} file` : "File";
}
