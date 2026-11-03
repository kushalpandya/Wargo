// SPDX-License-Identifier: AGPL-3.0-only
import { Component, Suspense, lazy, type ComponentType, type ReactNode } from "react";
import { Plug } from "lucide-react";
import { EmptyState, Spinner } from "@/components/ui/misc";

// The integrations panel is owned by the torrents feature. Resolve it with a
// glob so this page still builds (and shows a fallback) if it is absent.
const modules = import.meta.glob<Record<string, unknown>>("../torrents/IntegrationSettings.tsx");
const loader = modules["../torrents/IntegrationSettings.tsx"];

const Panel = loader
  ? lazy(async () => {
      const m = await loader();
      const C = (m.IntegrationSettings ?? m.default) as ComponentType | undefined;
      if (!C) throw new Error("IntegrationSettings export not found");
      return { default: C };
    })
  : null;

class Boundary extends Component<{ fallback: ReactNode; children: ReactNode }, { failed: boolean }> {
  state = { failed: false };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  render() {
    return this.state.failed ? this.props.fallback : this.props.children;
  }
}

function Unavailable() {
  return (
    <div className="rounded-lg border border-dashed border-border-strong">
      <EmptyState icon={<Plug />} title="Integrations are not available in this build">
        Prowlarr and Transmission settings will appear here.
      </EmptyState>
    </div>
  );
}

export default function Integrations() {
  return Panel ? (
    <Boundary fallback={<Unavailable />}>
      <Suspense
        fallback={
          <div className="flex h-32 items-center justify-center text-muted">
            <Spinner />
          </div>
        }
      >
        <Panel />
      </Suspense>
    </Boundary>
  ) : (
    <Unavailable />
  );
}
