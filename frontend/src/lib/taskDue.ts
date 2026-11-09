// Due-time helpers shared by the follow-up dialog and the Tasks page.

// Format a Date for a datetime-local input (local time, minute precision)
export function toLocalInput(date: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

export function inHours(hours: number): Date {
  const d = new Date(Date.now() + hours * 3_600_000)
  d.setSeconds(0, 0)
  return d
}

// 10:00 local time, `days` days from today
export function daysAtTen(days: number): Date {
  const d = new Date()
  d.setDate(d.getDate() + days)
  d.setHours(10, 0, 0, 0)
  return d
}

// Quick picks; labels are i18n keys under tasks.preset
export const DUE_PRESETS = [
  { key: 'in1h', date: () => inHours(1) },
  { key: 'tomorrow', date: () => daysAtTen(1) },
  { key: 'in3days', date: () => daysAtTen(3) },
  { key: 'nextWeek', date: () => daysAtTen(7) }
] as const
