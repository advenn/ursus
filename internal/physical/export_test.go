package physical

// CallCacheLen is the number of compiled calls callCache holds.
func CallCacheLen() int {
	n := 0
	callCache.Range(func(any, any) bool { n++; return true })
	return n
}

// PartitionedFolds is how many group-by folds have run partitioned (step 150).
func PartitionedFolds() int64 { return partitionedFolds.Load() }
