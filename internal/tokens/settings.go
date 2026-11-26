package tokens

import (
	"errors"
	"regexp"
	"slices"

	"golang.org/x/text/cases"

	"github.com/witseko/agentcy/internal/wire"
)

// The defaults and bounds of tokens.json (src/agentcy/tokens/settings.py).
const (
	DefaultWarnPercent         = 75
	DefaultImbalanceGapPercent = 20
	// MaxSettingsBytes caps the size of tokens.json.
	MaxSettingsBytes = 65_536
	// maxTokenBudget is the largest initial token budget a setting may give.
	maxTokenBudget = 1_000_000_000
)

// The two errors of a tokens.json that cannot be used. The texts are Python's.
var (
	ErrSettingsUnsafe  = errors.New("Token configuration must be a private file")
	ErrSettingsInvalid = errors.New("Invalid token configuration")
)

// modelNamePattern is the pattern that model names in tokens.json must match.
var modelNamePattern = regexp.MustCompile(`^[a-zA-Z0-9._:/-]{1,128}$`)

// ModelBudget is one model's starting token allowance and soft balancing thresholds.
// A nil threshold falls back to the global setting.
type ModelBudget struct {
	InitialTokens       int64
	WarnPercent         *int64
	ImbalanceGapPercent *int64
	ResetWindowHours    *int64
}

// NamedBudget is one entry of the models table, kept in file order because a
// case-insensitive lookup returns the first match.
type NamedBudget struct {
	Model  string
	Budget ModelBudget
}

// Settings is a validated tokens.json.
type Settings struct {
	Models               []NamedBudget
	DefaultInitialTokens *int64
	WarnPercent          int64
	ImbalanceGapPercent  int64
}

// BudgetFor returns the budget of model, matched exactly or else case-insensitively with
// Unicode case folding, or nil. An empty model has no budget.
func (settings *Settings) BudgetFor(model string) *ModelBudget {
	if model == "" {
		return nil
	}
	for index := range settings.Models {
		if settings.Models[index].Model == model {
			return &settings.Models[index].Budget
		}
	}
	folded := cases.Fold().String(model)
	for index := range settings.Models {
		if cases.Fold().String(settings.Models[index].Model) == folded {
			return &settings.Models[index].Budget
		}
	}
	return nil
}

// InitialTokensFor returns the per-conversation override, else the model's budget, else
// the global default, or nil when none is set.
func (settings *Settings) InitialTokensFor(model string, override *int64) *int64 {
	if override != nil {
		return override
	}
	if budget := settings.BudgetFor(model); budget != nil {
		initial := budget.InitialTokens
		return &initial
	}
	return settings.DefaultInitialTokens
}

// WarnPercentFor returns the warning threshold in percent for model.
func (settings *Settings) WarnPercentFor(model string) int64 {
	if budget := settings.BudgetFor(model); budget != nil && budget.WarnPercent != nil {
		return *budget.WarnPercent
	}
	return settings.WarnPercent
}

// ResetWindowSecondsFor returns the rolling spend window of model in seconds, or nil for
// cumulative tracking.
func (settings *Settings) ResetWindowSecondsFor(model string) *int64 {
	budget := settings.BudgetFor(model)
	if budget == nil || budget.ResetWindowHours == nil {
		return nil
	}
	seconds := *budget.ResetWindowHours * 3600
	return &seconds
}

// integerRange is an inclusive bound of an integer setting.
type integerRange struct {
	minimum, maximum int64
}

// The bounds of each integer setting, as in the pydantic types of settings.py.
var (
	tokenBudgetRange = integerRange{1, maxTokenBudget}
	percentRange     = integerRange{1, 99}
	gapRange         = integerRange{5, 90}
	windowRange      = integerRange{1, 720}
)

// ParseSettings validates the content of tokens.json like the pydantic model: an object
// with only known fields, strict JSON integers in range, and model names matching the
// pattern. Every problem returns ErrSettingsInvalid, so no input is echoed.
func ParseSettings(data []byte) (*Settings, error) {
	document, err := wire.Parse(data)
	if err != nil || !onlyKnownFields(document, "models", "default_initial_tokens", "warn_percent", "imbalance_gap_percent") {
		return nil, ErrSettingsInvalid
	}
	settings := &Settings{WarnPercent: DefaultWarnPercent, ImbalanceGapPercent: DefaultImbalanceGapPercent}
	valid := true
	if value, found := document.Get("default_initial_tokens"); found {
		settings.DefaultInitialTokens, valid = optionalInteger(value, tokenBudgetRange)
	}
	if value, found := document.Get("warn_percent"); found && valid {
		settings.WarnPercent, valid = strictInteger(value, percentRange)
	}
	if value, found := document.Get("imbalance_gap_percent"); found && valid {
		settings.ImbalanceGapPercent, valid = strictInteger(value, gapRange)
	}
	if value, found := document.Get("models"); found && valid {
		settings.Models, valid = parseModels(value)
	}
	if !valid {
		return nil, ErrSettingsInvalid
	}
	return settings, nil
}

// parseModels validates the models table and keeps its order.
func parseModels(value wire.Value) ([]NamedBudget, bool) {
	if value.Kind != wire.Object {
		return nil, false
	}
	models := []NamedBudget{}
	for _, name := range value.Keys() {
		entry, _ := value.Get(name)
		budget, valid := parseModelBudget(entry)
		if !valid || !modelNamePattern.MatchString(name) {
			return nil, false
		}
		models = append(models, NamedBudget{Model: name, Budget: budget})
	}
	return models, true
}

// parseModelBudget validates one model entry; initial_tokens is required.
func parseModelBudget(value wire.Value) (ModelBudget, bool) {
	var budget ModelBudget
	if !onlyKnownFields(value, "initial_tokens", "warn_percent", "imbalance_gap_percent", "reset_window_hours") {
		return budget, false
	}
	initial, found := value.Get("initial_tokens")
	if !found {
		return budget, false
	}
	valid := true
	budget.InitialTokens, valid = strictInteger(initial, tokenBudgetRange)
	optional := []struct {
		key    string
		target **int64
		bounds integerRange
	}{
		{"warn_percent", &budget.WarnPercent, percentRange},
		{"imbalance_gap_percent", &budget.ImbalanceGapPercent, gapRange},
		{"reset_window_hours", &budget.ResetWindowHours, windowRange},
	}
	for _, field := range optional {
		if entry, found := value.Get(field.key); found && valid {
			*field.target, valid = optionalInteger(entry, field.bounds)
		}
	}
	return budget, valid
}

// onlyKnownFields reports whether value is an object whose keys are all in known.
func onlyKnownFields(value wire.Value, known ...string) bool {
	if value.Kind != wire.Object {
		return false
	}
	for _, key := range value.Keys() {
		if !slices.Contains(known, key) {
			return false
		}
	}
	return true
}

// strictInteger accepts a JSON integer in bounds; floats such as 5.0, strings and
// booleans are rejected, as by a strict pydantic int.
func strictInteger(value wire.Value, bounds integerRange) (int64, bool) {
	number, ok := value.IntOf()
	if !ok || number < bounds.minimum || number > bounds.maximum {
		return 0, false
	}
	return number, true
}

// optionalInteger accepts null as nil, or a strict integer in bounds.
func optionalInteger(value wire.Value, bounds integerRange) (*int64, bool) {
	if value.Kind == wire.Null {
		return nil, true
	}
	number, ok := strictInteger(value, bounds)
	if !ok {
		return nil, false
	}
	return &number, true
}
