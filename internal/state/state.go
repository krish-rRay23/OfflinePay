package state

import (
	"offlinepay/internal/domain"
)

type StateMachine struct{}

func NewStateMachine() *StateMachine {
	return &StateMachine{}
}

// CanTransition validates that a state transition follows the defined transaction lifecycle
func (sm *StateMachine) CanTransition(from, to string) bool {
	return domain.IsValidTransition(from, to)
}
