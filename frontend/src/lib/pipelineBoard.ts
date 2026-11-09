// Pure helpers behind the Kanban board, kept apart so they can be unit tested.

export const POSITION_GAP = 1024

interface Positioned {
  pipeline_position: number
}

// Position for a card inserted at `index` of `cards` (the moving card excluded).
// Returns undefined when appending past the loaded cards of a partly loaded
// column, so the server appends after the real last card.
export function positionFor(
  cards: Positioned[],
  index: number,
  columnTotal: number,
  movingWithinColumn: boolean
): number | undefined {
  const prev = cards[index - 1]
  const next = cards[index]
  if (prev && next) return (prev.pipeline_position + next.pipeline_position) / 2
  if (next) return next.pipeline_position - POSITION_GAP
  if (prev) {
    if (columnTotal > cards.length + (movingWithinColumn ? 1 : 0)) return undefined
    return prev.pipeline_position + POSITION_GAP
  }
  return POSITION_GAP
}

// The stage one step left or right of `stageId`, or null at either end.
export function adjacentStageId(stageIds: string[], stageId: string, direction: -1 | 1): string | null {
  const index = stageIds.indexOf(stageId)
  if (index === -1) return null
  return stageIds[index + direction] ?? null
}

// "2d 4h" style label for a duration in seconds.
export function formatDuration(seconds: number): string {
  const hours = Math.round(seconds / 3600)
  if (hours < 1) return `${Math.max(1, Math.round(seconds / 60))}m`
  if (hours < 24) return `${hours}h`
  const days = Math.floor(hours / 24)
  const rest = hours % 24
  return rest ? `${days}d ${rest}h` : `${days}d`
}

// Time left in WhatsApp's 24-hour reply window, or null once it has closed.
export function windowLeft(expiresAt: string | undefined, now = Date.now()): { label: string; urgent: boolean } | null {
  if (!expiresAt) return null
  const ms = new Date(expiresAt).getTime() - now
  if (ms <= 0) return null
  const minutes = Math.ceil(ms / 60_000)
  const label = minutes < 60 ? `${minutes}m` : `${Math.floor(minutes / 60)}h`
  return { label, urgent: minutes <= 120 }
}

// Variable names in a template body or text header ({{1}}, {{name}}).
export function templateVariables(...contents: string[]): string[] {
  const names: string[] = []
  for (const content of contents) {
    for (const m of content.matchAll(/\{\{\s*([^{}]+?)\s*\}\}/g)) {
      if (!names.includes(m[1])) names.push(m[1])
    }
  }
  return names
}

// A dialable href, or null when the number is masked or unusable.
export function telHref(phone: string): string | null {
  if (phone.includes('*')) return null
  const digits = phone.replace(/\D/g, '')
  return digits.length >= 7 ? `tel:+${digits}` : null
}
