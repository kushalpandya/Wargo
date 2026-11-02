// SPDX-License-Identifier: AGPL-3.0-only
import { ShieldAlert } from "lucide-react";
import { Logo } from "./Logo";

/**
 * Full-page explanation for HTTP 421: the server rejects this Host name
 * (DNS-rebinding protection) until its URL is listed in auth.origins.
 */
export function HostBlocked() {
  const origin = window.location.origin;
  return (
    <main className="flex min-h-dvh items-center justify-center bg-bg px-4 py-10">
      <div className="w-full max-w-lg">
        <div className="mb-6 flex items-center gap-2.5">
          <Logo className="size-8" />
          <div className="text-base font-semibold tracking-tight">Wargo</div>
        </div>
        <div role="alert" className="rounded-lg border border-border bg-surface p-5 shadow-[var(--shadow)]">
          <h1 className="flex items-center gap-2 text-[0.9375rem] font-semibold">
            <ShieldAlert className="size-4 text-crit" aria-hidden="true" /> This address isn't allowed
          </h1>
          <p className="mt-2 text-sm text-muted">
            Wargo only answers on the host names it is configured for, which protects it from DNS-rebinding attacks. You opened it as:
          </p>
          <code className="num mt-2 block break-all rounded-md border border-border bg-surface-2 px-2.5 py-1.5 text-sm text-fg">{origin}</code>
          <p className="mt-3 text-sm text-muted">
            If this is your server, add it to <code className="num text-fg">auth.origins</code> in the config (or the <code className="num text-fg">WARGO_ORIGINS</code> environment
            variable) and restart Wargo:
          </p>
          <pre className="num mt-2 overflow-x-auto rounded-md border border-border bg-surface-2 px-2.5 py-2 text-xs text-fg">{`[auth]\norigins = ["${origin}"]`}</pre>
          <button
            type="button"
            onClick={() => window.location.reload()}
            className="mt-4 inline-flex h-8 items-center rounded-md bg-accent px-3 text-sm font-medium text-accent-fg hover:bg-accent-strong"
          >
            Try again
          </button>
        </div>
      </div>
    </main>
  );
}
