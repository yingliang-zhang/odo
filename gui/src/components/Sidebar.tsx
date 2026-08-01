import type { Conversation, Project, Workstream } from "../types";

interface Props {
  project: Project | null;
  workstream: Workstream | null;
  conversationId: Conversation["id"] | null;
}

// Minimal M0 chrome: which project/workstream this window is bound to.
export default function Sidebar({ project, workstream, conversationId }: Props) {
  return (
    <aside className="sidebar">
      <h1 className="sidebar-app">Odo</h1>
      <dl className="sidebar-facts">
        <dt>Project</dt>
        <dd>{project?.name ?? "—"}</dd>
        <dt>Root</dt>
        <dd className="mono truncate" title={project?.root_path ?? ""}>
          {project?.root_path ?? "—"}
        </dd>
        <dt>Workstream</dt>
        <dd>
          {workstream?.name ?? "—"}
          {workstream?.status ? <span className="dim"> ({workstream.status})</span> : null}
        </dd>
        <dt>Conversation</dt>
        <dd>{conversationId != null ? `#${conversationId}` : "—"}</dd>
      </dl>
    </aside>
  );
}
