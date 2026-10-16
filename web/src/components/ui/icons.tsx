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

// The sidebar's destinations: one simple outline each, so a returning user
// can find an area by shape as well as by name.
export const HomeIcon = (p: SVGProps<SVGSVGElement>) => <Icon {...p}><path d="M3 11.5 12 4l9 7.5" /><path d="M5.5 10v10h13V10" /></Icon>;
export const ServerIcon = (p: SVGProps<SVGSVGElement>) => <Icon {...p}><rect x="3" y="4" width="18" height="7" rx="2" /><rect x="3" y="13" width="18" height="7" rx="2" /><path d="M7 7.5h.01M7 16.5h.01" /></Icon>;
export const PackIcon = (p: SVGProps<SVGSVGElement>) => <Icon {...p}><rect x="3" y="3" width="7.5" height="7.5" rx="1.5" /><rect x="13.5" y="3" width="7.5" height="7.5" rx="1.5" /><rect x="3" y="13.5" width="7.5" height="7.5" rx="1.5" /><rect x="13.5" y="13.5" width="7.5" height="7.5" rx="1.5" /></Icon>;
export const ChipIcon = (p: SVGProps<SVGSVGElement>) => <Icon {...p}><rect x="6" y="6" width="12" height="12" rx="2" /><path d="M10 2.5V6M14 2.5V6M10 18v3.5M14 18v3.5M2.5 10H6M2.5 14H6M18 10h3.5M18 14h3.5" /></Icon>;
export const PulseIcon = (p: SVGProps<SVGSVGElement>) => <Icon {...p}><path d="M3 12h4l3-7 4 14 3-7h4" /></Icon>;
export const BellIcon = (p: SVGProps<SVGSVGElement>) => <Icon {...p}><path d="M6 16v-5a6 6 0 0 1 12 0v5l1.5 2h-15z" /><path d="M10 20.5a2 2 0 0 0 4 0" /></Icon>;
export const NetworkIcon = (p: SVGProps<SVGSVGElement>) => <Icon {...p}><circle cx="6" cy="6" r="2.5" /><circle cx="18" cy="6" r="2.5" /><circle cx="12" cy="18" r="2.5" /><path d="M8 7.5 10.8 16M16 7.5 13.2 16M8.5 6h7" /></Icon>;
export const PlayIcon = (p: SVGProps<SVGSVGElement>) => <Icon {...p}><path d="M7 4.5 19 12 7 19.5z" /></Icon>;
export const ClockIcon = (p: SVGProps<SVGSVGElement>) => <Icon {...p}><circle cx="12" cy="12" r="9" /><path d="M12 7v5l3.5 2.5" /></Icon>;
export const StepsIcon = (p: SVGProps<SVGSVGElement>) => <Icon {...p}><circle cx="5" cy="6" r="1.5" /><circle cx="5" cy="12" r="1.5" /><circle cx="5" cy="18" r="1.5" /><path d="M10 6h10M10 12h10M10 18h10" /></Icon>;
export const TargetIcon = (p: SVGProps<SVGSVGElement>) => <Icon {...p}><circle cx="12" cy="12" r="9" /><circle cx="12" cy="12" r="5" /><circle cx="12" cy="12" r="1" /></Icon>;
export const WallIcon = (p: SVGProps<SVGSVGElement>) => <Icon {...p}><rect x="3" y="5" width="18" height="14" rx="1.5" /><path d="M3 9.7h18M3 14.3h18M9 5v4.7M15 9.7v4.6M9 14.3V19" /></Icon>;
export const KeyIcon = (p: SVGProps<SVGSVGElement>) => <Icon {...p}><circle cx="8" cy="15" r="4" /><path d="m10.8 12.2 8.7-8.7M16.5 6.5l2 2M14.3 8.7l2 2" /></Icon>;
export const LogIcon = (p: SVGProps<SVGSVGElement>) => <Icon {...p}><path d="M6 3h8.5L19 7.5V21H6z" /><path d="M9 11h7M9 15h7M9 7.5h3" /></Icon>;
export const GearIcon = (p: SVGProps<SVGSVGElement>) => <Icon {...p}><circle cx="12" cy="12" r="3.2" /><path d="M12 2.5v3M12 18.5v3M2.5 12h3M18.5 12h3M5.3 5.3l2.1 2.1M16.6 16.6l2.1 2.1M5.3 18.7l2.1-2.1M16.6 7.4l2.1-2.1" /></Icon>;
