// SPDX-License-Identifier: AGPL-3.0-only
import { readPref, setPref } from "@/lib/prefs";
import { Suspense, useEffect, useMemo, useState, useSyncExternalStore, type ReactNode } from "react";
import { NavLink, Outlet, useLocation, useNavigate } from "react-router";
import { Dialog as D } from "radix-ui";
import { useQueryClient } from "@tanstack/react-query";
import {
  Boxes,
  Gauge,
  House,
  Info,
  LogOut,
  Menu as MenuIcon,
  Contrast,
  Moon,
  PackageCheck,
  PanelLeftClose,
  PanelLeftOpen,
  Settings2,
  Sun,
  X,
} from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { cn } from "@/lib/cn";
import { setTheme, useThemeMode, type ThemeMode } from "@/lib/theme";
import { useSocketStatus } from "@/lib/ws";
import { useCapabilities, useMe } from "./session";
import { useRadioGroup } from "@/components/ui/form";
import { useAccentSync } from "@/features/settings/accent";
import { Logo } from "./Logo";
import { PageSlotsContext } from "./pageSlots";
import { UpdatesBadge } from "@/features/updates/UpdatesBadge";
import { SelfUpdateDialog } from "@/features/containers/SelfUpdateDialog";
import { Bell } from "@/features/notifications/Bell";
import { clearAppBadge } from "@/features/notifications/push";
import { Tooltip } from "@/components/ui/menu";
import { IconButton } from "@/components/ui/button";
import { Badge, Spinner, StatusDot } from "@/components/ui/misc";
import { toast } from "@/components/ui/toast";
import { HomeSkeleton } from "./AppSkeleton";
import { ErrorBoundary } from "@/components/ui/error-boundary";

interface NavItem {
  to: string;
  label: string;
  icon: ReactNode;
  show?: boolean;
  badge?: ReactNode;
}

function useNav(): NavItem[] {
  const { data: caps } = useCapabilities();
  const items: NavItem[] = [
    { to: "/", label: "Home", icon: <House /> },
    { to: "/system", label: "System Monitor", icon: <Gauge /> },
    { to: "/containers", label: "Containers", icon: <Boxes /> },
    { to: "/updates", label: "Updates", icon: <PackageCheck />, badge: <UpdatesBadge />, show: caps?.pkg?.supported !== false || !!caps?.docker?.available || !!caps?.brew?.available },
    // Files, Terminal and Torrents are tiles on Home.
    { to: "/settings", label: "Settings", icon: <Settings2 /> },
  ];
  return items.filter((i) => i.show !== false);
}

/**
 * One sidebar link. In the collapsed rail the tooltip wraps a plain
 * element: Radix's Slot would otherwise stringify NavLink's className
 * function and break hover/active styles.
 */
function SideLink({ item, compact, onNavigate }: { item: NavItem; compact?: boolean; onNavigate?: () => void }) {
  const link = (
    <NavLink
      to={item.to}
      end={item.to === "/"}
      onClick={onNavigate}
      className={({ isActive }) =>
        cn(
          "group relative flex h-9 items-center gap-3 rounded-md px-3 text-ui font-medium transition-colors [&_svg]:size-[1.125rem] [&_svg]:shrink-0",
          compact && "justify-center px-0",
          isActive ? "bg-accent-soft font-semibold text-fg" : "text-muted hover:bg-surface-3 hover:text-fg",
        )
      }
    >
      {({ isActive }) => (
        <>
          <span className={cn(isActive ? "text-accent" : "text-subtle group-hover:text-muted")}>{item.icon}</span>
          <span className={cn(compact && "sr-only")}>{item.label}</span>
          {item.badge && <span className={cn(compact ? "absolute right-1 top-0.5 scale-90" : "ml-auto")}>{item.badge}</span>}
        </>
      )}
    </NavLink>
  );
  if (!compact) return link;
  return (
    <Tooltip content={item.label} side="right">
      <div>{link}</div>
    </Tooltip>
  );
}

