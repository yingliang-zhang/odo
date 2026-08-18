import { ContextMenu as RadixContextMenu } from "radix-ui";
import { cn } from "../../lib/utils";

/**
 * ContextMenu — Radix UI wrapper styled with Odo tokens via Tailwind.
 * Replaces hand-rolled WorkstreamContextMenu, ProjectContextMenu, FileRefContextMenu.
 *
 * Esc gate contract (tri-model 3/3): onEscapeKeyDown stops propagation
 * so App's global Esc handler doesn't fire when closing this menu.
 */

export const ContextMenu = RadixContextMenu.Root;
export const ContextMenuTrigger = RadixContextMenu.Trigger;
export const ContextMenuPortal = RadixContextMenu.Portal;

export function ContextMenuContent({
  className,
  ...props
}: React.ComponentProps<typeof RadixContextMenu.Content>) {
  return (
    <RadixContextMenu.Portal>
      <RadixContextMenu.Content
        onEscapeKeyDown={(e) => e.stopPropagation()}
        className={cn(
          "min-w-[160px] bg-[var(--bg-elevated)] border border-[var(--border)]",
          "rounded-[var(--radius-md)] p-1 shadow-[var(--shadow-panel)]",
          "z-[200] animate-[fade-in_0.12s_ease-out]",
          className,
        )}
        {...props}
      />
    </RadixContextMenu.Portal>
  );
}

export function ContextMenuItem({
  className,
  ...props
}: React.ComponentProps<typeof RadixContextMenu.Item>) {
  return (
    <RadixContextMenu.Item
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

export function ContextMenuSeparator({
  className,
  ...props
}: React.ComponentProps<typeof RadixContextMenu.Separator>) {
  return (
    <RadixContextMenu.Separator
      className={cn("my-1 h-px bg-[var(--border)]", className)}
      {...props}
    />
  );
}
