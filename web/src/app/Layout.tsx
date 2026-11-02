// SPDX-License-Identifier: AGPL-3.0-only
import { Suspense, useEffect, useState, type ReactNode } from "react";
import { NavLink, Outlet, useLocation, useNavigate } from "react-router";
import { Dialog as D } from "radix-ui";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Boxes,
  Download,
  FolderTree,
  Gauge,
  House,
  LayoutGrid,
  LogOut,
  Menu as MenuIcon,
  Monitor,
  Moon,
  PackageCheck,
  PanelLeftClose,
  PanelLeftOpen,
  Search,
  Settings2,
  SquareTerminal,
  Sun,
  X,
} from "lucide-react";
import { api } from "@/lib/api";
import { cn } from "@/lib/cn";
import { setTheme, useThemeMode, type ThemeMode } from "@/lib/theme";
import { useSocketStatus } from "@/lib/ws";
import { useCapabilities, useMe } from "./session";
import { Logo } from "./Logo";
import { UpdatesBadge } from "@/features/updates/UpdatesBadge";
import { Menu, MenuItem, MenuLabel, MenuSeparator, Tooltip } from "@/components/ui/menu";
import { IconButton } from "@/components/ui/button";
import { Badge, Spinner, StatusDot } from "@/components/ui/misc";

interface NavItem {
  to: string;
  label: string;
  icon: ReactNode;
  show?: boolean;
  badge?: ReactNode;
}

function useNav(): { section?: string; items: NavItem[] }[] {
  const { data: caps } = useCapabilities();
  const torrents = !!(caps?.prowlarr?.available || caps?.transmission?.available);
  return [
    {
      items: [
        { to: "/", label: "Home", icon: <House /> },
        { to: "/containers", label: "Containers", icon: <Boxes /> },
        { to: "/system", label: "System", icon: <Gauge /> },
        { to: "/files", label: "Files", icon: <FolderTree /> },
        { to: "/terminal", label: "Terminal", icon: <SquareTerminal /> },
        { to: "/updates", label: "Updates", icon: <PackageCheck />, badge: <UpdatesBadge /> },
        { to: "/apps", label: "Apps", icon: <LayoutGrid />, show: caps?.launcher?.enabled && caps.launcher.placement === "page" },
      ],
    },
    {
      section: "Torrents",
      items: [
        { to: "/torrents/search", label: "Search", icon: <Search />, show: !!caps?.prowlarr?.available },
        { to: "/torrents/downloads", label: "Downloads", icon: <Download />, show: !!caps?.transmission?.available },
      ].filter(() => torrents),
    },
  ].map((g) => ({ ...g, items: g.items.filter((i) => i.show !== false) }));
}

function NavLinks({ compact, onNavigate }: { compact?: boolean; onNavigate?: () => void }) {
  const groups = useNav();
  return (
    <nav aria-label="Main" className="flex flex-col gap-4">
      {groups.map((g, i) =>
        g.items.length === 0 ? null : (
          <div key={i} className="flex flex-col gap-0.5">
            {g.section && !compact && (
              <div className="px-2.5 pb-1 text-2xs font-semibold uppercase tracking-wider text-subtle">{g.section}</div>
            )}
            {g.section && compact && <div className="mx-auto mb-1 h-px w-5 bg-border" />}
            {g.items.map((it) => {
              const link = (
                <NavLink
                  key={it.to}
                  to={it.to}
                  end={it.to === "/"}
                  onClick={onNavigate}
                  className={({ isActive }) =>
                    cn(
                      "group relative flex h-8 items-center gap-2.5 rounded-md px-2.5 text-[0.8125rem] font-medium transition-colors [&_svg]:size-4 [&_svg]:shrink-0",
                      compact && "justify-center px-0",
                      isActive ? "bg-surface-3 text-fg" : "text-muted hover:bg-surface-2 hover:text-fg",
                    )
                  }
                >
                  {({ isActive }) => (
                    <>
                      {isActive && <span className="absolute inset-y-1.5 left-0 w-0.5 rounded-full bg-accent" aria-hidden="true" />}
                      <span className={cn(isActive ? "text-accent" : "text-subtle group-hover:text-muted")}>{it.icon}</span>
                      <span className={cn(compact && "sr-only")}>{it.label}</span>
                      {it.badge && <span className={cn(compact ? "absolute right-1 top-0.5 scale-90" : "ml-auto")}>{it.badge}</span>}
                    </>
                  )}
                </NavLink>
              );
              return compact ? (
                <Tooltip key={it.to} content={it.label} side="right">
                  {link}
                </Tooltip>
              ) : (
                link
              );
            })}
          </div>
        ),
      )}
    </nav>
  );
}

