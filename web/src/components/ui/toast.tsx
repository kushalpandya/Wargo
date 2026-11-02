// SPDX-License-Identifier: AGPL-3.0-only
import { useSyncExternalStore } from "react";
import { CheckCircle2, AlertTriangle, Info, X } from "lucide-react";
import { cn } from "@/lib/cn";

type Kind = "ok" | "error" | "info";
interface ToastItem {
  id: number;
  kind: Kind;
  title: string;
  body?: string;
}

let items: ToastItem[] = [];
let seq = 0;
const listeners = new Set<() => void>();
const emit = () => listeners.forEach((l) => l());

function push(kind: Kind, title: string, body?: string) {
  const id = ++seq;
  items = [...items.slice(-3), { id, kind, title, body }];
  emit();
  window.setTimeout(() => dismiss(id), kind === "error" ? 8000 : 4000);
}

function dismiss(id: number) {
  items = items.filter((t) => t.id !== id);
  emit();
}

export const toast = {
  ok: (title: string, body?: string) => push("ok", title, body),
  info: (title: string, body?: string) => push("info", title, body),
  error: (title: string, err?: unknown) => {
    // The user dismissed an admin approval prompt; that's not an error.
    if ((err as { code?: string } | undefined)?.code === "elevation_canceled") return;
    push("error", title, err instanceof Error ? err.message : err ? String(err) : undefined);
  },
};

export function Toaster() {
  const list = useSyncExternalStore(
    (l) => (listeners.add(l), () => listeners.delete(l)),
    () => items,
  );
  return (
    <div
      aria-live="polite"
      className="pointer-events-none fixed bottom-3 right-3 z-[70] flex w-[min(24rem,calc(100vw-1.5rem))] flex-col gap-2 max-md:bottom-[calc(env(safe-area-inset-bottom)+0.75rem)]"
    >
      {list.map((t) => (
        <div
          key={t.id}
          role={t.kind === "error" ? "alert" : "status"}
          className="pointer-events-auto flex items-start gap-2.5 rounded-lg border border-border-strong bg-surface px-3 py-2.5 shadow-xl"
        >
          {t.kind === "ok" ? (
            <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-ok" />
          ) : t.kind === "error" ? (
            <AlertTriangle className="mt-0.5 size-4 shrink-0 text-crit" />
          ) : (
            <Info className="mt-0.5 size-4 shrink-0 text-frost" />
          )}
          <div className="min-w-0 flex-1">
            <p className="text-sm font-medium">{t.title}</p>
            {t.body && <p className={cn("mt-0.5 break-words text-xs text-muted")}>{t.body}</p>}
          </div>
          <button aria-label="Dismiss" onClick={() => dismiss(t.id)} className="rounded p-0.5 text-subtle hover:text-fg">
            <X className="size-3.5" />
          </button>
        </div>
      ))}
    </div>
  );
}
