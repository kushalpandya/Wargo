// SPDX-License-Identifier: AGPL-3.0-only
import { DropdownMenu as M, Tooltip as T } from "radix-ui";
import type { ReactNode } from "react";
import { cn } from "@/lib/cn";

export function Menu({ trigger, children, align = "end" }: { trigger: ReactNode; children: ReactNode; align?: "start" | "end" | "center" }) {
  return (
    <M.Root modal={false}>
      <M.Trigger asChild>{trigger}</M.Trigger>
      <M.Portal>
        <M.Content
          align={align}
          sideOffset={4}
          className="z-50 min-w-44 rounded-md border border-border-strong bg-surface p-1 shadow-xl focus:outline-none"
        >
          {children}
        </M.Content>
      </M.Portal>
    </M.Root>
  );
}

export function MenuItem({
  children,
  icon,
  onSelect,
  danger,
  disabled,
  shortcut,
}: {
  children: ReactNode;
  icon?: ReactNode;
  onSelect?: () => void;
  danger?: boolean;
  disabled?: boolean;
  shortcut?: string;
}) {
  return (
    <M.Item
      disabled={disabled}
      onSelect={onSelect}
      className={cn(
        "flex h-7 cursor-default select-none items-center gap-2 rounded px-2 text-ui outline-none [&_svg]:size-3.5",
        "data-[highlighted]:bg-surface-3 data-[disabled]:opacity-40",
        danger ? "text-crit" : "text-fg",
      )}
    >
      {icon && <span className={cn(danger ? "text-crit" : "text-subtle")}>{icon}</span>}
      <span className="flex-1">{children}</span>
      {shortcut && <span className="text-2xs text-subtle">{shortcut}</span>}
    </M.Item>
  );
}

export function MenuSeparator() {
  return <M.Separator className="my-1 h-px bg-border" />;
}

export function MenuLabel({ children }: { children: ReactNode }) {
  return <M.Label className="px-2 py-1 text-2xs font-medium uppercase tracking-wider text-subtle">{children}</M.Label>;
}

export function TooltipProvider({ children }: { children: ReactNode }) {
  return <T.Provider delayDuration={300}>{children}</T.Provider>;
}

export function Tooltip({ content, children, side = "top" }: { content: ReactNode; children: ReactNode; side?: "top" | "bottom" | "left" | "right" }) {
  if (!content) return <>{children}</>;
  return (
    <T.Root>
      <T.Trigger asChild>{children}</T.Trigger>
      <T.Portal>
        <T.Content
          side={side}
          sideOffset={5}
          className="z-[60] max-w-xs rounded border border-border-strong bg-surface-3 px-2 py-1 text-xs text-fg shadow-lg"
        >
          {content}
        </T.Content>
      </T.Portal>
    </T.Root>
  );
}
