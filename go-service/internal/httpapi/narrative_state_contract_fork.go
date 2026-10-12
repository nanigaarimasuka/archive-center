package httpapi

type narrativeCurrentStateViewsByTurn struct {
	views []narrativeCurrentStateView
	turns []int
}

func (v narrativeCurrentStateViewsByTurn) Len() int { return len(v.views) }

func (v narrativeCurrentStateViewsByTurn) Less(i, j int) bool { return v.turns[i] > v.turns[j] }

func (v narrativeCurrentStateViewsByTurn) Swap(i, j int) {
	v.views[i], v.views[j] = v.views[j], v.views[i]
	v.turns[i], v.turns[j] = v.turns[j], v.turns[i]
}
