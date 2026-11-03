// SPDX-License-Identifier: AGPL-3.0-only
import "@xterm/xterm/css/xterm.css";
import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type ReactNode } from "react";
import {
  AArrowDown,
  AArrowUp,
  Box,
  CaseSensitive,
  ChevronDown,
  ChevronUp,
  ClipboardPaste,
  Copy,
  Ellipsis,
  Eraser,
  Maximize2,
  Minimize2,
  Pencil,
  Plus,
  RefreshCw,
  Search,
  Settings2,
  ShieldAlert,
  SquareTerminal,
  X,
  Accessibility,
  Layers,
} from "lucide-react";
import { cn } from "@/lib/cn";
import { copyText } from "@/lib/clipboard";
import { useIsDark } from "@/lib/theme";
import { useCapabilities } from "@/app/session";
import { Button, IconButton } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/dialog";
import { Menu, MenuItem, MenuLabel, MenuSeparator } from "@/components/ui/menu";
import { EmptyState, Kbd, PageHeader, Spinner, StatusDot, type Tone } from "@/components/ui/misc";
import { toast } from "@/components/ui/toast";
import { createSession, deleteSession, listSessions, TermSession, type ConnState, type TermInfo, type TermKind } from "./session";
import { searchDecorations, terminalTheme } from "./theme";
import { useLocalState, useMedia } from "@/lib/hooks";
import { useFullscreen, useSessionState, useVisualHeight } from "./hooks";
import { ExtraKeys } from "./ExtraKeys";
import { ContainerPicker, PasteDialog, RenameDialog } from "./dialogs";

interface Tab {
  id: string;
  kind: TermKind;
  target?: string;
  tmux?: boolean;
  sess: TermSession;
}

const FONT_MIN = 10;
const FONT_MAX = 24;
const FONT_FAMILY = '"JetBrains Mono Variable"';

function kindIcon(kind: TermKind) {
  return kind === "root" ? <ShieldAlert /> : kind === "container" ? <Box /> : <SquareTerminal />;
}

function kindLabel(t: Pick<Tab, "kind" | "target">) {
  return t.kind === "root" ? "root" : t.kind === "container" ? (t.target ?? "container") : "shell";
}

const stateMeta: Record<ConnState, { tone: Tone; label: string }> = {
  connecting: { tone: "warn", label: "Connecting" },
  open: { tone: "ok", label: "Connected" },
  retrying: { tone: "warn", label: "Reconnecting" },
  closed: { tone: "crit", label: "Disconnected" },
  exited: { tone: "neutral", label: "Exited" },
  takeover: { tone: "neutral", label: "Open elsewhere" },
  gone: { tone: "crit", label: "Session ended" },
};

