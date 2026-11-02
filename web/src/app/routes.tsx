// SPDX-License-Identifier: AGPL-3.0-only
import { lazy, useEffect } from "react";
import { createBrowserRouter, Navigate, Outlet, useNavigate } from "react-router";
import { setUnauthorizedHandler } from "@/lib/api";
import { events } from "@/lib/ws";
import { Spinner } from "@/components/ui/misc";
import { useAuthState, useMe } from "./session";
import { Layout } from "./Layout";
import { Login } from "@/features/auth/Login";
import { TotpSetupPage } from "@/features/auth/TotpSetup";

const Home = lazy(() => import("@/features/home/Home"));
const System = lazy(() => import("@/features/system/System"));
const Containers = lazy(() => import("@/features/containers/Containers"));
const Terminal = lazy(() => import("@/features/terminal/Terminal"));
const Files = lazy(() => import("@/features/files/Files"));
const Updates = lazy(() => import("@/features/updates/Updates"));
const Torrents = lazy(() => import("@/features/torrents/Torrents"));
const Apps = lazy(() => import("@/features/launcher/Apps"));
const Settings = lazy(() => import("@/features/settings/Settings"));

function FullPageSpinner() {
  return (
    <div className="flex min-h-dvh items-center justify-center text-muted">
      <Spinner />
    </div>
  );
}

function RequireAuth() {
  const state = useAuthState();
  const me = useMe(!!state.data?.authenticated);
  const nav = useNavigate();

  useEffect(() => {
    setUnauthorizedHandler(() => nav("/login", { replace: true }));
    return () => setUnauthorizedHandler(null);
  }, [nav]);

  const authed = !!state.data?.authenticated;
  useEffect(() => {
    if (authed) events.start();
    return () => events.stop();
  }, [authed]);

  if (state.isLoading || (authed && me.isLoading)) return <FullPageSpinner />;
  if (!authed) return <Navigate to="/login" replace />;
  if (me.data?.totpSetupRequired) return <Navigate to="/setup/2fa" replace />;
  return <Outlet />;
}

function PublicOnly() {
  const state = useAuthState();
  if (state.isLoading) return <FullPageSpinner />;
  if (state.data?.authenticated) return <Navigate to="/" replace />;
  return <Outlet />;
}

export const router = createBrowserRouter([
  {
    element: <PublicOnly />,
    children: [{ path: "/login", element: <Login /> }],
  },
  { path: "/setup/2fa", element: <TotpSetupPage /> },
  {
    element: <RequireAuth />,
    children: [
      {
        element: <Layout />,
        children: [
          { index: true, element: <Home /> },
          { path: "system", element: <System /> },
          { path: "apps", element: <Apps /> },
          { path: "processes", element: <Navigate to="/system" replace /> },
          { path: "containers", element: <Containers /> },
          { path: "containers/:id", element: <Containers /> },
          { path: "terminal", element: <Terminal /> },
          { path: "files/*", element: <Files /> },
          { path: "updates", element: <Updates /> },
          { path: "torrents", element: <Torrents /> },
          { path: "torrents/search", element: <Navigate to="/torrents" replace /> },
          { path: "torrents/downloads", element: <Navigate to="/torrents?tab=downloads" replace /> },
          { path: "settings/*", element: <Settings /> },
          { path: "*", element: <Navigate to="/" replace /> },
        ],
      },
    ],
  },
]);