const themeIcon: Record<ThemeMode, ReactNode> = { auto: <Monitor />, light: <Sun />, dark: <Moon /> };

function ThemeMenu() {
  const mode = useThemeMode();
  return (
    <Menu
      trigger={
        <IconButton label={`Theme: ${mode}`} size="sm">
          {themeIcon[mode]}
        </IconButton>
      }
    >
      <MenuLabel>Appearance</MenuLabel>
      {(["auto", "light", "dark"] as ThemeMode[]).map((m) => (
        <MenuItem key={m} icon={themeIcon[m]} onSelect={() => setTheme(m)} shortcut={mode === m ? "✓" : undefined}>
          {m === "auto" ? "Match system" : m === "light" ? "Light" : "Dark"}
        </MenuItem>
      ))}
    </Menu>
  );
}

function UserMenu() {
  const { data: me } = useMe();
  const nav = useNavigate();
  const qc = useQueryClient();
  const logout = async () => {
    await api.post("/api/auth/logout");
    qc.clear();
    nav("/login", { replace: true });
  };
  const initial = (me?.user ?? "?").slice(0, 1).toUpperCase();
  return (
    <Menu
      trigger={
        <button
          aria-label="Account menu"
          className="flex size-7 items-center justify-center rounded-full border border-border-strong bg-surface-3 text-xs font-semibold text-fg hover:border-accent"
        >
          {initial}
        </button>
      }
    >
      <MenuLabel>
        Signed in as <span className="normal-case text-fg">{me?.user}</span>
      </MenuLabel>
      <MenuItem icon={<Settings2 />} onSelect={() => nav("/settings")}>
        Settings
      </MenuItem>
      <MenuSeparator />
      <MenuItem icon={<LogOut />} onSelect={logout} disabled={me?.dev}>
        Sign out
      </MenuItem>
    </Menu>
  );
}

function ConnectionStatus() {
  const s = useSocketStatus();
  const { data: info } = useQuery({
    queryKey: ["system", "info"],
    queryFn: () => api.get<{ hostname: string; os: string }>("/api/system/info"),
    staleTime: Infinity,
  });
  const tone = s === "open" ? "ok" : s === "connecting" ? "warn" : "crit";
  const label = s === "open" ? "Live" : s === "connecting" ? "Connecting" : "Offline";
  return (
    <div className="flex min-w-0 items-center gap-2 text-xs text-muted" title={`${label} · ${info?.os ?? ""}`}>
      <StatusDot tone={tone} label={label} pulse={s === "connecting"} />
      <span className="num truncate">{info?.hostname ?? "…"}</span>
    </div>
  );
}

const SIDEBAR_KEY = "wargo.sidebar";

/** Collapsed (icon rail) vs expanded sidebar, remembered per browser. */
function useSidebarCollapsed(): [boolean, () => void] {
  const [collapsed, setCollapsed] = useState(() => {
    const v = localStorage.getItem(SIDEBAR_KEY);
    if (v === "collapsed") return true;
    if (v === "expanded") return false;
    // No preference yet: rail on tablets, full sidebar on desktops.
    return !window.matchMedia("(min-width: 1024px)").matches;
  });
  const toggle = () =>
    setCollapsed((c) => {
      localStorage.setItem(SIDEBAR_KEY, c ? "expanded" : "collapsed");
      return !c;
    });
  return [collapsed, toggle];
}