function NavLinks({ compact, onNavigate }: { compact?: boolean; onNavigate?: () => void }) {
  const items = useNav();
  return (
    <nav aria-label="Main" className="flex flex-col gap-0.5">
      {items.map((it) => (
        <SideLink key={it.to} item={it} compact={compact} onNavigate={onNavigate} />
      ))}
    </nav>
  );
}

// Below Sign out: this server's details and Wargo's version.
const aboutItem: NavItem = { to: "/about", label: "About", icon: <Info /> };

// Auto uses a half-filled circle, like the appearance control on Apple devices.
const themeIcon: Record<ThemeMode, ReactNode> = { auto: <Contrast />, light: <Sun />, dark: <Moon /> };

const themeModes: { mode: ThemeMode; label: string }[] = [
  { mode: "light", label: "Light" },
  { mode: "auto", label: "Match system" },
  { mode: "dark", label: "Dark" },
];

/**
 * Light, Auto and Dark as one row of icon buttons: one click to switch, the
 * current mode raised like a segmented control. Stacked in the collapsed rail.
 */
function ThemeSwitch({ vertical }: { vertical?: boolean }) {
  const mode = useThemeMode();
  const radio = useRadioGroup(
    themeModes.map((t) => t.mode),
    mode,
    setTheme,
  );
  return (
    <div {...radio.group} aria-label="Colour mode" className={cn("flex gap-0.5 rounded-lg bg-surface-3 p-0.5", vertical ? "flex-col" : "w-full")}>
      {themeModes.map((t) => {
        const on = mode === t.mode;
        const button = (
          <button
            type="button"
            {...radio.item(t.mode)}
            aria-label={t.label}
            onClick={() => setTheme(t.mode)}
            className={cn(
              "inline-flex h-7 items-center justify-center rounded-md transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus/25 [&_svg]:size-3.5",
              vertical ? "w-8" : "flex-1",
              on ? "bg-surface text-fg shadow-sm ring-1 ring-border" : "text-subtle hover:text-fg",
            )}
          >
            {themeIcon[t.mode]}
          </button>
        );
        return (
          <Tooltip key={t.mode} content={t.label} side={vertical ? "right" : "top"}>
            {button}
          </Tooltip>
        );
      })}
    </div>
  );
}

/** Sign out, styled like a sidebar link. */
function SignOut({ compact }: { compact?: boolean }) {
  const { data: me } = useMe();
  const nav = useNavigate();
  const qc = useQueryClient();
  const logout = async () => {
    try {
      await api.post("/api/auth/logout");
    } catch (e) {
      // 401: the session is already gone, which is what we wanted.
      if (!(e instanceof ApiError && e.status === 401)) {
        toast.error("Sign out failed", e);
        return;
      }
    }
    qc.clear();
    clearAppBadge();
    nav("/login", { replace: true });
  };
  const btn = (
    <button
      type="button"
      onClick={logout}
      disabled={me?.dev}
      className={cn(
        "flex h-9 w-full items-center gap-3 rounded-md px-3 text-ui font-medium text-muted transition-colors hover:bg-surface-3 hover:text-fg disabled:opacity-50 [&_svg]:size-[1.125rem] [&_svg]:shrink-0 [&_svg]:text-subtle",
        compact && "justify-center px-0",
      )}
      title={me?.user ? `Signed in as ${me.user}` : undefined}
    >
      <LogOut />
      <span className={cn(compact && "sr-only")}>Sign out</span>
    </button>
  );
  if (!compact) return btn;
  return (
    <Tooltip content="Sign out" side="right">
      <div>{btn}</div>
    </Tooltip>
  );
}

const subscribeOnline = (l: () => void) => {
  window.addEventListener("online", l);
  window.addEventListener("offline", l);
  return () => {
    window.removeEventListener("online", l);
    window.removeEventListener("offline", l);
  };
};
const isOnline = () => navigator.onLine;

/**
 * Shown only while the live connection (/ws/events) is down, so a healthy
 * session has no indicator at all. A short grace period keeps the first
 * connect and quick reconnects from flashing it.
 */
