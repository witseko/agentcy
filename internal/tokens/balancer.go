package tokens

import (
	"sort"

	"github.com/witseko/agentcy/internal/progress"
	"github.com/witseko/agentcy/internal/wire"
)

// The budget states of a member.
const (
	StatusOK        = "ok"
	StatusWarning   = "warning"
	StatusExhausted = "exhausted"
)

// The usage sources. Reported and plugin usage are exact; estimated usage is the bridge's
// own count.
const (
	SourceEstimated = "estimated"
	SourceReported  = "reported"
	SourcePlugin    = "plugin"
)

// MemberUsage is one member's public usage summary (the "usage" entry of a status
// member). Model is "" when the slot has none. Budget is nil when no initial budget is
// known.
type MemberUsage struct {
	ConversationTokens int64
	InputTokens        int64
	OutputTokens       int64
	ModelTokensUsed    int64
	EstimatedTokens    int64
	Source             string
	ActualTokens       *int64
	Model              string
	Budget             *BudgetUsage
}

// BudgetUsage is the part of a usage summary that needs an initial budget.
type BudgetUsage struct {
	InitialTokens    int64
	RemainingTokens  int64
	UsedPercent      float64
	RemainingPercent float64
	Status           string
}

// Value returns the summary as a JSON object in Python's field order.
func (usage *MemberUsage) Value() wire.Value {
	entry := wire.NewObject()
	entry.Set("conversation_tokens", wire.IntValue(usage.ConversationTokens))
	entry.Set("input_tokens", wire.IntValue(usage.InputTokens))
	entry.Set("output_tokens", wire.IntValue(usage.OutputTokens))
	entry.Set("model_tokens_used", wire.IntValue(usage.ModelTokensUsed))
	entry.Set("estimated_tokens", wire.IntValue(usage.EstimatedTokens))
	entry.Set("source", wire.TextValue(usage.Source))
	if usage.ActualTokens != nil {
		entry.Set("actual_tokens", wire.IntValue(*usage.ActualTokens))
	}
	if usage.Model != "" {
		entry.Set("model", wire.TextValue(usage.Model))
	}
	if budget := usage.Budget; budget != nil {
		entry.Set("initial_tokens", wire.IntValue(budget.InitialTokens))
		entry.Set("remaining_tokens", wire.IntValue(budget.RemainingTokens))
		entry.Set("used_percent", FloatValue(budget.UsedPercent))
		entry.Set("remaining_percent", FloatValue(budget.RemainingPercent))
		entry.Set("status", wire.TextValue(budget.Status))
	}
	return entry
}

// BudgetStatus classifies a model's budget pressure.
func BudgetStatus(usedPercent float64, warnPercent int64) string {
	if usedPercent >= 100.0 {
		return StatusExhausted
	}
	if usedPercent >= float64(warnPercent) {
		return StatusWarning
	}
	return StatusOK
}

// sourceOrDefault applies Python's raw.get("source", "estimated").
func sourceOrDefault(source string) string {
	if source == "" {
		return SourceEstimated
	}
	return source
}

// BuildMemberUsage builds one member's usage summary, or nil when the member has neither
// usage nor a budget. settings may be nil; ledgerSpend is ModelSpend's result.
func BuildMemberUsage(raw SlotUsage, settings *Settings, ledgerSpend map[string]int64) *MemberUsage {
	model := ""
	if raw.Model != nil {
		model = *raw.Model
	}
	initial := raw.TokenBudget
	if settings != nil {
		initial = settings.InitialTokensFor(model, raw.TokenBudget)
	}
	source := sourceOrDefault(raw.Source)
	hasExact := source == SourceReported || source == SourcePlugin
	trackingActive := settings != nil || raw.Model != nil || initial != nil || hasExact
	if !trackingActive || (!raw.HasUsage && initial == nil) {
		return nil
	}
	modelUsed := raw.ConversationTokens
	if model != "" {
		modelUsed = max(ledgerSpend[model], raw.ConversationTokens)
	}
	usage := &MemberUsage{
		ConversationTokens: raw.ConversationTokens,
		InputTokens:        raw.InputTokens,
		OutputTokens:       raw.OutputTokens,
		ModelTokensUsed:    modelUsed,
		EstimatedTokens:    raw.EstimatedTokens,
		Source:             source,
		ActualTokens:       raw.ActualTokens,
		Model:              model,
	}
	if initial != nil && *initial > 0 {
		usage.Budget = budgetUsage(*initial, modelUsed, settings, model)
	}
	return usage
}

