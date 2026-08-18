import { Dialog as RadixDialog } from "radix-ui";
import { cn } from "../../lib/utils";

/**
 * Dialog — Radix UI wrapper styled with Odo tokens via Tailwind.
 * Replaces hand-rolled overlay + focusTrap in SettingsPanel, FilePreview, lightbox.
 *
 * Esc gate contract (tri-model 3/3): onEscapeKeyDown stops propagation
 * so App's global Esc handler doesn't fire when closing this dialog.
 * Focus trap is built into Radix Dialog — the old hand-rolled trap hook is gone.
 */

export const Dialog = RadixDialog.Root;
export const DialogTrigger = RadixDialog.Trigger;
export const DialogPortal = RadixDialog.Portal;

export function DialogOverlay({
  className,
  ...props
}: React.ComponentProps<typeof RadixDialog.Overlay>) {
  return (
    <RadixDialog.Overlay
      className={cn(
        "fixed inset-0 bg-black/50 backdrop-blur-[4px]",
        "z-[100] animate-[fade-in_0.16s_ease-out]",
        className,
      )}
      {...props}
    />
  );
}

export function DialogContent({
  className,
  children,
  ...props
}: React.ComponentProps<typeof RadixDialog.Content>) {
  return (
    <RadixDialog.Portal>
      <DialogOverlay />
      <RadixDialog.Content
        onEscapeKeyDown={(e) => e.stopPropagation()}
        className={cn(
          "fixed left-1/2 top-1/2 -translate-x-1/2 -translate-y-1/2",
          "bg-[var(--bg-raised)] border border-[var(--stroke-primary)]",
          "rounded-[14px] shadow-[var(--shadow-panel)]",
          "z-[101] p-6 max-h-[calc(100vh-64px)] overflow-auto",
          "animate-[sheet-in_0.18s_var(--ease-out)]",
          className,
        )}
        {...props}
      >
        {children}
      </RadixDialog.Content>
    </RadixDialog.Portal>
  );
}

export function DialogTitle({
  className,
  ...props
}: React.ComponentProps<typeof RadixDialog.Title>) {
  return (
    <RadixDialog.Title
      className={cn("text-[var(--text)] font-semibold text-base mb-4", className)}
      {...props}
    />
  );
}

export function DialogDescription({
  className,
  ...props
}: React.ComponentProps<typeof RadixDialog.Description>) {
  return (
    <RadixDialog.Description
      className={cn("text-[var(--text-dim)] text-sm mb-4", className)}
      {...props}
    />
  );
}

export function DialogClose({
  className,
  ...props
}: React.ComponentProps<typeof RadixDialog.Close>) {
  return (
    <RadixDialog.Close
      className={cn("cursor-pointer", className)}
      {...props}
    />
  );
}
