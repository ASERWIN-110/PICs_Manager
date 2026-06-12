package scanner

import "PICs_Manager/pkg/runstate"

func hasJournalAction(events []runstate.Event, action string) bool {
	for _, event := range events {
		if event.Action == action {
			return true
		}
	}
	return false
}
