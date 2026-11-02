// SPDX-License-Identifier: AGPL-3.0-only
import { useState } from "react";
import { cn } from "@/lib/cn";
import { safeLocalPath } from "@/lib/url";
import { StatusDot, type Tone } from "@/components/ui/misc";
import type { Container } from "./types";

// Icon URLs that failed to load this session (avoids retry flicker on re-render).
const failed = new Set<string>();

const sizes = {
  sm: "size-6 rounded text-[0.6875rem]",
  md: "size-8 rounded-md text-sm",
  lg: "size-10 rounded-lg text-base",
} as const;

/**
 * Container logo (from `icon`), falling back to a letter avatar when there
 * is no icon or it 404s. Optionally shows a status dot in the corner.
 */
export function ContainerIcon({
  c,
  size = "md",
  status,
  statusLabel,
  className,
}: {
  c: Pick<Container, "name" | "icon">;
  size?: keyof typeof sizes;
  status?: Tone;
  statusLabel?: string;
  className?: string;
}) {
  const [broken, setBroken] = useState(() => !!c.icon && failed.has(c.icon));
  // Icons are proxied by Wargo (/api/containers/{id}/icon); never load a
  // third-party URL that would leak the viewer's IP to whoever set it.
  const icon = safeLocalPath(c.icon);
  const src = icon && !broken && !failed.has(icon) ? icon : null;
  const letter = (c.name.replace(/^[^a-z0-9]+/i, "")[0] ?? "?").toUpperCase();
  return (
    <span className={cn("relative inline-flex shrink-0", className)}>
      <span
        className={cn(
          "inline-flex items-center justify-center overflow-hidden border border-border bg-surface-2 font-semibold text-muted",
          sizes[size],
        )}
        aria-hidden="true"
      >
        {src ? (
          <img
            src={src}
            alt=""
            loading="lazy"
            decoding="async"
            className="size-full object-contain p-[12%]"
            onError={() => {
              failed.add(src);
              setBroken(true);
            }}
          />
        ) : (
          letter
        )}
      </span>
      {status && (
        <span className="absolute -bottom-0.5 -right-0.5 inline-flex rounded-full bg-surface p-[2px]">
          <StatusDot tone={status} label={statusLabel} />
        </span>
      )}
    </span>
  );
}
