# DuckDB storage and SQL queries for the web view — design

Date: 2026-11-23 | Revised: 2026-11-24 (v2)

Status: Revised first-pass proposal for Jim’s review. Supersedes the earlier proposal for this implementation slice; changes to `docs/DECISIONS.md` require review.

## Intent

Loghorn currently holds session records in a Go in-memory store and scans them for web queries. Historical text files are reparsed at startup. Spike 2 reported Table and Graph queries of 716 ms and 520 ms, compared with approximately 5 ms using DuckDB, and roughly 3 GB of RAM for the existing run. These are preliminary engine measurements, not results from the integrated implementation.

The goal is a small, working DuckDB-backed implementation for an individual developer debugging local applications. Prioritize simplicity, usable queries, persistence, and future schema interpretation over maximum performance.

## Goals

- Store logs and spans in DuckDB, together in one table in one database file; metrics remain out of scope.
- Provide a common SQL interface for querying both signals and correlating traces across services. Preserve the existing web UI and REST API contracts during migration.
- Reduce startup processing by reusing persisted records rather than reparsing historical text or loading all records into Go memory.
- Preserve original captured records for new field discovery, schema changes, and historical reprocessing.
- Target approximately 500 ms for representative interactive queries on a local developer workstation. Investigate queries that exceed this target; do not optimize acceptable queries merely to reach 50 ms.
- Typical data volumes are expected to be much smaller than 100 GB. Treat 100 GB as a rare upper-end planning scenario, not a verified capacity or latency guarantee. Use Parquet for historical storage.

## Scope and requirements

Implement the basic DuckDB path first, followed by basic Parquet archival and historical reads. Defer the SQL CLI, advanced compaction, and performance tuning. There are never multiple independent writers, because DuckDB does not support them; instead one process, `loghorn serve`, accepts OpenTelemetry from other processes (several at the same time) and writes the database, while also serving the web interface, which `loghorn ui` opens in the browser. Do not change input eligibility or redesign ingestion and the frontend in this slice.

Keep query semantics, counts, record identity, and trace relationships correct. Use native DuckDB aggregations where appropriate; document and test reasonable numerical and percentile differences. Preserve invalid values and make storage failures visible. Use UTC/epoch time semantics in the engine and local time in the UI. The current retention period is a default policy, not a fixed constraint.

## Approach

