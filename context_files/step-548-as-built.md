# Step 548 — v0.9's scope

**Docs only.** On 2026-11-27 the maintainer kept the roadmap's order: v0.9, Parquet
complete then Apache Iceberg, before v0.10's SQL. This step writes the scope,
[`v0.9-scope.md`](./v0.9-scope.md), as 0.8 had [`v0.8-scope.md`](./v0.8-scope.md).

It was surveyed by reading master's code at `1d2dc1b`, with no measurement, as the
laptop was kept quiet. Its findings:
- **Parquet** reads and writes one level of nesting.
- **Maps** have no ursus type.
- **Field IDs** are not handled.
- **Key-value metadata** is internal only.
- **Page indexes and bloom filters** are unused.
- **Write options** stop at one codec.
- **arrow-go v18.9.0** has writer properties, page-index and bloom-filter readers,
  field IDs in `pqarrow`, and an Avro reader (`arrow/avro`) that may serve Iceberg's
  manifests.

The scope holds six Parquet items (P1–P6), six Iceberg items (I1–I6), the testing
rules, the order, and four decisions for the maintainer: Avro, the REST catalog,
writing a map, and where v0.9.0 is tagged.
