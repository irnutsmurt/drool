import type { SVGProps } from "react";

// A few line icons, drawn inline (no icon library). Decorative: callers
// give the control an accessible name.

function Icon({ children, ...p }: SVGProps<SVGSVGElement>) {
  return (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round"
      strokeLinejoin="round" aria-hidden="true" focusable="false" {...p}>
      {children}
    </svg>
  );
}

export const MenuIcon = (p: SVGProps<SVGSVGElement>) => <Icon {...p}><path d="M4 6h16M4 12h16M4 18h16" /></Icon>;
export const SearchIcon = (p: SVGProps<SVGSVGElement>) => <Icon {...p}><circle cx="11" cy="11" r="7" /><path d="m20 20-3.5-3.5" /></Icon>;
export const CloseIcon = (p: SVGProps<SVGSVGElement>) => <Icon {...p}><path d="M6 6l12 12M18 6 6 18" /></Icon>;
export const ShieldIcon = (p: SVGProps<SVGSVGElement>) => <Icon {...p}><path d="M12 3 5 6v5c0 4.5 3 8.3 7 10 4-1.7 7-5.5 7-10V6l-7-3z" /><path d="M12 8v4M12 16h.01" /></Icon>;
export const ChevronDownIcon = (p: SVGProps<SVGSVGElement>) => <Icon {...p}><path d="m6 9 6 6 6-6" /></Icon>;
export const ArrowRightIcon = (p: SVGProps<SVGSVGElement>) => <Icon {...p}><path d="M5 12h14M13 6l6 6-6 6" /></Icon>;
