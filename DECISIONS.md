# WAPcrm — Decisions

Design record for WAPcrm by LGS. The product's working name was TeleCRM-FOSS, so older entries use that name and the upstream name Whatomate.

Commit hashes below refer to the pre-rebrand fork. That history was intentionally dropped when this repository was created, so those hashes do not resolve here. "Uncommitted on `telecrm-pipeline`" describes the state at the time; all of that work is in this repository's first commit.

## Phase 0 Decision Note

Date: 2026-11-08 · Base: `whatomate` @ `047127c` (AGPL-3.0) · Reference only: `twenty` @ `fdb60554`

## Reuse as-is (Whatomate)

| Need | Already there |
|---|---|
| Lead record | `models.Contact` (`internal/models/models.go:345`) — org-scoped, `AssignedUserID`, `Tags` (JSONB array), `Metadata` (JSONB object), `LastInboundAt` |
| Notes / activity log | `ConversationNote` + `/api/contacts/{id}/notes` CRUD |
| Assignment | `internal/assignment` — `AssignToTeam` with round_robin / load_balanced / manual, Redis-cached team config |
| Outbound events | `Webhook` model + `webhook_dispatch.go` (HMAC); events in `models/constants.go:213` |
| CSV import/export | `handlers/import_export.go` — config-driven `exportConfigs` / `ImportConfig` with column mapping |
| Service window | Chat already computes `service_window_open`; reuse for lead cards |
| Permissions | Resource/action RBAC in `models/roles.go`; add new resources there |
| Background loops | `SLAProcessor` pattern (ticker + ctx) — copy for task reminders |
| Migrations | GORM `AutoMigrate` via `database.GetMigrationModels()` — just append models |
| Frontend | Vue 3 + Pinia + reka-ui/shadcn-vue, `stores/*.ts`, `services/api.ts`, views under `views/` |

## Patterns borrowed from Twenty (ideas, no code copied)

- Opportunity = `stage` + float `position` per card → order within column without renumbering.
- Task = `title, dueAt, status, assigneeId` + target link to record. We use single `contact_id`, no TaskTarget join.
- Board = columns from stage list; card drag updates `stage` + `position` in one PATCH; optimistic UI.
- Timeline activity = event rows with `properties` JSON. **Deferred** — we write a `ConversationNote` instead.

## What we build (only these)

1. **Pipeline + Kanban** — `Pipeline`, `PipelineStage` (name, color, `position`, `is_won`/`is_lost`). Contact gets `pipeline_id`, `stage_id`, `stage_entered_at`, `pipeline_position` (float). Endpoint `PUT /api/contacts/{id}/stage` writes stage + auto `ConversationNote` ("Moved from X to Y"). New `views/pipeline/PipelineView.vue`, DnD via native HTML5 drag or `vuedraggable` (decide in Phase 1, prefer zero new deps).
2. **Tasks** — `Task` (org, contact_id, assignee_id, title, due_at, done_at, created_by). List/filter API (due range, assignee, overdue). "Follow-up" button in chat + Kanban card. Reminder loop modelled on `SLAProcessor`, fires in-app WS notification + new webhook event `task.due`.
3. **Lead fields on Metadata** — fixed key set under `metadata.lead.{source,budget,interest,city,status}`, namespaced so existing metadata is untouched. Editable panel in chat sidebar + `ContactDetailView`. Add `lead.*` columns to contacts import/export config.
4. **Lead capture + auto-assign** — inbound `POST /api/leads` (API-key auth, upsert by phone, set pipeline/stage/lead fields), then `AssignToTeam` round-robin if team configured. New webhook events `contact.stage_changed`, `task.due`.

## Decisions

- **Custom fields v1 = structured keys inside existing `Contact.Metadata` JSONB.** No definition tables.
- **No separate Lead table.** Contact is the lead.
- One pipeline per org is enough for v1, but schema allows many.
- Stage history = notes, not a new table. Add `stage_entered_at` for "days in stage".
- Note: `MetadataPanel.vue` is audit info (created/updated by). Contact.Metadata **is** shown read-only in chat via `components/chat/MetadataSection.vue` (inside `ContactInfoPanel.vue`) — Phase 3 makes the `lead.*` keys editable there.
- Twenty is AGPL too, but we copy no code from it — patterns only.

