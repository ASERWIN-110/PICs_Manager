package runstate

import "context"

type contextKey struct{}

type Recorder struct {
	Store *Store
	RunID string
}

func WithRecorder(ctx context.Context, recorder Recorder) context.Context {
	return context.WithValue(ctx, contextKey{}, recorder)
}

func FromContext(ctx context.Context) (Recorder, bool) {
	recorder, ok := ctx.Value(contextKey{}).(Recorder)
	if !ok || recorder.Store == nil || recorder.RunID == "" {
		return Recorder{}, false
	}
	return recorder, true
}

func (r Recorder) Event(ctx context.Context, event Event) {
	if r.Store == nil || r.RunID == "" {
		return
	}
	event.RunID = r.RunID
	_ = r.Store.AppendEvent(ctx, event)
}

func (r Recorder) Phase(ctx context.Context, phase string, counts map[string]int64) {
	if r.Store == nil || r.RunID == "" {
		return
	}
	_ = r.Store.Update(ctx, r.RunID, func(run *Run) {
		run.Phase = phase
		if run.Counts == nil {
			run.Counts = map[string]int64{}
		}
		for key, value := range counts {
			run.Counts[key] = value
		}
	})
	_ = r.Store.AppendEvent(ctx, Event{
		RunID:      r.RunID,
		Phase:      phase,
		Action:     "checkpoint",
		Counts:     counts,
		Checkpoint: true,
	})
}
