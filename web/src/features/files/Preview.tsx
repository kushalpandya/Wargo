// SPDX-License-Identifier: AGPL-3.0-only
import { lazy, Suspense, useCallback, useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Dialog as D } from "radix-ui";
import { ChevronLeft, ChevronRight, Download, FileWarning, Pencil, Save, WrapText, X } from "lucide-react";
import { ApiError } from "@/lib/api";
import { bytes, datetime } from "@/lib/format";
import { cn } from "@/lib/cn";
import { useIsDark } from "@/lib/theme";
import { Button, IconButton } from "@/components/ui/button";
import { ConfirmDialog, Dialog } from "@/components/ui/dialog";
import { EmptyState, ErrorNote, Spinner } from "@/components/ui/misc";
import { toast } from "@/components/ui/toast";
import { downloadUrl, fileApi, joinPath, startDownload, type Entry, type RootId } from "./api";
import { FileIcon, isEditable, kindOf, typeLabel } from "./kinds";
import type { EditorHandle } from "./Editor";

const CodeEditor = lazy(() => import("./Editor"));

function Centered({ children }: { children: React.ReactNode }) {
  return <div className="flex h-full items-center justify-center p-4 text-muted">{children}</div>;
}

function SvgImage({ root, path }: { root: RootId; path: string }) {
  // Fetched as text and shown through a blob: URL in <img>, where scripts never run.
  const [url, setUrl] = useState<string | null>(null);
  const [err, setErr] = useState<unknown>(null);
  useEffect(() => {
    let u: string | null = null;
    let alive = true;
    fetch(downloadUrl(root, path, true), { credentials: "same-origin" })
      .then((r) => (r.ok ? r.text() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((t) => {
        if (!alive) return;
        u = URL.createObjectURL(new Blob([t], { type: "image/svg+xml" }));
        setUrl(u);
      })
      .catch(setErr);
    return () => {
      alive = false;
      if (u) URL.revokeObjectURL(u);
    };
  }, [root, path]);
  if (err) return <ErrorNote error={err} className="m-4" />;
  if (!url) return <Centered><Spinner /></Centered>;
  return <img src={url} alt={path} className="max-h-full max-w-full object-contain" />;
}

function ImageView({ src, alt }: { src: string; alt: string }) {
  const [fit, setFit] = useState(true);
  const [failed, setFailed] = useState(false);
  if (failed) return <EmptyState icon={<FileWarning />} title="This image can't be displayed">Your browser may not support the format. Download it instead.</EmptyState>;
  return (
    <div className={cn("flex h-full w-full", fit ? "items-center justify-center" : "overflow-auto")}>
      <img
        src={src}
        alt={alt}
        onError={() => setFailed(true)}
        onClick={() => setFit(!fit)}
        className={cn(fit ? "max-h-full max-w-full cursor-zoom-in object-contain" : "m-auto max-w-none cursor-zoom-out")}
      />
    </div>
  );
}

interface TextState {
  dirty: boolean;
  saving: boolean;
}

function TextView({
  root,
  path,
  name,
  editorRef,
  onState,
  wrap,
}: {
  root: RootId;
  path: string;
  name: string;
  editorRef: React.RefObject<EditorHandle | null>;
  onState: (s: Partial<TextState> & { save?: () => void; writable?: boolean }) => void;
  wrap: boolean;
}) {
  const dark = useIsDark();
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["files", "content", root, path], queryFn: () => fileApi.content(root, path), staleTime: Infinity, gcTime: 0, retry: false });
  const mtime = useRef<string | null>(null);
  const [conflict, setConflict] = useState<{ mtime: string } | null>(null);
  useEffect(() => {
    if (q.data) mtime.current = q.data.mtime;
  }, [q.data]);

  const doSave = useCallback(
    async (force?: string) => {
      const ed = editorRef.current;
      if (!ed || !q.data) return;
      onState({ saving: true });
      try {
        const r = await fileApi.save(root, path, ed.getValue(), force ?? mtime.current, q.data.encoding);
        mtime.current = r.mtime;
        ed.markClean();
        toast.ok("Saved", `${name} · ${bytes(r.size)}`);
        qc.invalidateQueries({ queryKey: ["files", "list"] });
      } catch (e) {
        if (e instanceof ApiError && e.code === "conflict") setConflict(e.data as { mtime: string });
        else if (!(e instanceof ApiError && e.code === "elevation_canceled")) toast.error("Save failed", e);
      } finally {
        onState({ saving: false });
      }
    },
    [editorRef, q.data, root, path, name, onState, qc],
  );

  useEffect(() => {
    onState({ save: () => void doSave(), writable: q.data?.writable });
  }, [doSave, onState, q.data?.writable]);

  if (q.isLoading) return <Centered><Spinner /></Centered>;
  if (q.error) {
    const e = q.error as ApiError;
    const title = e.code === "too_large" ? "This file is too large to open in the editor" : e.code === "binary" ? "This looks like a binary file" : "Could not open the file";
    return (
      <EmptyState icon={<FileWarning />} title={title} action={<Button icon={<Download />} onClick={() => startDownload(downloadUrl(root, path))}>Download</Button>}>
        {e.code === "too_large" ? `Files up to ${bytes((e.data as { max: number })?.max)} can be edited.` : e.message}
      </EmptyState>
    );
  }
  if (!q.data) return null;
  return (
    <>
      <Suspense fallback={<Centered><Spinner /></Centered>}>
        <CodeEditor ref={editorRef} initial={q.data.content} filename={name} dark={dark} wrap={wrap} onDirty={(dirty) => onState({ dirty })} onSave={() => void doSave()} />
      </Suspense>
      <Dialog
        open={!!conflict}
        onOpenChange={(o) => !o && setConflict(null)}
        title="File changed on disk"
        description={`${name} was modified ${conflict ? datetime(conflict.mtime) : ""} after you opened it.`}
        size="sm"
        footer={
          <>
            <Button autoFocus onClick={() => setConflict(null)}>
              Cancel
            </Button>
            <Button
              onClick={async () => {
                setConflict(null);
                const fresh = await fileApi.content(root, path).catch((e) => (toast.error("Reload failed", e), null));
                if (fresh) {
                  mtime.current = fresh.mtime;
                  editorRef.current?.setValue(fresh.content);
                }
              }}
            >
              Discard mine &amp; reload
            </Button>
            <Button
              variant="danger"
              onClick={() => {
                const m = conflict!.mtime;
                setConflict(null);
                void doSave(m);
              }}
            >
              Overwrite
            </Button>
          </>
        }
      />
    </>
  );
}