## Phase 1 starts with

`internal/models/pipeline.go` → register in `GetMigrationModels()` → handlers + routes → `ResourcePipelines` permission → `stores/pipelines.ts` → `PipelineView.vue`.

**No more research.**

## Phase 1 decisions (as built)

- Permissions: board + stage moves use `chat:read` / `chat:write`, scoped like the contact list (no `contacts:read` → only own/transferred contacts). Pipeline create/edit/delete uses `settings.general:write`. No new permission resource, because `FixSystemRolePermissions` never adds new permissions to existing roles.
- First `GET /api/pipelines` per org lazily creates "Sales": New, Contacted, Qualified, Negotiation, Won, Lost.
- Card order: float `pipeline_position`, gap 1024, midpoint on insert.
- Stage change writes a `ConversationNote`, broadcasts WS `contact_stage_changed` to the org, fires webhook `contact.stage_changed`. Reorder within a stage writes nothing.
- A stage holding contacts cannot be deleted (409). Deleting a pipeline clears contacts' pipeline fields.
- DnD: native HTML5, no new dependency.


## Phases 2–5 decisions (as built)

- **Tasks**: `tasks` table (contact, assignee, title, notes, due_at, completed_at, reminder_sent_at). Same permission model as pipelines. Assignee defaults to creator. "Today/overdue" bounds are computed by the client in its own timezone and sent as `due_before` / `due_after`.
- **Reminders**: run on the existing SLA processor tick (1 min). Tasks are claimed with `UPDATE … FOR UPDATE SKIP LOCKED … RETURNING`, so several server instances never send the same reminder twice. One reminder per due time; changing `due_at` re-arms it. Sends WS `task_due` to the assignee and webhook `task.due`.
- **Task WS broadcasts carry ids only** (`task_changed`); clients refetch through their own scope, so task titles never reach users who cannot see the contact.
- **Lead fields**: `metadata.lead.{source,budget,interest,city,status}`, merged with one `jsonb_set` statement so other metadata keys survive concurrent writes. Edited with `chat:write` (agents lack `contacts:write`). CSV import/export gained `stage` + `lead_*` virtual columns via `ExportConfig.ColumnExpr` and `ImportConfig.ExtraColumns/AfterWrite`.
- **Lead capture**: `POST /api/leads` (`contacts:write`, API key). Upsert by digits-only phone; never replaces an existing owner; stage changes on existing contacts only when asked. Validates everything before writing. Auto-assign via `Assigner.AssignToTeam` with a new `LeadLoadCounter` (open-stage contacts per agent). Optional auto follow-up task = the automated follow-up path.
- **Stats**: `GET /api/pipelines/{id}/stats` — by stage, by source (with won), win rate = won/(won+lost), overdue tasks, open leads quiet 7+ days (`COALESCE(last_message_at, created_at)`).
- **Indexes added**: board `(org, pipeline, stage, position)`, `(org, assigned_user_id, last_message_at DESC)`, `(org, stage, assignee)`, partial task indexes for open/due/reminder.
- **Docs**: `docs/.../features/sales-pipeline.mdx`, `docs/.../api-reference/sales-pipeline.mdx`; webhook + import docs updated.

## Decision log (awaiting user review, 2026-11-08)

Claude chose these when asked to decide; the user will double-check them.