export default function TerminalPage() {
  const { data: caps } = useCapabilities();
  const tcaps = caps?.terminal;
  const dark = useIsDark();
  const touch = useMedia("(pointer: coarse)");
  const phone = useMedia("(max-width: 639px)");
  const vh = useVisualHeight(phone);

  const deepLink = useRef(new URLSearchParams(location.search).get("container"));
  const [tabs, setTabs] = useState<Tab[]>([]);
  const [activeId, setActiveId] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [names, setNames] = useLocalState<Record<string, string>>("wargo.term.names", {});
  const [fontSize, setFontSize] = useLocalState("wargo.term.fontSize", 13);
  const [screenReader, setScreenReader] = useLocalState("wargo.term.screenReader", false);
  const [useTmux, setUseTmux] = useLocalState("wargo.term.tmux", false);
  const [picker, setPicker] = useState(false);
  const [renaming, setRenaming] = useState<Tab | null>(null);
  const [closing, setClosing] = useState<Tab | null>(null);
  const [pasteOpen, setPasteOpen] = useState(false);
  const [searchOpen, setSearchOpen] = useState(false);

  const [rootEl, setRootEl] = useState<HTMLDivElement | null>(null);
  const areaRef = useRef<HTMLDivElement>(null);
  const panelRefs = useRef(new Map<string, HTMLDivElement>());
  const tabRefs = useRef(new Map<string, HTMLButtonElement>());
  const tabsRef = useRef<Tab[]>([]);
  tabsRef.current = tabs;
  const [fullscreen, toggleFullscreen, fsEnabled] = useFullscreen(rootEl);

  const active = tabs.find((t) => t.id === activeId);
  useSessionState(active?.sess);

  const optsRef = useRef({ fontSize, screenReader, dark });
  optsRef.current = { fontSize, screenReader, dark };

  const actionsRef = useRef({ openSearch: () => {}, copy: () => {} });

  const makeSession = useCallback((id: string) => {
    const o = optsRef.current;
    const s = new TermSession(id, {
      fontSize: o.fontSize,
      theme: terminalTheme(o.dark),
      screenReader: o.screenReader,
      search: searchDecorations(o.dark),
    });
    s.term.attachCustomKeyEventHandler((e) => {
      if (e.type !== "keydown" || !e.ctrlKey || !e.shiftKey) return true;
      const k = e.key.toLowerCase();
      if (k === "f") {
        e.preventDefault();
        actionsRef.current.openSearch();
        return false;
      }
      if (k === "c") {
        e.preventDefault();
        actionsRef.current.copy();
        return false;
      }
      // Ctrl+Shift+V: let the browser fire a native paste event.
      if (k === "v") return false;
      return true;
    });
    return s;
  }, []);

  // Reattach to existing sessions on load; dispose terminals on unmount
  // (the server keeps the processes alive for a grace period).
  useEffect(() => {
    let cancelled = false;
    (async () => {
      await document.fonts?.load(`13px ${FONT_FAMILY}`).catch(() => {});
      try {
        const list = await listSessions();
        if (cancelled) return;
        const next = list.map((i: TermInfo) => {
          const sess = makeSession(i.id);
          sess.title = i.title;
          return { id: i.id, kind: i.kind, target: i.target, tmux: i.tmux, sess };
        });
        setTabs(next);
        setActiveId(next[0]?.id ?? null);
        setNames((n) => Object.fromEntries(Object.entries(n).filter(([k]) => list.some((i) => i.id === k))));
      } catch (e) {
        if (!cancelled) toast.error("Could not load terminal sessions", e);
      } finally {
        if (!cancelled) setLoading(false);
      }
      // Deep link from Containers → "Open shell": /terminal?container=<name>
      const target = deepLink.current;
      if (!cancelled && target) {
        deepLink.current = null;
        history.replaceState(null, "", location.pathname);
        openRef.current("container", target);
      }
    })();
    return () => {
      cancelled = true;
      tabsRef.current.forEach((t) => t.sess.dispose());
    };
  }, [makeSession, setNames]);

  // Re-theme on light/dark switch.
  useEffect(() => {
    const theme = terminalTheme(dark);
    const deco = searchDecorations(dark);
    tabs.forEach((t) => {
      t.sess.term.options.theme = theme;
      t.sess.searchDeco = deco;
    });
  }, [dark, tabs]);

  useEffect(() => {
    tabs.forEach((t) => {
      t.sess.term.options.fontSize = fontSize;
      t.sess.term.options.screenReaderMode = screenReader;
    });
    active?.sess.fit();
  }, [fontSize, screenReader, tabs, active]);

  // Mount + fit + focus the active terminal.
  useEffect(() => {
    if (!active) return;
    const host = panelRefs.current.get(active.id);
    if (!host) return;
    active.sess.mount(host);
    requestAnimationFrame(() => {
      active.sess.fit();
      if (!touch) active.sess.focus();
    });
  }, [active, touch]);

  // Keep the active terminal fitted to its container.
  useEffect(() => {
    const el = areaRef.current;
    if (!el) return;
    let raf = 0;
    const ro = new ResizeObserver(() => {
      cancelAnimationFrame(raf);
      raf = requestAnimationFrame(() => tabsRef.current.find((t) => t.id === activeIdRef.current)?.sess.fit());
    });
    ro.observe(el);
    return () => {
      ro.disconnect();
      cancelAnimationFrame(raf);
    };
  }, [loading]);
  const activeIdRef = useRef(activeId);
  activeIdRef.current = activeId;

  const estimateSize = () => {
    const el = areaRef.current;
    if (active) return { cols: active.sess.term.cols, rows: active.sess.term.rows };
    if (!el) return { cols: 80, rows: 24 };
    return {
      cols: Math.max(20, Math.floor((el.clientWidth - 16) / (fontSize * 0.6))),
      rows: Math.max(5, Math.floor((el.clientHeight - 16) / (fontSize * 1.15 * 1.2))),
    };
  };

  const open = async (kind: TermKind, target?: string) => {
    setBusy(true);
    try {
      const tmux = kind === "host" && !!tcaps?.tmux && useTmux;
      const { id } = await createSession({ kind, target, tmux, ...estimateSize() });
      const sess = makeSession(id);
      sess.title = kind === "root" ? "root" : kind === "container" ? (target ?? "") : (tcaps?.shell.split("/").pop() ?? "shell");
      setTabs((t) => [...t, { id, kind, target, tmux, sess }]);
      setActiveId(id);
    } catch (e) {
      toast.error("Could not open terminal", e);
    } finally {
      setBusy(false);
    }
  };

  const openRef = useRef(open);
  openRef.current = open;

  const removeTab = (tab: Tab) => {
    tab.sess.dispose();
    panelRefs.current.delete(tab.id);
    setTabs((list) => {
      const i = list.findIndex((t) => t.id === tab.id);
      const next = list.filter((t) => t.id !== tab.id);
      if (activeIdRef.current === tab.id) setActiveId(next[Math.min(i, next.length - 1)]?.id ?? null);
      return next;
    });
    setNames((n) => {
      const { [tab.id]: _, ...rest } = n;
      return rest;
    });
  };

  const closeTab = async (tab: Tab, confirmed = false) => {
    const running = tab.sess.state !== "exited" && tab.sess.state !== "gone";
    if (running && !confirmed) {
      setClosing(tab);
      return;
    }
    setClosing(null);
    try {
      await deleteSession(tab.id);
    } catch {
      /* already gone */
    }
    removeTab(tab);
  };

  const copy = async () => {
    const sel = active?.sess.term.getSelection();
    if (!sel) {
      toast.info("Nothing selected", "Select text in the terminal first.");
      return;
    }
    if (await copyText(sel)) toast.ok("Copied to clipboard");
    else toast.error("Copy failed");
    active?.sess.focus();
  };

  const paste = async () => {
    if (!active) return;
    try {
      if (!navigator.clipboard?.readText) throw new Error("unavailable");
      const text = await navigator.clipboard.readText();
      if (text) active.sess.paste(text);
      active.sess.focus();
    } catch {
      setPasteOpen(true);
    }
  };

  actionsRef.current = { openSearch: () => setSearchOpen(true), copy };

  const label = (t: Tab) => names[t.id] || t.sess.title || kindLabel(t);
  const max = tcaps?.maxSessions ?? 8;
  const liveCount = tabs.filter((t) => t.sess.state !== "exited" && t.sess.state !== "gone").length;
  const canOpen = liveCount < max && !busy;

  const onTabKey = (e: ReactKeyboardEvent, i: number) => {
    let j = -1;
    if (e.key === "ArrowRight") j = (i + 1) % tabs.length;
    else if (e.key === "ArrowLeft") j = (i - 1 + tabs.length) % tabs.length;
    else if (e.key === "Home") j = 0;
    else if (e.key === "End") j = tabs.length - 1;
    else if (e.key === "F2") {
      e.preventDefault();
      setRenaming(tabs[i]);
      return;
    } else if (e.key === "Delete") {
      e.preventDefault();
      closeTab(tabs[i]);
      return;
    }
    if (j < 0) return;
    e.preventDefault();
    setActiveId(tabs[j].id);
    tabRefs.current.get(tabs[j].id)?.focus();
  };

  const newMenu = (
    <Menu
      align="start"
      trigger={
        <IconButton label="New terminal" size="sm" disabled={!canOpen} className="mx-1 max-md:size-8">
          {busy ? <Spinner /> : <Plus />}
        </IconButton>
      }
    >
      <MenuLabel>New terminal</MenuLabel>
      <MenuItem icon={<SquareTerminal />} onSelect={() => open("host")}>
        Host shell
      </MenuItem>
      <MenuItem icon={<ShieldAlert />} onSelect={() => open("root")} disabled={tcaps?.sudo === false}>
        Root shell (sudo -i)
      </MenuItem>
      <MenuItem icon={<Box />} onSelect={() => setPicker(true)} disabled={tcaps?.docker === false && caps?.docker?.available !== true}>
        Container shell…
      </MenuItem>
      {tcaps?.tmux && (
        <>
          <MenuSeparator />
          <MenuItem icon={<Layers />} onSelect={() => setUseTmux(!useTmux)} shortcut={useTmux ? "On" : "Off"}>
            Use tmux for host shells
          </MenuItem>
        </>
      )}
    </Menu>
  );

  const fontDown = () => setFontSize((f) => Math.max(FONT_MIN, f - 1));
  const fontUp = () => setFontSize((f) => Math.min(FONT_MAX, f + 1));
  const clear = () => {
    active?.sess.term.clear();
    active?.sess.focus();
  };

  const settingsItems = (
    <>
      <MenuItem icon={<Pencil />} onSelect={() => active && setRenaming(active)} disabled={!active} shortcut="F2">
        Rename tab
      </MenuItem>
      <MenuItem icon={<Accessibility />} onSelect={() => setScreenReader(!screenReader)} shortcut={screenReader ? "On" : "Off"}>
        Screen reader mode
      </MenuItem>
      {tcaps?.tmux && (
        <MenuItem icon={<Layers />} onSelect={() => setUseTmux(!useTmux)} shortcut={useTmux ? "On" : "Off"}>
          tmux for new host shells
        </MenuItem>
      )}
    </>
  );

  const height = phone ? (vh ? `${vh - 64}px` : "calc(100dvh - 4rem)") : undefined;

  return (
    <div
      ref={setRootEl}
      style={{ height }}
      className={cn(
        "flex min-h-0 flex-col overflow-hidden border-border bg-surface",
        "-mx-3 -mb-8 -mt-1 border-y sm:mx-0 sm:my-0 sm:h-[calc(100dvh-6.25rem)] sm:rounded-lg sm:border",
        fullscreen && "!m-0 !h-full rounded-none border-0",
      )}
    >
      <PageHeader title="Terminal" />
      {/* Tabs + toolbar */}
      <div className="flex h-10 shrink-0 items-stretch border-b border-border bg-surface-2 max-md:h-11">
        <div role="tablist" aria-label="Terminal sessions" className="flex [scrollbar-width:none] min-w-0 items-stretch overflow-x-auto">
          {tabs.map((t, i) => (
            <TabButton
              key={t.id}
              tab={t}
              label={label(t)}
              selected={t.id === activeId}
              refCb={(el) => (el ? tabRefs.current.set(t.id, el) : tabRefs.current.delete(t.id))}
              onSelect={() => setActiveId(t.id)}
              onClose={() => closeTab(t)}
              onRename={() => setRenaming(t)}
              onKeyDown={(e) => onTabKey(e, i)}
            />
          ))}
        </div>
        <div className="flex shrink-0 items-center border-l border-border first:border-l-0">{newMenu}</div>
        <div className="ml-auto flex shrink-0 items-center gap-0.5 border-l border-border px-1.5">
          <div className="flex items-center gap-0.5 max-md:hidden">
            <IconButton label="Find in scrollback (Ctrl+Shift+F)" size="sm" onClick={() => setSearchOpen((v) => !v)} disabled={!active} aria-pressed={searchOpen}>
              <Search />
            </IconButton>
            <div className="mx-1 h-4 w-px bg-border" />
            <IconButton label="Smaller font" size="sm" onClick={fontDown} disabled={fontSize <= FONT_MIN}>
              <AArrowDown />
            </IconButton>
            <span className="num w-6 text-center text-xs text-muted" aria-live="polite" aria-label={`Font size ${fontSize}`}>
              {fontSize}
            </span>
            <IconButton label="Larger font" size="sm" onClick={fontUp} disabled={fontSize >= FONT_MAX}>
              <AArrowUp />
            </IconButton>
            <div className="mx-1 h-4 w-px bg-border" />
            <IconButton label="Copy selection (Ctrl+Shift+C)" size="sm" onClick={copy} disabled={!active}>
              <Copy />
            </IconButton>
            <IconButton label="Paste" size="sm" onClick={paste} disabled={!active || active.sess.state !== "open"}>
              <ClipboardPaste />
            </IconButton>
            <IconButton label="Clear scrollback" size="sm" onClick={clear} disabled={!active}>
              <Eraser />
            </IconButton>
            {fsEnabled && (
              <IconButton label={fullscreen ? "Exit full screen" : "Full screen"} size="sm" onClick={toggleFullscreen}>
                {fullscreen ? <Minimize2 /> : <Maximize2 />}
              </IconButton>
            )}
            <Menu
              trigger={
                <IconButton label="Terminal settings" size="sm">
                  <Settings2 />
                </IconButton>
              }
            >
              <MenuLabel>Terminal</MenuLabel>
              {settingsItems}
            </Menu>
          </div>
          {/* Compact toolbar for phones and narrow tablets */}
          <div className="flex items-center gap-0.5 md:hidden">
            <IconButton label="Paste" onClick={paste} disabled={!active || active.sess.state !== "open"}>
              <ClipboardPaste />
            </IconButton>
            <Menu
              trigger={
                <IconButton label="More terminal actions">
                  <Ellipsis />
                </IconButton>
              }
            >
              <MenuItem icon={<Search />} onSelect={() => setSearchOpen(true)} disabled={!active}>
                Find in scrollback
              </MenuItem>
              <MenuItem icon={<Copy />} onSelect={copy} disabled={!active}>
                Copy selection
              </MenuItem>
              <MenuItem icon={<Eraser />} onSelect={clear} disabled={!active}>
                Clear scrollback
              </MenuItem>
              <MenuSeparator />
              <MenuItem icon={<AArrowUp />} onSelect={fontUp} disabled={fontSize >= FONT_MAX} shortcut={String(fontSize)}>
                Larger font
              </MenuItem>
              <MenuItem icon={<AArrowDown />} onSelect={fontDown} disabled={fontSize <= FONT_MIN}>
                Smaller font
              </MenuItem>
              {fsEnabled && (
                <MenuItem icon={fullscreen ? <Minimize2 /> : <Maximize2 />} onSelect={toggleFullscreen}>
                  {fullscreen ? "Exit full screen" : "Full screen"}
                </MenuItem>
              )}
              <MenuSeparator />
              {settingsItems}
              {active && (
                <>
                  <MenuSeparator />
                  <MenuItem icon={<X />} onSelect={() => closeTab(active)} danger>
                    Close tab
                  </MenuItem>
                </>
              )}
            </Menu>
          </div>
        </div>
      </div>

      {searchOpen && active && <SearchBar sess={active.sess} onClose={() => (setSearchOpen(false), active.sess.focus())} />}

      {/* Terminal area */}
      <div ref={areaRef} className="relative min-h-0 flex-1 bg-surface dark:bg-bg">
        {loading ? (
          <div className="flex h-full items-center justify-center text-muted">
            <Spinner />
          </div>
        ) : tabs.length === 0 ? (
          <EmptyState
            icon={<SquareTerminal />}
            title="No open terminals"
            className="h-full"
            action={
              <div className="flex flex-wrap justify-center gap-2">
                <Button variant="primary" icon={<SquareTerminal />} onClick={() => open("host")} loading={busy}>
                  Host shell
                </Button>
                <Button icon={<ShieldAlert />} onClick={() => open("root")} disabled={busy || tcaps?.sudo === false}>
                  Root shell
                </Button>
                <Button icon={<Box />} onClick={() => setPicker(true)} disabled={busy || (tcaps?.docker === false && caps?.docker?.available !== true)}>
                  Container
                </Button>
              </div>
            }
          >
            Sessions keep running for 10 minutes after you leave, so reloading the page reattaches.
            {!touch && (
              <span className="mt-2 block text-xs text-subtle">
                <Kbd>Ctrl</Kbd> <Kbd>Shift</Kbd> <Kbd>C</Kbd>/<Kbd>V</Kbd> copy &amp; paste · <Kbd>Ctrl</Kbd> <Kbd>Shift</Kbd> <Kbd>F</Kbd> find
              </span>
            )}
          </EmptyState>
        ) : null}
        {tabs.map((t) => (
          <div
            key={t.id}
            id={`term-panel-${t.id}`}
            role="tabpanel"
            aria-labelledby={`term-tab-${t.id}`}
            hidden={t.id !== activeId}
            className="absolute inset-0 py-1.5 pl-2 pr-0.5 sm:pl-2.5"
          >
            <div
              className="h-full w-full"
              ref={(el) => {
                if (el) panelRefs.current.set(t.id, el);
              }}
            />
          </div>
        ))}
        {active && <Overlay tab={active} onReconnect={() => active.sess.reconnect()} onClose={() => closeTab(active, true)} onRestart={() => open(active.kind, active.target)} />}
      </div>

      {active && !phone && <StatusBar tab={active} />}
      {(touch || phone) && tabs.length > 0 && <ExtraKeys session={active?.sess} />}

      <ContainerPicker
        open={picker}
        onOpenChange={setPicker}
        onPick={(target) => {
          setPicker(false);
          open("container", target);
        }}
      />
      <RenameDialog
        open={!!renaming}
        onOpenChange={(o) => !o && setRenaming(null)}
        initial={renaming ? names[renaming.id] ?? "" : ""}
        onSave={(name) => {
          if (renaming) setNames((n) => ({ ...n, [renaming.id]: name }));
          setRenaming(null);
          active?.sess.focus();
        }}
      />
      <PasteDialog
        open={pasteOpen}
        onOpenChange={setPasteOpen}
        onPaste={(text) => {
          active?.sess.paste(text);
          setPasteOpen(false);
          active?.sess.focus();
        }}
      />
      <ConfirmDialog
        open={!!closing}
        onOpenChange={(o) => !o && setClosing(null)}
        title="Close terminal?"
        description={
          closing ? (
            <>
              <span className="font-medium text-fg">{label(closing)}</span> is still running. Closing the tab ends the session
              {closing.tmux ? " (the tmux session itself keeps running)." : " and its processes."}
            </>
          ) : undefined
        }
        confirmLabel="Close"
        danger
        onConfirm={() => closing && closeTab(closing, true)}
      />
    </div>
  );
}

