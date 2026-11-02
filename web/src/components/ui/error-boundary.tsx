// SPDX-License-Identifier: AGPL-3.0-only
import { Component, type ErrorInfo, type ReactNode } from "react";
import { AlertTriangle, RotateCw } from "lucide-react";
import { cn } from "@/lib/cn";

/** Chunks that fail to load after an upgrade (old index.html, new assets). */
function isChunkError(e: unknown) {
  const m = e instanceof Error ? e.message : String(e);
  return /dynamically imported module|Importing a module script failed|error loading dynamically|Failed to fetch dynamically/i.test(m);
}

export function ErrorFallback({ error, onRetry, className }: { error: unknown; onRetry?: () => void; className?: string }) {
  const chunk = isChunkError(error);
  const msg = error instanceof Error ? error.message : String(error);
  return (
    <div role="alert" className={cn("flex flex-col items-center justify-center gap-3 px-6 py-12 text-center", className)}>
      <AlertTriangle className="size-7 text-crit" aria-hidden="true" />
      <p className="font-medium text-fg">{chunk ? "Wargo was updated" : "Something went wrong"}</p>
      <p className="max-w-md text-sm text-muted">
        {chunk ? "This page needs to be reloaded to load the new version." : "This part of the page crashed. Reloading usually fixes it; your server was not affected."}
      </p>
      {!chunk && msg && <code className="num max-w-lg break-words rounded-md border border-border bg-surface-2 px-2.5 py-1.5 text-xs text-muted">{msg.slice(0, 300)}</code>}
      <div className="mt-1 flex gap-2">
        {onRetry && !chunk && (
          <button
            type="button"
            onClick={onRetry}
            className="inline-flex h-8 items-center gap-2 rounded-md border border-border bg-surface-2 px-3 text-sm text-fg hover:border-border-strong hover:bg-surface-3"
          >
            Try again
          </button>
        )}
        <button
          type="button"
          onClick={() => window.location.reload()}
          className="inline-flex h-8 items-center gap-2 rounded-md border border-transparent bg-accent px-3 text-sm font-medium text-accent-fg hover:bg-accent-strong"
        >
          <RotateCw className="size-4" aria-hidden="true" /> Reload
        </button>
      </div>
    </div>
  );
}

/**
 * Catches render errors so one broken view shows a fallback instead of a
 * blank app. Change `resetKey` (e.g. the route) to clear the error.
 */
export class ErrorBoundary extends Component<{ children: ReactNode; resetKey?: unknown; className?: string }, { error: unknown; key: unknown }> {
  state = { error: null as unknown, key: this.props.resetKey };

  static getDerivedStateFromError(error: unknown) {
    return { error };
  }

  static getDerivedStateFromProps(props: { resetKey?: unknown }, state: { error: unknown; key: unknown }) {
    if (props.resetKey !== state.key) return { error: null, key: props.resetKey };
    return null;
  }

  componentDidCatch(error: unknown, info: ErrorInfo) {
    console.error("Render error", error, info.componentStack);
  }

  render() {
    if (this.state.error) return <ErrorFallback error={this.state.error} onRetry={() => this.setState({ error: null })} className={this.props.className} />;
    return this.props.children;
  }
}
