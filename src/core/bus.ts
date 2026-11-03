// SPDX-License-Identifier: AGPL-3.0-only
// The fake /ws/events socket: clients send {topics: [...]} and receive
// {topic, data} messages, like Wargo's event hub. Features register topic
// sources here; a source only runs while someone is subscribed to it.
import { ws } from "msw";

type Client = { send(data: string): void };

interface Source {
  /** Current value, sent at once to a new subscriber (sticky topics). */
  snapshot?: () => unknown;
  /** Called every `every` ms while anyone is subscribed; return undefined to skip. */
  tick?: () => unknown;
  every?: number;
  timer?: number;
}

const sources = new Map<string, Source>();
const subs = new Map<Client, Set<string>>();

/** Registers a topic. */
export function topic(name: string, src: Omit<Source, "timer">) {
  sources.set(name, { ...src });
}

function subscribers(name: string) {
  return [...subs.entries()].filter(([, t]) => t.has(name)).map(([c]) => c);
}

/** Sends data to everyone subscribed to topic. */
export function publish(name: string, data: unknown) {
  const msg = JSON.stringify({ topic: name, data });
  for (const c of subscribers(name)) c.send(msg);
}

/** Re-sends a topic's snapshot (call after the demo state changed). */
export function refresh(name: string) {
  const s = sources.get(name);
  if (s?.snapshot) publish(name, s.snapshot());
}

function schedule() {
  for (const [name, s] of sources) {
    const wanted = subscribers(name).length > 0;
    if (wanted && s.tick && !s.timer) {
      s.timer = window.setInterval(() => {
        const d = s.tick!();
        if (d !== undefined) publish(name, d);
      }, s.every ?? 1000);
    } else if (!wanted && s.timer) {
      clearInterval(s.timer);
      s.timer = undefined;
    }
  }
}

function setTopics(c: Client, list: string[]) {
  const before = subs.get(c) ?? new Set<string>();
  const next = new Set(list.filter((t) => sources.has(t)));
  subs.set(c, next);
  for (const t of next) {
    if (before.has(t)) continue;
    const snap = sources.get(t)?.snapshot?.();
    if (snap !== undefined) c.send(JSON.stringify({ topic: t, data: snap }));
  }
  schedule();
}

export const eventsSocket = ws.link(/\/ws\/events/);

export const eventsHandler = eventsSocket.addEventListener("connection", ({ client }) => {
  const c: Client = { send: (d) => client.send(d) };
  const initial = new URL(client.url).searchParams.get("topics");
  setTopics(c, initial ? initial.split(",").filter(Boolean) : []);
  client.addEventListener("message", (ev) => {
    try {
      const m = JSON.parse(String(ev.data)) as { topics?: string[] };
      if (Array.isArray(m.topics)) setTopics(c, m.topics);
    } catch {
      /* ignore */
    }
  });
  client.addEventListener("close", () => {
    subs.delete(c);
    schedule();
  });
});