function TabButton({
  tab,
  label,
  selected,
  refCb,
  onSelect,
  onClose,
  onRename,
  onKeyDown,
}: {
  tab: Tab;
  label: string;
  selected: boolean;
  refCb: (el: HTMLButtonElement | null) => void;
  onSelect: () => void;
  onClose: () => void;
  onRename: () => void;
  onKeyDown: (e: ReactKeyboardEvent) => void;
}) {
  useSessionState(tab.sess);
  const st = stateMeta[tab.sess.state];
  return (
    <div
      role="presentation"
      className={cn(
        "group relative flex max-w-56 shrink-0 items-center border-r border-border",
        selected ? "bg-surface text-fg dark:bg-bg" : "text-muted hover:bg-surface-3 hover:text-fg",
      )}
    >
      {selected && <span className="absolute inset-x-0 top-0 h-0.5 bg-accent" aria-hidden="true" />}
      <button
        ref={refCb}
        id={`term-tab-${tab.id}`}
        role="tab"
        type="button"
        aria-selected={selected}
        aria-controls={`term-panel-${tab.id}`}
        tabIndex={selected ? 0 : -1}
        onClick={onSelect}
        onDoubleClick={onRename}
        onKeyDown={onKeyDown}
        title={`${label} · ${st.label}`}
        aria-keyshortcuts="Delete F2"
        aria-description={`${st.label}. Press Delete to close, F2 to rename.`}
        className="flex h-full min-w-0 items-center gap-2 pl-3 pr-1 text-ui font-medium outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-focus"
      >
        <span
          className={cn(
            "relative shrink-0 [&_svg]:size-3.5",
            tab.kind === "root" ? "text-warn" : tab.kind === "container" ? "text-frost" : selected ? "text-accent" : "text-subtle",
          )}
        >
          {kindIcon(tab.kind)}
          {tab.sess.state !== "open" && (
            <StatusDot tone={st.tone} className="absolute -bottom-0.5 -right-0.5 size-1.5 ring-2 ring-surface-2 [&>span]:size-1.5" />
          )}
        </span>
        <span className={cn("truncate", tab.sess.state === "exited" && "text-subtle line-through decoration-subtle/60")}>{label}</span>
        {tab.kind === "root" && <span className="rounded bg-warn-soft px-1 text-2xs font-semibold uppercase tracking-wide text-warn">root</span>}
      </button>
      {/* Pointer-only close affordance: inside a tablist only tabs may be
          interactive, so keyboard users close with Delete (or the menu). */}
      <span
        aria-hidden="true"
        title="Close tab"
        onClick={onClose}
        className={cn(
          "mr-1.5 flex size-6 shrink-0 cursor-pointer items-center justify-center rounded text-subtle hover:bg-surface-3 hover:text-fg max-md:size-8",
          !selected && "md:opacity-0 md:group-hover:opacity-100",
        )}
      >
        <X className="size-3.5" />
      </span>
    </div>
  );
}

