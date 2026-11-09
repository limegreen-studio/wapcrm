import { test, expect, Page } from '@playwright/test'
import { loginAsAdmin } from '../../helpers'

// Phone-width checks for the sales screens. Set SHOT_DIR to keep screenshots.
const PHONE = { width: 390, height: 800 }
const shotDir = process.env.SHOT_DIR

async function api(page: Page, method: string, path: string, body?: unknown): Promise<any> {
  return page.evaluate(async ({ method, path, body }) => {
    const csrf = document.cookie.match(/whm_csrf=([^;]+)/)?.[1] ?? ''
    const response = await fetch(`/api${path}`, {
      method,
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf },
      body: body === undefined ? undefined : JSON.stringify(body)
    })
    const json = await response.json()
    if (!response.ok) throw new Error(`${method} ${path}: ${JSON.stringify(json)}`)
    return json.data
  }, { method, path, body })
}

async function seedLead(page: Page, name: string): Promise<string> {
  const phone = `91${Date.now().toString().slice(-8)}${Math.floor(Math.random() * 90 + 10)}`
  const lead = await api(page, 'POST', '/leads', { phone_number: phone, name, tags: ['vip', 'hot-lead'] })
  return lead.contact_id
}

async function shot(page: Page, name: string) {
  if (shotDir) await page.screenshot({ path: `${shotDir}/${name}.png` })
}

async function expectFits(page: Page, label: string) {
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
  expect(overflow, `${label} scrolls sideways by ${overflow}px`).toBeLessThanOrEqual(0)
}

async function expectDialogFits(page: Page, label: string) {
  const box = await page.getByRole('dialog').boundingBox()
  expect(box, `${label} dialog is visible`).not.toBeNull()
  expect(box!.x, `${label} dialog left edge`).toBeGreaterThanOrEqual(0)
  expect(box!.x + box!.width, `${label} dialog right edge`).toBeLessThanOrEqual(PHONE.width)
}

test.describe('Phone width', () => {
  test.use({ viewport: PHONE })

  test.beforeEach(async ({ page }) => {
    await loginAsAdmin(page)
  })

  for (const [name, path, ready] of [
    ['board', '/pipeline', 'Filters'],
    ['tasks', '/tasks', 'All open'],
    ['reports', '/pipeline/reports', 'By period'],
    ['team', '/pipeline/team', 'Today'],
    ['automations', '/pipeline/automations', 'New automation']
  ] as const) {
    test(`${name} page fits`, async ({ page }) => {
      await seedLead(page, 'E2E Mobile Lead With A Fairly Long Name')
      await page.goto(path)
      await expect(page.getByText(ready, { exact: true }).first()).toBeVisible()
      await page.waitForTimeout(400)
      await expectFits(page, name)
      await shot(page, `phone-${name}`)
    })
  }

  test('chat sidebar fits', async ({ page }) => {
    const id = await seedLead(page, 'E2E Mobile Chat')
    await page.goto(`/chat/${id}`)
    await page.waitForTimeout(800)
    await expectFits(page, 'chat')
    await shot(page, 'phone-chat')
  })

  test('board menu and dialogs fit', async ({ page }) => {
    await seedLead(page, 'E2E Mobile Dialogs')
    await page.goto('/pipeline')
    await expect(page.getByText('Filters', { exact: true })).toBeVisible()

    const more = page.getByRole('button', { name: 'More' })
    for (const [item, name] of [
      ['Duplicates', 'duplicates'],
      ['Sales settings', 'sales'],
      ['Lead sources', 'sources'],
      ['No-reply rule', 'noreply'],
      ['Lead fields', 'fields'],
      ['Edit stages', 'stages']
    ] as const) {
      await more.click()
      if (item === 'Duplicates') await shot(page, 'phone-more-menu')
      await page.getByRole('menuitem', { name: item }).click()
      await expect(page.getByRole('dialog')).toBeVisible()
      await page.waitForTimeout(300)
      await expectDialogFits(page, name)
      await shot(page, `phone-dialog-${name}`)
      if (name === 'sources') {
        await expect(page.getByRole('dialog').locator('input[readonly]').first()).toHaveValue(/^https?:\/\/[^/]*\d\/api\/public\/leads\/[0-9a-f]{48}$/)
      }
      await page.keyboard.press('Escape')
      await expect(page.getByRole('dialog')).toBeHidden()
    }

    await page.getByRole('button', { name: 'Lead queue' }).click()
    await expect(page.getByRole('dialog')).toBeVisible()
    await page.waitForTimeout(300)
    await expectDialogFits(page, 'queue')
    await shot(page, 'phone-dialog-queue')
    await page.keyboard.press('Escape')
  })

  test('bulk bar fits and opens the template dialog', async ({ page }) => {
    const id = await seedLead(page, 'E2E Mobile Bulk')
    await page.goto('/pipeline')
    const card = page.locator(`[data-card-id="${id}"]`)
    await card.scrollIntoViewIfNeeded()
    await card.getByRole('checkbox').click()
    await expect(page.getByText('1 selected')).toBeVisible()
    await expectFits(page, 'bulk bar')
    await shot(page, 'phone-bulk-bar')
    await page.getByRole('button', { name: 'Send template' }).click()
    await expect(page.getByRole('dialog')).toBeVisible()
    await page.waitForTimeout(300)
    await expectDialogFits(page, 'bulk template')
    await shot(page, 'phone-dialog-bulk-template')
  })

  test('automation editor fits', async ({ page }) => {
    await page.goto('/pipeline/automations')
    await page.getByRole('button', { name: 'New automation' }).click()
    await expect(page.getByRole('dialog')).toBeVisible()
    await page.waitForTimeout(300)
    await expectDialogFits(page, 'automation editor')
    await shot(page, 'phone-dialog-automation')
  })
})

