// SPDX-License-Identifier: AGPL-3.0-only
import { Terminal, type ITheme } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { SearchAddon } from "@xterm/addon-search";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { api } from "@/lib/api";
import { isHttpUrl } from "@/lib/url";

export type TermKind = "host" | "root" | "container";

/** Shape of GET /api/term entries. */
export interface TermInfo {
  id: string;
  kind: TermKind;
  target?: string;
  title: string;
  createdAt: string;
  attached: boolean;
  cols: number;
  rows: number;
  exited: boolean;
  exitCode?: number;
  tmux?: boolean;
}

export const listSessions = () => api.get<TermInfo[]>("/api/term");
export const createSession = (body: { kind: TermKind; target?: string; cols: number; rows: number; tmux?: boolean }) =>
  api.post<{ id: string }>("/api/term", body);
export const deleteSession = (id: string) => api.del<void>(`/api/term/${encodeURIComponent(id)}`);

/**
 * Connection state:
 *  connecting – socket opening; open – live; retrying – lost, auto-reconnecting;
 *  closed – gave up / user action needed; exited – process ended;
 *  takeover – attached from another window; gone – session no longer exists.
 */
export type ConnState = "connecting" | "open" | "retrying" | "closed" | "exited" | "takeover" | "gone";

export interface Modifiers {
  ctrl: boolean;
  alt: boolean;
}

const CLOSE_EXITED = 4000;
const CLOSE_TAKEOVER = 4001;
const CLOSE_SLOW = 4002;
const CLOSE_KILLED = 4003;
const CLOSE_NOT_FOUND = 4004;

const enc = new TextEncoder();

/**
 * Opens a link printed in the terminal: http(s) only (never javascript:,
 * data: or file:), in a new tab without opener or referrer. No clipboard
 * addon is loaded, so OSC 52 clipboard writes from programs are ignored.
 */
export function openTermLink(uri: string) {
  if (!isHttpUrl(uri)) return;
  window.open(uri, "_blank", "noopener,noreferrer");
}

/** Applies a Ctrl modifier to a single typed character. */
export function ctrlChar(ch: string): string {
  if (ch.length !== 1) return ch;
  const c = ch.charCodeAt(0);
  if (ch === " " || ch === "@") return "\x00";
  if (ch === "?") return "\x7f";
  if (c >= 0x61 && c <= 0x7a) return String.fromCharCode(c - 0x60);
  if (c >= 0x40 && c <= 0x5f) return String.fromCharCode(c - 0x40);
  return ch;
}

export interface SessionOptions {
  fontSize: number;
  theme: ITheme;
  screenReader: boolean;
  search: { matchBackground: string; matchOverviewRuler: string; activeMatchBackground: string; activeMatchColorOverviewRuler: string };
}

/**
 * One browser-side terminal: an xterm instance plus its WebSocket. Lives
 * outside React so switching tabs never re-creates the terminal.
 */
export class TermSession {
  readonly id: string;
  readonly term: Terminal;
  readonly el: HTMLDivElement;
  private fitAddon = new FitAddon();
  readonly searchAddon = new SearchAddon({ highlightLimit: 2000 });
  private ws: WebSocket | null = null;
  private retries = 0;
  private timer: number | undefined;
  private disposed = false;
  private opened = false;
  private listeners = new Set<() => void>();
  state: ConnState = "connecting";
  exitCode: number | null = null;
  title = "";
  mods: Modifiers = { ctrl: false, alt: false };
  searchResult: { index: number; count: number } | null = null;

  constructor(id: string, opts: SessionOptions) {
    this.id = id;
    this.el = document.createElement("div");
    this.el.className = "h-full w-full";
    this.term = new Terminal({
      allowProposedApi: true,
      cursorBlink: true,
      cursorStyle: "bar",
      fontFamily: '"JetBrains Mono Variable", ui-monospace, monospace',
      fontSize: opts.fontSize,
      lineHeight: 1.15,
      scrollback: 10000,
      theme: opts.theme,
      screenReaderMode: opts.screenReader,
      macOptionIsMeta: true,
      rightClickSelectsWord: false,
      drawBoldTextInBrightColors: false,
      minimumContrastRatio: 1,
    });
    this.term.loadAddon(this.fitAddon);
    this.term.loadAddon(this.searchAddon);
    // Plain-text URLs in the output: the visible text is the target.
    this.term.loadAddon(
      new WebLinksAddon((ev, uri) => {
        ev.preventDefault();
        openTermLink(uri);
      }),
    );
    // OSC 8 hyperlinks: the program chooses both the text and the hidden
    // target, so show the real URL before leaving.
    this.term.options.linkHandler = {
      allowNonHttpProtocols: false,
      activate: (ev, uri) => {
        ev.preventDefault();
        if (isHttpUrl(uri) && window.confirm(`Open this link from the terminal?\n\n${uri}`)) openTermLink(uri);
      },
    };
    this.searchDeco = opts.search;
    this.searchAddon.onDidChangeResults((r) => {
      this.searchResult = { index: r.resultIndex, count: r.resultCount };
      this.emit();
    });
    this.term.onData((d) => this.input(d));
    this.term.onBinary((d) => this.sendRaw(Uint8Array.from(d, (c) => c.charCodeAt(0))));
    this.term.onResize(({ cols, rows }) => {
      this.sendCtrl({ type: "resize", cols, rows });
      this.emit();
    });
    this.term.onTitleChange((t) => this.setTitle(t));
  }

