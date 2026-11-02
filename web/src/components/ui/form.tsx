// SPDX-License-Identifier: AGPL-3.0-only
import { forwardRef, useId, type InputHTMLAttributes, type ReactNode, type SelectHTMLAttributes } from "react";
import { Switch as S, Checkbox as C } from "radix-ui";
import { Check, Search, X } from "lucide-react";
import { cn } from "@/lib/cn";

const inputBase =
  "h-8 w-full min-w-0 rounded-md border border-border bg-surface-2 px-2.5 text-sm text-fg placeholder:text-subtle " +
  "transition-colors hover:border-border-strong focus:border-focus focus:outline-none focus-visible:outline-none focus:ring-2 focus:ring-focus/25 " +
  "disabled:opacity-50";

export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(function Input({ className, ...rest }, ref) {
  return <input ref={ref} className={cn(inputBase, className)} {...rest} />;
});

export const Select = forwardRef<HTMLSelectElement, SelectHTMLAttributes<HTMLSelectElement>>(function Select(
  { className, children, ...rest },
  ref,
) {
  return (
    <select ref={ref} className={cn(inputBase, "appearance-none bg-[length:12px] bg-[right_0.5rem_center] bg-no-repeat pr-7", className)} style={{ backgroundImage: chevron }} {...rest}>
      {children}
    </select>
  );
});

const chevron =
  "url(\"data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 12 12'%3E%3Cpath d='M3 4.5l3 3 3-3' fill='none' stroke='%238b96a3' stroke-width='1.5'/%3E%3C/svg%3E\")";

/** Label + control + hint wrapper. */
export function FormField({
  label,
  hint,
  error,
  children,
  htmlFor,
  className,
}: {
  label: ReactNode;
  hint?: ReactNode;
  error?: ReactNode;
  children: ReactNode;
  htmlFor?: string;
  className?: string;
}) {
  return (
    <div className={cn("flex flex-col gap-1", className)}>
      <label htmlFor={htmlFor} className="text-[0.8125rem] font-medium text-fg">
        {label}
      </label>
      {children}
      {error ? <p className="text-xs text-crit">{error}</p> : hint ? <p className="text-xs text-muted">{hint}</p> : null}
    </div>
  );
}

export function SearchInput({
  value,
  onChange,
  placeholder = "Search…",
  className,
  autoFocus,
  label,
}: {
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  className?: string;
  autoFocus?: boolean;
  label?: string;
}) {
  return (
    <div className={cn("relative", className)}>
      <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-subtle" />
      <input
        type="search"
        value={value}
        autoFocus={autoFocus}
        aria-label={label ?? placeholder}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        className={cn(inputBase, "pl-8 pr-7 [&::-webkit-search-cancel-button]:hidden")}
      />
      {value && (
        <button
          type="button"
          aria-label="Clear search"
          onClick={() => onChange("")}
          className="absolute right-1.5 top-1/2 -translate-y-1/2 rounded p-0.5 text-subtle hover:text-fg"
        >
          <X className="size-3.5" />
        </button>
      )}
    </div>
  );
}

export function Switch({
  checked,
  onCheckedChange,
  label,
  description,
  disabled,
}: {
  checked: boolean;
  onCheckedChange: (v: boolean) => void;
  label?: ReactNode;
  description?: ReactNode;
  disabled?: boolean;
}) {
  const id = useId();
  const control = (
    <S.Root
      id={id}
      checked={checked}
      onCheckedChange={onCheckedChange}
      disabled={disabled}
      className="relative h-5 w-9 shrink-0 rounded-full border border-border-strong bg-surface-3 transition-colors data-[state=checked]:border-accent data-[state=checked]:bg-accent disabled:opacity-50 pointer-coarse:h-6 pointer-coarse:w-11"
    >
      <S.Thumb className="block size-3.5 translate-x-0.5 rounded-full bg-fg shadow transition-transform data-[state=checked]:translate-x-[18px] data-[state=checked]:bg-accent-fg pointer-coarse:size-4.5 pointer-coarse:data-[state=checked]:translate-x-[22px]" />
    </S.Root>
  );
  if (!label) return control;
  return (
    <div className="flex items-start justify-between gap-4">
      <label htmlFor={id} className="min-w-0 cursor-pointer">
        <span className="block text-sm font-medium">{label}</span>
        {description && <span className="mt-0.5 block text-xs text-muted">{description}</span>}
      </label>
      {control}
    </div>
  );
}

export function Checkbox({
  checked,
  onCheckedChange,
  label,
  className,
  "aria-label": ariaLabel,
}: {
  checked: boolean | "indeterminate";
  onCheckedChange: (v: boolean) => void;
  label?: ReactNode;
  className?: string;
  "aria-label"?: string;
}) {
  const id = useId();
  return (
    <span className={cn("inline-flex items-center gap-2", className)}>
      <C.Root
        id={id}
        checked={checked}
        aria-label={ariaLabel}
        onCheckedChange={(v) => onCheckedChange(v === true)}
        className="flex size-4 shrink-0 items-center justify-center rounded border border-border-strong bg-surface-2 data-[state=checked]:border-accent data-[state=checked]:bg-accent data-[state=indeterminate]:border-accent data-[state=indeterminate]:bg-accent"
      >
        <C.Indicator className="text-accent-fg">
          {checked === "indeterminate" ? <span className="block h-0.5 w-2 bg-current" /> : <Check className="size-3" strokeWidth={3} />}
        </C.Indicator>
      </C.Root>
      {label && (
        <label htmlFor={id} className="cursor-pointer text-sm">
          {label}
        </label>
      )}
    </span>
  );
}

/** Segmented control (radio group semantics). */
export function Segmented<T extends string>({
  value,
  onChange,
  options,
  label,
  size = "md",
}: {
  value: T;
  onChange: (v: T) => void;
  options: { value: T; label: ReactNode; title?: string }[];
  label: string;
  size?: "sm" | "md";
}) {
  return (
    <div role="radiogroup" aria-label={label} className="inline-flex rounded-md border border-border bg-surface-2 p-0.5">
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          role="radio"
          aria-checked={value === o.value}
          title={o.title}
          onClick={() => onChange(o.value)}
          className={cn(
            "inline-flex items-center gap-1.5 rounded-[5px] px-2.5 font-medium transition-colors [&_svg]:size-3.5",
            size === "sm" ? "h-6 text-xs" : "h-7 text-[0.8125rem]",
            value === o.value ? "bg-surface text-fg shadow-sm ring-1 ring-border" : "text-muted hover:text-fg",
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}
