// SPDX-License-Identifier: AGPL-3.0-only
import type { ReactNode } from "react";
import { useSearchParams } from "react-router";
import { Tabs } from "radix-ui";
import { Beer, Boxes, PackageCheck } from "lucide-react";
import { cn } from "@/lib/cn";
import { useCapabilities } from "@/app/session";
import { PageHeader } from "@/components/ui/misc";
import { pendingCount, useBrewStatus, usePkgStatus } from "./api";
import { useContainers, useContainerUpdates } from "@/features/containers/api";
import { ContainerUpdates } from "./ContainerUpdates";
import { CheckButton } from "./shared";
import { useCheckAll, type CheckSource } from "./checkAll";
import { SystemUpdates } from "./SystemUpdates";
import { BrewUpdates } from "./BrewUpdates";

type Tab = "containers" | "system" | "brew";

const tabCls =
  "inline-flex h-10 items-center gap-2 whitespace-nowrap border-b-2 border-transparent px-3 text-ui font-medium text-muted hover:text-fg data-[state=active]:border-accent data-[state=active]:text-fg [&_svg]:size-4";

function Count({ n }: { n?: number }) {
  if (n === undefined) return null;
  return (
    <span
      className={cn(
        "num inline-flex h-4 min-w-4 items-center justify-center rounded-full px-1 text-2xs font-semibold leading-none",
        n > 0 ? "bg-accent-soft text-accent" : "bg-surface-3 text-muted",
      )}
    >
      {n > 999 ? "999+" : n}
    </span>
  );
}

interface TabDef {
  id: Tab;
  label: string;
  icon: ReactNode;
  count?: number;
  content: ReactNode;
  /** Whether the page's "Check for updates" can check this source now. */
  checkable: boolean;
  checking: boolean;
  /** Checked at least once and still current. */
  checked: boolean;
}

/** Updates page: container images, OS packages and Homebrew, as tabs. */
export default function UpdatesPage() {
  const [params, setParams] = useSearchParams();
  const caps = useCapabilities().data;
  // macOS has no OS package updates; Homebrew exists only where it's installed.
  const pkgOn = !!caps && caps.pkg?.supported !== false;
  const brewOn = !!caps?.brew?.available;
  const docker = caps?.docker;
  const dockerOn = docker?.available !== false;
  const pkg = usePkgStatus({ enabled: pkgOn });
  const brew = useBrewStatus({ enabled: brewOn });
  const containers = useContainers({ enabled: dockerOn });
  const updates = useContainerUpdates({ enabled: dockerOn });
  const checkAll = useCheckAll();

  const tabs: TabDef[] = [];
  // Without Docker the containers tab only says so; keep it when it's the only tab.
  if (dockerOn || (!pkgOn && !brewOn)) {
    tabs.push({
      id: "containers",
      label: "Containers",
      icon: <Boxes />,
      count: dockerOn && containers.data ? containers.data.filter((c) => c.updateAvailable).length : undefined,
      checkable: dockerOn && !!containers.data,
      checking: !!updates.data?.checking,
      checked: !!updates.data?.checkedAt,
      content: (
        <ContainerUpdates
          docker={docker}
          list={containers.data}
          listError={containers.error}
          updates={updates.data}
          updatesError={updates.error}
        />
      ),
    });
  }
  if (pkgOn) {
    tabs.push({
      id: "system",
      label: "System updates",
      icon: <PackageCheck />,
      count: pkg.data?.lastCheck && !pkg.data.lastCheck.error ? pendingCount(pkg.data) : undefined,
      checkable: !!pkg.data?.available && !pkg.data.running,
      checking: !!pkg.data?.checking,
      checked: !!pkg.data?.lastCheck && !pkg.data.lastCheck.stale,
      content: <SystemUpdates />,
    });
  }
  if (brewOn) {
    tabs.push({
      id: "brew",
      label: "Homebrew",
      icon: <Beer />,
      count: brew.data?.lastCheck && !brew.data.lastCheck.error ? pendingCount(brew.data) : undefined,
      checkable: !!brew.data?.available && !brew.data.running,
      checking: !!brew.data?.checking,
      checked: !!brew.data?.lastCheck && !brew.data.lastCheck.stale,
      content: <BrewUpdates />,
    });
  }

  const param = params.get("tab");
  // Without an explicit tab, open the first one that has something to do.
  const tab: Tab | undefined = tabs.find((t) => t.id === param)?.id ?? (tabs.find((t) => (t.count ?? 0) > 0) ?? tabs[0])?.id;

  // One check for every source; a source that's busy (installing, or
  // already checking) is left alone.
  const checking = checkAll.isPending || tabs.some((t) => t.checking);
  const ready = tabs.filter((t) => t.checkable && !t.checking).map((t) => t.id as CheckSource);
  const checkBtn = tabs.some((t) => t.checkable || t.checking) && (
    <CheckButton
      size="sm"
      checked={tabs.every((t) => !t.checkable || t.checked)}
      checking={checking}
      disabled={!ready.length}
      onClick={() => checkAll.mutate(ready)}
    />
  );

  return (
    <div className="min-w-0">
      <Tabs.Root value={tab} onValueChange={(v) => setParams({ tab: v }, { replace: true })}>
        <PageHeader title="Updates" description="Container images and packages, installed when you choose." />
        {/* Tabs on the left, the page's one "Check for updates" at the right end of the same row. */}
        <div className="mb-4 flex items-center gap-3 border-b border-border">
          <Tabs.List className="-mb-px flex min-w-0 flex-1 gap-1 overflow-x-auto [scrollbar-width:none]" aria-label="Update sources">
            {tabs.map((t) => (
              <Tabs.Trigger key={t.id} value={t.id} className={tabCls}>
                {t.icon} {t.label} <Count n={t.count} />
              </Tabs.Trigger>
            ))}
          </Tabs.List>
          {checkBtn}
        </div>
        {tabs.map((t) => (
          <Tabs.Content key={t.id} value={t.id} className="focus-visible:outline-none">
            {t.content}
          </Tabs.Content>
        ))}
      </Tabs.Root>
    </div>
  );
}
