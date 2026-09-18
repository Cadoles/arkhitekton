# 8. Data model

PostgreSQL holds all state. The object store holds what is large; the database keeps its
key. Migrations are embedded in the binary and applied by the `migrate` role — see
[10 §10.1](10-operations.md#101-schema-migrations).

Every table below carries `created_at`, and most carry `updated_at`; they are omitted
from the column lists for brevity when they add nothing.

## 8.1 Identity and access

| Table | Key columns | Purpose |
|---|---|---|
| `sessions` | `id`, `subject`, `username`, `email`, `expires_at`, `last_used` | Server-side sessions. The cookie carries the id, nothing else |
| `auth_flows` | `state`, `nonce`, `code_verifier`, `redirect_after`, `expires_at` | In-flight OIDC authorization codes. Purged when expired |
| `user_roles` | `subject`, `username`, `role`, `project_id`, `granted_by`, `last_seen` | A role, globally (`project_id` null) or in one project |
| `bootstrap_grants` | `identifier`, `role`, `claimed_at`, `claimed_by` | Roles waiting for their first sign-in, from `BOOTSTRAP_*` |
| `bootstrap_steps` | `step`, `applied_at` | Which one-off start-up steps already ran |
| `audit_log` | `actor`, `action`, `target`, `payload` | Every administrative act |
| `instance` | `lock_id`, `id` | The instance's own identity, one row. Labels PipelineRuns so that a run left over from another database is never mistaken for one of ours |

## 8.2 Projects

`projects` — one row per repository Arkhitekton manages:

| Group | Columns |
|---|---|
| Identity | `slug`, `label`, `repository`, `forge_url`, `tracker_project` |
| Build | `dockerfile`, `build_context`, `multi_stage`, `targets`, `stack` |
| Images | `image_registry`, `registry_secret` |
| Deploy key | `deploy_key_public`, `deploy_key_created_at`, `deploy_key_rotate_requested_at`, `deploy_key_active` |
| Releases | `version_scheme`, `release_kinds` |
| Health | `checked_at`, `check_ok`, `check_note` |

| Table | Purpose |
|---|---|
| `project_pipelines` | Which catalogue kinds this project enables |
| `project_plugins` | Per-project plugin state and settings. **Absence means inheritance** |
| `sync_state` | Cursor and last outcome of each source's synchronisation, per project |
| `issue_status_mapping` | How this project's tracker statuses map to buckets |

## 8.3 Versions and issues

| Table | Key columns |
|---|---|
| `versions` | `name`, `normalized_name`, `status`, `due_date`, `origin`, `tool`, `external_id`, `project_id` |
| `issues` | `version_id`, `subject`, `status_name`, `status_bucket`, `priority`, `assignee`, `done_ratio`, `is_closed`, `tool`, `external_id` |

`origin`, `tool` and `external_id` are what make a tracker optional: a version created
locally and one mirrored from Redmine differ by these columns alone.

## 8.4 Releases

| Table | Key columns |
|---|---|
| `releases` | `tag`, `kind`, `state`, `git_ref`, `git_branch`, `git_commit`, `image_version`, `version_id`, `source_release_id`, `notes_md`, `quality_score`, `manual_actions_md`, `released_at` |
| `release_images` | `name`, `repository`, `tag`, `digest`, `size_bytes` |
| `release_notes` | `source`, `pull_request`, `external_id`, `added_by` |
| `artifacts` | `kind`, `format`, `object`, `size_bytes`, `generated_by` — `object` is a key in the store |

`source_release_id` links a final release to the release candidate it was promoted from,
which is how a final release inherits results it did not produce itself.

## 8.5 Queue and runs

| Table | Key columns |
|---|---|
| `jobs` | `kind`, `queue_class`, `priority`, `state`, `params`, `release_id`, `project_id`, `requested_by`, `attempt`, `queue_reason`, `cancel_reason`, `error`, `run_name`, `exclusive_at_start`, `concurrent_at_start` |
| `runs` | `job_id`, `pipeline_name`, `pipelinerun_name`, `namespace`, `status`, `reason`, `results`, `observed`, `declared_tasks` |
| `task_runs` | `run_id`, `name`, `display_name`, `status`, `pod_name`, `log_object`, `log_sealed` |
| `queue_limits` | `kind`, `max_concurrent` — an administrator's override of the catalogue |

`runs.observed` distinguishes a run Arkhitekton created from one it merely watches.
`task_runs.log_sealed` says the log has been fully copied to the object store and will
not change again.

## 8.6 Results

| Table | Purpose |
|---|---|
| `test_results` | One row per report: `family`, `tool`, `status`, counts, `duration_ms`, `summary`, `raw_object`, `parsed` |
| `test_cases` | Individual cases of a result: `suite`, `name`, `status`, `duration_ms`, failure message and trace |
| `security_audits` | One row per audit: severity counts, deltas against the previous run, `failed_scanners`, `scanner_status`, `policy` |
| `security_findings` | One row per finding: `fingerprint`, `tool`, `rule_id`, `severity`, `cvss`, package and fix columns, `status`, `suppressed` |
| `bench_measures` | `scenario`, `metric`, `value`, `unit`, `lower_better`, `threshold_ok` |
| `bench_baselines` | The measure pinned as reference for a scenario and metric |

`security_audits.failed_scanners` is why a crashed scanner cannot pass for clean code: a
tool that failed is recorded as failed, not as zero findings.

## 8.7 Events, plans and automation

| Table | Key columns |
|---|---|
| `events` | `type`, `source`, `project_id`, `subject`, `payload`, `occurred_at`, `depth`, `dedup_key` |
| `reactions` | `name`, `event_type`, `enabled`, `match`, `plan` |
| `release_automations` | The plan a project runs for a release, per project |
| `plan_runs` | `trigger`, `event_key`, `plan`, `context`, `state` |
| `plan_steps` | `stage`, `position`, `kind`, `ref`, `state`, `job_id`, `detail` |
| `plan_artifacts` | What a step produced: `name`, `kind`, `reference` |

`events.depth` bounds cascades — a reaction that triggers an event that triggers a
reaction cannot recurse indefinitely. `dedup_key` makes a probe that sees the same fact
twice produce one event.

## 8.8 Plugin settings

| Table | Key columns |
|---|---|
| `plugin_settings` | `plugin_id`, `enabled`, `config`, `updated_by` — instance scope |
| `project_plugins` | Same, per project. A missing row means inheritance |

Secret fields inside `config` are sealed with `SECRETS_KEY`; the API never returns them.

## 8.9 Tables owned by the MSE plugin

These live in the same database but under the plugin's own migration history
(`goose_mse_db_version`), and are absent from the core's schema:

| Table | Purpose |
|---|---|
| `environments` | Declared environments per project: `rank`, `namespace`, `deployer_settings`, `observable`, `visible`, `security_policy` |
| `environment_observations` | What each environment runs: `declared_state` (what Flux applied), `portal_version` and `migration_state` (what the application attests), `resolution`, `git_tag`, `git_commit` |
| `image_promotions` | One row per promotion: `release_id`, `environment_id`, `image_name`, `tag`, `digest`, `job_id` |
| `release_signoffs` | Per environment sign-off on a release: `role`, `status`, `detail`, `decided_by` |

`resolution` is the hierarchy of evidence: `unobservable` (nothing can be asserted),
`declared` (Flux applied it), `attested` (the application reports it running with its
migrations done). Only the last one justifies saying "deployed".

## 8.10 Object store layout

    raw/<pipelinerun>/<artifact>           uploaded reports, verbatim
    raw/<pipelinerun>/meta/<tool>.json     the descriptor a tool emitted
    logs/<pipelinerun>/<task>/<step>.log   collected task logs
    artifacts/…                            exports produced by the dashboard

Every key segment is validated before use. Tags are user input, and S3 has no notion of
directory to object to `../`: the check is the only barrier, so a tag like
`../../artifacts` is refused rather than allowed to write outside its prefix.
