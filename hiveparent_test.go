package ursus_test

// Step 420: a partition path with a parent directory component is refused, as Polars
// refuses it, where `..\..\x` escaped the dataset's directory on Windows; `a\b` and `..`
// alone, which make one segment, are written. Since step 502 every case is Polars
// 2.1.0's answer, recorded in testdata/oracle/hiveparent.json by
// bench/scripts/oracle/hiveparent.py and spelled in oracle_hiveparent_test.go: the
// refusals name the '..', and the written datasets' file names are compared.
