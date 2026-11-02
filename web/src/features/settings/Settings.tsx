// SPDX-License-Identifier: AGPL-3.0-only
import { Suspense, lazy, type ReactNode } from "react";
import { Navigate, NavLink, useNavigate, useParams } from "react-router";
import { Info, LayoutGrid, Palette, Plug, ScrollText, ShieldCheck } from "lucide-react";
import { cn } from "@/lib/cn";
import { Select } from "@/components/ui/form";
import { Spinner } from "@/components/ui/misc";

const Appearance = lazy(() => import("./Appearance"));
const Security = lazy(() => import("./Security"));
const Integrations = lazy(() => import("./Integrations"));
const Launcher = lazy(() => import("./LauncherSettings"));
const Audit = lazy(() => import("./Audit"));
const About = lazy(() => import("./About"));

interface Section {
  id: string;
  label: string;
  icon: ReactNode;
  el: ReactNode;
}

const sections: Section[] = [
  { id: "appearance", label: "Appearance", icon: <Palette />, el: <Appearance /> },
  { id: "security", label: "Security", icon: <ShieldCheck />, el: <Security /> },
  { id: "integrations", label: "Integrations", icon: <Plug />, el: <Integrations /> },
  { id: "launcher", label: "App launcher", icon: <LayoutGrid />, el: <Launcher /> },
  { id: "audit", label: "Audit log", icon: <ScrollText />, el: <Audit /> },
  { id: "about", label: "About", icon: <Info />, el: <About /> },
];

export default function Settings() {
  const params = useParams();
  const nav = useNavigate();
  const id = (params["*"] ?? "").split("/")[0];
  const current = sections.find((s) => s.id === id);
  if (!current) return <Navigate to="/settings/appearance" replace />;

  return (
    <div className="mx-auto max-w-6xl">
      <h1 className="mb-4 text-lg font-semibold tracking-tight">Settings</h1>
      <div className="flex flex-col gap-4 md:flex-row md:gap-6">
        <div className="md:hidden">
          <label htmlFor="settings-section" className="sr-only">
            Settings section
          </label>
          <Select id="settings-section" value={current.id} onChange={(e) => nav(`/settings/${e.target.value}`)} className="h-9">
            {sections.map((s) => (
              <option key={s.id} value={s.id}>
                {s.label}
              </option>
            ))}
          </Select>
        </div>
        <nav aria-label="Settings" className="hidden w-48 shrink-0 md:block">
          <ul className="sticky top-16 flex flex-col gap-0.5">
            {sections.map((s) => (
              <li key={s.id}>
                <NavLink
                  to={`/settings/${s.id}`}
                  className={({ isActive }) =>
                    cn(
                      "flex h-8 items-center gap-2.5 rounded-md px-2.5 text-[0.8125rem] font-medium transition-colors [&_svg]:size-4",
                      isActive ? "bg-surface-3 text-fg [&_svg]:text-accent" : "text-muted hover:bg-surface-2 hover:text-fg [&_svg]:text-subtle",
                    )
                  }
                >
                  {s.icon}
                  {s.label}
                </NavLink>
              </li>
            ))}
          </ul>
        </nav>
        <div className="min-w-0 flex-1">
          <Suspense
            fallback={
              <div className="flex h-40 items-center justify-center text-muted">
                <Spinner />
              </div>
            }
          >
            {current.el}
          </Suspense>
        </div>
      </div>
    </div>
  );
}
