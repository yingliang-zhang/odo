import { useEffect, useRef, useState, useCallback } from "react";
import { BookMarked, Plus, Pencil, Trash2, X } from "lucide-react";
import { listSkills, readSkill, updateSkill, deleteSkill, errorMessage } from "../api";
import type { SkillInfo } from "../types";
import LoadingInline from "./LoadingInline";
import { Button } from "./ui/button";
import { cn } from "../lib/utils";

// M8 (Skills): skills panel — list, preview, create, edit, delete.
// Skills are markdown files in ~/.odo/skills/ (global) and .odo/skills/
// (project-local). The daemon scans and keyword-matches them for prompt
// injection. This panel is the human-in-the-loop write path: the user
// creates, edits, and deletes skills here; the daemon never auto-writes.

interface Props {
  projectRoot?: string | null;
}

export default function SkillsPanel({ projectRoot }: Props) {
  const [skills, setSkills] = useState<SkillInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<SkillInfo | null>(null);
  const [content, setContent] = useState<string>("");
  const [contentLoading, setContentLoading] = useState(false);
  const [editing, setEditing] = useState(false);
  const [editText, setEditText] = useState("");
  const [saving, setSaving] = useState(false);
  const [newScope, setNewScope] = useState<"project" | "global">("project");
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const [deleting, setDeleting] = useState(false);
  // Unmount guard: every async path below checks this before touching
  // state so a late resolution can't setState on a dead component.
  const mountedRef = useRef(true);
  // Mirrored in MemoryPanel: setup body resets the flag so StrictMode's
  // double-invoke (setup → cleanup → setup) leaves it true for the second
  // pass; a cleanup-only guard would stay false and starve every load.
  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
  }, []);

  const refresh = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const resp = await listSkills(projectRoot ?? undefined);
      if (!mountedRef.current) return;
      setSkills(resp.skills ?? []);
      setError(null);
    } catch (e) {
      if (mountedRef.current) setError(errorMessage(e));
    } finally {
      if (mountedRef.current) setLoading(false);
    }
  }, [projectRoot]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const selectSkill = async (skill: SkillInfo) => {
    setSelected(skill);
    setEditing(false);
    setConfirmingDelete(false);
    setContentLoading(true);
    try {
      const resp = await readSkill(skill.path, projectRoot ?? undefined);
      if (!mountedRef.current) return;
      setContent(resp.skill_content ?? "");
    } catch {
      if (mountedRef.current) setContent("");
    } finally {
      if (mountedRef.current) setContentLoading(false);
    }
  };

  const startEdit = () => {
    setEditText(content);
    setEditing(true);
    setConfirmingDelete(false);
  };

  const startCreate = () => {
    setSelected(null);
    setContent("");
    setNewScope("project");
    setEditText("---\nname: new-skill\ndescription: Use when ...\nkeywords: []\norigin: human\n---\n\n# New Skill\n\nDescribe the procedure here.\n");
    setEditing(true);
    setConfirmingDelete(false);
  };

  const saveSkill = async () => {
    setSaving(true);
    try {
      const nameMatch = editText.match(/^name:\s*(.+)$/m);
      const name = nameMatch?.[1]?.trim() ?? "untitled";
      const scope = selected?.scope ?? newScope;
      await updateSkill(name, editText, scope, selected?.path, projectRoot ?? undefined);
      if (!mountedRef.current) return;
      setEditing(false);
      setContent(editText);
      if (selected) {
        setSelected({ ...selected, name });
      }
      await refresh();
      if (!mountedRef.current) return;
      setError(null);
    } catch (e) {
      if (mountedRef.current) setError(errorMessage(e));
    } finally {
      if (mountedRef.current) setSaving(false);
    }
  };

  const confirmDelete = async () => {
    if (!selected) return;
    setDeleting(true);
    try {
      await deleteSkill(selected.name, selected.scope, projectRoot ?? undefined);
      if (!mountedRef.current) return;
      setConfirmingDelete(false);
      setSelected(null);
      setContent("");
      await refresh();
      if (!mountedRef.current) return;
      setError(null);
    } catch (e) {
      if (mountedRef.current) setError(errorMessage(e));
    } finally {
      if (mountedRef.current) setDeleting(false);
    }
  };

  if (loading) return <LoadingInline />;

  return (
    <div className="skills-panel flex h-full flex-col">
      {error && (
        <div className="skill-error-banner flex items-center justify-between gap-2 border-b border-[rgba(239,68,68,0.3)] bg-[rgba(239,68,68,0.12)] px-2.5 py-1.5 text-[length:var(--text-caption)] text-[var(--err-surface-text)]">
          {error}
          <button type="button" className="skill-error-dismiss cursor-pointer border-0 bg-transparent px-1 py-0 text-[length:var(--text-caption)] leading-none text-[var(--err-surface-text)]" onClick={() => setError(null)}>✕</button>
        </div>
      )}
      <div className="skills-list flex max-h-[40%] flex-col gap-0.5 overflow-y-auto border-b border-[var(--stroke-tertiary)] pb-2">
        <div className="skills-list-head flex items-center justify-between px-2 pt-1 pb-1.5">
          <span className="skills-count text-[length:var(--text-micro)] uppercase tracking-[0.06em] text-[var(--text-dim)]">{skills.length} skill{skills.length !== 1 ? "s" : ""}</span>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="skills-add-btn gap-1 hover:bg-[var(--bg-raised)] hover:text-[var(--accent-user)]"
            title="Create a new skill"
            onClick={startCreate}
          >
            <Plus size={12} /> New
          </Button>
        </div>
        {skills.length === 0 ? (
          <div className="skills-empty px-4 py-3 text-[length:var(--text-caption)] leading-[1.5] text-[var(--text-dim)]">
            No skills yet. Skills are reusable procedures that the agent loads on demand.
            <br /><br />
            Create one with the <strong>New</strong> button, or drop a <code>.md</code> file
            into <code>~/.odo/skills/</code> or <code>.odo/skills/</code>.
          </div>
        ) : (
          skills.map((s) => (
            <button
              key={`${s.scope}:${s.name}`}
              type="button"
              className={cn(
                "skill-row flex w-full cursor-pointer items-start gap-2 rounded-[var(--radius-sm)] bg-transparent px-2 py-1.5 text-left hover:bg-[var(--bg-raised)]",
                selected?.name === s.name && "active border-l-2 border-[var(--accent-user)] bg-[var(--bg-raised)]",
              )}
              aria-pressed={selected?.name === s.name}
              onClick={() => void selectSkill(s)}
            >
              <span
                className={cn(
                  "skill-scope-dot mt-[5px] h-1.5 w-1.5 shrink-0 rounded-full",
                  `scope-${s.scope}`,
                  s.scope === "project" ? "bg-[#22c55e]" : "bg-[var(--accent-user)]",
                )}
                title={s.scope}
              />
              <div className="skill-row-info min-w-0 flex-1">
                <div className="skill-row-name text-[length:var(--text-body)] font-medium text-[var(--text)]">{s.name}</div>
                {s.description && (
                  <div className="skill-row-desc overflow-hidden text-ellipsis whitespace-nowrap text-[length:var(--text-caption)] text-[var(--text-dim)]">{s.description}</div>
                )}
              </div>
              {s.keywords && s.keywords.length > 0 && (
                <div className="skill-row-kws flex max-w-[30%] shrink-0 flex-wrap justify-end gap-[3px]">
                  {s.keywords.slice(0, 3).map((k) => (
                    <span key={k} className="skill-kw rounded-[var(--radius-sm)] bg-[var(--bg-raised)] px-[5px] py-px text-[length:var(--text-micro)] text-[var(--text-dim)]">{k}</span>
                  ))}
                </div>
              )}
            </button>
          ))
        )}
      </div>

      {editing ? (
        <div className="skill-editor flex flex-1 flex-col overflow-hidden">
          <div className="skill-editor-head flex items-center justify-between border-b border-[var(--stroke-tertiary)] px-2.5 py-2 text-[length:var(--text-caption)] text-[var(--text-dim)]">
            <span>{selected ? `Editing: ${selected.name}` : "New skill"}</span>
            <div className="flex items-center gap-1.5">
              <Button
                type="button"
                variant="default"
                size="sm"
                className="skill-save-btn"
                disabled={saving}
                onClick={() => void saveSkill()}
              >
                {saving ? "Saving…" : "Save"}
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="skill-cancel-btn border-[var(--stroke-secondary)] px-1"
                aria-label="Cancel edit"
                disabled={saving}
                onClick={() => {
                  setEditing(false);
                  if (selected) setContent(editText);
                }}
              >
                <X size={12} />
              </Button>
            </div>
          </div>
          {!selected && (
            <div className="skill-scope-selector flex gap-2 border-b border-[var(--stroke-tertiary)] px-2.5 py-1.5">
              <button
                type="button"
                className={cn(
                  "skill-scope-opt inline-flex cursor-pointer items-center gap-1 rounded-[var(--radius-sm)] border border-[var(--stroke-tertiary)] bg-transparent px-2 py-[3px] text-[length:var(--text-caption)] text-[var(--text-dim)]",
                  newScope === "project" && "active border-[var(--accent-user)] bg-[var(--bg-raised)] text-[var(--text)]",
                )}
                aria-pressed={newScope === "project"}
                onClick={() => setNewScope("project")}
              >
                <span className="skill-scope-dot scope-project mt-[5px] h-1.5 w-1.5 shrink-0 rounded-full bg-[#22c55e]" /> Project
                <span className="skill-scope-path ml-0.5 text-[length:var(--text-micro)] text-[var(--text-dim)]">.odo/skills/</span>
              </button>
              <button
                type="button"
                className={cn(
                  "skill-scope-opt inline-flex cursor-pointer items-center gap-1 rounded-[var(--radius-sm)] border border-[var(--stroke-tertiary)] bg-transparent px-2 py-[3px] text-[length:var(--text-caption)] text-[var(--text-dim)]",
                  newScope === "global" && "active border-[var(--accent-user)] bg-[var(--bg-raised)] text-[var(--text)]",
                )}
                aria-pressed={newScope === "global"}
                onClick={() => setNewScope("global")}
              >
                <span className="skill-scope-dot scope-global mt-[5px] h-1.5 w-1.5 shrink-0 rounded-full bg-[var(--accent-user)]" /> Global
                <span className="skill-scope-path ml-0.5 text-[length:var(--text-micro)] text-[var(--text-dim)]">~/.odo/skills/</span>
              </button>
            </div>
          )}
          <textarea
            className="skill-editor-textarea w-full flex-1 resize-none border-t border-[var(--stroke-tertiary)] bg-[var(--bg)] px-3.5 py-2.5 font-mono text-[length:var(--text-caption)] leading-[1.6] text-[var(--text)] outline-none"
            value={editText}
            onChange={(e) => setEditText(e.target.value)}
            spellCheck={false}
            autoFocus
          />
        </div>
      ) : selected ? (
        <div className="skill-preview flex flex-1 flex-col overflow-hidden">
          <div className="skill-preview-head flex items-center gap-1.5 border-b border-[var(--stroke-tertiary)] px-2.5 py-2">
            <BookMarked size={12} />
            <span className="skill-preview-name flex-1 text-[length:var(--text-title)] font-semibold text-[var(--text)]">{selected.name}</span>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="skill-edit-btn gap-[3px] border-[var(--stroke-secondary)] hover:bg-transparent hover:border-[var(--accent-user)] hover:text-[var(--accent-user)]"
              title="Edit this skill"
              onClick={startEdit}
            >
              <Pencil size={11} /> Edit
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="skill-delete-btn gap-[3px] border-[var(--stroke-secondary)] hover:bg-transparent hover:border-[var(--err-surface-text)] hover:text-[var(--err-surface-text)]"
              title="Delete this skill"
              onClick={() => setConfirmingDelete(true)}
            >
              <Trash2 size={11} /> Delete
            </Button>
          </div>
          {confirmingDelete ? (
            <div className="skill-delete-confirm flex flex-col gap-3 px-3.5 py-4 text-[length:var(--text-caption)] leading-[1.5] text-[var(--text-dim)]">
              <span>Delete <strong>{selected.name}</strong> ({selected.scope})? This cannot be undone.</span>
              <div className="skill-delete-confirm-actions flex gap-2">
                <Button
                  type="button"
                  variant="danger"
                  size="sm"
                  className="skill-delete-confirm-btn"
                  disabled={deleting}
                  onClick={() => void confirmDelete()}
                >
                  {deleting ? "Deleting…" : "Delete"}
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="skill-delete-cancel-btn border-[var(--stroke-secondary)]"
                  disabled={deleting}
                  onClick={() => setConfirmingDelete(false)}
                >
                  Cancel
                </Button>
              </div>
            </div>
          ) : (
            <>
              <div className="skill-meta flex flex-wrap items-center gap-1.5 border-b border-[var(--stroke-tertiary)] px-2.5 py-1">
                {selected.scope === "project" ? (
                  <span className="skill-scope-tag project rounded-[var(--radius-sm)] bg-[rgba(34,197,94,0.15)] px-1.5 py-px text-[length:var(--text-micro)] uppercase tracking-[0.04em] text-[var(--text)]">project</span>
                ) : (
                  <span className="skill-scope-tag global rounded-[var(--radius-sm)] bg-[color-mix(in_srgb,var(--accent-user)_15%,transparent)] px-1.5 py-px text-[length:var(--text-micro)] uppercase tracking-[0.04em] text-[var(--accent-user)]">global</span>
                )}
                {selected.origin !== "human" && (
                  <span className="skill-origin-tag rounded-[var(--radius-sm)] bg-[var(--bg-raised)] px-1.5 py-px text-[length:var(--text-micro)] text-[var(--text-dim)]">{selected.origin}</span>
                )}
                {selected.keywords && selected.keywords.length > 0 && (
                  <span className="skill-kw-list inline-flex flex-wrap gap-[3px]">
                    {selected.keywords.map((k) => (
                      <span key={k} className="skill-kw rounded-[var(--radius-sm)] bg-[var(--bg-raised)] px-[5px] py-px text-[length:var(--text-micro)] text-[var(--text-dim)]">{k}</span>
                    ))}
                  </span>
                )}
              </div>
              {contentLoading ? (
                <LoadingInline />
              ) : (
                <pre className="skill-body flex-1 overflow-y-auto whitespace-pre-wrap px-3.5 py-2.5 font-mono text-[length:var(--text-caption)] leading-[1.6] text-[var(--text)]">{content}</pre>
              )}
            </>
          )}
        </div>
      ) : (
        <div className="skill-preview flex flex-1 flex-col overflow-hidden">
          <div className="panel-empty">
            Select a skill to preview, or click <strong>New</strong> to create one.
          </div>
        </div>
      )}
    </div>
  );
}
