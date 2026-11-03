// SPDX-License-Identifier: AGPL-3.0-only
import { Suspense, lazy, type ReactNode } from "react";
import { Navigate, NavLink, useNavigate, useParams } from "react-router";
import { Info, Palette, Plug, ShieldCheck, SlidersHorizontal } from "lucide-react";
import { cn } from "@/lib/cn";
import { Select } from "@/components/ui/form";
import { PageHeader, Spinner } from "@/components/ui/misc";

const General = lazy(() => import("./General"));
const Appearance = lazy(() => import("./Appearance"));
const Security = lazy(() => import("./Security"));
const Integrations = lazy(() => import("./Integrations"));
const About = lazy(() => import("./About"));

interface Section {
  id: string;
  label: string;
  icon: ReactNode;
  el: ReactNode;
}

const sections: Section[] = [
  { id: "general", label: "General", icon: <SlidersHorizontal />, el: <General /> },
  { id: "appearance", label: "Appearance", icon: <Palette />, el: <Appearance /> },
  { id: "integrations", label: "Integrations", icon: <Plug />, el: <Integrations /> },
  { id: "security", label: "Security", icon: <ShieldCheck />, el: <Security /> },
  { id: "about", label: "About", icon: <Info />, el: <About /> },
];

/** Older section links that moved. */
const moved: Record<string, string> = { launcher: "/settings/general", audit: "/settings/about#audit" };

export default function Settings() {
  const params = useParams();
  const nav = useNavigate();
  const id = (params["*"] ?? "").split("/")[0];
  const current = sections.find((s) => s.id === id);
  if (!current) return <Navigate to={moved[id] ?? "/settings/general"} replace />;

  return (
    <div className="min-w-0">
      <PageHeader title={current.label} parent={{ label: "Settings", to: "/settings" }} />
      <div className="flex flex-col gap-4 md:flex-row md:gap-8">
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
        <nav aria-label="Settings" className="hidden w-52 shrink-0 md:block">
          <ul className="sticky top-16 flex flex-col gap-0.5">
            {sections.map((s) => (
              <li key={s.id}>
                <NavLink
                  to={`/settings/${s.id}`}
                  className={({ isActive }) =>
                    cn(
                      "flex h-9 items-center gap-3 rounded-md px-3 text-ui font-medium transition-colors [&_svg]:size-[1.125rem]",
                      isActive ? "bg-accent-soft font-semibold text-fg [&_svg]:text-accent" : "text-muted hover:bg-surface-3 hover:text-fg [&_svg]:text-subtle",
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
