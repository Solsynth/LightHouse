package progression

// Normalize applies the defaults an upsert must guarantee: an enabled flag by
// default, a target of at least one, and a concrete trigger mode. It keeps the
// wire shape stable for a definition created through the admin API, which may
// omit the optional fields.
func (e *AchievementDefinitionEntity) Normalize() {
	e.TargetCount = maxInt(e.TargetCount, 1)
	if len(e.Trigger) == 0 {
		e.Trigger = mustTriggerJSON(ProgressTrigger{Mode: ModeCount})
	}
	if len(e.Reward) == 0 {
		e.Reward = mustRewardJSON(ProgressReward{})
	}
}

// Normalize is the quest counterpart of AchievementDefinitionEntity.Normalize.
func (e *QuestDefinitionEntity) Normalize() {
	e.TargetCount = maxInt(e.TargetCount, 1)
	if len(e.Trigger) == 0 {
		e.Trigger = mustTriggerJSON(ProgressTrigger{Mode: ModeCount})
	}
	if len(e.Schedule) == 0 {
		e.Schedule = mustScheduleJSON(QuestSchedule{Repeatability: RepeatNone})
	}
	if len(e.Reward) == 0 {
		e.Reward = mustRewardJSON(ProgressReward{})
	}
}