// budgetUsage computes the percentages of a member with an initial budget.
func budgetUsage(initial, modelUsed int64, settings *Settings, model string) *BudgetUsage {
	if initial <= 0 {
		return nil
	}
	usedPercent := roundOneDecimal(100.0 * float64(modelUsed) / float64(initial))
	warnPercent := int64(DefaultWarnPercent)
	if settings != nil {
		warnPercent = settings.WarnPercentFor(model)
	}
	return &BudgetUsage{
		InitialTokens:    initial,
		RemainingTokens:  max(0, initial-modelUsed),
		UsedPercent:      usedPercent,
		RemainingPercent: maxZero(roundOneDecimal(100.0 - usedPercent)),
		Status:           BudgetStatus(usedPercent, warnPercent),
	}
}

// maxZero is Python's max(0.0, number): 0.0 unless number is greater.
func maxZero(number float64) float64 {
	if number > 0 {
		return number
	}
	return 0.0
}

// ConversationUsage is the token spend of all slots of one conversation:
// TotalTokens = ActualTokens + EstimatedTokens, where a slot with exact usage counts its
// actual total and a slot without counts its bridge estimate.
type ConversationUsage struct {
	TotalTokens      int64
	ActualTokens     int64
	EstimatedTokens  int64
	ActualMembers    int64
	EstimatedMembers int64
}

// Value returns the spend as a JSON object in Python's field order.
func (usage *ConversationUsage) Value() wire.Value {
	entry := wire.NewObject()
	entry.Set("total_tokens", wire.IntValue(usage.TotalTokens))
	entry.Set("actual_tokens", wire.IntValue(usage.ActualTokens))
	entry.Set("estimated_tokens", wire.IntValue(usage.EstimatedTokens))
	entry.Set("actual_members", wire.IntValue(usage.ActualMembers))
	entry.Set("estimated_members", wire.IntValue(usage.EstimatedMembers))
	return entry
}

// SumConversationUsage sums the spend of the slots that have usage, or returns nil when
// none has (port of conversation_usage).
func SumConversationUsage(slots []SlotUsage) *ConversationUsage {
	usage := &ConversationUsage{}
	for _, slot := range slots {
		if !slot.HasUsage {
			continue
		}
		if slot.ActualTokens != nil {
			usage.ActualTokens += *slot.ActualTokens
			usage.ActualMembers++
		} else {
			usage.EstimatedTokens += slot.EstimatedTokens
			usage.EstimatedMembers++
		}
	}
	if usage.ActualMembers+usage.EstimatedMembers == 0 {
		return nil
	}
	usage.TotalTokens = usage.ActualTokens + usage.EstimatedTokens
	return usage
}

// BalanceMember is one status member as the balancer reads it, in status order. Usage
// is nil when the member has no usage summary.
type BalanceMember struct {
	progress.Member
	Usage *MemberUsage
}

// Balance is the lead guidance about token balance. Overloaded, Preferred and
// Recommendation are set only when Balanced is false.
type Balance struct {
	Balanced            bool
	ImbalanceGapPercent float64
	Overloaded          []string
	Preferred           []string
	Recommendation      string
}

// Value returns the guidance as a JSON object in Python's field order.
func (balance *Balance) Value() wire.Value {
	entry := wire.NewObject()
	entry.Set("balanced", wire.BoolValue(balance.Balanced))
	entry.Set("imbalance_gap_percent", FloatValue(balance.ImbalanceGapPercent))
	if !balance.Balanced {
		entry.Set("overloaded", wire.TextList(balance.Overloaded))
		entry.Set("preferred", wire.TextList(balance.Preferred))
		entry.Set("recommendation", wire.TextValue(balance.Recommendation))
	}
	return entry
}

// rankedMember is an active member with a budget.
type rankedMember struct {
	name  string
	usage *MemberUsage
}

// activeBudgetedMembers returns the members that are not human, retired or an ended
// fresh run and that have a known initial budget.
func activeBudgetedMembers(members []BalanceMember) []rankedMember {
	var active []rankedMember
	for _, member := range members {
		if member.Role == progress.RoleHuman || progress.IsRetired(member.Member) || progress.FreshRunEnded(member.Member) {
			continue
		}
		if member.Usage != nil && member.Usage.Budget != nil {
			active = append(active, rankedMember{name: member.Name, usage: member.Usage})
		}
	}
	return active
}

// effectiveGapThreshold returns the tightest imbalance gap among the active models.
func effectiveGapThreshold(active []rankedMember, settings *Settings) int64 {
	if settings == nil {
		return DefaultImbalanceGapPercent
	}
	gap := settings.ImbalanceGapPercent
	for _, member := range active {
		budget := settings.BudgetFor(member.usage.Model)
		if budget != nil && budget.ImbalanceGapPercent != nil {
			gap = min(gap, *budget.ImbalanceGapPercent)
		}
	}
	return gap
}