| # | Question | Options | Chosen | Why |
|---|---|---|---|---|
| D1 | How to commit | one commit / one per phase | One commit for phases 1–5 (`e699e72`), then one per gap | Phase files interleave (same handlers/views touched by several phases), so per-phase splits would be artificial. |
| D2 | Permissions for board, tasks, lead fields | new `pipelines`/`tasks` permission / reuse `chat:*` | Reuse `chat:read` / `chat:write`; pipeline admin = `settings.general:write`; lead API = `contacts:write` | `FixSystemRolePermissions` never adds new permissions to existing roles, so a new resource would lock everyone out after upgrade. Agents still only see their own leads via the existing `scopeAssignedContact` rule. |
| D3 | Lead fields fixed or admin-editable | fixed in code / admin-editable keys | **Revised (user asked to close gaps): admin-editable per org** in `Organization.Settings.lead_fields` (key, label, options; max 20). Keys are immutable once saved and become CSV column `lead_<key>`; removing a field hides it but keeps stored values. Defaults: source, budget, interest, city, status. | Keys locked so CSV/API integrations don't break silently; settings JSONB avoids a definitions table (plan rule 2). Commit `d2ce958`. |
| D4 | Translations for new UI | English only / translate all locales | **Revised: translated into hi, es, pt, ar, ta** (all 5 existing locales; 137 strings each), checked for key parity and placeholder parity against en. Not reviewed by native speakers. | Hindi/Tamil matter for the Indian TeleCRM market. Commit `a503545`. Native review recommended. |
| D5 | "No reply in X hours" automation | rule engine / one hardcoded rule / webhooks only | **Revised: one built-in rule** (`Organization.Settings.no_reply_rule`), not an engine. Leads on open stages whose last message is ours and older than N h (1–720): create owner follow-up and/or send an approved non-media template (`{{name}}`/`{{phone}}` fill). Fires once per silence (`contacts.no_reply_handled_at`); claimed with SKIP LOCKED. Acts as lead owner, else as the admin who saved the rule. | Plan allows "one or two hardcoded automation rules". Cost/policy risk mitigated by: admin-only, approved templates only, once per silence, caution in docs. Commit `80dc7a8`. |
| D6 | Board column size | load everything / page | 50 cards per column + "Show more" per column (`?stage_id=&offset=`) | Keeps the board fast for large pipelines. Filled in commit `c5e500b`. |
| D7 | Follow-up assignee | always creator / picker | Picker for users with `users:read` (managers/admins), else creator | Agents cannot list users (same rule as the chat assign menu). Filled in commit `95a8a7f`. |
| D8 | Brand-new lead without messages: "stale"? | yes / no | No: staleness uses last message, else creation time | A lead created 1 minute ago is not quiet. |
| D10 | Two simultaneous captures of one new phone | 500 on the loser / treat as update | Treat as update (`ON CONFLICT DO NOTHING`, then load) | Ad platforms retry and double-submit; both calls should succeed. Commit after `95a8a7f`. |
| D11 | Reassign existing follow-ups | no / from Tasks page | From Tasks page, same `users:read` rule as D7 | Commit `c24ee59`. |
| D9 | Re-capturing an existing lead via API | overwrite / merge | Merge: never replace owner or name; stage moves only if `stage`/`pipeline_id` sent | Forms often resubmit; overwriting would steal leads from agents. |

## Known gaps / next if users ask

- Translations (D4) are not native-speaker reviewed.
- No-reply rule (D5) is one rule per org; no per-pipeline or multi-step sequences.
- Template send from the no-reply rule was tested against a mock WhatsApp API only (no Meta account in dev).

## Backlog status (2026-11-09)

Items 3-17 and the two item 19 problems are built, uncommitted on `telecrm-pipeline`. Go tests, type check, unit tests and 8 Playwright specs pass.

**#2 done 2026-11-09:** `hello_world` sent through the no-reply test endpoint on real Meta, and `delivered` came back by webhook. The timed rule itself has not fired on real Meta; it stays off.

Still open:
- **#14** Facebook Lead Ads tested against a fake Graph API only.
- **#13** SMTP sending not run against a real mail server.
- **#18** Native-speaker review of hi / ta / ar / es / pt (not reviewed; 120+ new strings added).
- **#20** Resolved 2026-11-09: no upstream PR. Product moved to `limegreen-studio/wapcrm` (D30-D34).

### Decisions (awaiting user review)

