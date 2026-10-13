# New database schema: implementation brief

A brief for whoever (person or agent) implements the schema drawn in
[`db-schema-2.png`](db-schema-2.png).
The schema in use today is [`db-schema.png`](db-schema.png).
Nothing below is implemented yet.

## Goal

Make `templates` a table and the hub of the schema, and declare the
relationships as real foreign keys:

| change | rule |
|--------|------|
| new table `templates` (`id`, `name` unique, `builtin`, `topology` JSON, `created_at`, `updated_at`) | replaces `templates.json` |
| `deployments.template_id` → `templates.id` | nullable, `ON DELETE SET NULL`, index `deployments_template` |
| `runs.template_id` → `templates.id` | nullable, `ON DELETE SET NULL`, index `runs_template` |
| `faults.slug` → `deployments.slug` | `ON DELETE CASCADE` (the index `faults_slug` already exists) |

`meta` stays as it is. `runs.template` (the name) and `runs.body` stay too: a
run must remain readable after its template is deleted.

This is about integrity and queryability, not speed. Every hot lookup is
already by primary key or an index.

## Where things are today

- `src/engine/store/store.go`: the schema (`schema` const), `schemaVersion = 3`,
  `Open`, deployments, faults, `ImportLegacy`.
- `src/engine/store/runs.go`: `runsSchema`, runs, `ImportRuns`.
- `src/engine/library/library.go`: saved templates in `templates.json` under
  the user config directory (`List`, `Save`, `Delete`, matched by name).
- `src/engine/spec/presets.go`: the built-in templates, Go literals from
  `spec.Presets()`.
- `src/engine/backend/backend.go`: opens the store and builds the library.
- `src/engine/api/api.go`: the template calls the window and the CLI use
  (`a.lib.List/Save/Delete`).
- `src/engine/core/restore.go`: writes deployments (`store.Save`, `SetPhase`,
  `SetLoad`).
- `src/engine/tests/runs.go`, `runner.go`: write runs; a run knows its
  template only by `template.Name`.

## Steps

1. **Migrations.** `Open` runs `CREATE TABLE IF NOT EXISTS` and writes
   `PRAGMA user_version`, but never reads it, so there is no migration path
   yet. Read `user_version` first and, for a database below 4, run the upgrade
   in one transaction: create `templates`, `ALTER TABLE deployments ADD COLUMN
   template_id INTEGER REFERENCES templates(id) ON DELETE SET NULL`, the same
   for `runs`, create the two indexes, then set the version to 4. A new
   database gets the final schema directly.
2. **`faults` needs a rebuild.** SQLite cannot add a foreign key to an
   existing column. Create `faults_new` with the constraint, copy the rows
   whose `slug` still exists in `deployments`, drop the old table, rename, and
   recreate `faults_slug`.
3. **Turn foreign keys on.** Add `PRAGMA foreign_keys = ON` to the pragmas in
   `Open`. It is per connection; the store uses a single one
   (`SetMaxOpenConns(1)`), so once is enough. Run the `faults` rebuild with it
   off, as SQLite's documentation prescribes.
4. **Template methods on the store.** `Templates()`, `SaveTemplate(t)`
   (upsert by name, keep the `id`), `DeleteTemplate(name)`,
   `TemplateID(name)`. Keep the nil-`*Store` convention: every method on a nil
   store is a harmless no-op.
5. **Seed the built-ins.** On open, upsert `spec.Presets()` with
   `builtin = 1`, so a new release refreshes them. `DeleteTemplate` must
   refuse a built-in.
6. **Import `templates.json` once.** Same pattern as `ImportLegacy` and
   `ImportRuns`: guard with a `meta` key (`templates_imported`), leave the
   file where it is.
7. **Point the library at the store.** Keep the `library.Library` API (`List`,
   `Save`, `Delete` returning the list) so `api.go` and the CLI do not change,
   and back it with the store instead of the file. `List` returns saved
   templates only, as now; the built-ins keep coming from `spec.Presets()`.
8. **Fill `template_id`.** In `store.Save` (deployments) and `store.SaveRun`,
   resolve the id from the template name with a subquery
   (`(SELECT id FROM templates WHERE name = ?)`), which yields NULL for a
   topology applied from a file or under another name. Add the field to
   `store.Deployment` and `store.Run` only if a caller needs to read it.
9. **Simplify `Store.Delete`.** With the cascade, the explicit
   `DELETE FROM faults` goes away.
10. **Backfill.** In the migration, set `template_id` on existing
    `deployments` (by `name`) and `runs` (by `template`) where a template of
    that name exists.

## Things to watch

- **Two processes share the file.** The window and `symphony-cli` open the
  same database, possibly in different versions. The migration must be safe to
  run while the other process is attached: one transaction, `busy_timeout`
  already set.
- **The fallback database.** When the binary's directory is not writable the
  database lives in the config directory (`backend.openStore`); the import in
  step 6 must find `templates.json` in both cases.
- **Names are still the public identity.** The CLI, the API and the window
  address templates and deployments by name; `id` stays internal. Do not
  change any command or JSON output.
- **Renames.** Saving a template under a new name creates a new row today
  (the library matches by name). Keep that behaviour.

## Tests

- `store_test.go`: opening a version 3 database migrates it and keeps its
  rows; deleting a deployment removes its faults; deleting a template nulls
  `template_id` and keeps the deployments and runs; the built-ins are seeded
  and cannot be deleted; `templates.json` is imported once.
- `go test ./...` in `src/` must pass, including `tests/runner_test.go` and
  the library's callers.

## Docs to update afterwards

- `docs/INTERNALS.md`, section *State*: templates are in the database, not in
  `templates.json`.
- Replace `db-schema.png` with the new diagram once the code matches it.
