package expr

// ModeAggs rewrites each mode of a group's values, the list kernel over the values
// imploded as LowerReshapes lowers it, into the aggregate that counts them, AggMode
// (step 340): its state is an entry a distinct value, which spills past a limit,
// where the implode's was every value. A mode over another reshape keeps the
// kernel, which reads that reshape's list; so does one in a window, whose
// partitions implode as they did.
func ModeAggs(n Node) Node {
	switch t := n.(type) {
	case *Call:
		if t.Fn == FnListMode && len(t.Args) == 2 {
			imp, ok := t.Args[0].(*Agg)
			m, lit := t.Args[1].(*Lit)
			if ok && lit && imp.Op == AggImplode {
				maintain, _ := m.Value.(bool)
				return &Agg{Op: AggMode, Child: imp.Child, Params: AggParams{MaintainOrder: maintain}}
			}
		}
	case *Window:
		return n
	}
	kids := n.Children()
	if len(kids) == 0 {
		return n
	}
	out := make([]Node, len(kids))
	changed := false
	for i, c := range kids {
		out[i] = ModeAggs(c)
		changed = changed || out[i] != c
	}
	if !changed {
		return n
	}
	return Rebuild(n, out)
}
