package ipc

// M12 D-budget: the prompt budget registry. Every byte cap the send and
// slash paths can inject into one prompt is enumerated here — name, owning
// constant, default bytes, clamp-max bytes (the ceiling a prefs-clamped
// value can reach), and the injection path(s) it feeds. The registry is
// documentation the compiler checks: PromptBudgets references the owning
// constants directly, so a cap change cannot drift from the ledger.
//
// The bound test (budgets_test.go) sums PER PATH: the send path and the
// slash path are alternative pipelines (a slash prompt never carries the
// send path's replay/recall stack), so a single additive Σ across both
// paths would charge bytes no prompt ever carries. Per path:
//   Σ defaults   ≤  48 KB (soft bound — today's effective send stack)
//   Σ clamp-max  ≤ 128 KB (hard bound — the worst a prefs edit can ship)
// The replay rows are summed pessimistically: replay_turn nests inside
// replay_total, but both count toward the Σ so a raise of either cap is
// forced through this ledger.

// Prompt-path identifiers used by PromptBudget.Paths.
const (
	budgetPathSend  = "send"
	budgetPathSlash = "slash"
)

// memoryMapAllowance is the measured byte allowance for the generated R2
// read-back block (memoryMapBlock). The block is static prose plus two
// absolute paths; measured 907 bytes with a ~50-char project root
// (2026-08-10), so 1 KB covers realistic root lengths. The block is not
// prefs-clamped: default == clamp-max.
const memoryMapAllowance = 1024

// PromptBudget is one row of the budget registry.
type PromptBudget struct {
	Name          string   // registry entry, e.g. "user_memory"
	Constant      string   // owning Go symbol (documentation column)
	Layer         string   // ADR-0003 memory layer name
	Paths         []string // injection pipelines the row feeds
	DefaultBytes  int      // cap when prefs.md sets nothing
	ClampMaxBytes int      // ceiling after prefs clamping (== default when not prefs-configurable)
}

// PromptBudgets is the full registry, in send-path injection order (ADR-0003
// inv 6), slash-only rows last.
var PromptBudgets = []PromptBudget{
	{"user_memory", "userMemoryCap", "user", []string{budgetPathSend, budgetPathSlash}, userMemoryCap, userMemoryCap},
	{"project_memory", "memoryCap", "project", []string{budgetPathSend, budgetPathSlash}, memoryCap, memoryCap},
	{"pins", "pinsCap", "pins", []string{budgetPathSend, budgetPathSlash}, pinsCap, pinsCap},
	{"skills", "skillsInjectionCap", "skills", []string{budgetPathSend}, skillsInjectionCap, skillsInjectionCap},
	{"wiki_index", "indexCap", "index", []string{budgetPathSend, budgetPathSlash}, indexCap, indexCap},
	{"recall_notes", "recallMemoryCap", "recall", []string{budgetPathSend}, recallMemoryCap, recallMemoryCap},
	{"memory_map", "memoryMapAllowance", "memory_map", []string{budgetPathSend}, memoryMapAllowance, memoryMapAllowance},
	{"resume_card", "resumeCardCap", "resume", []string{budgetPathSend}, resumeCardCap, resumeCardCap},
	{"replay_total", "replayTotalCapDefault/replayTotalKBMax", "replay", []string{budgetPathSend}, replayTotalCapDefault, replayTotalKBMax * 1024},
	{"replay_turn", "replayTurnCapDefault/replayTurnKBMax", "replay", []string{budgetPathSend}, replayTurnCapDefault, replayTurnKBMax * 1024},
	{"slash_recall", "slashRecallCap", "recall", []string{budgetPathSlash}, slashRecallCap, slashRecallCap},
	{"slash_conversation", "slashConvCap", "conversation", []string{budgetPathSlash}, slashConvCap, slashConvCap},
}
