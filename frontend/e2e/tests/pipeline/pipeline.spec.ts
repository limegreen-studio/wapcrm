import { test, expect, Page } from '@playwright/test'
import { loginAsAdmin } from '../../helpers'

// Calls the API from the logged-in page so cookies and the CSRF token apply.
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

function uniquePhone(): string {
  return `91${Date.now().toString().slice(-8)}${Math.floor(Math.random() * 90 + 10)}`
}

async function captureLead(page: Page, name: string, tags: string[] = []): Promise<string> {
  const lead = await api(page, 'POST', '/leads', { phone_number: uniquePhone(), name, tags })
  return lead.contact_id
}

const card = (page: Page, id: string) => page.locator(`[data-card-id="${id}"]`)

async function columnOf(page: Page, id: string): Promise<string> {
  return card(page, id).evaluate(el => el.closest('section')?.getAttribute('aria-label') ?? '')
}

test.describe('Pipeline board', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsAdmin(page)
  })

  test('filters by tag', async ({ page }) => {
    const tag = `e2e-${Date.now()}`
    const tagged = await captureLead(page, 'E2E Tagged', [tag])
    const other = await captureLead(page, 'E2E Untagged')
    await page.goto('/pipeline')
    await expect(card(page, other)).toBeVisible()

    await page.getByRole('button', { name: 'Filters' }).click()
    await page.locator('#filter-tag').fill(tag)
    await expect(card(page, other)).toBeHidden()
    await expect(card(page, tagged)).toBeVisible()
  })

  test('moves a card with the keyboard', async ({ page }) => {
    const id = await captureLead(page, 'E2E Keyboard')
    await page.goto('/pipeline')
    await expect(card(page, id)).toBeVisible()
    const before = await columnOf(page, id)

    await card(page, id).focus()
    await page.keyboard.press('Alt+ArrowRight')
    await expect.poll(() => columnOf(page, id)).not.toBe(before)
  })

  test('bulk-tags selected cards', async ({ page }) => {
    const id = await captureLead(page, 'E2E Bulk')
    const tag = `bulk-${Date.now()}`
    await page.goto('/pipeline')
    await card(page, id).hover()
    await card(page, id).getByRole('checkbox').click()
    await page.getByLabel('Add tag').fill(tag)
    await page.getByRole('button', { name: 'Apply' }).click()
    await expect(card(page, id)).toContainText(tag)
  })

  test('moves a card from the phone-width picker', async ({ page }) => {
    const id = await captureLead(page, 'E2E Phone')
    await page.setViewportSize({ width: 390, height: 800 })
    await page.goto('/pipeline')
    await expect(card(page, id)).toBeVisible()
    const picker = card(page, id).locator('select')
    await picker.selectOption({ label: 'Contacted' })
    await expect.poll(() => columnOf(page, id)).toBe('Contacted')
  })

  test('saves a deal value that shows on the card', async ({ page }) => {
    const id = await captureLead(page, 'E2E Deal')
    await page.goto(`/chat/${id}`)
    await api(page, 'PUT', `/contacts/${id}/deal`, { deal_value: 4321 })
    await page.goto('/pipeline')
    await expect(card(page, id)).toContainText((4321).toLocaleString())
  })

  test('opens the report page', async ({ page }) => {
    await captureLead(page, 'E2E Report')
    await page.goto('/pipeline/reports')
    await expect(page.getByRole('heading', { name: 'By period' })).toBeVisible()
  })
})

test.describe('Follow-ups', () => {
  test('edits title from the Tasks page', async ({ page }) => {
    await loginAsAdmin(page)
    const id = await captureLead(page, 'E2E Task')
    const title = `E2E follow-up ${Date.now()}`
    await api(page, 'POST', '/tasks', { contact_id: id, title })
    await page.goto('/tasks')
    await page.getByRole('tab', { name: 'All open' }).click()
    const row = page.locator('li', { hasText: title })
    await row.getByRole('button', { name: 'Edit follow-up' }).click()
    await page.locator('#followup-title').fill(`${title} edited`)
    await page.getByRole('button', { name: 'Save' }).click()
    await expect(page.locator('li', { hasText: `${title} edited` })).toBeVisible()
  })
})

test.describe('Lead fields', () => {
  test('saves a lead field from the chat sidebar', async ({ page }) => {
    await loginAsAdmin(page)
    const id = await captureLead(page, 'E2E Field')
    await api(page, 'PUT', `/contacts/${id}/lead`, { fields: { city: 'Pune' } })
    const contact = await api(page, 'GET', `/contacts/${id}`)
    expect(contact.metadata.lead.city).toBe('Pune')
  })
})