function SearchBar({ sess, onClose }: { sess: TermSession; onClose: () => void }) {
  useSessionState(sess);
  const [q, setQ] = useState("");
  const [cs, setCs] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const opts = useMemo(() => ({ caseSensitive: cs, decorations: sess.searchDeco }), [cs, sess.searchDeco]);
  useEffect(() => {
    inputRef.current?.focus();
    inputRef.current?.select();
  }, []);
  useEffect(() => {
    if (q) sess.searchAddon.findNext(q, { ...opts, incremental: true });
    else {
      sess.searchAddon.clearDecorations();
      sess.searchResult = null;
    }
  }, [q, opts, sess]);
  useEffect(() => () => sess.searchAddon.clearDecorations(), [sess]);
  const r = sess.searchResult;
  const next = () => q && sess.searchAddon.findNext(q, opts);
  const prev = () => q && sess.searchAddon.findPrevious(q, opts);
  return (
    <div role="search" className="flex h-10 shrink-0 items-center gap-1 border-b border-border bg-surface px-2">
      <Search className="ml-1 size-3.5 shrink-0 text-subtle" aria-hidden="true" />
      <input
        ref={inputRef}
        value={q}
        onChange={(e) => setQ(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            if (e.shiftKey) prev();
            else next();
          } else if (e.key === "Escape") {
            e.preventDefault();
            onClose();
          }
        }}
        placeholder="Find in scrollback"
        aria-label="Find in scrollback"
        spellCheck={false}
        className="num h-8 min-w-0 flex-1 bg-transparent px-1.5 text-ui text-fg placeholder:text-subtle focus:outline-none"
      />
      <span className="num shrink-0 text-xs text-muted" aria-live="polite">
        {q && r ? (r.count === 0 ? "No results" : r.index < 0 ? `${r.count}+` : `${r.index + 1}/${r.count}`) : ""}
      </span>
      <IconButton label="Match case" size="sm" aria-pressed={cs} onClick={() => setCs(!cs)} className={cn(cs && "bg-surface-3 text-fg")}>
        <CaseSensitive />
      </IconButton>
      <IconButton label="Previous match (Shift+Enter)" size="sm" onClick={prev} disabled={!q}>
        <ChevronUp />
      </IconButton>
      <IconButton label="Next match (Enter)" size="sm" onClick={next} disabled={!q}>
        <ChevronDown />
      </IconButton>
      <IconButton label="Close search (Esc)" size="sm" onClick={onClose}>
        <X />
      </IconButton>
    </div>
  );
}

