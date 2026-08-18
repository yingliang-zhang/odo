import { Popover as RadixPopover } from "radix-ui";
import { cn } from "../../lib/utils";

/**
 * Popover — Radix UI wrapper styled with Odo tokens via Tailwind.
 * Replaces hand-rolled bg-runs-menu, QueueDock popover, TopBar overflow.
 *
 * Esc gate contract (tri-model 3/3): onEscapeKeyDown stops propagation.
 */

export const Popover = RadixPopover.Root;
export const PopoverTrigger = RadixPopover.Trigger;
export const PopoverPortal = RadixPopover.Portal;

export function PopoverContent({
  className,
  ...props
}: React.ComponentProps<typeof RadixPopover.Content>) {
  return (
    <RadixPopover.Portal>
      <RadixPopover.Content
        onEscapeKeyDown={(e) => e.stopPropagation()}
        className={cn(
          "min-w-[160px] bg-[var(--bg-elevated)] border border-[var(--border)]",
          "rounded-[var(--radius-md)] p-1 shadow-[var(--shadow-panel)]",
          "z-[200] animate-[fade-in_0.12s_ease-out]",
          className,
        )}
        {...props}
      />
    </RadixPopover.Portal>
  );
}
