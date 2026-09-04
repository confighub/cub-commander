package catalog

// LiveWith builds a Live from label-key counts and totals, for tests.
func LiveWith(keys map[string]map[string]int, totals map[string]int) *Live {
	l := NewLive()
	for ent, m := range keys {
		l.labelKeys[ent] = m
		l.labelVals[ent] = map[string]map[string]int{}
	}
	for ent, n := range totals {
		l.totals[ent] = n
	}
	return l
}
