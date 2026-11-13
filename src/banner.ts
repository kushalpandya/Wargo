// SPDX-License-Identifier: AGPL-3.0-only
// A "Demo" badge next to the Wargo wordmark (sidebar and mobile menu), where
// Wargo itself shows its "Dev" badge, so nobody mistakes the demo for a real
// server. It opens a small card with what the demo is, Reset and GitHub.
// Added from outside the app, like the sign-in hint, and re-added whenever
// the app re-renders; the sign-in page has its own hint (login-hint.ts).
import { resetDemo } from "./core/store";

const BADGE = "wargo-demo-badge";
const CARD = "wargo-demo-card";

const linkClass =
  "inline-flex h-7 shrink-0 items-center whitespace-nowrap rounded-md px-2.5 text-xs font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus/25";

function closeCard() {
  document.getElementById(CARD)?.remove();
  document.querySelectorAll(`.${BADGE}`).forEach((b) => b.setAttribute("aria-expanded", "false"));
}

function openCard(badge: HTMLButtonElement) {
  closeCard();
  const card = document.createElement("div");
  card.id = CARD;
  card.setAttribute("role", "dialog");
  card.setAttribute("aria-label", "About this demo");
  card.className = "fixed z-[70] w-72 max-w-[calc(100vw-1.5rem)] rounded-lg border border-border-strong bg-surface p-3 text-sm shadow-2xl";
  card.innerHTML = `
    <p class="font-semibold text-fg">This is a demo</p>
    <p class="mt-1 text-xs leading-relaxed text-muted">Everything here is made up and runs in your browser. Nothing touches a real server, and your changes stay on this device.</p>
    <div class="mt-3 flex items-center justify-between gap-2">
      <button type="button" class="${linkClass} border border-border bg-surface-2 text-fg hover:border-border-strong">Reset demo</button>
      <a href="https://github.com/kushalpandya/wargo" target="_blank" rel="noopener noreferrer" class="${linkClass} text-accent hover:bg-accent-soft">Wargo on GitHub</a>
    </div>`;
  card.querySelector("button")!.addEventListener("click", () => {
    if (confirm("Reset the demo? Every change you made here is forgotten.")) resetDemo();
  });
  // Inside the mobile menu, stay within it so its focus trap and outside-click
  // handling treat the card as part of the menu.
  (badge.closest('[role="dialog"]') ?? document.body).appendChild(card);
  const r = badge.getBoundingClientRect();
  // Phones: centered on the screen; wider screens: under the badge.
  const phone = matchMedia("(max-width: 767px)").matches;
  const left = phone ? (innerWidth - card.offsetWidth) / 2 : Math.max(12, Math.min(r.left, innerWidth - card.offsetWidth - 12));
  card.style.top = `${r.bottom + 6}px`;
  card.style.left = `${left}px`;
  badge.setAttribute("aria-expanded", "true");
  card.querySelector<HTMLElement>("button")!.focus();
}

function makeBadge() {
  const b = document.createElement("button");
  b.type = "button";
  b.className = `${BADGE} inline-flex h-6 shrink-0 items-center whitespace-nowrap rounded-md border border-accent/25 bg-accent-soft px-2 text-2xs font-semibold text-accent hover:border-accent/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus/25`;
  b.textContent = "Demo";
  b.setAttribute("aria-haspopup", "dialog");
  b.setAttribute("aria-expanded", "false");
  b.title = "About this demo";
  b.addEventListener("click", (e) => {
    e.stopPropagation();
    if (b.getAttribute("aria-expanded") === "true") closeCard();
    else openCard(b);
  });
  return b;
}

/** The Wargo wordmarks on screen: the sidebar's (when expanded) and the mobile menu's. */
function wordmarks() {
  return [...document.querySelectorAll<HTMLSpanElement>("span.font-bold")].filter((s) => s.textContent === "Wargo" && s.closest('aside, [role="dialog"]'));
}

function place() {
  const marks = wordmarks();
  // A collapsed sidebar drops its wordmark: drop the badge with it.
  document.querySelectorAll<HTMLElement>(`.${BADGE}`).forEach((b) => {
    if (!marks.includes(b.previousElementSibling as HTMLSpanElement)) b.remove();
  });
  for (const m of marks) {
    if (!m.nextElementSibling?.classList.contains(BADGE)) m.after(makeBadge());
  }
  const card = document.getElementById(CARD);
  if (card && !document.querySelector(`.${BADGE}[aria-expanded="true"]`)) card.remove();
}

export function mountBanner() {
  new MutationObserver(place).observe(document.body, { childList: true, subtree: true });
  place();
  addEventListener("pointerdown", (e) => {
    const t = e.target as Element;
    if (!t.closest(`#${CARD}, .${BADGE}`)) closeCard();
  });
  addEventListener("keydown", (e) => {
    if (e.key === "Escape" && document.getElementById(CARD)) {
      const badge = document.querySelector<HTMLElement>(`.${BADGE}[aria-expanded="true"]`);
      closeCard();
      badge?.focus();
      e.stopPropagation();
    }
  }, true);
  addEventListener("resize", closeCard);
}
