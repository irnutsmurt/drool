import type { ButtonHTMLAttributes } from "react";
import { cx } from "./cx";

type Variant = "primary" | "secondary" | "danger" | "ghost";

const variants: Record<Variant, string> = {
  primary: "bg-accent text-accent-fg hover:opacity-90",
  secondary: "bg-surface text-fg border border-line hover:bg-surface-2",
  danger: "bg-danger text-danger-fg hover:opacity-90",
  ghost: "text-fg hover:bg-surface-2",
};

export function Button({
  variant = "secondary",
  size = "md",
  className,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant; size?: "sm" | "md" }) {
  return (
    <button
      className={cx(
        "inline-flex items-center justify-center gap-1.5 rounded-ui font-medium transition disabled:cursor-not-allowed disabled:opacity-50",
        size === "sm" ? "h-7 px-2.5 text-xs" : "h-9 px-3.5 text-sm",
        variants[variant],
        className,
      )}
      {...props}
    />
  );
}

/** Link styles matching the buttons, for navigation that looks like an action. */
export const primaryLink = "inline-flex h-9 items-center justify-center rounded-ui bg-accent px-3.5 text-sm font-medium text-accent-fg hover:opacity-90";
export const secondaryLink = "inline-flex h-9 items-center justify-center rounded-ui border border-line bg-surface px-3.5 text-sm font-medium text-fg hover:bg-surface-2";
export const smallSecondaryLink = "inline-flex h-7 items-center justify-center rounded-ui border border-line bg-surface px-2.5 text-xs font-medium text-fg hover:bg-surface-2";
