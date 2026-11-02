// SPDX-License-Identifier: AGPL-3.0-only
import { toast } from "@/components/ui/toast";

/**
 * Copies text to the clipboard. Uses the async Clipboard API when available
 * (secure contexts) and falls back to a hidden textarea + execCommand, which
 * still works on plain HTTP when called from a user gesture. Returns false
 * when both fail so callers can offer a manual copy instead.
 */
export async function copyText(text: string): Promise<boolean> {
  if (window.isSecureContext && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      /* denied: try the fallback */
    }
  }
  try {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.setAttribute("readonly", "");
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand("copy");
    ta.remove();
    return ok;
  } catch {
    return false;
  }
}

/** copyText with a success/failure toast. */
export async function copyWithToast(text: string, ok = "Copied to clipboard") {
  if (await copyText(text)) toast.ok(ok);
  else toast.error("Clipboard unavailable", "Select the text and copy it manually.");
}