function ConnectionPill() {
  const status = useSocketStatus();
  const online = useSyncExternalStore(subscribeOnline, isOnline);
  const down = status !== "open";
  const [show, setShow] = useState(false);
  useEffect(() => {
    if (!down) {
      setShow(false);
      return;
    }
    const t = window.setTimeout(() => setShow(true), 2000);
    return () => window.clearTimeout(t);
  }, [down]);
  // The live region stays mounted so screen readers announce the change.
  return (
    <div role="status" className="contents">
      {show && down && (
        <Badge tone={online ? "warn" : "crit"} title="Live updates are paused until the connection is back">
          <StatusDot tone={online ? "warn" : "crit"} pulse={online} />
          {online ? "Reconnecting…" : "Offline"}
        </Badge>
      )}
    </div>
  );
}

/**
 * Sticky top bar: transparent over the page background until the page
 * scrolls. It owns the scroll state so scrolling re-renders only the bar.
 */
function TopBar({
  onOpenNav,
  titleRef,
  actionsRef,
}: {
  onOpenNav: () => void;
  titleRef: (el: HTMLDivElement | null) => void;
  actionsRef: (el: HTMLDivElement | null) => void;
}) {
  const [scrolled, setScrolled] = useState(false);
  useEffect(() => {
    const onScroll = () => setScrolled(window.scrollY > 2);
    onScroll();
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => window.removeEventListener("scroll", onScroll);
  }, []);
  return (
    <header
      className={cn(
        // Clears the iPhone status bar in the Home Screen app, where iOS blurs whatever sits under it.
        // The left padding lines the title up with the page. On the right, the bell sits as far from
        // the edge as from the top: half of the bar (4rem, less its 1px border) minus the 2.25rem button.
        "sticky top-0 z-30 flex h-[calc(4rem+env(safe-area-inset-top))] shrink-0 items-center gap-3 border-b pl-3 pr-[calc((4rem-1px-2.25rem)/2)] pt-[env(safe-area-inset-top)] transition-[background-color,border-color] duration-200 sm:pl-5 lg:pl-7",
        scrolled ? "border-border bg-bg/75 backdrop-blur-md" : "border-transparent",
      )}
    >
      <IconButton label="Open navigation" className="md:hidden" onClick={onOpenNav}>
        <MenuIcon />
      </IconButton>
      {/* The current page's title and actions (PageHeader) land here. */}
      <div ref={titleRef} className="min-w-0 flex-1" />
      <ConnectionPill />
      <div ref={actionsRef} className="flex shrink-0 items-center gap-2 max-md:hidden" />
      <Bell />
    </header>
  );
}

const SIDEBAR_KEY = "wargo.sidebar";

/** Collapsed (icon rail) vs expanded sidebar, remembered per browser. */
function useSidebarCollapsed(): [boolean, () => void] {
  const [collapsed, setCollapsed] = useState(() => {
    const v = readPref(SIDEBAR_KEY);
    if (v === "collapsed") return true;
    if (v === "expanded") return false;
    // No preference yet: rail on tablets, full sidebar on desktops.
    return !window.matchMedia("(min-width: 1024px)").matches;
  });
  const toggle = () => {
    setPref(SIDEBAR_KEY, collapsed ? "expanded" : "collapsed");
    setCollapsed(!collapsed);
  };
  return [collapsed, toggle];
}

