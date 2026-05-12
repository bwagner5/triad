// Package wizardstate is the UI-agnostic state machine that backs both
// the inline CLI wizard (pkg/ui/wizard) and the full-screen TUI wizard
// overlay (pkg/ui/tui). It owns the canonical answer, cursor, choice,
// and visibility state for an in-flight wizard run so the two UIs can
// share behavior and so a future "swap UIs mid-session" handoff can
// preserve every cursor position and pre-fetched choice.
//
// State is deliberately free of bubbletea, widget types, and tea.Cmd —
// callers are expected to keep widget instances (textinput, filepicker)
// in their own model and hydrate them from the State on focus, writing
// back via SetText / SetFilePath / SetSelIdx / ToggleMulti.
//
// The cascading-clear rule: any mutation that could affect a When
// predicate (Commit, CommitValue, SkipOptional, SetSelIdx, ToggleMulti,
// SetText, SetFilePath) re-evaluates visibility for fields strictly
// after the change point and clears entries whose When flipped from
// true to false. Hidden fields' values do not carry forward — matches
// the existing behavior of both UIs at submit time.
//
// Field.When predicates must be cheap and pure: State evaluates them on
// every mutation rather than only on render.
package wizardstate

import (
	"fmt"
	"strings"

	"github.com/bwagner5/triad/pkg/registry"
)

// FieldEntry holds all per-field state. One per field, allocated once
// in New. Widgets re-hydrate from this on focus; State does not hold
// widget instances.
type FieldEntry struct {
	// Text is the text-input value (also used for committed-history
	// rendering, including sensitive masking at display time).
	Text string
	// FilePath is the selected path for File fields.
	FilePath string
	// Choices holds the last loaded Suggest choices (nil if not yet
	// fetched). Survives shift+tab navigation so re-entering a Suggest
	// field does not re-fetch and the cursor lands at the previously
	// picked row.
	Choices []registry.Choice
	// ChoicesErr is the last Suggest fetch error, if any.
	ChoicesErr error
	// SelIdx is the choice cursor for Suggest fields (single or multi).
	SelIdx int
	// MultiSel is the per-choice-idx checkbox state for Multi fields.
	// nil for non-Multi fields.
	MultiSel map[int]bool
	// Loading is true while a Suggest fetch is in flight.
	Loading bool
	// Err is the last validation error to render under the field.
	Err string
	// Committed is true once the user has pressed enter on this field
	// (or it was auto-recommitted from a pre-seeded Input on New).
	// goBack flips this back to false for fields >= the target, but
	// does NOT clear the answer slots — that is the behavior that
	// lets the user shift+tab back to fix a typo.
	Committed bool
}

// HistoryEntry is a denormalized record for the CLI wizard's
// "previously answered" history rendering. DisplayVal is pre-masked
// for sensitive fields so callers do not need to know about masking.
type HistoryEntry struct {
	Idx        int
	Field      registry.Field
	Section    string
	DisplayVal string
}

// State owns all per-field state for one wizard run. Construct with New.
// All methods take pointer receivers; do not copy a State by value.
type State struct {
	fields  []registry.Field
	entries []FieldEntry
	seed    registry.Input // caller-supplied baseline (config/flags/Pre hooks)
	idx     int
}

// New builds a State for fields, seeded from in. Fields with a
// pre-existing in[Flag] are marked Committed so a downstream UI can
// fast-forward through them. Suggest fetches are NOT dispatched here —
// the caller decides when (CLI fetches lazily as the cursor reaches a
// field; TUI fetches all up front from Show).
func New(fields []registry.Field, in registry.Input) *State {
	s := &State{
		fields:  fields,
		entries: make([]FieldEntry, len(fields)),
		seed:    cloneInput(in),
	}
	for i, f := range fields {
		// Three-tier seed precedence (Input > Prefill > Default) into
		// Text so widget hydration can read directly from the entry.
		// Suggest fields read from Choices+SelIdx; their Text seed is
		// not used for display, so skip.
		if f.Suggest != nil {
			if v, ok := in[f.Flag]; ok && v != "" {
				// Pre-seeded answer: mark committed so it auto-skips.
				s.entries[i].Text = v
				s.entries[i].Committed = true
			}
			continue
		}
		if v, ok := in[f.Flag]; ok && v != "" {
			s.entries[i].Text = v
			s.entries[i].Committed = true
			continue
		}
		if f.Prefill != nil {
			s.entries[i].Text = f.Prefill()
		} else if f.Default != nil {
			s.entries[i].Text = sprintAny(f.Default)
		}
	}
	return s
}

