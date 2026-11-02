// SPDX-License-Identifier: AGPL-3.0-only
import { useMemo, useState, type HTMLAttributes, type ReactNode, type ThHTMLAttributes, type TdHTMLAttributes } from "react";
import { ArrowDown, ArrowUp } from "lucide-react";
import { cn } from "@/lib/cn";

export function Table({ className, children, ...rest }: HTMLAttributes<HTMLTableElement>) {
  return (
    <table className={cn("w-full border-collapse text-[0.8125rem]", className)} {...rest}>
      {children}
    </table>
  );
}

export function THead({ children, sticky = true }: { children: ReactNode; sticky?: boolean }) {
  return <thead className={cn("bg-surface-2 text-left", sticky && "sticky top-0 z-10")}>{children}</thead>;
}

export function Th({ className, children, ...rest }: ThHTMLAttributes<HTMLTableCellElement>) {
  return (
    <th
      className={cn(
        "h-8 whitespace-nowrap border-b border-border px-3 text-2xs font-semibold uppercase tracking-wider text-subtle",
        className,
      )}
      {...rest}
    >
      {children}
    </th>
  );
}

export function Td({ className, children, ...rest }: TdHTMLAttributes<HTMLTableCellElement>) {
  return (
    <td className={cn("h-9 border-b border-border px-3 align-middle", className)} {...rest}>
      {children}
    </td>
  );
}

export function Tr({ className, children, onClick, selected, ...rest }: HTMLAttributes<HTMLTableRowElement> & { selected?: boolean }) {
  return (
    <tr
      onClick={onClick}
      aria-selected={selected}
      className={cn("transition-colors hover:bg-surface-2", onClick && "cursor-pointer", selected && "bg-accent-soft hover:bg-accent-soft", className)}
      {...rest}
    >
      {children}
    </tr>
  );
}

export type SortDir = "asc" | "desc";
export interface SortState<K extends string> {
  key: K;
  dir: SortDir;
}

/** Generic client-side sort helper. */
export function useSort<T, K extends string>(rows: T[] | undefined, initial: SortState<K>, accessors: Record<K, (r: T) => string | number | undefined>) {
  const [sort, setSort] = useState<SortState<K>>(initial);
  const sorted = useMemo(() => {
    if (!rows) return [];
    const get = accessors[sort.key];
    const m = sort.dir === "asc" ? 1 : -1;
    return [...rows].sort((a, b) => {
      const x = get(a);
      const y = get(b);
      if (x === y) return 0;
      if (x === undefined) return 1;
      if (y === undefined) return -1;
      if (typeof x === "number" && typeof y === "number") return (x - y) * m;
      return String(x).localeCompare(String(y), undefined, { numeric: true, sensitivity: "base" }) * m;
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rows, sort]);
  const toggle = (key: K, defaultDir: SortDir = "desc") =>
    setSort((s) => (s.key === key ? { key, dir: s.dir === "asc" ? "desc" : "asc" } : { key, dir: defaultDir }));
  return { sorted, sort, toggle, setSort };
}

export function SortTh<K extends string>({
  k,
  sort,
  onSort,
  children,
  className,
  defaultDir,
  align,
}: {
  k: K;
  sort: SortState<K>;
  onSort: (k: K, d?: SortDir) => void;
  children: ReactNode;
  className?: string;
  defaultDir?: SortDir;
  align?: "right";
}) {
  const active = sort.key === k;
  return (
    <Th className={cn(align === "right" && "text-right", className)} aria-sort={active ? (sort.dir === "asc" ? "ascending" : "descending") : "none"}>
      <button
        type="button"
        onClick={() => onSort(k, defaultDir)}
        className={cn("inline-flex items-center gap-1 uppercase tracking-wider hover:text-fg", active && "text-fg", align === "right" && "flex-row-reverse")}
      >
        {children}
        {active ? sort.dir === "asc" ? <ArrowUp className="size-3" /> : <ArrowDown className="size-3" /> : <span className="size-3" />}
      </button>
    </Th>
  );
}
