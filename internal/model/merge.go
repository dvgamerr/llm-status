package model

// HasValues reports whether this context block carries any real
// provider-reported numbers, as opposed to being the empty struct a
// hook-only snapshot serializes.
func (context Context) HasValues() bool {
	return context.UsedPercentage != nil ||
		context.RemainingPercentage != nil ||
		context.WindowSize != nil ||
		context.Exceeds200KTokens != nil ||
		context.InputTokens() != nil ||
		context.OutputTokens() != nil
}

// HasValues reports whether either account-level window, the plan name, or
// the unlimited flag was actually reported.
func (limits RateLimits) HasValues() bool {
	return limits.FiveHour.UsedPercentage != nil ||
		limits.FiveHour.ResetsAt != nil ||
		limits.SevenDay.UsedPercentage != nil ||
		limits.SevenDay.ResetsAt != nil ||
		limits.Plan != "" ||
		limits.Unlimited != nil
}

// HasValues reports whether any aggregate cost metric was reported.
func (cost Cost) HasValues() bool {
	return cost != Cost{}
}

// HasUsage reports whether a snapshot carries real provider numbers.
//
// It is false for the snapshot shape a Claude Code session running inside
// the VS Code extension produces: statusLine never fires there (see
// CLAUDE.md), so the only thing that ever writes that session's file is the
// `activity` hook, which fills in a session id and an activity state and
// leaves every usage field zero.
func (snapshot Snapshot) HasUsage() bool {
	return snapshot.Context.HasValues() || snapshot.RateLimits.HasValues()
}

// WithUsageFrom returns snapshot with each usage-bearing field it is missing
// copied from donor, and returns snapshot unchanged where it already has a
// value of its own.
//
// Session, Activity, Provider, and CapturedAt are deliberately never copied:
// those are what identify the snapshot as the live session's, and the whole
// point of the backfill is to keep the live session's activity while showing
// numbers that exist. Rate limits are account-wide, so borrowing them across
// sessions reports the same account quota either way.
func (snapshot Snapshot) WithUsageFrom(donor Snapshot) Snapshot {
	merged := snapshot
	if merged.Model.ID == "" {
		merged.Model.ID = donor.Model.ID
	}
	if merged.Model.DisplayName == "" {
		merged.Model.DisplayName = donor.Model.DisplayName
	}
	if merged.ClientVersion == "" {
		merged.ClientVersion = donor.ClientVersion
	}
	if merged.ClaudeCodeVersion == "" {
		merged.ClaudeCodeVersion = donor.ClaudeCodeVersion
	}
	if !merged.Context.HasValues() {
		merged.Context = donor.Context
	}
	if !merged.RateLimits.HasValues() {
		merged.RateLimits = donor.RateLimits
	}
	if !merged.Cost.HasValues() {
		merged.Cost = donor.Cost
	}
	if merged.Effort == "" {
		merged.Effort = donor.Effort
	}
	if merged.ThinkingEnabled == nil {
		merged.ThinkingEnabled = donor.ThinkingEnabled
	}
	return merged
}

// LatestWithUsage picks the newest snapshot in group and backfills whatever
// numbers it lacks from the newest member of the same group that has them.
//
// Without the backfill, starting a Claude Code session in the VS Code
// extension makes that hook-only snapshot the newest one for the provider,
// so the dashboard follows its mascot correctly but reports every usage
// figure as "--". When the newest snapshot already carries its own numbers —
// the ordinary CLI case — it is its own donor and this returns it unchanged.
//
// A donor is not bounded by age on purpose: rate-limit windows carry their
// own reset timestamps and the dashboard already renders a window whose
// reset has passed as expired, so a stale donor degrades visibly rather than
// silently reporting an old percentage as current.
func LatestWithUsage(group []Snapshot) (Snapshot, bool) {
	var latest, donor Snapshot
	haveLatest, haveDonor := false, false
	for _, snapshot := range group {
		if !haveLatest || SnapshotIsNewer(snapshot, latest) {
			latest = snapshot
			haveLatest = true
		}
		if snapshot.HasUsage() && (!haveDonor || SnapshotIsNewer(snapshot, donor)) {
			donor = snapshot
			haveDonor = true
		}
	}
	if !haveLatest {
		return Snapshot{}, false
	}
	if haveDonor {
		latest = latest.WithUsageFrom(donor)
	}
	return latest, true
}