// EvaluateBalance compares the budget use of the active members and returns lead
// guidance, or nil when fewer than two active members have a budget. settings may be nil.
func EvaluateBalance(members []BalanceMember, settings *Settings) *Balance {
	active := activeBudgetedMembers(members)
	if len(active) < 2 {
		return nil
	}
	ranked := append([]rankedMember(nil), active...)
	sort.SliceStable(ranked, func(left, right int) bool {
		leftUsed, rightUsed := ranked[left].usage.Budget.UsedPercent, ranked[right].usage.Budget.UsedPercent
		if leftUsed != rightUsed {
			return leftUsed > rightUsed
		}
		return ranked[left].name < ranked[right].name
	})
	highest := ranked[0]
	alternatives := lessLoadedPeers(ranked, highest)
	if len(alternatives) == 0 {
		return &Balance{Balanced: true, ImbalanceGapPercent: 0.0}
	}
	highestUsed := highest.usage.Budget.UsedPercent
	lowestUsed := alternatives[0].usage.Budget.UsedPercent
	gap := roundOneDecimal(maxZero(highestUsed - lowestUsed))
	threshold := float64(effectiveGapThreshold(active, settings))
	pressured := underPressure(highest.usage) && lowestUsed < highestUsed
	if gap < threshold && !pressured {
		return &Balance{Balanced: true, ImbalanceGapPercent: gap}
	}
	overloaded := overloadedMembers(ranked, lowestUsed, threshold)
	preferred := preferredMembers(alternatives, highestUsed)
	if len(overloaded) == 0 || len(preferred) == 0 {
		return &Balance{Balanced: true, ImbalanceGapPercent: gap}
	}
	return &Balance{
		Balanced:            false,
		ImbalanceGapPercent: gap,
		Overloaded:          memberNames(overloaded),
		Preferred:           memberNames(preferred),
		Recommendation:      formatRecommendation(overloaded[0], preferred[0]),
	}
}

// lessLoadedPeers returns the ranked members other than highest, least loaded first,
// skipping peers that share highest's exact model pool.
func lessLoadedPeers(ranked []rankedMember, highest rankedMember) []rankedMember {
	var peers []rankedMember
	for index := len(ranked) - 1; index >= 0; index-- {
		member := ranked[index]
		if member.name == highest.name {
			continue
		}
		if highest.usage.Model == "" || member.usage.Model != highest.usage.Model {
			peers = append(peers, member)
		}
	}
	return peers
}

// overloadedMembers returns the ranked members under pressure or at least threshold
// points above the least loaded peer.
func overloadedMembers(ranked []rankedMember, lowestUsed, threshold float64) []rankedMember {
	var overloaded []rankedMember
	for _, member := range ranked {
		memberGap := roundOneDecimal(maxZero(member.usage.Budget.UsedPercent - lowestUsed))
		if underPressure(member.usage) || memberGap >= threshold {
			overloaded = append(overloaded, member)
		}
	}
	return overloaded
}

// preferredMembers returns the peers that are not exhausted and less loaded than the
// most loaded member, least loaded first.
func preferredMembers(alternatives []rankedMember, highestUsed float64) []rankedMember {
	var preferred []rankedMember
	for _, member := range alternatives {
		if member.usage.Budget.Status != StatusExhausted && member.usage.Budget.UsedPercent < highestUsed {
			preferred = append(preferred, member)
		}
	}
	return preferred
}

// underPressure reports a member at warning or exhausted.
func underPressure(usage *MemberUsage) bool {
	return usage.Budget.Status == StatusWarning || usage.Budget.Status == StatusExhausted
}

// memberNames returns the names of members in order.
func memberNames(members []rankedMember) []string {
	names := make([]string, 0, len(members))
	for _, member := range members {
		names = append(names, member.name)
	}
	return names
}

// formatRecommendation writes the rebalancing hint for the lead.
func formatRecommendation(top, best rankedMember) string {
	topBudget, bestBudget := top.usage.Budget, best.usage.Budget
	if topBudget.Status == StatusExhausted {
		return "Token budget warning: " + top.name + " has reached " + formatOneDecimal(topBudget.UsedPercent) +
			"% of its initial budget. Route follow-up tasks or handoffs to " + best.name +
			" (" + formatOneDecimal(bestBudget.RemainingPercent) + "% left)."
	}
	return "Token balance: " + top.name + " is at " + formatOneDecimal(topBudget.UsedPercent) + "% of budget (" +
		formatOneDecimal(topBudget.RemainingPercent) + "% left) while " + best.name + " is at " +
		formatOneDecimal(bestBudget.UsedPercent) + "% (" + formatOneDecimal(bestBudget.RemainingPercent) + "% left). " +
		"Prefer assigning new tasks or handoffs to " + best.name + "."
}