export function Preview({
  root,
  dir,
  entries,
  name,
  onClose,
  onNavigate,
}: {
  root: RootId;
  dir: string;
  entries: Entry[];
  name: string | null;
  onClose: () => void;
  onNavigate: (name: string) => void;
}) {
  const entry = entries.find((e) => e.name === name);
  const idx = entry ? entries.indexOf(entry) : -1;
  const [text, setText] = useState<TextState & { save?: () => void; writable?: boolean }>({ dirty: false, saving: false });
  const [editSvg, setEditSvg] = useState(false);
  const [wrap, setWrap] = useState(() => localStorage.getItem("wargo.files.wrap") === "1");
  const [guard, setGuard] = useState<null | (() => void)>(null);
  const editorRef = useRef<EditorHandle | null>(null);
  const onState = useCallback((s: Partial<typeof text>) => setText((t) => ({ ...t, ...s })), []);
  useEffect(() => {
    setText({ dirty: false, saving: false });
    setEditSvg(false);
  }, [name]);
  useEffect(() => localStorage.setItem("wargo.files.wrap", wrap ? "1" : "0"), [wrap]);

  const guarded = (fn: () => void) => (text.dirty ? setGuard(() => fn) : fn());
  const go = (d: number) => {
    const n = entries[idx + d];
    if (n) guarded(() => onNavigate(n.name));
  };
  const kind = entry ? kindOf(entry) : "other";
  const showText = kind === "text" || kind === "code" || (kind === "svg" && editSvg) || kind === "other";
  const path = entry ? joinPath(dir, entry.name) : "";
  const src = entry ? downloadUrl(root, path, true) : "";

  useEffect(() => {
    if (!text.dirty) return;
    const h = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", h);
    return () => window.removeEventListener("beforeunload", h);
  }, [text.dirty]);

  return (
    <D.Root open={!!entry} onOpenChange={(o) => !o && guarded(onClose)}>
      <D.Portal>
        <D.Overlay className="fixed inset-0 z-50 bg-overlay" />
        <D.Content
          className="fixed inset-0 z-50 flex flex-col bg-surface focus:outline-none sm:inset-3 sm:rounded-lg sm:border sm:border-border-strong lg:inset-6"
          onKeyDown={(e) => {
            if (showText) return;
            if (e.key === "ArrowRight") go(1);
            if (e.key === "ArrowLeft") go(-1);
          }}
          aria-describedby={undefined}
        >
          {entry && (
            <>
              <header className="flex min-h-12 items-center gap-2 border-b border-border px-2 py-1.5 sm:px-3">
                <FileIcon entry={entry} className="ml-1 size-4" />
                <div className="min-w-0 flex-1">
                  <D.Title className="flex items-center gap-2 truncate text-[0.875rem] font-semibold">
                    <span className="truncate">{entry.name}</span>
                    {text.dirty && (
                      <span className="size-2 shrink-0 rounded-full bg-accent" role="img" aria-label="Unsaved changes" title="Unsaved changes" />
                    )}
                  </D.Title>
                  <p className="num truncate text-2xs text-muted">
                    {typeLabel(entry)} · {bytes(entry.size)} · {datetime(entry.mtime)}
                    {showText && text.writable === false && " · read-only"}
                  </p>
                </div>
                <div className="flex shrink-0 items-center gap-1">
                  {kind === "svg" && !editSvg && (
                    <Button size="sm" variant="ghost" icon={<Pencil />} onClick={() => setEditSvg(true)}>
                      <span className="max-sm:sr-only">Edit source</span>
                    </Button>
                  )}
                  {showText && (
                    <>
                      <IconButton label={wrap ? "Disable line wrap" : "Wrap long lines"} size="sm" onClick={() => setWrap(!wrap)} className={cn(wrap && "text-accent")} aria-pressed={wrap}>
                        <WrapText />
                      </IconButton>
                      <Button size="sm" variant={text.dirty ? "primary" : "secondary"} icon={<Save />} loading={text.saving} disabled={!text.dirty} onClick={() => text.save?.()} title="Save (Ctrl+S)">
                        Save
                      </Button>
                    </>
                  )}
                  <IconButton label="Download" size="sm" onClick={() => startDownload(downloadUrl(root, path))}>
                    <Download />
                  </IconButton>
                  <div className="mx-0.5 h-5 w-px bg-border max-sm:hidden" />
                  <IconButton label="Previous file" size="sm" disabled={idx <= 0} onClick={() => go(-1)} className="max-sm:hidden">
                    <ChevronLeft />
                  </IconButton>
                  <IconButton label="Next file" size="sm" disabled={idx >= entries.length - 1} onClick={() => go(1)} className="max-sm:hidden">
                    <ChevronRight />
                  </IconButton>
                  <IconButton label="Close preview" size="sm" onClick={() => guarded(onClose)}>
                    <X />
                  </IconButton>
                </div>
              </header>
              <div className={cn("relative min-h-0 flex-1", !showText && "bg-surface-2 p-2 sm:p-4 sm:rounded-b-lg")}>
                {kind === "image" && <ImageView key={src} src={src} alt={entry.name} />}
                {kind === "svg" && !editSvg && (
                  <Centered>
                    <SvgImage root={root} path={path} />
                  </Centered>
                )}
                {kind === "video" && (
                  <Centered>
                    <video key={src} src={src} controls autoPlay className="max-h-full max-w-full rounded bg-black" />
                  </Centered>
                )}
                {kind === "audio" && (
                  <Centered>
                    <div className="flex w-full max-w-md flex-col items-center gap-4 rounded-lg border border-border bg-surface p-6">
                      <FileIcon entry={entry} className="size-10" />
                      <div className="w-full truncate text-center font-medium">{entry.name}</div>
                      <audio key={src} src={src} controls autoPlay className="w-full" />
                    </div>
                  </Centered>
                )}
                {/* No sandbox attribute: Chrome's PDF viewer refuses to run in a sandboxed frame. The
                    server serves inline files with nosniff and a script-less CSP, and only as
                    application/pdf for .pdf names, so the frame can't run page script. */}
                {kind === "pdf" && <iframe key={src} src={src} title={entry.name} className="h-full w-full rounded border border-border bg-white" />}
                {showText && <TextView key={path} root={root} path={path} name={entry.name} editorRef={editorRef} onState={onState} wrap={wrap} />}
                {kind === "archive" && (
                  <EmptyState icon={<FileWarning />} title="No preview for archives">
                    Use Extract from the context menu.
                  </EmptyState>
                )}
              </div>
              {entries.length > 1 && !showText && (
                <div className="flex items-center justify-between border-t border-border px-2 py-1.5 sm:hidden">
                  <Button size="sm" variant="ghost" icon={<ChevronLeft />} disabled={idx <= 0} onClick={() => go(-1)}>
                    Previous
                  </Button>
                  <span className="num text-xs text-muted">
                    {idx + 1} / {entries.length}
                  </span>
                  <Button size="sm" variant="ghost" disabled={idx >= entries.length - 1} onClick={() => go(1)}>
                    Next <ChevronRight />
                  </Button>
                </div>
              )}
            </>
          )}
          <ConfirmDialog
            open={!!guard}
            onOpenChange={(o) => !o && setGuard(null)}
            title="Discard unsaved changes?"
            description="Your edits to this file will be lost."
            confirmLabel="Discard"
            danger
            onConfirm={() => {
              const fn = guard;
              setGuard(null);
              setText((t) => ({ ...t, dirty: false }));
              fn?.();
            }}
          />
        </D.Content>
      </D.Portal>
    </D.Root>
  );
}

export function canPreview(e: Entry) {
  const k = kindOf(e);
  return e.type !== "dir" && e.targetType !== "dir" && e.targetType !== "outside" && e.targetType !== "broken" && k !== "archive" && (e.type === "file" || e.type === "symlink");
}

export { isEditable };
