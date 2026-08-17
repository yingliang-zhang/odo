import { Component, type ErrorInfo, type ReactNode } from "react";

// Tri-model review Item 2: a per-run-group error boundary so one
// malformed event (cyclic result, unknown payload shape, parse
// overflow) degrades locally instead of blanking the entire chat
// surface. Hermes wraps every message in a boundary; Odo had zero
// ErrorBoundary/componentDidCatch matches in the codebase.
//
// The boundary's resetKey is the group's structural identity (start
// seq + event count), NOT per-token — per-token keys measured 540
// wasted Block renders in Hermes's own profiling. When the
// conversation changes, the resetKey changes, re-mounting the
// boundary clean.

interface Props {
  children: ReactNode;
  // Changing this key resets the boundary (re-mounts children clean).
  resetKey: string;
  // Context-specific suffix for the fallback message.
  // Default: "other runs are unaffected" (chat context).
  // ContextPanel passes "other tabs are unaffected".
  fallbackNote?: string;
}

interface State {
  hasError: boolean;
  error?: Error;
}

export default class RunGroupBoundary extends Component<Props, State> {
  state: State = { hasError: false };

  static getDerivedStateFromError(error: Error): State {
    return { hasError: true, error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // Log to the console — the daemon journal is not touched (pure GUI).
    console.error("RunGroupBoundary caught:", error, info.componentStack);
  }

  // Reset when the resetKey changes (conversation switch, new events).
  componentDidUpdate(prevProps: Props) {
    if (prevProps.resetKey !== this.props.resetKey && this.state.hasError) {
      this.setState({ hasError: false, error: undefined });
    }
  }

  render() {
    if (this.state.hasError) {
      // Render a degraded fallback showing the error, so the rest of the
      // UI stays visible and the user can still interact.
      const note = this.props.fallbackNote ?? "other runs are unaffected";
      return (
        <div className="bubble bubble-error run-boundary-fallback">
          <span className="bubble-icon">⚠</span>{" "}
          <details>
            <summary>This section failed to render — {note}</summary>
            <pre>{this.state.error?.message ?? "Unknown error"}</pre>
          </details>
        </div>
      );
    }
    return this.props.children;
  }
}
