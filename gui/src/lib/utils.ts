import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

/**
 * cn — className merge utility (shadcn pattern).
 * Combines clsx (conditional classes) + tailwind-merge (dedupe conflicting TW utilities).
 *
 * Usage: cn("px-2 py-1", isActive && "bg-accent", className)
 */
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
