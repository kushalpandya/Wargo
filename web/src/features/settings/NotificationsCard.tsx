// SPDX-License-Identifier: AGPL-3.0-only
import { useCallback, useEffect, useRef, useState } from "react";
import { useLocation } from "react-router";
import { Bell } from "lucide-react";
import { shouldRegister } from "@/lib/sw";
import { useMe } from "@/app/session";
import { Button } from "@/components/ui/button";
import { Card, CardBody, CardHeader } from "@/components/ui/card";
import { Segmented } from "@/components/ui/form";
import { ErrorNote, Skeleton } from "@/components/ui/misc";
import { toast } from "@/components/ui/toast";
import {
  useNotifications,
  usePushState,
  useSaveNotificationSettings,
  useSetPush,
  type Category,
  type Mode,
  type Settings,
} from "@/features/notifications/api";
import { isIOS, localPushKey, pushIsOn, pushSupport, sessionEndsNote, type PushSupport } from "@/features/notifications/push";
import { SettingRow, SettingsList, usePatchSave } from "./shared";

const categories: { id: Category; title: string; hint: string }[] = [
  { id: "download.started", title: "Download started", hint: "A torrent starts downloading." },
  { id: "download.finished", title: "Download finished", hint: "A torrent finishes downloading." },
  { id: "stream.started", title: "Plex stream started", hint: "Someone starts watching something on Plex." },
  { id: "container.unhealthy", title: "Container unhealthy", hint: "A container's health check starts failing." },
  { id: "container.failed", title: "Container failed to start", hint: "A container stops with an error or keeps restarting." },
  { id: "updates.found", title: "Updates found", hint: "A background check finds new updates to install." },
  { id: "download.error", title: "Download error", hint: "A torrent stops because of a tracker or disk problem." },
  { id: "disk.full", title: "Disk nearly full", hint: "A disk is 90% full or more." },
];

const modes: { value: Mode; label: string }[] = [
  { value: "off", label: "Off" },
  { value: "app", label: "In app" },
  { value: "push", label: "In app and push" },
];

/** What stands between this device and push right now. */
function deviceSupport(): PushSupport {
  const secure = window.isSecureContext;
  return pushSupport({
    secure,
    // The worker only runs in the built app (see lib/sw.ts), and push arrives through it.
    sw: shouldRegister({ prod: import.meta.env.PROD, secure, supported: "serviceWorker" in navigator, base: import.meta.env.BASE_URL }),
    push: "PushManager" in window && "Notification" in window,
    standalone:
      window.matchMedia("(display-mode: standalone)").matches || (navigator as Navigator & { standalone?: boolean }).standalone === true,
    ios: isIOS(navigator.userAgent, navigator.maxTouchPoints),
    permission: "Notification" in window ? Notification.permission : "default",
  });
}

const blocked: Record<Exclude<PushSupport, "ok">, string> = {
  "not-https": "Push needs Wargo over HTTPS. Open Wargo at its HTTPS address to get push on this device.",
  "install-first": "On iPhone, add Wargo to your Home Screen and open it from there to get push notifications.",
  denied: "Notifications for Wargo are blocked on this device. Allow them in your system settings, then come back here.",
  unsupported: "This browser can't receive push notifications.",
};

function PushRow() {
  const [support, setSupport] = useState(deviceSupport);
  // The browser's own subscription key (null: none; undefined: still looking).
  const [localKey, setLocalKey] = useState<ArrayBuffer | null | undefined>(undefined);
  const state = usePushState();
  const set = useSetPush();
  // Push belongs to this sign-in: without "Keep me signed in" it stops when the session times out.
  const { data: me } = useMe();
  const look = useCallback(() => {
    setSupport(deviceSupport());
    localPushKey().then(setLocalKey, () => setLocalKey(null));
  }, []);
  // Permission and the browser's subscription can change outside the app
  // (system settings, cleared site data), so look on load and again on return.
  useEffect(() => {
    look();
    const again = () => document.visibilityState === "visible" && look();
    document.addEventListener("visibilitychange", again);
    return () => document.removeEventListener("visibilitychange", again);
  }, [look]);
  const on = pushIsOn(state.data, localKey ?? null);
  const change = (enable: boolean) =>
    set.mutate(enable, {
      onSettled: look,
      onError: (e) => toast.error(enable ? "Could not turn on push" : "Could not turn off push", e),
    });

  return (
    <SettingRow
      title="Push on this device"
      description={
        support !== "ok"
          ? blocked[support]
          : on
            ? me && !me.remember
              ? sessionEndsNote(me.idleMinutes)
              : "Notifications set to push arrive here even when Wargo is closed."
            : "Push is off here. You still see notifications in the app."
      }
    >
      {state.isLoading || localKey === undefined ? (
        <Skeleton className="h-8 w-48" />
      ) : on ? (
        <Button size="sm" loading={set.isPending} onClick={() => change(false)}>
          Turn off push on this device
        </Button>
      ) : (
        support === "ok" && (
          <Button size="sm" variant="primary" loading={set.isPending} onClick={() => change(true)}>
            Enable push on this device
          </Button>
        )
      )}
    </SettingRow>
  );
}

/** Which notifications Wargo shows and pushes, and push for this device. */
export function NotificationsCard() {
  const { data, isLoading, error } = useNotifications();
  const save = useSaveNotificationSettings();
  const [v, update] = usePatchSave<Settings>(data?.settings, save);

  // The panel's gear links to /settings/general#notifications: scroll here once
  // the card has its rows, and again on each new visit to that link.
  const { hash, key } = useLocation();
  const scrolledFor = useRef<string | null>(null);
  const loaded = !!v;
  useEffect(() => {
    if (!loaded || hash !== "#notifications" || scrolledFor.current === key) return;
    scrolledFor.current = key;
    document.getElementById("notifications")?.scrollIntoView();
  }, [loaded, hash, key]);

  return (
    <Card id="notifications" className="scroll-mt-20">
      <CardHeader title="Notifications" icon={<Bell />} tint="butter" />
      <CardBody>
        {isLoading ? (
          <Skeleton className="h-64" />
        ) : error ? (
          <ErrorNote error={error} />
        ) : (
          v && (
            <SettingsList>
              {categories.map((c) => (
                <SettingRow key={c.id} title={c.title} description={c.hint}>
                  <Segmented label={c.title} size="sm" value={v[c.id]} onChange={(mode) => update({ [c.id]: mode })} options={modes} />
                </SettingRow>
              ))}
              <PushRow />
            </SettingsList>
          )
        )}
      </CardBody>
    </Card>
  );
}
