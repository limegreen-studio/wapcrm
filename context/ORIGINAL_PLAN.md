# TeleCRM-FOSS Plan: Smart / Low-Effort Version

**Goal**: High-quality, self-hostable AGPL WhatsApp-first sales CRM that feels like TeleCRM for WhatsApp-centric teams — without building a full CRM platform from scratch.

**Core principle**: Do less. Reuse Whatomate’s existing strengths aggressively. Only build what is truly missing and high-leverage.

**What already exists in Whatomate (leverage this)**:
- Real-time WhatsApp inbox + chat
- Contact management with assignment, tags, notes, Metadata JSONB
- CSV import/export
- Chatbots, flows, canned responses, campaigns
- Teams, roles, multi-tenant
- Voice calling + recordings
- Webhooks and REST API

**What is actually missing for a TeleCRM-like product**:
1. Visual pipeline (Kanban) with stages
2. Simple tasks / follow-ups
3. Better structured custom data on leads (initially just use Metadata JSONB)
4. Lightweight assignment rules and basic automation

**Non-goals (explicitly deferred)**:
- Full dynamic schema / custom objects engine
- Dedicated CustomFieldDefinition tables (use JSONB first)
- Complex workflow builder
- Native PSTN dialer
- Email/calendar sync
- Perfect reporting

---

## Phase 0: Quick Analysis & Scope Lock (2–3 days)

**Do this once, then stop analyzing.**

1. Clone both repos:
   ```bash
   git clone https://github.com/shridarpatil/whatomate.git
   git clone https://github.com/twentyhq/twenty.git
   ```
2. In Whatomate, look only at:
   - `internal/models/` → Contact struct and Metadata JSONB
   - How tags, notes, and assignment already work
   - Existing webhook and campaign systems
3. In Twenty, look only at:
   - Pipeline / stage / Kanban UX patterns
   - Task model
   - How they attach activities to records
4. Write a 1-page note:
   - What we will reuse as-is
   - The 3–4 new things we will build
   - Decision: custom fields = structured keys inside existing Metadata JSONB for v1

**Checkpoint**: One-page decision note exists. No more research.

---

## Phase 1: Pipeline MVP (1–2 weeks) — highest leverage

**Make the product feel like a sales CRM.**

Build only:
- `Pipeline` and `PipelineStage` models (minimal)
- Add `pipeline_id`, `stage_id`, `stage_entered_at` to Contact (or a thin Lead view)
- Kanban board UI as the main new screen
- Drag-and-drop between stages
- Stage change logged as a note or simple activity

Reuse everything else (assignment, tags, notes, chat, Metadata).

**Checkpoint**:
- [ ] Pipeline with stages exists
- [ ] Contacts appear on Kanban and can be moved
- [ ] Moving a stage is visible in the existing chat/notes view

---

## Phase 2: Tasks + Follow-ups (1 week)

**The daily operational layer.**

Build only:
- Simple `Task` model (title, due_at, assignee, contact_id, done)
- “Create follow-up” button from chat and from lead
- Overdue / upcoming task list
- Basic reminder (in-app or webhook)

Do **not** build a full activity timeline yet. Reuse existing notes and message history.

**Checkpoint**:
- [ ] Agent can create a task linked to a contact from the chat
- [ ] Tasks are filterable by due date and assignee

---

## Phase 3: Make Custom Data Usable (3–5 days)

**Smart shortcut**: Do not build a custom field definition system yet.

- Define a small set of conventional keys inside Contact.Metadata (e.g. `source`, `budget`, `interest`, `city`, `status`)
- Show them cleanly in the lead detail panel
- Allow editing them in the UI
- CSV import maps columns into these Metadata keys
- Optional later: admin UI to list/rename the keys

This is “good enough” for most TeleCRM-style use cases and avoids a complex schema layer.

**Checkpoint**:
- [ ] Important lead fields are visible and editable without touching raw JSON
- [ ] CSV import populates them

---

## Phase 4: Lightweight Automation & Capture (1 week)

Reuse existing systems instead of building a workflow engine.

- Webhook endpoint that creates/updates a Contact and puts it in a pipeline stage
- Simple round-robin assignment (reuse existing team/assignment logic)
- Use existing chatbot flows or canned responses for “no reply in X hours” follow-ups
- Optional: one or two hardcoded automation rules (new lead → assign + send template)

**Checkpoint**:
- [ ] External system can create a lead via webhook and it lands on the Kanban
- [ ] Round-robin works
- [ ] At least one automated follow-up path exists

---

## Phase 5: Polish & Ship (1 week)

- Basic conversion numbers (leads by stage, by source)
- Service-window indicator on lead card
- Documentation: how to self-host, how to use pipelines, how to send leads via webhook
- Performance indexes on stage + assignee + last activity

**Checkpoint**:
- [ ] Small team can run daily sales work inside the product
- [ ] Self-host docs are usable

---

## Total estimated effort for usable MVP

**~5–7 weeks** of focused work (instead of 12–16+ in the previous plan).

After this you have something that already feels like a WhatsApp-first TeleCRM alternative. Further features (proper custom field definitions, full activity timeline, advanced automations, telephony) can be added only if users actually ask for them.

---

## Smart Design Rules

1. Prefer extending Contact over creating a separate Lead table.
2. Prefer Metadata JSONB + convention over a custom field definition system for v1.
3. Prefer reusing notes, tags, chatbots, and webhooks over building new systems.
4. Ship the Kanban + tasks first. Everything else is secondary.
5. Stop analyzing after Phase 0. Build.

---

**Next action**: Clone both repos, spend 2–3 days on the one-page decision note, then immediately start the Pipeline models and Kanban. That is the highest-leverage piece of work.