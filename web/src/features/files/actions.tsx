// SPDX-License-Identifier: AGPL-3.0-only
import type { ReactNode } from "react";
import { ContextMenu as CM, Dialog as D, DropdownMenu as DM } from "radix-ui";
import { ChevronRight } from "lucide-react";
import { cn } from "@/lib/cn";
import { Menu, MenuItem, MenuLabel, MenuSeparator } from "@/components/ui/menu";

export interface Action {
  key: string;
  label: string;
  icon?: ReactNode;
  shortcut?: string;
  danger?: boolean;
  disabled?: boolean;
  run: () => void;
  /** Rendered as a submenu (flattened in the phone action sheet). */
  children?: Action[];
}

/** Groups of actions, rendered with separators between them. */
export type ActionGroups = Action[][];

const itemCls =
  "flex h-7 cursor-default select-none items-center gap-2 rounded px-2 text-ui outline-none [&_svg]:size-3.5 data-[highlighted]:bg-surface-3 data-[state=open]:bg-surface-3 data-[disabled]:opacity-40";
const contentCls = "z-50 min-w-52 rounded-md border border-border-strong bg-surface p-1 shadow-xl focus:outline-none";

function ItemBody({ a, sub }: { a: Action; sub?: boolean }) {
  return (
    <>
      {a.icon && <span className={a.danger ? "text-crit" : "text-subtle"}>{a.icon}</span>}
      <span className="flex-1 truncate">{a.label}</span>
      {a.shortcut && <span className="text-2xs text-subtle">{a.shortcut}</span>}
      {sub && <ChevronRight className="text-subtle" />}
    </>
  );
}

/** Flattens submenus for lists without nesting ("Copy to · Pane 2"). */
export function flatten(groups: ActionGroups): ActionGroups {
  return groups.map((g) => g.flatMap((a) => (a.children ? a.children.map((c) => ({ ...c, label: `${a.label} · ${c.label}`, icon: c.icon ?? a.icon })) : [a])));
}

export function ContextMenuContent({ title, groups }: { title?: string; groups: ActionGroups }) {
  const gs = groups.filter((g) => g.length);
  return (
    <CM.Portal>
      <CM.Content className={contentCls}>
        {title && <CM.Label className="truncate px-2 py-1 text-2xs font-medium uppercase tracking-wider text-subtle">{title}</CM.Label>}
        {gs.map((g, i) => (
          <div key={i}>
            {i > 0 && <CM.Separator className="my-1 h-px bg-border" />}
            {g.map((a) =>
              a.children ? (
                <CM.Sub key={a.key}>
                  <CM.SubTrigger disabled={a.disabled} className={cn(itemCls, "text-fg")}>
                    <ItemBody a={a} sub />
                  </CM.SubTrigger>
                  <CM.Portal>
                    <CM.SubContent className={cn(contentCls, "min-w-44")} sideOffset={2}>
                      {a.children.map((c) => (
                        <CM.Item key={c.key} disabled={c.disabled} onSelect={c.run} className={cn(itemCls, "text-fg")}>
                          <ItemBody a={c} />
                        </CM.Item>
                      ))}
                    </CM.SubContent>
                  </CM.Portal>
                </CM.Sub>
              ) : (
                <CM.Item key={a.key} disabled={a.disabled} onSelect={a.run} className={cn(itemCls, a.danger ? "text-crit" : "text-fg")}>
                  <ItemBody a={a} />
                </CM.Item>
              ),
            )}
          </div>
        ))}
      </CM.Content>
    </CM.Portal>
  );
}

export function ActionMenu({ trigger, title, groups, align = "end" }: { trigger: ReactNode; title?: string; groups: ActionGroups; align?: "start" | "end" }) {
  const gs = groups.filter((g) => g.length);
  return (
    <Menu trigger={trigger} align={align}>
      {title && <MenuLabel>{title}</MenuLabel>}
      {gs.map((g, i) => (
        <div key={i}>
          {i > 0 && <MenuSeparator />}
          {g.map((a) =>
            a.children ? (
              <DM.Sub key={a.key}>
                <DM.SubTrigger disabled={a.disabled} className={cn(itemCls, "text-fg")}>
                  <ItemBody a={a} sub />
                </DM.SubTrigger>
                <DM.Portal>
                  <DM.SubContent className={cn(contentCls, "min-w-44")} sideOffset={2}>
                    {a.children.map((c) => (
                      <DM.Item key={c.key} disabled={c.disabled} onSelect={c.run} className={cn(itemCls, "text-fg")}>
                        <ItemBody a={c} />
                      </DM.Item>
                    ))}
                  </DM.SubContent>
                </DM.Portal>
              </DM.Sub>
            ) : (
              <MenuItem key={a.key} icon={a.icon} onSelect={a.run} danger={a.danger} disabled={a.disabled} shortcut={a.shortcut}>
                {a.label}
              </MenuItem>
            ),
          )}
        </div>
      ))}
    </Menu>
  );
}

/** Bottom sheet used for menus on phones. */
export function ActionSheet({ open, onOpenChange, title, subtitle, groups }: { open: boolean; onOpenChange: (o: boolean) => void; title: ReactNode; subtitle?: ReactNode; groups: ActionGroups }) {
  const gs = flatten(groups).filter((g) => g.length);
  return (
    <D.Root open={open} onOpenChange={onOpenChange}>
      <D.Portal>
        <D.Overlay className="fixed inset-0 z-50 bg-overlay" />
        <D.Content className="fixed inset-x-0 bottom-0 z-50 max-h-[85dvh] overflow-auto rounded-t-xl border-t border-border-strong bg-surface pb-[calc(env(safe-area-inset-bottom)+0.5rem)] focus:outline-none">
          <div className="mx-auto mt-2 h-1 w-10 rounded-full bg-border-strong" aria-hidden="true" />
          <div className="border-b border-border px-4 pb-3 pt-2">
            <D.Title className="truncate text-md font-semibold">{title}</D.Title>
            <D.Description className={cn("truncate text-xs text-muted", !subtitle && "sr-only")}>{subtitle ?? "Actions"}</D.Description>
          </div>
          {gs.map((g, i) => (
            <div key={i} className={cn("px-2 py-1", i > 0 && "border-t border-border")}>
              {g.map((a) => (
                <button
                  key={a.key}
                  type="button"
                  disabled={a.disabled}
                  onClick={() => {
                    onOpenChange(false);
                    // Let the sheet close before opening follow-up dialogs.
                    window.setTimeout(a.run, 10);
                  }}
                  className={cn(
                    "flex h-11 w-full items-center gap-3 rounded-md px-3 text-left text-md active:bg-surface-3 disabled:opacity-40 [&_svg]:size-[1.1rem]",
                    a.danger ? "text-crit" : "text-fg",
                  )}
                >
                  <span className={a.danger ? "text-crit" : "text-muted"}>{a.icon}</span>
                  {a.label}
                </button>
              ))}
            </div>
          ))}
        </D.Content>
      </D.Portal>
    </D.Root>
  );
}
