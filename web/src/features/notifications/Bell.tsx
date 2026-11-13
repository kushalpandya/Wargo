// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { Popover } from "radix-ui";
import { Bell as BellIcon } from "lucide-react";
import { IconButton } from "@/components/ui/button";
import { useNotifications } from "./api";
import { badgeText } from "./format";
import { Panel } from "./Panel";
import { clearAppBadge, notificationOpen } from "./push";

/** Sets the Home Screen icon's badge where the browser supports it, and clears it when the bell goes (sign-out). */
function useAppBadge(n: number | undefined) {
  useEffect(() => {
    if (n === undefined || !("setAppBadge" in navigator)) return;
    try {
      (n > 0 ? navigator.setAppBadge(n) : navigator.clearAppBadge()).catch(() => {});
    } catch {
      /* not allowed here (no installed app, no permission) */
    }
  }, [n]);
  useEffect(() => clearAppBadge, []);
}

/** The header's bell: its count, and the notifications panel hanging from it. */
export function Bell() {
  const [open, setOpen] = useState(false);
  const contentRef = useRef<HTMLDivElement>(null);
  const { data, isLoading, error } = useNotifications();
  const [params, setParams] = useSearchParams();
  const badge = data?.badge;
  const text = badgeText(badge ?? 0);
  useAppBadge(badge);

  // A push notification opens Wargo with ?notifications=1: show the panel, then tidy the URL.
  const fromPush = params.get("notifications") === "1";
  useEffect(() => {
    if (!fromPush) return;
    setOpen(true);
    setParams(
      (p) => {
        p.delete("notifications");
        return p;
      },
      { replace: true },
    );
  }, [fromPush, setParams]);

  // A tap on a push while Wargo is already open: the service worker focuses
  // this window and says where to go (the panel; a link if one is given).
  const navigate = useNavigate();
  useEffect(() => {
    if (!("serviceWorker" in navigator)) return;
    const onMessage = (e: MessageEvent) => {
      const m = notificationOpen(e.data);
      if (!m) return;
      if (m.link) navigate(m.link);
      else setOpen(true);
    };
    navigator.serviceWorker.addEventListener("message", onMessage);
    return () => navigator.serviceWorker.removeEventListener("message", onMessage);
  }, [navigate]);

  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger asChild>
        <IconButton label={text ? `Notifications, ${text} new` : "Notifications"} className="relative shrink-0">
          <BellIcon />
          {text && (
            <span
              className="num absolute -right-0.5 -top-0.5 flex h-4 min-w-4 items-center justify-center rounded-full bg-crit px-1 text-[0.625rem] font-bold leading-none text-surface ring-2 ring-bg"
              aria-hidden="true"
            >
              {text}
            </span>
          )}
        </IconButton>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          side="bottom"
          align="end"
          sideOffset={6}
          collisionPadding={12}
          ref={contentRef}
          // Focus the panel itself, not its first button, so opening shows no focus ring.
          onOpenAutoFocus={(e) => {
            e.preventDefault();
            contentRef.current?.focus();
          }}
          aria-labelledby="notifications-title"
          // Phones: the screen width less equal 12 px gaps (collisionPadding), so it sits centered; wider screens: 24rem under the bell.
          className="z-50 flex max-h-[calc(100dvh-4.5rem-env(safe-area-inset-top)-env(safe-area-inset-bottom))] w-[calc(100vw-1.5rem)] flex-col sm:w-96 rounded-lg border border-border-strong bg-surface shadow-2xl focus:outline-none"
        >
          <Panel data={data} loading={isLoading} error={error} onClose={() => setOpen(false)} />
          <Popover.Arrow asChild width={16} height={8}>
            {/* Two sides only, so the pointer's outline joins the panel's border without a line across it. */}
            <svg viewBox="0 0 16 8" className="-translate-y-px overflow-visible">
              <path d="M0 0 L8 8 L16 0" className="fill-surface stroke-border-strong" />
            </svg>
          </Popover.Arrow>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