function Overlay({ tab, onReconnect, onClose, onRestart }: { tab: Tab; onReconnect: () => void; onClose: () => void; onRestart: () => void }) {
  const s = tab.sess;
  let body: ReactNode = null;
  switch (s.state) {
    case "closed":
      body = (
        <Panel title="Disconnected" text="The connection to this terminal was lost. The session keeps running on the server.">
          <Button variant="primary" icon={<RefreshCw />} onClick={onReconnect} autoFocus>
            Reconnect
          </Button>
        </Panel>
      );
      break;
    case "takeover":
      body = (
        <Panel title="Open in another window" text="This session was attached from another tab or device.">
          <Button variant="primary" icon={<RefreshCw />} onClick={onReconnect} autoFocus>
            Use here
          </Button>
        </Panel>
      );
      break;
    case "gone":
      body = (
        <Panel title="Session ended" text="This session no longer exists on the server (it may have been idle too long).">
          <Button onClick={onClose} autoFocus>
            Close tab
          </Button>
          <Button variant="primary" icon={<RefreshCw />} onClick={onRestart}>
            New session
          </Button>
        </Panel>
      );
      break;
  }
  if (body) {
    return (
      <div className="absolute inset-0 z-10 flex items-center justify-center bg-overlay p-4" role="alertdialog" aria-live="assertive" aria-label={stateMeta[s.state].label}>
        {body}
      </div>
    );
  }
  if (s.state === "retrying") {
    return (
      <div role="status" className="absolute right-3 top-2 z-10 flex items-center gap-2 rounded-md border border-border-strong bg-surface px-2.5 py-1.5 text-xs text-muted shadow-lg">
        <Spinner className="size-3.5" /> Reconnecting…
      </div>
    );
  }
  if (s.state === "exited") {
    return (
      <div role="status" className="absolute inset-x-0 bottom-0 z-10 flex flex-wrap items-center gap-x-3 gap-y-2 border-t border-border-strong bg-surface-2 px-3 py-2 text-sm">
        <StatusDot tone={s.exitCode ? "crit" : "neutral"} />
        <span className="min-w-0 flex-1 text-muted">
          Process exited{s.exitCode != null && <> with code <span className="num text-fg">{s.exitCode}</span></>}
        </span>
        <Button size="sm" onClick={onClose}>
          Close tab
        </Button>
        <Button size="sm" variant="primary" icon={<RefreshCw />} onClick={onRestart}>
          Restart
        </Button>
      </div>
    );
  }
  return null;
}