| # | Question | Chosen | Why |
|---|---|---|---|
| D12 | Who sees the unassigned queue | Anyone with `chat:write`; preview hidden until claim | Agents have no other way to pick up leads; hiding the preview limits exposure. |
| D13 | Multi-step timing | Each step counts from the previous step | A sent template updates `last_message_at`, so counting from our last message would drift. |
| D14 | Per-pipeline rule | Overrides the org rule even when disabled | Gives a way to turn automation off for one pipeline. |
| D15 | Duplicate detection | Same last 10 digits, human confirms each merge | Numbers in different countries can collide, so merging is never automatic. |
| D16 | Stage history | New table from now on; no backfill | Old moves exist only as free-text notes. |
| D17 | Outside-app alerts | Email (optional SMTP) and browser notifications; no daily digest | Smallest set that reaches someone away from the page. |
| D18 | Public form auth | Secret URL token, honeypot, no pipeline/owner control | Browser forms cannot hold an API key. |

| D19 | Automation engine | Trigger + filters + ordered actions in `automation_rules`; delayed steps in `automation_jobs`, run on the SLA tick | Reuses capture, assignment and template paths; no visual builder (plan non-goal). |
| D20 | Can rules loop | Actions never fire rules | Removes the need for cycle detection. |
| D21 | Template spend guard | One rule, lead and template at most once per 24h (Redis); skip marketing opt-outs | Bulk moves and form spam can refire triggers. |
| D22 | Drip cancellation | Dropped if the lead replies or leaves the stage | Avoids nagging a lead who already answered. |
| D23 | Bulk template permission | `contacts:write` | Templates cost money; agents lack it by default. |

| D24 | Repeat deals | One active deal per contact; each closed deal kept in `stage_history` with its value, split by `deal_seq` | Fixes lost repeat revenue without replacing the contact-based board. Two parallel deals on one contact are still not possible. |
| D25 | Speed-to-lead scope | Only leads created after the rule is on; first human reply; max 3 moves; respects chatbot business hours | Prevents a mass reshuffle of existing leads. |
| D26 | Team-manager visibility | Team role `manager` adds members' leads to the existing scope | Reuses the existing role; widens access for current team managers on upgrade. |
| D27 | Push endpoints | Allowlist of FCM, Apple, Mozilla, Windows hosts | The URL comes from the client, so an open list would allow request forgery. |
| D28 | Quote sending | Free text inside the 24-hour window only; otherwise copy the link | WhatsApp rejects free text outside it. |
| D29 | Forecast | Open deal value times stage probability | Simple and visible; no historical modelling. |

IndiaMART and JustDial capture skipped at the user's request (2026-11-09).

Fixed along the way: concurrent first leads created duplicate default pipelines (advisory lock in `createDefaultPipeline`).

## Rebrand to WAPcrm (2026-11-09)

The owner asked for the WAPcrm name, a Nord theme with a tint of green, and a new repository with old history removed. The details below were chosen by Claude and await owner review.

| # | Question | Chosen | Why |
|---|---|---|---|
| D30 | How deep the rename goes | Everything: UI, docs, Go module, binary, `WAPCRM_` env prefix, default DB and Docker names, Redis stream and channel names, JWT issuer, webhook User-Agent | No production deployment exists yet, so this is the cheapest moment. Issuer is not validated, so old tokens keep working. Anything integrated with the old env vars or User-Agent must be updated. |
| D31 | Theme mechanism | Nord tokens in `index.css`; stock Tailwind hues remapped to Nord in `tailwind.config.cjs`; hardcoded dark surfaces replaced with semantic classes | About 1,000 color utilities across 56 files change from one place. Status colors keep their meaning. |
| D32 | What "tint of green" means | Polar Night surfaces stay canonical; Aurora green `#A3BE8C` is primary, with a green-tinted hover surface and page glow | Keeps it recognizably Nord. Light mode uses `#54743D` because nord14 fails contrast on light surfaces. |
| D33 | WhatsApp previews | Flow and template previews keep real WhatsApp colors | They simulate how a message looks in WhatsApp. |
| D34 | CI on the new repo | Docker Hub removed, GHCR only. Develop image and docs deploy are manual until the owner enables them. Tests still run on push | Avoids Actions minutes on image builds, and GitHub Pages from a private repo can be public. |
