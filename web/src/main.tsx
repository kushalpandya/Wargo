// SPDX-License-Identifier: AGPL-3.0-only
import { StrictMode, useSyncExternalStore } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "react-router";
import { ApiError, isHostBlocked, onHostBlocked } from "@/lib/api";
import { events } from "@/lib/ws";
import { HostBlocked } from "@/app/HostBlocked";
import { router } from "@/app/routes";
import { ElevationProvider } from "@/app/Elevation";
import { Toaster } from "@/components/ui/toast";
import { TooltipProvider } from "@/components/ui/menu";
import { ErrorBoundary } from "@/components/ui/error-boundary";
import "@/styles/index.css";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: (n, err) => !isHostBlocked() && !(err instanceof ApiError && err.status < 500) && n < 2,
      refetchOnWindowFocus: false,
    },
    // Mutation state (variables like passwords or API keys, results like
    // TOTP secrets) is dropped as soon as no component observes it.
    mutations: { gcTime: 0 },
  },
});

onHostBlocked(() => {
  // Stop everything that would keep hitting the server from this origin.
  events.stop();
  queryClient.cancelQueries();
});

function App() {
  const blocked = useSyncExternalStore(onHostBlocked, isHostBlocked);
  if (blocked) return <HostBlocked />;
  return (
    <QueryClientProvider client={queryClient}>
      <TooltipProvider>
        <ErrorBoundary className="min-h-dvh">
          <RouterProvider router={router} />
        </ErrorBoundary>
        <ElevationProvider />
        <Toaster />
      </TooltipProvider>
    </QueryClientProvider>
  );
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