function Panel({ title, text, children }: { title: string; text: string; children: ReactNode }) {
  return (
    <div className="w-full max-w-sm rounded-lg border border-border-strong bg-surface p-4 shadow-2xl">
      <p className="font-semibold">{title}</p>
      <p className="mt-1 text-sm text-muted">{text}</p>
      <div className="mt-4 flex flex-wrap justify-end gap-2">{children}</div>
    </div>
  );
}

function StatusBar({ tab }: { tab: Tab }) {
  const s = useSessionState(tab.sess)!;
  const st = stateMeta[s.state];
  const kind = tab.kind === "host" ? (tab.tmux ? "host · tmux" : "host") : tab.kind === "root" ? "root · sudo -i" : `container · ${tab.target}`;
  return (
    <div className="flex h-7 shrink-0 items-center gap-3 border-t border-border bg-surface-2 px-3 text-2xs text-muted">
      <span className="flex items-center gap-1.5">
        <StatusDot tone={st.tone} pulse={s.state === "connecting" || s.state === "retrying"} />
        {st.label}
      </span>
      <span className="h-3 w-px bg-border" aria-hidden="true" />
      <span className="num truncate">{kind}</span>
      <span className="num ml-auto shrink-0">
        {s.term.cols}×{s.term.rows}
      </span>
    </div>
  );
}
