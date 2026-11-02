// SPDX-License-Identifier: AGPL-3.0-only
import type { ReactNode } from "react";
import { ArrowDown, ArrowLeft, ArrowRight, ArrowUp } from "lucide-react";
import { cn } from "@/lib/cn";
import type { TermSession } from "./session";

type KeyDef =
  | { label: ReactNode; aria: string; key: Parameters<TermSession["key"]>[0] }
  | { label: ReactNode; aria: string; text: string }
  | { label: ReactNode; aria: string; mod: "ctrl" | "alt" };

const KEYS: KeyDef[] = [
  { label: "Esc", aria: "Esc", key: "esc" },
  { label: "Tab", aria: "Tab", key: "tab" },
  { label: "Ctrl", aria: "Ctrl (sticky)", mod: "ctrl" },
  { label: "Alt", aria: "Alt (sticky)", mod: "alt" },
  { label: <ArrowLeft />, aria: "Left arrow", key: "left" },
  { label: <ArrowUp />, aria: "Up arrow", key: "up" },
  { label: <ArrowDown />, aria: "Down arrow", key: "down" },
  { label: <ArrowRight />, aria: "Right arrow", key: "right" },
  { label: "|", aria: "Pipe", text: "|" },
  { label: "~", aria: "Tilde", text: "~" },
  { label: "/", aria: "Slash", text: "/" },
  { label: "-", aria: "Hyphen", text: "-" },
];

/**
 * On-screen keys phone keyboards lack. Buttons never take focus, so the
 * terminal (and the soft keyboard) stay active while tapping them.
 */
export function ExtraKeys({ session }: { session: TermSession | undefined }) {
  const mods = session?.mods ?? { ctrl: false, alt: false };
  return (
    <div
      role="toolbar"
      aria-label="Extra keys"
      className="grid shrink-0 grid-cols-6 gap-1 min-[520px]:grid-cols-12 border-t border-border bg-surface-2 px-1.5 py-1.5 pb-[max(0.375rem,env(safe-area-inset-bottom))]"
    >
      {KEYS.map((k) => {
        const active = "mod" in k && mods[k.mod];
        return (
          <button
            key={k.aria}
            type="button"
            tabIndex={-1}
            aria-label={k.aria}
            aria-pressed={"mod" in k ? active : undefined}
            disabled={!session}
            onPointerDown={(e) => e.preventDefault()}
            onClick={() => {
              if (!session) return;
              if ("mod" in k) session.toggleMod(k.mod);
              else if ("key" in k) session.key(k.key);
              else session.input(k.text);
              session.focus();
            }}
            className={cn(
              "num flex h-9 min-w-0 items-center justify-center rounded-md border text-[0.8125rem] font-medium [&_svg]:size-4",
              "touch-manipulation select-none disabled:opacity-40",
              active ? "border-accent bg-accent-soft text-accent" : "border-border bg-surface text-fg active:bg-surface-3",
            )}
          >
            {k.label}
          </button>
        );
      })}
    </div>
  );
}
