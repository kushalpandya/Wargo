// SPDX-License-Identifier: AGPL-3.0-only
// Puts the demo credentials on the sign-in page (above the password form),
// with a button that fills them in. Added from outside the app so Wargo's
// own sign-in page stays unchanged; re-added whenever the page re-renders.

const ID = "wargo-demo-login-hint";

/** Sets a React-controlled input's value the way typing would. */
function type(input: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  setter.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

function hint(form: HTMLFormElement) {
  const el = document.createElement("div");
  el.id = ID;
  el.setAttribute("role", "note");
  el.className = "mb-3 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-md border border-frost/25 bg-frost-soft px-3 py-2.5 text-sm text-fg";
  el.innerHTML = `
    <span class="min-w-0 flex-1">This is a demo with made-up data. Sign in with username <strong class="num font-semibold">demo</strong> and password <strong class="num font-semibold">demo</strong>.</span>
    <button type="button" class="inline-flex h-7 shrink-0 items-center rounded-md border border-frost/30 bg-surface px-2.5 text-xs font-medium text-frost hover:border-frost/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus/25">Fill in for me</button>`;
  el.querySelector("button")!.addEventListener("click", () => {
    const user = form.querySelector<HTMLInputElement>("#username");
    const pass = form.querySelector<HTMLInputElement>("#password");
    if (user) type(user, "demo");
    if (pass) type(pass, "demo");
    form.querySelector<HTMLButtonElement>('button[type="submit"]')?.focus();
  });
  return el;
}

function place() {
  const form = document.querySelector<HTMLInputElement>("#username")?.closest("form");
  if (!form || document.getElementById(ID)) return;
  form.parentElement?.insertBefore(hint(form), form);
}

export function installLoginHint() {
  new MutationObserver(place).observe(document.body, { childList: true, subtree: true });
  place();
}
