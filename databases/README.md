# databases

One project on three SQL databases: Postgres, MySQL and SQLite. It shows
the column types and functions that only one database has, and what the
compiler emits when the same DQL query runs on all three.

## Domains

- **catalog** (`proto/domains/catalog/`): documents and estates on Postgres
  (the `catalog-postgres` connection), with hstore, ltree and PostGIS
  columns, behind a REST API. The end-to-end suite drives this domain.
- **dialects** (`proto/domains/dialects/`): sites on MySQL (`spatial`, the
  domain's connection) and sensor readings on SQLite (`local`, which
  overrides it). A module has one connection; a domain may have several.

`proto/shared/estate` is a model shared between the two: `catalog` stores
it in its own table, and `dialects` only uses it as the shape of a nested
message.

## Postgres types and functions

Postgres columns can take types the cross-dialect vocabulary cannot name.
They are declared in a registry, and a column refers to an entry by its
alias:

| Where | Annotation | What it declares |
|---|---|---|
| `proto/project.proto` | `(w17.pg.project)` | `tag_map` → `HSTORE` (extension `hstore`), with conversions both ways; the functions `string_agg` (an aggregate) and `array_to_string` |
| `proto/domains/catalog/w17.proto` | `(w17.pg.module)` | `taxonomy_path` → `LTREE` (extension `ltree`); `nlevel`, `akeys`, `unaccent` and the PostGIS functions |
| `proto/domains/catalog/docs/types/models.proto` | `(w17.pg.field)` | `Document.labels` → `tag_map`; `Document.path` → `taxonomy_path`, plus `btree_gist` for this column |

- Entries in `project.proto` are visible to every domain; entries in a
  domain's `w17.proto` only to that domain. An alias declared on both
  levels is refused.
- `custom_type` takes an alias, not raw SQL. The registry entry carries the
  extension the type needs, and the conversion a later type change renders
  its `USING` clause from.
- The column stays an ordinary `string` in the proto and a `TEXT` column in
  the vocabulary; only the SQL column type changes. String-only rules
  (`min_len`, `max_len`, `pattern`, `choices`, `blank`) are refused on such
  a column, because the CHECKs they generate do not type-check against
  `hstore` or `ltree`.
- Every extension a type or function needs, `unaccent` included (no column
  uses it), becomes a prerequisite of the migration plan, created before
  the table that needs it.

The geometric columns use `(w17.db.column).db_type: GEOMETRY` and
`GEOGRAPHY`, with a GIST index; `DocumentsCovering` asks which footprints
contain a point.

## MySQL and SQLite types

`(w17.mysql.field).custom_type` and `(w17.sqlite.field).custom_type` take
the SQL type itself: neither database has extensions, so there is no
registry.

| Column | Database | Declared type | Why |
|---|---|---|---|
| `Site.footprint` | MySQL | `GEOMETRY SRID 4326` | the SRID qualifier is MySQL syntax |
| `Site.flags` | MySQL | `SET('draft','review','published')` | several values from a fixed list in one column |
| `Reading.magnitude` | SQLite | `NUMERIC` | a storage affinity, which only SQLite has |
| `Reading.payload` | SQLite | `BLOB` | and so no Unicode `COLLATE`, which SQLite text columns get |

A field takes at most one database's `custom_type`, so these live in their
own domain rather than as more columns on `Document`.

## One query, three databases

Most queries are declared three times with the same DQL, once per module:
`docs.DocumentQuery` (Postgres), `spatial.SiteQuery` (MySQL) and
`local.ReadingQuery` (SQLite). The comments on each say what the compiler
emits for that database. Among them:

- `EXTRACT(QUARTER …)` and `EXTRACT(DOW …)`: SQLite has neither, MySQL
  numbers weekdays from 1.
- Integer and decimal division, a float cast to an integer, a bool cast to
  a string: each database answers differently unless the compiler rewrites
  the expression.
- `NULLS LAST`, `FILTER`, `GROUP BY ROLLUP`, JSON paths, window frames,
  upserts.
- Postgres-only constructs (`DISTINCT ON`, `INTERVAL`, `LATERAL`,
  `INTERSECT` / `EXCEPT`, a data-modifying CTE), which the compiler refuses
  on the other two.

`spatial.SiteMutation.RenameSite` is one method over both MySQL and SQLite,
with an `execution_plan` that runs its ops out of source order.

## Tests

The suite in `w17/e2e/catalog/tests/rest/` runs against the Postgres
domain over REST, from the rows in `fixtures/catalog/test.json`; most cases
say in a comment what they check and why the seed is shaped the way it is.
The MySQL and SQLite modules are generated and built; nothing calls them
over the network yet.

## Run it

```sh
w17ctl codegen
w17ctl test
```

`w17ctl test` brings the stack up from `compose.yaml` and runs the suite.
`compose.databases.yaml` adds the step that seeds the rows before the
storage service starts.