export function Layout() {
  const [drawer, setDrawer] = useState(false);
  const [collapsed, toggleCollapsed] = useSidebarCollapsed();
  const loc = useLocation();
  const { data: me } = useMe();
  const { data: caps } = useCapabilities();
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
    <div className="flex min-h-dvh">
      <aside
        className={cn(
          "sticky top-0 hidden h-dvh shrink-0 flex-col border-r border-border bg-surface transition-[width] duration-200 md:flex",
          collapsed ? "w-14" : "w-52",
        )}
        aria-label="Sidebar"
      >
        <div className={cn("flex h-12 items-center gap-2 border-b border-border", collapsed ? "justify-center px-0" : "px-3.5")}>
          <Logo className="size-6 shrink-0" />
          {!collapsed && (
            <>
              <span className="text-[0.9375rem] font-semibold tracking-tight">Wargo</span>
              {me?.dev && <Badge tone="warn">Dev</Badge>}
              <Tooltip content={`${toggleLabel} (Ctrl+B)`} side="right">
                <IconButton label={toggleLabel} size="sm" className="ml-auto" onClick={toggleCollapsed} aria-expanded={!collapsed}>
                  <PanelLeftClose />
                </IconButton>
              </Tooltip>
            </>
          )}
        </div>
        {collapsed && (
          <div className="flex justify-center border-b border-border py-1.5">
            <Tooltip content={`${toggleLabel} (Ctrl+B)`} side="right">
              <IconButton label={toggleLabel} size="sm" onClick={toggleCollapsed} aria-expanded={!collapsed}>
                <PanelLeftOpen />
              </IconButton>
            </Tooltip>
          </div>
        )}
        <div className="scroll-area flex-1 p-2">
          <NavLinks compact={collapsed} />
        </div>
        <div className="border-t border-border p-2">
          {(() => {
            const link = (
              <NavLink
                to="/settings"
                className={({ isActive }) =>
                  cn(
                    "flex h-8 items-center gap-2.5 rounded-md px-2.5 text-[0.8125rem] font-medium [&_svg]:size-4",
                    collapsed && "justify-center px-0",
                    isActive ? "bg-surface-3 text-fg" : "text-muted hover:bg-surface-2 hover:text-fg",
                  )
                }
                aria-label="Settings"
              >
                <Settings2 className="text-subtle" />
                <span className={cn(collapsed && "sr-only")}>Settings</span>
              </NavLink>
            );
            return collapsed ? (
              <Tooltip content="Settings" side="right">
                {link}
              </Tooltip>
            ) : (
              link
            );
          })()}
          {!collapsed && <div className="num mt-1 px-2.5 text-2xs text-subtle">v{caps?.version ?? ""}</div>}
        </div>
      </aside>

      {/* Mobile drawer */}
      <D.Root open={drawer} onOpenChange={setDrawer}>
        <D.Portal>
          <D.Overlay className="fixed inset-0 z-50 bg-overlay md:hidden" />
          <D.Content className="fixed inset-y-0 left-0 z-50 flex w-64 flex-col border-r border-border bg-surface pb-[env(safe-area-inset-bottom)] focus:outline-none md:hidden">
            <D.Title className="sr-only">Navigation</D.Title>
            <D.Description className="sr-only">Main navigation</D.Description>
            <div className="flex h-12 items-center gap-2 border-b border-border px-3.5">
              <Logo className="size-6" />
              <span className="text-[0.9375rem] font-semibold">Wargo</span>
              <D.Close className="ml-auto rounded p-1 text-subtle hover:text-fg" aria-label="Close navigation">
                <X className="size-4" />
              </D.Close>
            </div>
            <div className="scroll-area flex-1 p-2">
              <NavLinks onNavigate={() => setDrawer(false)} />
              <div className="mt-4 border-t border-border pt-2">
                <NavLink to="/settings" className="flex h-8 items-center gap-2.5 rounded-md px-2.5 text-[0.8125rem] font-medium text-muted hover:bg-surface-2 hover:text-fg [&_svg]:size-4">
                  <Settings2 className="text-subtle" /> Settings
                </NavLink>
              </div>
            </div>
          </D.Content>
        </D.Portal>
      </D.Root>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-30 flex h-12 shrink-0 items-center gap-2 border-b border-border bg-bg/85 px-3 backdrop-blur-md sm:px-4">
          <IconButton label="Open navigation" className="md:hidden" onClick={() => setDrawer(true)}>
            <MenuIcon />
          </IconButton>
          <div className="flex items-center gap-2 md:hidden">
            <Logo className="size-5" />
            <span className="font-semibold">Wargo</span>
          </div>
          <div className="ml-auto flex min-w-0 items-center gap-2 sm:gap-3">
            <ConnectionStatus />
            <div className="h-4 w-px bg-border" />
            <ThemeMenu />
            <UserMenu />
          </div>
        </header>
        <main id="main" className="min-w-0 flex-1 px-3 py-4 sm:px-5 sm:py-5 lg:px-6">
          <Suspense
            fallback={
              <div className="flex h-40 items-center justify-center text-muted">
                <Spinner />
              </div>
            }
          >
            <Outlet />
          </Suspense>
        </main>
      </div>
    </div>
  );
}
