// SPDX-License-Identifier: AGPL-3.0-only
import { ArrowRight } from "lucide-react";
import { cn } from "@/lib/cn";

const SEP = /[.\-:+_~]/;

/** Splits `next` into [unchanged prefix, changed rest] relative to `cur`, on segment boundaries. */
export function splitVersion(cur: string, next: string): [string, string] {
  if (!cur) return ["", next];
  let i = 0;
  while (i < cur.length && i < next.length && cur[i] === next[i]) i++;
  if (i === next.length && i === cur.length) return [next, ""];
  let j = i;
  while (j > 0 && !SEP.test(next[j - 1])) j--;
  return [next.slice(0, j), next.slice(j)];
}

/** "current → new" with the changed part of the new version highlighted. */
export function VersionDiff({ current, next, className }: { current: string; next: string; className?: string }) {
  const [same, changed] = splitVersion(current, next);
  const [, oldChanged] = splitVersion(next, current);
  const oldSame = current.slice(0, current.length - oldChanged.length);
  return (
    <span className={cn("num inline-flex min-w-0 flex-wrap items-center gap-x-1.5 text-[0.8125rem]", className)}>
      {current ? (
        <span className="min-w-0 break-all text-muted">
          {oldSame}
          <span className="text-muted">{oldChanged}</span>
        </span>
      ) : (
        <span className="text-subtle">new</span>
      )}
      <ArrowRight className="size-3 shrink-0 text-subtle" aria-label="to" />
      <span className="min-w-0 break-all text-fg">
        {same}
        <span className="rounded-sm bg-accent-soft px-0.5 font-medium text-accent">{changed}</span>
      </span>
    </span>
  );
}