  searchDeco: SessionOptions["search"];

  /** Mounts the terminal into host (first call opens xterm). */
  mount(host: HTMLElement) {
    if (this.el.parentElement !== host) host.appendChild(this.el);
    if (!this.opened) {
      this.term.open(this.el);
      this.opened = true;
      this.fit();
      this.connect();
    }
  }

  subscribe(l: () => void) {
    this.listeners.add(l);
    return () => this.listeners.delete(l);
  }

  private emit() {
    this.listeners.forEach((l) => l());
  }

  private setState(s: ConnState) {
    if (this.state === s) return;
    this.state = s;
    this.emit();
  }

  private setTitle(t: string) {
    // Set by whatever runs in the shell (OSC 0/2): strip controls, cap length.
    // eslint-disable-next-line no-control-regex
    t = t.replace(/[\u0000-\u001f\u007f-\u009f]/g, "").trim().slice(0, 120);
    if (!t || t === this.title) return;
    this.title = t;
    this.emit();
  }

  fit() {
    if (!this.opened || this.el.offsetParent === null || this.el.clientWidth === 0) return;
    try {
      this.fitAddon.fit();
    } catch {
      /* not measurable yet */
    }
  }

  connect() {
    if (this.disposed) return;
    window.clearTimeout(this.timer);
    this.ws?.close();
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const ws = new WebSocket(`${proto}//${location.host}/ws/term/${encodeURIComponent(this.id)}`);
    ws.binaryType = "arraybuffer";
    this.ws = ws;
    this.setState(this.state === "retrying" ? "retrying" : "connecting");
    let first = true;
    ws.onopen = () => {
      this.retries = 0;
      this.exitCode = null;
      this.setState("open");
      this.sendCtrl({ type: "resize", cols: this.term.cols, rows: this.term.rows });
    };
    ws.onmessage = (ev) => {
      if (ev.data instanceof ArrayBuffer) {
        if (first) {
          // The server replays scrollback on every attach; start clean.
          first = false;
          this.term.reset();
        }
        this.term.write(new Uint8Array(ev.data));
        return;
      }
      try {
        const m = JSON.parse(ev.data as string) as { type: string; code?: number; title?: string };
        if (m.type === "title" && m.title) this.setTitle(m.title);
        if (m.type === "exit") {
          this.exitCode = m.code ?? 0;
          this.setState("exited");
        }
      } catch {
        /* ignore */
      }
      first = false;
    };
    ws.onclose = (ev) => {
      if (this.ws !== ws || this.disposed) return;
      this.ws = null;
      switch (ev.code) {
        case CLOSE_EXITED:
          this.setState("exited");
          return;
        case CLOSE_TAKEOVER:
          this.setState("takeover");
          return;
        case CLOSE_KILLED:
        case CLOSE_NOT_FOUND:
          this.setState("gone");
          return;
        case CLOSE_SLOW:
          this.connect();
          return;
      }
      if (this.retries >= 6) {
        this.setState("closed");
        return;
      }
      this.setState("retrying");
      const delay = Math.min(8000, 400 * 2 ** this.retries++);
      this.timer = window.setTimeout(() => this.connect(), delay);
    };
  }

  /** Manual reconnect (also used to take over from another window). */
  reconnect() {
    this.retries = 0;
    this.setState("connecting");
    this.connect();
  }

  private sendRaw(b: Uint8Array<ArrayBuffer>) {
    if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(b);
  }

  private sendCtrl(m: object) {
    if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(JSON.stringify(m));
  }

  /** Keyboard/paste input, applying sticky modifiers from the extra-keys bar. */
  input(d: string) {
    if (this.mods.ctrl || this.mods.alt) {
      if (this.mods.ctrl) d = ctrlChar(d);
      if (this.mods.alt) d = "\x1b" + d;
      this.mods = { ctrl: false, alt: false };
      this.emit();
    }
    this.sendRaw(enc.encode(d));
  }

  /** Sends a special key from the extra-keys bar. */
  key(name: "esc" | "tab" | "up" | "down" | "left" | "right" | "home" | "end" | "pgup" | "pgdn") {
    const app = this.term.modes.applicationCursorKeysMode;
    const arrows: Record<string, string> = { up: "A", down: "B", right: "C", left: "D", home: "H", end: "F" };
    let seq: string;
    if (name === "esc") seq = "\x1b";
    else if (name === "tab") seq = "\t";
    else if (name === "pgup") seq = "\x1b[5~";
    else if (name === "pgdn") seq = "\x1b[6~";
    else if (this.mods.ctrl) seq = `\x1b[1;5${arrows[name]}`;
    else seq = (app ? "\x1bO" : "\x1b[") + arrows[name];
    this.input(seq);
  }

  toggleMod(m: keyof Modifiers) {
    this.mods = { ...this.mods, [m]: !this.mods[m] };
    this.emit();
  }

  paste(text: string) {
    this.term.paste(text);
  }

  focus() {
    this.term.focus();
  }

  dispose() {
    this.disposed = true;
    window.clearTimeout(this.timer);
    const ws = this.ws;
    this.ws = null;
    ws?.close();
    this.term.dispose();
    this.el.remove();
    this.listeners.clear();
  }
}
