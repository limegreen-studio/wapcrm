# WAPcrm

WhatsApp-first sales CRM, a product by LGS. Self-hosted, single Go binary with an embedded Vue frontend.
Forked from Whatomate (AGPL-3.0). See `NOTICE.md` before touching licensing or attribution.

Read these first:

- `DECISIONS.md`: what was built and why, including the decision log.
- `context/ORIGINAL_PLAN.md`: the plan the product started from. It uses the working name "TeleCRM-FOSS".

## Product direction

- Focus is the sales CRM side: pipeline, follow-ups, lead capture, automation, quotes, forecast. Telecalling is not the focus.
- IndiaMART and JustDial capture were skipped on purpose.
- Extend `Contact` rather than adding a Lead table. Prefer `Metadata` JSONB and existing notes, tags, chatbots and webhooks over new systems.
- Non-goals: custom objects engine, visual workflow builder, native PSTN dialer, email or calendar sync.

## Stack and layout

- Backend: Go, fastglue/fasthttp, GORM on Postgres, Redis. Entry point `cmd/wapcrm`. Module `github.com/limegreen-studio/wapcrm`.
- Frontend: Vue 3, Pinia, shadcn-vue/reka-ui, Tailwind 3 in `frontend/`. Production build is embedded from `internal/frontend/dist` (git-ignored).
- Docs site: Astro Starlight in `docs/`.
- Migrations: GORM `AutoMigrate` through `database.GetMigrationModels()`; append new models there.
- Config: `config.toml` (git-ignored; start from `config.example.toml`). Environment overrides use the `WAPCRM_` prefix and a double underscore for nesting, for example `WAPCRM_DATABASE__HOST`.

## Commands

```bash
# One-time local test setup
createdb wapcrm_test
redis-server --port 6390 --daemonize yes

# Backend tests
TEST_DATABASE_URL="postgres://$USER@localhost:5432/wapcrm_test?sslmode=disable" \
  TEST_REDIS_URL="redis://localhost:6390/0" go test ./...

# Frontend checks (run in frontend/)
npx vue-tsc --noEmit
npx vitest run
npx vite build

# Run locally
make run-migrate       # backend, needs config.toml
cd frontend && npm run dev
```

Dev database notes: the database password in config must be non-empty (any value such as `unused`) or the DSN breaks. Seed login is `admin@admin.com` / `admin`.

## Brand and theme rules

- Product name is written `WAPcrm`. Identifiers use `wapcrm` and `WAPCRM_`.
- Theme is Nord with Aurora green (`#A3BE8C`, nord14) as the accent. Light mode uses a darkened green (`#54743D`) so text passes contrast.
- Use semantic classes (`bg-background`, `bg-card`, `bg-popover`, `text-primary`, `border-border`). Do not add raw hex surfaces.
- `frontend/tailwind.config.cjs` remaps the stock Tailwind hues to Nord, so `text-emerald-400` or `bg-red-500` already render as Nord. `white` is a CSS variable: Snow Storm in dark mode, true white in light mode.
- Exceptions: `components/chatbot/flow-preview/` and the template previews deliberately keep real WhatsApp colors, because they simulate WhatsApp itself.
- Keep body text at 4.5:1 contrast or better. Green at shade 500 is too light for white text; use shade 600 or `bg-primary text-primary-foreground`.

## Working rules

- Do not remove `LICENSE` or `NOTICE.md`, and do not strip the Whatomate attribution there.
- Entries in the `DECISIONS.md` decision log were chosen by Claude and await owner review. Do not treat them as confirmed. Add new choices there as the next `D` number.
- Never commit `config.toml`, `.env` files, or Meta tokens.
- Any test against a real WhatsApp number can cost money and reach real people. Send only to a number the owner names. Keep the no-reply rule off unless asked.
- VAPID keys in a deployed config must not be regenerated, because that breaks existing browser push subscriptions.
- Local test stack lives outside this repo at `../podman-test` and holds secrets. Do not copy it in.

## Open items

- Facebook Lead Ads tested against a fake Graph API only.
- SMTP sending not run against a real mail server.
- Translations (hi, ta, ar, es, pt) not reviewed by native speakers.
- Push alerts not tested on a real subscribed device.
- The timed no-reply rule has not fired on real Meta.
- Most screenshots in `docs/public/images` still show the old name and theme.
