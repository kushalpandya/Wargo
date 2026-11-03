// SPDX-License-Identifier: AGPL-3.0-only
import { cn } from "@/lib/cn";

/**
 * Wargo mark: the raven on watch at night. Same file as the favicon and the
 * app icons (scripts/icons.mjs renders those from it), cached once by the
 * browser.
 */
export function Logo({ className }: { className?: string }) {
  return (
    <img
      src={`${import.meta.env.BASE_URL}favicon.svg`}
      alt=""
      draggable={false}
      decoding="async"
      className={cn("rounded-[22%]", className)}
    />
  );
}