export function Layout() {
  // The accent colour is saved on the server; apply it once signed in.
  useAccentSync();
  const [drawer, setDrawer] = useState(false);
  const [titleSlot, setTitleSlot] = useState<HTMLDivElement | null>(null);
  const [actionsSlot, setActionsSlot] = useState<HTMLDivElement | null>(null);
  const slots = useMemo(() => ({ title: titleSlot, actions: actionsSlot }), [titleSlot, actionsSlot]);
  const [collapsed, toggleCollapsed] = useSidebarCollapsed();
  const loc = useLocation();
  const { data: me } = useMe();
  useEffect(() => setDrawer(false), [loc.pathname]);

  // Ctrl/Cmd+B toggles the sidebar (outside text fields and the terminal).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey && e.key.toLowerCase() === "b") {
        const t = e.target as HTMLElement | null;
        if (t?.closest("input, textarea, [contenteditable=true], .xterm, .cm-editor")) return;
        e.preventDefault();
        toggleCollapsed();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  });

  const toggleLabel = collapsed ? "Expand sidebar" : "Collapse sidebar";

  return (
    <PageSlotsContext.Provider value={slots}>
    <div className="wargo-canvas flex min-h-dvh">
      <aside
        className={cn(
          "sticky top-0 hidden h-dvh shrink-0 flex-col border-r border-border bg-surface pt-[env(safe-area-inset-top)] transition-[width] duration-200 md:flex",
          collapsed ? "w-[4.25rem]" : "w-60",
        )}
        aria-label="Sidebar"
      >
        <div className={cn("flex items-center gap-2.5", collapsed ? "flex-col justify-center gap-1 py-3" : "h-16 px-4")}>
          <Logo className="size-8 shrink-0" />
          {!collapsed && (
            <>
              <span className="text-lg font-bold tracking-tight">Wargo</span>
              {me?.dev && <Badge tone="warn">Dev</Badge>}
            </>
          )}
          <div className={cn(!collapsed && "ml-auto")}>
            <Tooltip content={`${toggleLabel} (Ctrl+B)`} side="right">
              <IconButton label={toggleLabel} size="sm" onClick={toggleCollapsed} aria-expanded={!collapsed}>
                {collapsed ? <PanelLeftOpen /> : <PanelLeftClose />}
              </IconButton>
            </Tooltip>
          </div>
        </div>
        <div className="scroll-area flex-1 px-3 py-2">
          <NavLinks compact={collapsed} />
        </div>
        <div className="flex flex-col gap-0.5 border-t border-border p-3">
          <SignOut compact={collapsed} />
          <SideLink item={aboutItem} compact={collapsed} />
        </div>
        <div className="flex justify-center border-t border-border p-3">
          <ThemeSwitch vertical={collapsed} />
        </div>
      </aside>

      {/* Mobile drawer */}
      <D.Root open={drawer} onOpenChange={setDrawer}>
        <D.Portal>
          <D.Overlay className="fixed inset-0 z-50 bg-overlay md:hidden" />
          <D.Content className="fixed inset-y-0 left-0 z-50 flex w-72 flex-col border-r border-border bg-surface pt-[env(safe-area-inset-top)] pb-[env(safe-area-inset-bottom)] shadow-pop focus:outline-none md:hidden">
            <D.Title className="sr-only">Navigation</D.Title>
            <D.Description className="sr-only">Main navigation</D.Description>
            <div className="flex h-16 items-center gap-2.5 px-4">
              <Logo className="size-8" />
              <span className="text-lg font-bold tracking-tight">Wargo</span>
              <D.Close className="ml-auto rounded p-1 text-subtle hover:text-fg" aria-label="Close navigation">
                <X className="size-4" />
              </D.Close>
            </div>
            <div className="scroll-area flex-1 px-3 py-2">
              <NavLinks onNavigate={() => setDrawer(false)} />
              <div className="mt-4 border-t border-border pt-3">
                <SignOut />
                <SideLink item={aboutItem} onNavigate={() => setDrawer(false)} />
              </div>
            </div>
            <div className="border-t border-border p-3">
              <ThemeSwitch />
            </div>
          </D.Content>
        </D.Portal>
      </D.Root>

      <div className="flex min-w-0 flex-1 flex-col">
        <TopBar onOpenNav={() => setDrawer(true)} titleRef={setTitleSlot} actionsRef={setActionsSlot} />
        <main id="main" className="@container/main min-w-0 flex-1 px-3 pb-8 pt-1 sm:px-5 lg:px-7">
          {/* Per-route boundary: a crashing page keeps the shell usable. */}
          <ErrorBoundary resetKey={loc.pathname}>
            <Suspense
              fallback={
                loc.pathname === "/" ? (
                  <HomeSkeleton />
                ) : (
                  <div className="flex h-40 items-center justify-center text-muted">
                    <Spinner />
                  </div>
                )
              }
            >
              <Outlet />
            </Suspense>
          </ErrorBoundary>
        </main>
      </div>
      <SelfUpdateDialog />
    </div>
    </PageSlotsContext.Provider>
  );
}
