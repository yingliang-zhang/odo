import { Component, type ErrorInfo, type ReactNode } from "react";
import { Button } from "./ui/button";

interface Props {
  children: ReactNode;
}

interface State {
  error: Error | null;
}

// Root boundary: a render crash anywhere in the tree previously unmounted
// the entire app to a white screen. Catch it, show a reload affordance,
// and keep the stack in the console for the developer.
export default class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    console.error("odo gui: uncaught render error", error, info.componentStack);
  }

  render(): ReactNode {
    const { error } = this.state;
    if (error) {
      return (
        <div className="error-boundary" role="alert">
          <h1>Something went wrong</h1>
          <p className="error-boundary-message">{error.message}</p>
          <Button type="button" variant="default" size="md" onClick={() => window.location.reload()}>
            Reload
          </Button>
        </div>
      );
    }
    return this.props.children;
  }
}
