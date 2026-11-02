// SPDX-License-Identifier: AGPL-3.0-only

/** Wargo mark: a wolf's ears and muzzle folded into a "W". */
export function Logo({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 32 32" className={className} aria-hidden="true">
      <rect width="32" height="32" rx="7" fill="var(--surface-3)" />
      <path d="M6 9l4.5 14 3.2-8.2L16 20l2.3-5.2 3.2 8.2L26 9l-4.6 4.4L16 7l-5.4 6.4z" fill="var(--accent)" />
    </svg>
  );
}
