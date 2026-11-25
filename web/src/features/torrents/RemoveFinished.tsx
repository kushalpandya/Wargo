// SPDX-License-Identifier: AGPL-3.0-only
import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { AlertTriangle } from "lucide-react";
import { cn } from "@/lib/cn";
import { useCapabilities } from "@/app/session";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { Switch } from "@/components/ui/form";
import { ErrorNote } from "@/components/ui/misc";
import { toast } from "@/components/ui/toast";
import { errorMessage, fetchTorrents, torrentsKey, useSaveTransmissionSettings, useTransmissionSettings } from "./api";
import { confirmPlan, countFinished } from "./finished";

const LABEL = "Remove torrents when they finish";
const HELP = "Finished torrents are taken off Transmission so they stop seeding. Their files are kept. Torrents that are already finished are removed when you turn this on.";

/**
 * The "Remove torrents when they finish" switch, with a heads-up when turning it on would remove torrents at once.
 * `compact` draws it as a small control for the Downloads heading row; otherwise it is a settings row with its help text.
 */
export function RemoveFinishedSwitch({ available, compact }: { available: boolean; compact?: boolean }) {
  const qc = useQueryClient();
  const caps = useCapabilities().data;
  const settings = useTransmissionSettings();
  const save = useSaveTransmissionSettings();
  const [confirming, setConfirming] = useState(false);
  const [checking, setChecking] = useState(false);
  // The dialog body for turning removal on, worked out from a fresh count each time.
  const [prompt, setPrompt] = useState("");
  // When the setting could not be read, the switch is disabled and shows what the capabilities say, if anything,
  // so it never reads "off" while removal may be on.
  const failed = settings.isError && !settings.data;
  const stored = settings.data?.removeFinished ?? caps?.transmission?.removeFinished ?? false;
  // While a save is in flight show the value being saved; on failure that falls back to the stored one.
  const checked = save.isPending ? save.variables.removeFinished : stored;
  const disabled = settings.isLoading || failed || save.isPending || checking;
  const failure = failed ? `Could not read this setting: ${errorMessage(settings.error)}` : "";

  const commit = (removeFinished: boolean) =>
    save.mutate(
      { removeFinished },
      {
        onSuccess: () => setConfirming(false),
        onError: (e) => {
          setConfirming(false);
          toast.error("Could not save", errorMessage(e));
        },
      },
    );

  const change = async (on: boolean) => {
    if (!on) return commit(false);
    // Never rely on a cached list: the server removes whatever is finished within a second of saving.
    let n: number | null = null;
    if (available) {
      setChecking(true);
      try {
        // staleTime 0 always fetches; an error throws rather than handing back a cached list.
        const p = await qc.fetchQuery({ queryKey: torrentsKey, queryFn: fetchTorrents, staleTime: 0, retry: false });
        n = countFinished(p.torrents);
      } catch {
        n = null;
      }
      setChecking(false);
    }
    // An unknown count fails closed: ask, with the generic text.
    const plan = confirmPlan(n);
    if ("save" in plan) return commit(true);
    setPrompt(plan.confirm);
    setConfirming(true);
  };

  const dialog = (
    <Dialog
      open={confirming}
      onOpenChange={(o) => !o && !save.isPending && setConfirming(false)}
      size="sm"
      title="Remove finished torrents?"
      description={prompt}
      footer={
        <>
          <Button autoFocus disabled={save.isPending} onClick={() => setConfirming(false)}>
            Cancel
          </Button>
          <Button variant="primary" loading={save.isPending} onClick={() => commit(true)}>
            Turn on
          </Button>
        </>
      }
    />
  );

  if (compact)
    return (
      <>
        {/* Same height and edge as the small Add button beside it; clicking the text flips the switch. */}
        <label
          title={failure || HELP}
          className={cn(
            "inline-flex h-8 shrink-0 select-none items-center gap-2 rounded-md border border-border bg-surface px-3 text-ui font-medium text-fg transition-colors",
            disabled ? "cursor-default" : "cursor-pointer hover:border-border-strong",
          )}
        >
          {/* The visible words start the accessible name, so voice control finds it by what it says. */}
          Remove finished
          {failed && (
            <>
              <AlertTriangle aria-hidden="true" className="size-3.5 shrink-0 text-warn" />
              <span className="sr-only">{failure}</span>
            </>
          )}
          <Switch checked={checked} disabled={disabled} onCheckedChange={change} />
        </label>
        {dialog}
      </>
    );

  return (
    <div className="border-t border-border p-4">
      <Switch checked={checked} disabled={disabled} onCheckedChange={change} label={LABEL} description={HELP} />
      {failed && <ErrorNote error={failure} className="mt-3" />}
      {dialog}
    </div>
  );
}
