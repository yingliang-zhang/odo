import { DropdownMenu as RadixDropdownMenu } from "radix-ui";
import { cn } from "../../lib/utils";

/**
 * DropdownMenu — Radix UI wrapper styled with Odo tokens via Tailwind.
 * Replaces hand-rolled model-pill-menu and similar dropdown menus.
 *
 * Esc gate contract (tri-model 3/3): onEscapeKeyDown stops propagation
 * so App's global Esc handler doesn't fire when closing this menu.
 */

export const DropdownMenu = RadixDropdownMenu.Root;
export const DropdownMenuTrigger = RadixDropdownMenu.Trigger;
export const DropdownMenuPortal = RadixDropdownMenu.Portal;

export function DropdownMenuContent({
  className,
  ...props
}: React.ComponentProps<typeof RadixDropdownMenu.Content>) {
  return (
    <RadixDropdownMenu.Portal>
      <RadixDropdownMenu.Content
        onEscapeKeyDown={(e) => e.stopPropagation()}
        className={cn(
          "min-w-[160px] bg-[var(--bg-elevated)] border border-[var(--border)]",
          "rounded-[var(--radius-md)] p-1 shadow-[var(--shadow-panel)]",
          "z-[200] animate-[fade-in_0.12s_ease-out]",
          className,
        )}
        {...props}
      />
    </RadixDropdownMenu.Portal>
  );
}

export function DropdownMenuItem({
  className,
  ...props
}: React.ComponentProps<typeof RadixDropdownMenu.Item>) {
  return (
    <RadixDropdownMenu.Item
      className={cn(
        "flex items-center gap-2 w-full px-2 py-1.5 text-xs text-[var(--text)]",
        "rounded-[var(--radius-sm)] cursor-pointer text-left",
        "outline-none data-[highlighted]:bg-[var(--bg-hover)]",
        className,
      )}
      {...props}
    />
  );
}

export function DropdownMenuSeparator({
  className,
  ...props
}: React.ComponentProps<typeof RadixDropdownMenu.Separator>) {
  return (
    <RadixDropdownMenu.Separator
      className={cn("my-1 h-px bg-[var(--border)]", className)}
      {...props}
    />
  );
}
