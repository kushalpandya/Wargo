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
  LayoutGrid,
  ListTree,
  LogOut,
  Menu as MenuIcon,
  Monitor,
  Moon,
  PackageCheck,
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
import { Menu, MenuItem, MenuLabel, MenuSeparator, Tooltip } from "@/components/ui/menu";
import { IconButton } from "@/components/ui/button";
import { Badge, Spinner, StatusDot } from "@/components/ui/misc";

interface NavItem {
  to: string;
  label: string;
  icon: ReactNode;
  show?: boolean;
}

function useNav(): { section?: string; items: NavItem[] }[] {
  const { data: caps } = useCapabilities();
  const torrents = !!(caps?.prowlarr?.available || caps?.transmission?.available);
  return [
    {
      items: [
        { to: "/", label: "Overview", icon: <Gauge /> },
        { to: "/apps", label: "Apps", icon: <LayoutGrid />, show: caps?.launcher?.enabled && caps.launcher.placement === "page" },
        { to: "/processes", label: "Processes", icon: <ListTree /> },
        { to: "/containers", label: "Containers", icon: <Boxes /> },
        { to: "/terminal", label: "Terminal", icon: <SquareTerminal /> },
        { to: "/files", label: "Files", icon: <FolderTree /> },
        { to: "/updates", label: "Updates", icon: <PackageCheck /> },
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

export function Layout() {
  const [drawer, setDrawer] = useState(false);
  const loc = useLocation();
  const { data: me } = useMe();
  const { data: caps } = useCapabilities();
  useEffect(() => setDrawer(false), [loc.pathname]);

  return (
    <div className="flex min-h-dvh">
      {/* Sidebar: icon rail on md, full on lg+ */}
      <aside className="sticky top-0 hidden h-dvh shrink-0 flex-col border-r border-border bg-surface md:flex md:w-14 lg:w-52">
        <div className="flex h-12 items-center gap-2 border-b border-border px-3.5 max-lg:justify-center max-lg:px-0">
          <Logo className="size-6" />
          <span className="hidden text-[0.9375rem] font-semibold tracking-tight lg:inline">Wargo</span>
          {me?.dev && (
            <Badge tone="warn" className="ml-auto hidden lg:inline-flex">
              Dev
            </Badge>
          )}
        </div>
        <div className="scroll-area flex-1 p-2 max-lg:hidden">
          <NavLinks />
        </div>
        <div className="scroll-area flex-1 p-2 lg:hidden">
          <NavLinks compact />
        </div>
        <div className="border-t border-border p-2">
          <NavLink
            to="/settings"
            className={({ isActive }) =>
              cn(
                "flex h-8 items-center gap-2.5 rounded-md px-2.5 text-[0.8125rem] font-medium [&_svg]:size-4 max-lg:justify-center max-lg:px-0",
                isActive ? "bg-surface-3 text-fg" : "text-muted hover:bg-surface-2 hover:text-fg",
              )
            }
            aria-label="Settings"
          >
            <Settings2 className="text-subtle" />
            <span className="max-lg:sr-only">Settings</span>
          </NavLink>
          <div className="num mt-1 px-2.5 text-2xs text-subtle max-lg:hidden">v{caps?.version ?? ""}</div>
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
