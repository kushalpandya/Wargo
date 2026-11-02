// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState, useSyncExternalStore } from "react";

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

  start() {
    this.enabled = true;
    if (!this.ws) this.connect();
  }

  stop() {
    this.enabled = false;
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
    if (!this.enabled) return;
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const url = `${proto}//${location.host}/ws/events?topics=${encodeURIComponent(this.topics().join(","))}`;
    this.setStatus("connecting");
    const ws = new WebSocket(url);
    this.ws = ws;
    ws.onopen = () => {
      this.retry = 0;
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
      const delay = Math.min(15000, 500 * 2 ** this.retry++);
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