Use a single table, `records`, for both logs and spans (Jim's unified-table Layout D). Common attributes are typed columns, and every other attribute is kept together in one JSON `attributes` column; `_source` tells logs from spans. There is no table per signal, attribute, service or stream, so reconstructing a record needs no JOIN. Avoid dynamic schemas per service, automatic column creation, and type-widening rewrites. The original typed-table Layout B remains an alternative if a concrete need emerges.

## Design

### Files and ownership

Use one database file, `loghorn.duckdb`, for active data. A configurable user-local storage directory outside repositories is the proposed direction; its exact location and migration from `.loghorn/` remain open. Retain service and project provenance where available. The `loghorn serve` process is the single owner: it writes the database and serves queries. Other processes send it OpenTelemetry rather than opening the database file themselves.

### Tables and common query interface

The `records` table has the following fixed columns, with NULL where a field does not apply to a record's signal:

| Field | Type / purpose |
|---|---|
| `_id` | BIGINT; stable unique identifier within retained data |
| `_time`, `_time_inherited` | TIMESTAMP_NS (UTC convention), BOOLEAN |
| `_source`, `_service`, `_stream` | VARCHAR; existing signal, service, and stream identifiers |
| `trace_id`, `span_id`, `parent_span_id` | VARCHAR; correlation and span hierarchy |
| `severity`, `message`, `duration_ms` | VARCHAR, VARCHAR, DOUBLE; common nullable fields |
| `attributes` | JSON; other normalized field paths and values |
| `_raw` | Original captured record, preserved losslessly |

Use `all_records`, a logical UNION ALL view with explicit compatible columns over the `records` table and the Parquet archives. Filter by service and stream values rather than creating new physical tables or per-stream views. The existing Go `record.Record` remains the application-level representation. Custom fields are extracted and converted safely from JSON; values that fail conversion remain available in the raw data.

### Original records and schema discovery

**Open question:** Do we need separate original text files as a source of truth for new schemas?

Preserve original records, but do not assume permanent duplicate text files are necessary. Store the actual record captured by Loghorn, not a reconstructed JSON string. Use lossless bytes for non-text input if needed. This does not promise preservation of discarded input or the original OTLP wire payload. Keep effective timestamp, service and other normalization metadata separately so reprocessing does not change them.

When a new attribute or type setting is introduced, extract it from normalized JSON or reparse the original record using existing normalization and schema inference logic. Do not alter physical tables for every new attribute. Preserve the distinction between missing, null and invalid values where required by query semantics.

Retain existing text files and the writer during migration until raw-record completeness and crash recovery are verified. Decide with Jim whether permanent text files, a temporary durable recovery journal, or DuckDB alone are appropriate. Do not delete existing text logs in this prototype.

### Parquet historical storage

Keep active records in DuckDB. Export committed records from closed periods to Parquet, including raw records, effective metadata and identifiers. Start with one archive per closed period, holding both signals, rather than per signal, service or stream; avoid excessive small files. The Parquet physical schema need not mirror DuckDB’s schema, provided the canonical fields are compatible.

Read active and archived records through DuckDB. Validate an archive before publishing it (row counts, identifiers, representative raw values). Avoid duplicate reads and retain original data if export fails. Remove archived hot records only after validation and safe retention rules are established. Basic archival is part of this design, not an indefinite deferred goal.

### Queries and integration

Reuse existing ingestion, normalization and REST contracts. Start with insert, record lookup, time/service/stream filters, custom-field filtering and trace ID lookup. Translate validated requests into parameterized SQL. Migrate Samples, Table, Graph, Heatmap, and the remaining endpoints incrementally. Keep existing trace-building logic where useful after SQL selects relevant records. Report endpoints not yet migrated rather than claiming full compatibility.

Use simple batched writes if necessary, keep writes off the query path where practical, flush on orderly shutdown, and handle write failure or queue saturation visibly. Do not claim lossless recovery before testing it.

## Implementation milestones

1. **Basic DuckDB prototype:** create the fixed `records` table in one file, ingest representative logs and spans, retain raw records, query through `all_records`, and demonstrate restart persistence.
2. **Minimal integration:** connect the existing normalizers and basic web query path. Verify filters, record details, and trace correlation. Extend remaining endpoints incrementally.
3. **Basic Parquet path:** export a closed period safely, query historical plus active records without duplicates, and verify historical raw-record reprocessing.
4. **Review:** measure representative workloads. Investigate separate signal tables or files, promoted fields, or other changes only when a concrete issue warrants them.

## Testing and acceptance

Use existing `testdata/web` fixtures and the Spike 2 dataset shape (approximately 770,000 records) if available. Include correlated logs and spans from multiple services, new attributes, missing/null/invalid fields, type changes, and inherited timestamps. Keep `query.Run` as a reference until migrated behaviors are verified.

Verify restart persistence, raw preservation, schema reinterpretation, archive readback, trace relationships, and failure handling. Test startup separately from migration or recovery. Measure representative end-to-end API latency, startup time, memory, and disk size; record the dataset, hardware, query and warm/cold conditions. Investigate representative queries above approximately 500 ms. Characterize larger historical datasets only when needed. Do not promise every query over 100 GB will finish in 500 ms.

## Decisions and open questions

Confirm with Jim: exact storage directory and owner model; the need for separate original text files after recovery testing; retention and archive boundaries; and numerical/query-latency expectations. Reconcile the earlier decisions on dynamic tables, per-repository storage, permanent text persistence and exact parity in `docs/DECISIONS.md` after review. Driver choice, batching, and exact SQL are left to the builder.

Basis: original DuckDB storage design (2026-11-23), Jim’s review comments, and `docs/superpowers/specs/2026-11-19-architecture-working-notes.md` in Loghorn. The revised design is a proposal, not an implementation or benchmark result.