test.describe('Phone width: lead sidebar', () => {
  test.use({ viewport: PHONE })

  test('deal, quote and activity work and fit', async ({ page }) => {
    await loginAsAdmin(page)
    const id = await seedLead(page, 'E2E Mobile Quote')
    const pipelines = await api(page, 'GET', '/pipelines')
    const won = pipelines.pipelines[0].stages.find((s: any) => s.kind === 'won').id
    await api(page, 'PUT', `/contacts/${id}/deal`, { deal_value: 900 })
    await api(page, 'PUT', `/contacts/${id}/stage`, { stage_id: won })

    await page.goto(`/chat/${id}`)
    await page.locator('#info-button').click()
    const newDeal = page.getByRole('button', { name: 'New deal' })
    await expect(newDeal).toBeVisible({ timeout: 8000 })
    await expectFits(page, 'sidebar')
    await shot(page, 'phone-sidebar')

    await newDeal.click()
    await expect(newDeal).toBeHidden()

    await page.getByRole('button', { name: 'New quote' }).click()
    await expect(page.getByRole('dialog')).toBeVisible()
    await page.locator('#quote-title').fill('E2E quote')
    await page.getByLabel('Item', { exact: true }).fill('Teak plank')
    await page.getByLabel('Quantity').fill('2')
    await page.getByLabel('Price').fill('150')
    await page.waitForTimeout(200)
    await expectDialogFits(page, 'quote editor')
    await shot(page, 'phone-dialog-quote')
    await page.getByRole('button', { name: 'Save' }).click()
    await expect(page.getByText('E2E quote')).toBeVisible()

    const quotes = await api(page, 'GET', `/contacts/${id}/quotes`)
    expect(quotes.quotes[0].total).toBe(300)
    await page.goto(new URL(quotes.quotes[0].link).pathname)
    await expect(page.getByRole('heading', { name: 'E2E quote' })).toBeVisible()
    await expectFits(page, 'public quote')
    await shot(page, 'phone-public-quote')
    await page.getByRole('button', { name: 'Accept quote' }).click()
    await expect(page.getByText('This quote is accepted')).toBeVisible()
  })

  test('the app can be installed', async ({ page, request }) => {
    await page.goto('/login')
    expect(await page.locator('link[rel="manifest"]').count()).toBe(1)
    const base = new URL(page.url()).origin
    const manifest = await (await request.get(`${base}/manifest.webmanifest`)).json()
    expect(manifest.display).toBe('standalone')
    expect((await request.get(`${base}/icons/icon-512.png`)).ok()).toBe(true)
    const worker = await request.get(`${base}/sw.js`)
    expect(worker.headers()['content-type']).toContain('javascript')
  })
})

test.describe('Automations', () => {
  test('a rule built in the UI runs on a stage move', async ({ page }) => {
    await loginAsAdmin(page)
    const id = await seedLead(page, 'E2E Automation')
    const name = `E2E rule ${Date.now()}`
    await page.goto('/pipeline/automations')
    await page.getByRole('button', { name: 'New automation' }).click()
    await page.locator('#rule-name').fill(name)

    await page.getByRole('combobox', { name: 'Action' }).click()
    await page.getByRole('option', { name: 'Add tag' }).click()
    await page.getByPlaceholder('Tag').fill('auto-tagged')
    await page.getByRole('button', { name: 'Save' }).click()
    await expect(page.getByText(name)).toBeVisible()
    if (shotDir) await page.screenshot({ path: `${shotDir}/desktop-automations.png` })

    const pipelines = await api(page, 'GET', '/pipelines')
    await api(page, 'PUT', `/contacts/${id}/stage`, { stage_id: pipelines.pipelines[0].stages[1].id })
    await expect.poll(async () => (await api(page, 'GET', `/contacts/${id}`)).tags, { timeout: 5000 }).toContain('auto-tagged')

    const rules = await api(page, 'GET', '/automations')
    const rule = rules.rules.find((r: any) => r.name === name)
    await api(page, 'DELETE', `/automations/${rule.id}`)
  })

  test('team page shows agents', async ({ page }) => {
    await loginAsAdmin(page)
    await page.goto('/pipeline/team')
    await expect(page.getByText('Waiting for an owner')).toBeVisible()
    if (shotDir) await page.screenshot({ path: `${shotDir}/desktop-team.png` })
    await page.goto('/pipeline')
    await page.waitForTimeout(600)
    if (shotDir) await page.screenshot({ path: `${shotDir}/desktop-board.png` })
  })
})
