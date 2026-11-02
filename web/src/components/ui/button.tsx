// SPDX-License-Identifier: AGPL-3.0-only
import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from "react";
import { Slot } from "radix-ui";
import { cn } from "@/lib/cn";
import { Spinner } from "./misc";

type Variant = "primary" | "secondary" | "ghost" | "danger" | "outline-danger";
type Size = "xs" | "sm" | "md";

const variants: Record<Variant, string> = {
  primary: "bg-accent text-accent-fg hover:bg-accent-strong border-transparent font-medium",
  secondary: "bg-surface-2 text-fg border-border hover:bg-surface-3 hover:border-border-strong",
  ghost: "bg-transparent text-muted border-transparent hover:bg-surface-3 hover:text-fg",
  danger: "bg-crit text-white border-transparent hover:brightness-110 font-medium",
  "outline-danger": "bg-transparent text-crit border-border hover:bg-crit-soft hover:border-crit/40",
};

const sizes: Record<Size, string> = {
  xs: "h-6 px-2 text-xs gap-1 rounded-[5px]",
  sm: "h-7 px-2.5 text-[0.8125rem] gap-1.5 rounded-md",
  md: "h-8 px-3 text-sm gap-2 rounded-md",
};

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant;
  size?: Size;
  loading?: boolean;
  icon?: ReactNode;
  /** Render the child element (e.g. a router Link) with button styling. */
  asChild?: boolean;
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = "secondary", size = "md", loading, icon, className, children, disabled, type = "button", asChild, ...rest },
  ref,
) {
  if (asChild) {
    return (
      <Slot.Root
        ref={ref}
        className={cn(
          "inline-flex shrink-0 select-none items-center justify-center whitespace-nowrap border transition-colors [&_svg]:size-[1.1em] [&_svg]:shrink-0",
          variants[variant],
          sizes[size],
          className,
        )}
        {...rest}
      >
        {children}
      </Slot.Root>
    );
  }
  return (
    <button
      ref={ref}
      type={type}
      disabled={disabled || loading}
      className={cn(
        "inline-flex shrink-0 select-none items-center justify-center whitespace-nowrap border transition-colors",
        "disabled:pointer-events-none disabled:opacity-50 [&_svg]:size-[1.1em] [&_svg]:shrink-0",
        variants[variant],
        sizes[size],
        className,
      )}
      {...rest}
    >
      {loading ? <Spinner className="size-[1.1em]" /> : icon}
      {children}
    </button>
  );
});

export interface IconButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  label: string;
  size?: Size;
  variant?: Variant;
}

/** Square icon-only button; `label` is required for accessibility. */
export const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(function IconButton(
  { label, size = "md", variant = "ghost", className, children, type = "button", ...rest },
  ref,
) {
  const dim = size === "xs" ? "size-6" : size === "sm" ? "size-7" : "size-8";
  return (
    <button
      ref={ref}
      type={type}
      aria-label={label}
      title={label}
      className={cn(
        "inline-flex shrink-0 items-center justify-center rounded-md border transition-colors",
        "disabled:pointer-events-none disabled:opacity-40 [&_svg]:size-4",
        variants[variant],
        dim,
        className,
      )}
      {...rest}
    >
      {children}
    </button>
  );
});
