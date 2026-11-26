// SPDX-License-Identifier: AGPL-3.0-only
import { lazy, Suspense, useEffect } from "react";
import { createBrowserRouter, Navigate, Outlet, useLocation, useNavigate, useRouteError } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { isUnreachable, setUnauthorizedHandler } from "@/lib/api";
import { events } from "@/lib/ws";
import { Spinner } from "@/components/ui/misc";
import { ErrorFallback } from "@/components/ui/error-boundary";
import { useAuthState, useMe, type AuthState } from "./session";
import { Layout } from "./Layout";
import { Unreachable, useAutoRetry } from "./Unreachable";
import { AppSkeleton } from "./AppSkeleton";
import { returnTo } from "./returnTo";

// Sign-in screens are only needed when signed out; keep them out of the main bundle.
const Login = lazy(() => import("@/features/auth/Login").then((m) => ({ default: m.Login })));
const TotpSetupPage = lazy(() => import("@/features/auth/TotpSetup").then((m) => ({ default: m.TotpSetupPage })));
const Home = lazy(() => import("@/features/home/Home"));
const System = lazy(() => import("@/features/system/System"));
const Containers = lazy(() => import("@/features/containers/Containers"));
const Terminal = lazy(() => import("@/features/terminal/Terminal"));
const Files = lazy(() => import("@/features/files/Files"));
const Updates = lazy(() => import("@/features/updates/Updates"));
const Torrents = lazy(() => import("@/features/torrents/Torrents"));
const Settings = lazy(() => import("@/features/settings/Settings"));
const About = lazy(() => import("@/features/about/About"));
const Approve = lazy(() => import("@/features/mcp/Approve"));

/** Errors thrown while rendering a route outside the app shell. */
function RouteError() {
  return (
    <div className="flex min-h-dvh items-center justify-center">
      <ErrorFallback error={useRouteError()} />
    </div>
  );
}

function FullPageSpinner() {
  return (
    <div className="flex min-h-dvh items-center justify-center text-muted">
      <Spinner />
    </div>
  );
}

/** The first auth check never got an answer from Wargo (see app/Unreachable.tsx). */
function useUnreachable(state: ReturnType<typeof useAuthState>) {
  const down = !state.data && isUnreachable(state.error);
  useAutoRetry(down, () => void state.refetch());
  return down ? <Unreachable onRetry={() => void state.refetch()} retrying={state.isFetching} /> : null;
}

function RequireAuth() {
  const state = useAuthState();
  const unreachable = useUnreachable(state);
  const me = useMe(!!state.data?.authenticated);
  const nav = useNavigate();
  const loc = useLocation();

  const qc = useQueryClient();
  useEffect(() => {
    // Session expired or revoked: mark it signed out first, or PublicOnly
    // would see the cached "authenticated" state and bounce straight back.
    setUnauthorizedHandler(() => {
      qc.setQueryData<AuthState>(["auth", "state"], (s) => (s ? { ...s, authenticated: false } : s));
      nav("/login", { replace: true });
    });
    return () => setUnauthorizedHandler(null);
  }, [nav, qc]);

  const authed = !!state.data?.authenticated;
  // Only a fully enrolled session may open /ws/events: one that still has to
  // set up TOTP gets 403 from everything but the enrolment endpoints.
  const ready = authed && !!me.data && !me.data.totpSetupRequired;
  useEffect(() => {
    if (ready) events.start();
    return () => events.stop();
  }, [ready]);

  if (unreachable) return unreachable;
  if (state.isLoading || (authed && me.isLoading)) return <AppSkeleton />;
  // Sign-in brings the browser back here (an approval link opened while signed out).
  if (!authed) return <Navigate to="/login" replace state={{ from: loc.pathname + loc.search + loc.hash }} />;
  if (me.data?.totpSetupRequired) return <Navigate to="/setup/2fa" replace />;
  return <Outlet />;
}

function PublicOnly() {
  const state = useAuthState();
  const unreachable = useUnreachable(state);
  const loc = useLocation();
  if (unreachable) return unreachable;
  if (state.isLoading) return <FullPageSpinner />;
  if (state.data?.authenticated) return <Navigate to={returnTo(loc.state)} replace />;
  return <Outlet />;
}

export const router = createBrowserRouter([
  {
    element: <PublicOnly />,
    errorElement: <RouteError />,
    children: [{ path: "/login", element: <Suspense fallback={<FullPageSpinner />}><Login /></Suspense> }],
  },
  { path: "/setup/2fa", element: <Suspense fallback={<FullPageSpinner />}><TotpSetupPage /></Suspense>, errorElement: <RouteError /> },
  {
    element: <RequireAuth />,
    errorElement: <RouteError />,
    children: [
      {
        element: <Layout />,
        children: [
          { index: true, element: <Home /> },
          { path: "system/*", element: <System /> },
          { path: "containers", element: <Containers /> },
          { path: "containers/:id/:tab?", element: <Containers /> },
          { path: "terminal", element: <Terminal /> },
          { path: "files/*", element: <Files /> },
          { path: "updates/*", element: <Updates /> },
          { path: "torrents", element: <Torrents /> },
          { path: "settings/*", element: <Settings /> },
          { path: "about", element: <About /> },
          { path: "approve/:id", element: <Approve /> },
          { path: "*", element: <Navigate to="/" replace /> },
        ],
      },
    ],
  },
],
  // "/" for Wargo itself; a sub-path when the UI is built for another base
  // (e.g. the demo site under /Wargo/ on GitHub Pages).
  { basename: import.meta.env.BASE_URL.replace(/\/+$/, "") || "/" },
);
