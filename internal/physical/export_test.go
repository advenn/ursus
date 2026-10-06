package physical

// CallCacheLen is the number of compiled calls callCache holds.
func CallCacheLen() int {
	n := 0
	callCache.Range(func(any, any) bool { n++; return true })
	return n
}
