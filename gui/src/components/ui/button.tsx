import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "../../lib/utils";

/**
 * Button — shadcn-pattern button with CVA variants.
 * Replaces all hand-written .settings-save, .btn-accept, .btn-comments etc.
 * Tailwind utilities + Odo tokens via @theme inline.
 */
const buttonVariants = cva(
  "inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-md font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent-user)] disabled:pointer-events-none disabled:opacity-50 cursor-pointer",
  {
    variants: {
      variant: {
        default:
          "bg-[var(--accent-user)] text-white border border-transparent",
        secondary:
          "bg-[var(--bg-input)] text-[var(--text)] border border-[var(--border)]",
        ghost:
          "bg-transparent text-[var(--text-dim)] hover:bg-[var(--bg-hover)] border border-transparent",
        danger:
          "bg-[var(--err)] text-white border border-transparent",
        outline:
          "bg-transparent text-[var(--text)] border border-[var(--border)]",
      },
      size: {
        sm: "h-7 px-3 text-xs",
        md: "h-9 px-4 text-sm",
        lg: "h-10 px-5 text-base",
        icon: "h-9 w-9",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "md",
    },
  },
);

export interface ButtonProps
  extends React.ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {}

export function Button({ className, variant, size, ...props }: ButtonProps) {
  return (
    <button
      className={cn(buttonVariants({ variant, size }), className)}
      {...props}
    />
  );
}

export { buttonVariants };
