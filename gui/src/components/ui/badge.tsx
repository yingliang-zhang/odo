import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "../../lib/utils";

/**
 * Badge — shared outcome/verdict pill (CVA), replacing the .badge/.badge-*
 * and .verdict-badge/.verdict-* rule families.
 *
 * Consumers pass their legacy class names via className as inert DOM hooks
 * for e2e selectors, e.g. className="badge badge-accept" or
 * className="verdict-badge verdict-needs_fixes capitalize".
 *
 * LedgerPanel still relies on the legacy .badge/.badge-accept/.badge-reject/
 * .badge-other CSS rules (it needs blocked/refresh/actor variants this
 * component intentionally doesn't cover), so those four rules stay in
 * app.css until LedgerPanel migrates. With the marker classes present, the
 * legacy rules harmlessly overlap this component's utilities (app.css wins
 * ties on identical values) — same rendering today, one style once the
 * rules are finally deleted.
 */
const badgeVariants = cva(
  "inline-block rounded-[10px] px-2.5 py-0.5 text-xs font-medium",
  {
    variants: {
      variant: {
        accept: "bg-ok/18 text-ok-text",
        reject: "bg-err/15 text-err-text",
        needs_fixes: "bg-warn/12 text-warn",
        other: "bg-bg-raised text-text-dim border border-border",
      },
    },
    defaultVariants: {
      variant: "other",
    },
  },
);

export interface BadgeProps
  extends React.HTMLAttributes<HTMLSpanElement>,
    VariantProps<typeof badgeVariants> {}

export function Badge({ className, variant, ...props }: BadgeProps) {
  return (
    <span className={cn(badgeVariants({ variant }), className)} {...props} />
  );
}

export { badgeVariants };
