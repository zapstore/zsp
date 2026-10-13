package zsp

import _ "embed"

// Skill is the agent skill for writing and maintaining zapstore.yaml, as
// printed by `zsp skill`.
//
//go:embed SKILL.md
var Skill string