func cloneInput(in registry.Input) registry.Input {
	out := registry.Input{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

func sprintAny(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

// ---- Reads ---------------------------------------------------------------

// Field returns the registry.Field at idx. Panics on out-of-range.
func (s *State) Field(idx int) registry.Field { return s.fields[idx] }

// Fields returns the field slice the State was built for. The returned
// slice is the same one passed to New — callers that need to feed it
// to a sibling UI (e.g. the TUI overlay during a Ctrl+T handoff) get
// guaranteed alignment with State's entries without having to track
// the slice separately.
func (s *State) Fields() []registry.Field { return s.fields }

// Len returns the number of fields.
func (s *State) Len() int { return len(s.fields) }

// Idx returns the currently focused field index (CLI cursor / TUI focus).
func (s *State) Idx() int { return s.idx }

// Entry returns a mutable handle to the entry at idx. Callers must not
// hold the pointer across mutations that may grow the entries slice
// (none today, but a defensive convention).
func (s *State) Entry(idx int) *FieldEntry { return &s.entries[idx] }

// Value returns the string that would be committed for field idx if
// the user pressed enter right now.
func (s *State) Value(idx int) string {
	if idx < 0 || idx >= len(s.fields) {
		return ""
	}
	f := s.fields[idx]
	e := &s.entries[idx]
	if f.Suggest != nil && f.Multi {
		if len(e.MultiSel) == 0 {
			return ""
		}
		var picks []string
		for i, c := range e.Choices {
			if e.MultiSel[i] {
				picks = append(picks, c.Value)
			}
		}
		return strings.Join(picks, ",")
	}
	if f.Suggest != nil {
		if len(e.Choices) > 0 && e.SelIdx >= 0 && e.SelIdx < len(e.Choices) {
			return e.Choices[e.SelIdx].Value
		}
		// Pre-seeded answer (from in) before choices arrive.
		return e.Text
	}
	if f.File || f.EffectiveKind() == registry.KindFile {
		return e.FilePath
	}
	return e.Text
}

// LiveInput returns "what would Input look like if the user submitted
// right now?" — seed overlaid with each visible field's current Value.
// Hidden fields are excluded.
func (s *State) LiveInput() registry.Input {
	out := cloneInput(s.seed)
	for i, f := range s.fields {
		if !s.FieldVisible(i) {
			delete(out, f.Flag)
			continue
		}
		if v := s.Value(i); v != "" {
			out[f.Flag] = v
		}
	}
	return out
}

// LiveInputForWhen builds the input snapshot used to evaluate When for
// field excludeIdx. Rules mirror today's wizardoverlay.liveInputForWhen:
//  1. excludeIdx's own value is excluded (a field cannot hide itself).
//  2. Suggest fields contribute only if they precede excludeIdx — a
//     later field's uncommitted cursor cannot hide an earlier one,
//     which would block shift+tab navigation.
//  3. Text and File fields always contribute.
//  4. seed contributes for all keys other than excludeFlag.
func (s *State) LiveInputForWhen(excludeIdx int) registry.Input {
	out := registry.Input{}
	excludeFlag := ""
	if excludeIdx >= 0 && excludeIdx < len(s.fields) {
		excludeFlag = s.fields[excludeIdx].Flag
	}
	for k, v := range s.seed {
		if k == excludeFlag {
			continue
		}
		out[k] = v
	}
	for i, f := range s.fields {
		if i == excludeIdx {
			continue
		}
		if f.Suggest != nil && i >= excludeIdx {
			continue
		}
		if v := s.Value(i); v != "" {
			out[f.Flag] = v
		}
	}
	return out
}

// FieldVisible reports whether field idx's When predicate (if any)
// holds against the live input snapshot.
func (s *State) FieldVisible(idx int) bool {
	if idx < 0 || idx >= len(s.fields) {
		return false
	}
	f := s.fields[idx]
	if f.When == nil {
		return true
	}
	return f.When(s.LiveInputForWhen(idx))
}

// NextVisible returns the index of the first visible field strictly
// after i, or -1 if none.
func (s *State) NextVisible(i int) int {
	for j := i + 1; j < len(s.fields); j++ {
		if s.FieldVisible(j) {
			return j
		}
	}
	return -1
}

// PrevVisible returns the index of the first visible field strictly
// before i, or -1 if none.
func (s *State) PrevVisible(i int) int {
	for j := i - 1; j >= 0; j-- {
		if s.FieldVisible(j) {
			return j
		}
	}
	return -1
}

// FirstVisible returns the first visible field, or -1 if all hidden.
func (s *State) FirstVisible() int {
	for j := 0; j < len(s.fields); j++ {
		if s.FieldVisible(j) {
			return j
		}
	}
	return -1
}

// LastVisible returns the last visible field, or -1 if all hidden.
func (s *State) LastVisible() int {
	for j := len(s.fields) - 1; j >= 0; j-- {
		if s.FieldVisible(j) {
			return j
		}
	}
	return -1
}

// CommittedHistory returns one HistoryEntry per visible, committed
// field in field order. DisplayVal is pre-masked for sensitive fields.
func (s *State) CommittedHistory() []HistoryEntry {
	var out []HistoryEntry
	for i, f := range s.fields {
		if !s.entries[i].Committed {
			continue
		}
		if !s.FieldVisible(i) {
			continue
		}
		val := s.Value(i)
		if f.Sensitive {
			val = strings.Repeat("•", len(val))
		}
		out = append(out, HistoryEntry{
			Idx:        i,
			Field:      f,
			Section:    f.Section,
			DisplayVal: val,
		})
	}
	return out
}

// ---- Writes --------------------------------------------------------------

// SetText assigns a free-text value to entry idx without committing.
// The TUI overlay calls this on every keystroke so When predicates see
// in-flight text; the CLI wizard relies on its widget being the source
// of truth and only calls this on commit.
func (s *State) SetText(idx int, v string) {
	if idx < 0 || idx >= len(s.entries) {
		return
	}
	s.entries[idx].Text = v
	s.cascadeFrom(idx)
}

// SetFilePath assigns a file path without committing.
func (s *State) SetFilePath(idx int, v string) {
	if idx < 0 || idx >= len(s.entries) {
		return
	}
	s.entries[idx].FilePath = v
	s.cascadeFrom(idx)
}

// SetSelIdx moves the choice cursor for a Suggest field. Returns false
// when idx or sel is out of range.
func (s *State) SetSelIdx(idx, sel int) bool {
	if idx < 0 || idx >= len(s.entries) {
		return false
	}
	e := &s.entries[idx]
	if sel < 0 || sel >= len(e.Choices) {
		return false
	}
	e.SelIdx = sel
	s.cascadeFrom(idx)
	return true
}

// ToggleMulti flips the checkbox at choiceIdx for field idx. Allocates
// the set on first use so Value can distinguish "never touched" from
// "explicitly empty".
func (s *State) ToggleMulti(idx, choiceIdx int) {
	if idx < 0 || idx >= len(s.entries) {
		return
	}
	e := &s.entries[idx]
	if e.MultiSel == nil {
		e.MultiSel = map[int]bool{}
	}
	if e.MultiSel[choiceIdx] {
		delete(e.MultiSel, choiceIdx)
	} else {
		e.MultiSel[choiceIdx] = true
	}
	s.cascadeFrom(idx)
}

// SetChoices stores choices for field idx (called when a Suggest fetch
// returns) and seeds the cursor / multi-select set from the existing
// answer using Input > Prefill > Default precedence. Returns the
// resolved single-select cursor for tests.
func (s *State) SetChoices(idx int, choices []registry.Choice) int {
	if idx < 0 || idx >= len(s.entries) {
		return 0
	}
	e := &s.entries[idx]
	f := s.fields[idx]
	e.Choices = choices
	e.Loading = false
	e.ChoicesErr = nil

	want := e.Text
	if want == "" {
		if v, ok := s.seed[f.Flag]; ok && v != "" {
			want = v
		} else if f.Prefill != nil {
			want = f.Prefill()
		} else if f.Default != nil {
			want = sprintAny(f.Default)
		}
	}
	if want == "" {
		s.cascadeFrom(idx)
		return e.SelIdx
	}
	if f.Multi {
		set := map[string]bool{}
		for _, v := range strings.Split(want, ",") {
			v = strings.TrimSpace(v)
			if v != "" {
				set[v] = true
			}
		}
		sel := map[int]bool{}
		for i, c := range choices {
			if set[c.Value] {
				sel[i] = true
			}
		}
		if len(sel) > 0 {
			e.MultiSel = sel
		}
	} else {
		for i, c := range choices {
			if c.Value == want {
				e.SelIdx = i
				break
			}
		}
	}
	s.cascadeFrom(idx)
	return e.SelIdx
}

// SetChoicesErr stores an async error for field idx. Also clears
// Loading so callers do not have to remember.
func (s *State) SetChoicesErr(idx int, err error) {
	if idx < 0 || idx >= len(s.entries) {
		return
	}
	s.entries[idx].ChoicesErr = err
	s.entries[idx].Loading = false
}

// SetLoading marks a Suggest fetch as in-flight (or done).
func (s *State) SetLoading(idx int, loading bool) {
	if idx < 0 || idx >= len(s.entries) {
		return
	}
	s.entries[idx].Loading = loading
}

// Focus updates the focused index. State does not enforce visibility on
// Focus — callers are expected to have computed a visible target via
// NextVisible / PrevVisible.
func (s *State) Focus(idx int) { s.idx = idx }

// ---- Lifecycle -----------------------------------------------------------

// CommitValue sets a free-text / file value for field idx and commits.
// Returns the validation error from Commit (if any).
func (s *State) CommitValue(idx int, v string) error {
	if idx < 0 || idx >= len(s.entries) {
		return nil
	}
	f := s.fields[idx]
	if f.File || f.EffectiveKind() == registry.KindFile {
		s.entries[idx].FilePath = v
	} else if f.Suggest == nil {
		s.entries[idx].Text = v
	}
	return s.Commit(idx)
}

// Commit validates Value(idx) against the field's ValidateValue and
// marks the entry committed when valid. Runs the cascading-clear sweep
// for fields after idx. Returns the validation error if any (which the
// caller typically stores in entry.Err for render).
func (s *State) Commit(idx int) error {
	if idx < 0 || idx >= len(s.entries) {
		return nil
	}
	f := s.fields[idx]
	val := s.Value(idx)
	if err := f.ValidateValue(val); err != nil {
		s.entries[idx].Err = err.Error()
		return err
	}
	s.entries[idx].Err = ""
	s.entries[idx].Committed = true
	s.cascadeFrom(idx)
	return nil
}

// SkipOptional marks an optional field as committed with empty value.
// Returns an error when the field is Required.
func (s *State) SkipOptional(idx int) error {
	if idx < 0 || idx >= len(s.entries) {
		return nil
	}
	f := s.fields[idx]
	if f.Required {
		return errRequired{flag: f.Flag}
	}
	s.entries[idx].Text = ""
	s.entries[idx].FilePath = ""
	s.entries[idx].Err = ""
	s.entries[idx].Committed = true
	s.cascadeFrom(idx)
	return nil
}

// errRequired is returned by SkipOptional on a required field.
type errRequired struct{ flag string }

func (e errRequired) Error() string { return "required" }

// GoBack returns the largest visible idx j < from where Committed is
// true, or -1 if none. Marks all entries at or after the returned idx
// as Committed=false so re-reaching them re-prompts the user. Does NOT
// clear Text / SelIdx / MultiSel / FilePath — that preserves the user's
// previous answer so they can edit it (the shift+tab "fix my typo"
// flow). Downstream answers also survive unless their When predicate
// flips during subsequent edits, in which case the cascading-clear
// sweep handles them at the point of mutation.
func (s *State) GoBack(from int) int {
	target := -1
	for j := from - 1; j >= 0; j-- {
		if s.entries[j].Committed && s.FieldVisible(j) {
			target = j
			break
		}
	}
	if target < 0 {
		return -1
	}
	for j := target; j < len(s.entries); j++ {
		s.entries[j].Committed = false
		s.entries[j].Err = ""
	}
	s.idx = target
	return target
}

// Submit validates every visible field. Returns the index of the first
// invalid field, or -1 if all valid. Populates Err on each invalid
// field. Required-but-empty fields are flagged with the "required"
// error. Hidden fields are skipped.
func (s *State) Submit() int {
	first := -1
	for i, f := range s.fields {
		s.entries[i].Err = ""
		if !s.FieldVisible(i) {
			continue
		}
		val := s.Value(i)
		if f.Required && val == "" {
			s.entries[i].Err = "required"
			if first < 0 {
				first = i
			}
			continue
		}
		if val == "" {
			continue
		}
		if err := f.ValidateValue(val); err != nil {
			s.entries[i].Err = err.Error()
			if first < 0 {
				first = i
			}
		}
	}
	return first
}

// ApplyToInput writes the final answers into out. Visible fields'
// Value() goes into out[Flag]; hidden fields are deleted from out so
// downstream sagas do not see ghost values left over from a When that
// flipped after seeding.
func (s *State) ApplyToInput(out registry.Input) {
	for i, f := range s.fields {
		if s.FieldVisible(i) {
			v := s.Value(i)
			if v != "" {
				out[f.Flag] = v
			} else {
				delete(out, f.Flag)
			}
		} else {
			delete(out, f.Flag)
		}
	}
}

// cascadeFrom re-evaluates visibility for every field strictly after
// changedIdx. Fields whose When flipped from true to false get their
// answer slots cleared and Committed set to false. Fields whose
// visibility did not change are left alone, including those that
// became visible (their slots are already in whatever state seeding
// left them in).
//
// Cost is O(n_fields × cost_of_When) per mutation. Field.When
// predicates must remain cheap and pure — State evaluates them on
// every mutation, not only on render.
func (s *State) cascadeFrom(changedIdx int) {
	for j := changedIdx + 1; j < len(s.fields); j++ {
		if s.fields[j].When == nil {
			continue
		}
		visible := s.FieldVisible(j)
		if !visible && s.entries[j].Committed {
			// Was committed, now hidden — drop the answer.
			s.entries[j] = FieldEntry{}
		}
	}
}
