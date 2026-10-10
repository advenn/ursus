package physical

// CallCacheLen is the number of compiled calls callCache holds.
func CallCacheLen() int {
	n := 0
	callCache.Range(func(any, any) bool { n++; return true })
	return n
}

// PartitionedFolds is how many group-by folds have run partitioned (step 150).
func PartitionedFolds() int64 { return partitionedFolds.Load() }

// RuntimeFiltered is the rows runtime filters have dropped (step 167).
func RuntimeFiltered() int64 { return runtimeFiltered.Load() }

// RuntimeRetired is the runtime filters that have retired as unselective.
func RuntimeRetired() int64 { return runtimeRetired.Load() }
