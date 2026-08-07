import { useEffect, useState, useCallback } from "react";
import { BookMarked, Plus, Pencil, X } from "lucide-react";
import { listSkills, readSkill, updateSkill, errorMessage } from "../api";
import type { SkillInfo } from "../types";
import LoadingInline from "./LoadingInline";

// M8 (Skills): skills panel — list, preview, create, edit.
// Skills are markdown files in ~/.odo/skills/ (global) and .odo/skills/
// (project-local). The daemon scans and keyword-matches them for prompt
// injection. This panel is the human-in-the-loop write path: the user
// creates and edits skills here; the daemon never auto-writes skills.

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

  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      const resp = await listSkills(projectRoot ?? undefined);
      setSkills(resp.skills ?? []);
      setError(null);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setLoading(false);
    }
  }, [projectRoot]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const selectSkill = async (skill: SkillInfo) => {
    setSelected(skill);
    setEditing(false);
    setContentLoading(true);
    try {
      const resp = await readSkill(skill.path, projectRoot ?? undefined);
      setContent(resp.skill_content ?? "");
    } catch {
      setContent("");
    } finally {
      setContentLoading(false);
    }
  };

  const startEdit = () => {
    setEditText(content);
    setEditing(true);
  };

  const startCreate = () => {
    setSelected(null);
    setContent("");
    setEditText("---\nname: new-skill\ndescription: Use when ...\nkeywords: []\norigin: human\n---\n\n# New Skill\n\nDescribe the procedure here.\n");
    setEditing(true);
  };

  const saveSkill = async () => {
    setSaving(true);
    try {
      // Extract name from frontmatter for new skills
      const nameMatch = editText.match(/^name:\s*(.+)$/m);
      const name = nameMatch?.[1]?.trim() ?? "untitled";
      // Pass scope: use selected skill's scope if editing, default to project for new
      const scope = selected?.scope ?? "project";
      await updateSkill(name, editText, scope, selected?.path, projectRoot ?? undefined);
      setEditing(false);
      setContent(editText);
      if (selected) {
        setSelected({ ...selected, name });
      }
      await refresh();
      setError(null);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setSaving(false);
    }
  };

  if (loading) return <LoadingInline />;
  if (error) return <div className="panel-empty">{error}</div>;

  return (
    <div className="skills-panel">
      <div className="skills-list">
        <div className="skills-list-head">
          <span className="skills-count">{skills.length} skill{skills.length !== 1 ? "s" : ""}</span>
          <button
            type="button"
            className="skills-add-btn"
            title="Create a new skill"
            onClick={startCreate}
          >
            <Plus size={12} /> New
          </button>
        </div>
        {skills.length === 0 ? (
          <div className="skills-empty">
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
              className={`skill-row${selected?.name === s.name ? " active" : ""}`}
              onClick={() => void selectSkill(s)}
            >
              <span className={`skill-scope-dot scope-${s.scope}`} title={s.scope} />
              <div className="skill-row-info">
                <div className="skill-row-name">{s.name}</div>
                {s.description && (
                  <div className="skill-row-desc">{s.description}</div>
                )}
              </div>
              {s.keywords && s.keywords.length > 0 && (
                <div className="skill-row-kws">
                  {s.keywords.slice(0, 3).map((k) => (
                    <span key={k} className="skill-kw">{k}</span>
                  ))}
                </div>
              )}
            </button>
          ))
        )}
      </div>

      {editing ? (
        <div className="skill-editor">
          <div className="skill-editor-head">
            <span>{selected ? `Editing: ${selected.name}` : "New skill"}</span>
            <div>
              <button
                type="button"
                className="skill-save-btn"
                disabled={saving}
                onClick={() => void saveSkill()}
              >
                {saving ? "Saving…" : "Save"}
              </button>
              <button
                type="button"
                className="skill-cancel-btn"
                disabled={saving}
                onClick={() => {
                  setEditing(false);
                  if (selected) setContent(editText);
                }}
              >
                <X size={12} />
              </button>
            </div>
          </div>
          <textarea
            className="skill-editor-textarea"
            value={editText}
            onChange={(e) => setEditText(e.target.value)}
            spellCheck={false}
            autoFocus
          />
        </div>
      ) : selected ? (
        <div className="skill-preview">
          <div className="skill-preview-head">
            <BookMarked size={12} />
            <span className="skill-preview-name">{selected.name}</span>
            <button
              type="button"
              className="skill-edit-btn"
              title="Edit this skill"
              onClick={startEdit}
            >
              <Pencil size={11} /> Edit
            </button>
          </div>
          <div className="skill-meta">
            {selected.scope === "project" ? (
              <span className="skill-scope-tag project">project</span>
            ) : (
              <span className="skill-scope-tag global">global</span>
            )}
            {selected.origin !== "human" && (
              <span className="skill-origin-tag">{selected.origin}</span>
            )}
            {selected.keywords && selected.keywords.length > 0 && (
              <span className="skill-kw-list">
                {selected.keywords.map((k) => (
                  <span key={k} className="skill-kw">{k}</span>
                ))}
              </span>
            )}
          </div>
          {contentLoading ? (
            <LoadingInline />
          ) : (
            <pre className="skill-body">{content}</pre>
          )}
        </div>
      ) : (
        <div className="skill-preview">
          <div className="panel-empty">
            Select a skill to preview, or click <strong>New</strong> to create one.
          </div>
        </div>
      )}
    </div>
  );
}
