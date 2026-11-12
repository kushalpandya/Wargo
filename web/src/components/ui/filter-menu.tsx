// SPDX-License-Identifier: AGPL-3.0-only
import { useId, type ReactNode } from "react";
import { Dialog as D, DropdownMenu as M } from "radix-ui";
import { Check, ListFilter } from "lucide-react";
import { cn } from "@/lib/cn";
import { useMedia } from "@/lib/hooks";

/*
 * A page's filters behind one icon button, following menu conventions:
 * titled sections of radio rows (one choice) or checkbox rows (on/off),
 * separators between sections, and "Clear filters" as the last item when
 * it helps: the menu has two or more filters and at least one is set (with a
 * single filter, picking its default does the same).
 *
 * Desktop and tablet: a real dropdown menu (arrow keys, typeahead, Escape),
 * which stays open while you pick so several filters can be set at once.
 * Phones: a bottom sheet with the same sections and touch-sized rows.
 *
 * `active` counts filters that differ from their defaults; the button shows
 * it so a filtered list never looks like the full one. View settings such as
 * grouping can be a section too, but the page leaves them out of `active`.
 */

export interface FilterOption<T extends string = string> {
  value: T;
  label: ReactNode;
  /** Dimmed at the end of the row, such as a count. */
  hint?: ReactNode;
  title?: string;
}

export type FilterGroup = (
  | { kind: "radio"; title: string; value: string; onChange: (v: string) => void; options: FilterOption[] }
  | { kind: "check"; title: string; items: { label: ReactNode; checked: boolean; onChange: (v: boolean) => void }[] }
) & {
  /** A view setting (such as grouping), not a filter: never counted, not cleared. */
  view?: boolean;
};

/** Typed helper for a radio section (keeps the page's value type). */
export function radioGroup<T extends string>(title: string, value: T, onChange: (v: T) => void, options: FilterOption<T>[]): FilterGroup {
  return { kind: "radio", title, value, onChange: (v) => onChange(v as T), options };
}

const rowBase = "flex w-full cursor-default select-none items-center gap-2 rounded px-2 text-ui outline-none";
const hintClass = "num ml-4 text-2xs text-subtle";
const checkClass = "size-3.5 shrink-0 text-accent";

export function FilterMenu({ active, onClear, groups }: { active: number; onClear: () => void; groups: FilterGroup[] }) {
  const wide = useMedia("(min-width: 48rem)");
  const filters = groups.reduce((n, g) => n + (g.view ? 0 : g.kind === "radio" ? 1 : g.items.length), 0);
  const clear = filters >= 2 && active > 0 ? onClear : undefined;
  const label = active > 0 ? `Filters, ${active} active` : "Filters";
  const trigger = (
    <button
      type="button"
      aria-label={label}
      title={label}
      className={cn(
        "relative inline-flex size-8 shrink-0 items-center justify-center rounded-md border transition-colors [&_svg]:size-4",
        "data-[state=open]:border-border-strong data-[state=open]:bg-surface-3",
        active > 0
          ? "border-accent/50 bg-accent-soft text-accent hover:border-accent"
          : "border-border bg-surface text-muted hover:border-border-strong hover:bg-surface-3 hover:text-fg",
      )}
    >
      <ListFilter aria-hidden="true" />
      {active > 0 && (
        <span aria-hidden="true" className="num absolute -right-1.5 -top-1.5 min-w-4 rounded-full bg-accent px-1 text-center text-2xs font-semibold leading-4 text-accent-fg">
          {active}
        </span>
      )}
    </button>
  );
  return wide ? (
    <MenuForm trigger={trigger} onClear={clear} groups={groups} />
  ) : (
    <SheetForm trigger={trigger} onClear={clear} groups={groups} />
  );
}

