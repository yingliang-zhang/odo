// P5: user-facing string consolidation — typed, NOT i18n. Components read
// strings.<section>.<key> instead of inline literals so a future locale pass
// has exactly one place to swap. Sections map to surfaces; only the migrated
// subset exists — add keys as migration continues, never up front.
export interface Strings {
  common: {
    cancel: string;
  };
  composer: {
    send: string;
    steer: string;
    park: string;
    stop: string;
    stopTitle: string;
    messageInputLabel: string;
    placeholderIdle: string;
    placeholderRunning: string;
    placeholderDrop: string;
    parkToggleLabel: string;
    parkToggleTitleArmed: string;
    parkToggleTitleDisarmed: string;
    atMenuLabel: string;
  };
  banner: {
    daemonDown: string;
    daemonRestart: string;
    daemonRestartTitle: string;
  };
  sidebar: {
    newWorkstream: string;
    newWorkstreamTitle: string;
    workstreamNamePlaceholder: string;
    rename: string;
    delete: string;
    confirmDeleteTitle: string;
    confirmRemoveTitle: string;
    cancelDeleteLabel: string;
    cancelRemoveLabel: string;
    removeProjectTitle: string;
    expandSidebar: string;
    expandSidebarTitle: string;
    collapseSidebar: string;
    collapseSidebarTitle: string;
    newProject: string;
    newProjectTitle: string;
    statusRunning: string;
    statusBackground: string;
    statusPending: string;
    statusIdle: string;
  };
  statusbar: {
    promptCompositionLabel: string;
    reviewPanelLabel: string;
    reviewPanelReadonlyTitle: string;
    overflowLabel: string;
    overflowTitle: string;
  };
  steerQueue: {
    title: (n: number) => string;
    activeLabel: string;
    activeJoined: (n: number) => string;
    drop: string;
    dropConfirm: string;
    dropTitle: string;
  };
}

export const en: Strings = {
  common: {
    cancel: "Cancel",
  },
  composer: {
    send: "Send",
    steer: "Steer",
    park: "Park",
    stop: "Stop",
    stopTitle: "Stop the running agent (Esc)",
    messageInputLabel: "Message input",
    placeholderIdle: "Describe the change you want…",
    placeholderRunning: "Steer the running agent… (Esc stops)",
    placeholderDrop: "Drop files to attach them…",
    parkToggleLabel: "Park: queue this goal for later",
    parkToggleTitleArmed: "Parked — this goal queues for later",
    parkToggleTitleDisarmed: "Park: queue this goal for later",
    atMenuLabel: "Mention completions",
  },
  banner: {
    daemonDown: "Daemon connection lost — retrying…",
    daemonRestart: "Restart daemon",
    daemonRestartTitle: "Reload the app — bootstrap respawns the daemon if its socket is dead",
  },
  sidebar: {
    newWorkstream: "+ New workstream",
    newWorkstreamTitle: "New workstream (⌘N)",
    workstreamNamePlaceholder: "workstream name",
    rename: "Rename",
    delete: "Delete",
    confirmDeleteTitle: "Confirm delete",
    confirmRemoveTitle: "Confirm removal",
    cancelDeleteLabel: "Cancel delete",
    cancelRemoveLabel: "Cancel remove",
    removeProjectTitle: "Remove from project list (registry only; files untouched)",
    expandSidebar: "Expand sidebar",
    expandSidebarTitle: "Expand sidebar (⌘B)",
    collapseSidebar: "Collapse sidebar",
    collapseSidebarTitle: "Collapse (⌘B)",
    newProject: "New",
    newProjectTitle: "New project",
    statusRunning: "Running",
    statusBackground: "Running in background",
    statusPending: "Pending review",
    statusIdle: "Idle",
  },
  statusbar: {
    promptCompositionLabel: "Prompt composition",
    reviewPanelLabel: "Review panel",
    reviewPanelReadonlyTitle: "review panel — read-only (⌘, to change)",
    overflowLabel: "Hidden status items",
    overflowTitle: "hidden by overflow — live values; rows navigate",
  },
  steerQueue: {
    title: (n) => (n === 1 ? "Queued steer · 1" : `Queued steers · ${n}`),
    activeLabel: "Processing",
    activeJoined: (n) => `Processing ${n} queued steers`,
    drop: "Drop",
    dropConfirm: "Drop?",
    dropTitle: "Drop this queued steer — it will not reach the agent",
  },
};

export const strings = en;
