// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { isHostBlocked } from "@/lib/api";

type Handler = (data: unknown) => void;

/**
 * A single multiplexed WebSocket to /ws/events. Components subscribe to
 * topics; the socket's subscription set follows the mounted listeners so
 * the server only produces what is on screen.
 */
class EventSocket {
  private ws: WebSocket | null = null;
  private handlers = new Map<string, Set<Handler>>();
  private retry = 0;
  private timer: number | undefined;
  private statusListeners = new Set<() => void>();
  status: "connecting" | "open" | "closed" = "closed";
  private enabled = false;
  private openedAt = 0;

  /** Back online (or tab visible again): reconnect now instead of waiting out the backoff. */
  private wake = () => {
    if (!this.enabled || this.ws || document.visibilityState === "hidden") return;
    window.clearTimeout(this.timer);
    this.connect();
  };

  start() {
    this.enabled = true;
    window.addEventListener("online", this.wake);
    document.addEventListener("visibilitychange", this.wake);
    if (!this.ws) this.connect();
  }

  stop() {
    this.enabled = false;
    window.removeEventListener("online", this.wake);
    document.removeEventListener("visibilitychange", this.wake);
    window.clearTimeout(this.timer);
    this.ws?.close();
    this.ws = null;
    this.setStatus("closed");
  }

  private setStatus(s: EventSocket["status"]) {
    this.status = s;
    this.statusListeners.forEach((l) => l());
  }

  onStatus(l: () => void) {
    this.statusListeners.add(l);
    return () => this.statusListeners.delete(l);
  }

  private topics() {
    return [...this.handlers.entries()].filter(([, s]) => s.size > 0).map(([t]) => t);
  }

  private connect() {
    // 421: this origin is rejected; reconnecting can't help.
    if (!this.enabled || isHostBlocked()) return;
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const url = `${proto}//${location.host}/ws/events?topics=${encodeURIComponent(this.topics().join(","))}`;
    this.setStatus("connecting");
    const ws = new WebSocket(url);
    this.ws = ws;
    ws.onopen = () => {
      this.openedAt = Date.now();
      this.setStatus("open");
      this.sync();
    };
    ws.onmessage = (ev) => {
      try {
        const msg = JSON.parse(ev.data as string) as { topic: string; data: unknown };
        this.handlers.get(msg.topic)?.forEach((h) => h(msg.data));
      } catch {
        /* ignore malformed */
      }
    };
    ws.onclose = () => {
      if (this.ws !== ws) return;
      this.ws = null;
      this.setStatus("closed");
      if (!this.enabled) return;
      // Only a connection that stayed up resets the backoff; a server that
      // accepts and immediately drops us must not cause a tight loop.
      if (this.openedAt && Date.now() - this.openedAt > 10_000) this.retry = 0;
      this.openedAt = 0;
      const base = Math.min(15000, 500 * 2 ** this.retry++);
      const delay = base / 2 + Math.random() * (base / 2);
      this.timer = window.setTimeout(() => this.connect(), delay);
    };
  }

  private sync() {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify({ topics: this.topics() }));
    }
  }

  subscribe(topic: string, h: Handler) {
    let set = this.handlers.get(topic);
    const isNew = !set || set.size === 0;
    if (!set) this.handlers.set(topic, (set = new Set()));
    set.add(h);
    if (isNew) this.sync();
    return () => {
      set!.delete(h);
      if (set!.size === 0) this.sync();
    };
  }
}

export const events = new EventSocket();

/** Calls handler for each message on topic while mounted. */
export function useTopic<T>(topic: string | null, handler: (data: T) => void) {
  const ref = useRef(handler);
  ref.current = handler;
  useEffect(() => {
    if (!topic) return;
    return events.subscribe(topic, (d) => ref.current(d as T));
  }, [topic]);
}

/** Latest value published on topic. */
export function useTopicValue<T>(topic: string | null, initial?: T): T | undefined {
  const [v, setV] = useState<T | undefined>(initial);
  useTopic<T>(topic, setV);
  return v;
}

export function useSocketStatus() {
  return useSyncExternalStore(
    (l) => events.onStatus(l),
    () => events.status,
  );
}