function MenuForm({ trigger, onClear, groups }: { trigger: ReactNode; onClear?: () => void; groups: FilterGroup[] }) {
  // Picking a row keeps the menu open, so several filters can be set in one go.
  const keepOpen = (e: Event) => e.preventDefault();
  return (
    <M.Root>
      <M.Trigger asChild>{trigger}</M.Trigger>
      <M.Portal>
        <M.Content
          align="end"
          sideOffset={4}
          collisionPadding={8}
          aria-label="Filters"
          className="z-50 max-h-[var(--radix-dropdown-menu-content-available-height)] min-w-52 overflow-y-auto rounded-md border border-border-strong bg-surface p-1 shadow-xl focus:outline-none"
        >
          {groups.map((g, i) => (
            <M.Group key={g.title}>
              {i > 0 && <M.Separator className="my-1 h-px bg-border" />}
              <M.Label className="px-2 pb-1 pt-1.5 text-2xs font-medium uppercase tracking-wider text-subtle">{g.title}</M.Label>
              {g.kind === "radio" ? (
                <M.RadioGroup value={g.value} onValueChange={g.onChange}>
                  {g.options.map((o) => (
                    <M.RadioItem key={o.value} value={o.value} onSelect={keepOpen} title={o.title} className={cn(rowBase, "h-7 data-[highlighted]:bg-surface-3 data-[state=checked]:font-medium")}>
                      <span className="grid size-3.5 shrink-0 place-items-center">
                        <M.ItemIndicator>
                          <Check className={checkClass} strokeWidth={2.5} />
                        </M.ItemIndicator>
                      </span>
                      <span className="flex-1">{o.label}</span>
                      {o.hint !== undefined && <span className={hintClass}>{o.hint}</span>}
                    </M.RadioItem>
                  ))}
                </M.RadioGroup>
              ) : (
                g.items.map((it, j) => (
                  <M.CheckboxItem key={j} checked={it.checked} onCheckedChange={(v) => it.onChange(v === true)} onSelect={keepOpen} className={cn(rowBase, "h-7 data-[highlighted]:bg-surface-3")}>
                    <span className="grid size-3.5 shrink-0 place-items-center">
                      <M.ItemIndicator>
                        <Check className={checkClass} strokeWidth={2.5} />
                      </M.ItemIndicator>
                    </span>
                    <span className="flex-1">{it.label}</span>
                  </M.CheckboxItem>
                ))
              )}
            </M.Group>
          ))}
          {onClear && (
            <>
              <M.Separator className="my-1 h-px bg-border" />
              <M.Item onSelect={onClear} className={cn(rowBase, "h-7 text-accent data-[highlighted]:bg-surface-3")}>
                <span className="size-3.5 shrink-0" />
                Clear filters
              </M.Item>
            </>
          )}
        </M.Content>
      </M.Portal>
    </M.Root>
  );
}

function SheetForm({ trigger, onClear, groups }: { trigger: ReactNode; onClear?: () => void; groups: FilterGroup[] }) {
  const base = useId();
  const row = cn(rowBase, "min-h-11 py-2 text-sm active:bg-surface-3 has-[:focus-visible]:bg-surface-3 has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-accent/50");
  return (
    <D.Root>
      <D.Trigger asChild>{trigger}</D.Trigger>
      <D.Portal>
        <D.Overlay className="fixed inset-0 z-50 bg-overlay" />
        <D.Content className="fixed inset-x-0 bottom-0 z-50 max-h-[80vh] overflow-y-auto rounded-t-xl border-t border-border-strong bg-surface px-3 pt-3 pb-[max(1rem,env(safe-area-inset-bottom))] shadow-2xl focus:outline-none">
          <div aria-hidden="true" className="mx-auto mb-2 h-1 w-10 rounded-full bg-border-strong" />
          <div className="flex items-center justify-between px-2 pb-1">
            <D.Title className="text-md font-semibold">Filters</D.Title>
            {onClear && (
              <button type="button" onClick={onClear} className="text-sm font-medium text-accent">
                Clear filters
              </button>
            )}
          </div>
          <D.Description className="sr-only">Narrow down the list. Changes apply right away.</D.Description>
          {groups.map((g, i) => (
            <div key={g.title} role="group" aria-labelledby={`${base}-${i}`} className={cn(i > 0 && "mt-1 border-t border-border pt-1")}>
              <div id={`${base}-${i}`} className="px-2 pb-1 pt-2 text-2xs font-medium uppercase tracking-wider text-subtle">
                {g.title}
              </div>
              {g.kind === "radio" ? (
                <div role="radiogroup" aria-labelledby={`${base}-${i}`}>
                  {g.options.map((o) => {
                    const on = o.value === g.value;
                    return (
                      <label key={o.value} className={row} title={o.title}>
                        <input type="radio" name={`${base}-${i}`} checked={on} onChange={() => g.onChange(o.value)} className="sr-only" />
                        <span className="grid size-4 shrink-0 place-items-center">{on && <Check className={checkClass} strokeWidth={2.5} />}</span>
                        <span className={cn("flex-1", on && "font-medium")}>{o.label}</span>
                        {o.hint !== undefined && <span className={hintClass}>{o.hint}</span>}
                      </label>
                    );
                  })}
                </div>
              ) : (
                g.items.map((it, j) => (
                  <label key={j} className={row}>
                    <input type="checkbox" checked={it.checked} onChange={(e) => it.onChange(e.target.checked)} className="sr-only" />
                    <span className="grid size-4 shrink-0 place-items-center">{it.checked && <Check className={checkClass} strokeWidth={2.5} />}</span>
                    <span className="flex-1">{it.label}</span>
                  </label>
                ))
              )}
            </div>
          ))}
        </D.Content>
      </D.Portal>
    </D.Root>
  );
}
