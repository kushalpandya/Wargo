// SPDX-License-Identifier: AGPL-3.0-only
import { createContext, useContext } from "react";

/**
 * Places in the app's top bar where the current page shows its title (and
 * description) and its main actions, so pages don't spend a row of their
 * own on a heading. PageHeader renders into these with a portal.
 */
export interface PageSlots {
  title: HTMLElement | null;
  actions: HTMLElement | null;
}

export const PageSlotsContext = createContext<PageSlots>({ title: null, actions: null });

export const usePageSlots = () => useContext(PageSlotsContext);
