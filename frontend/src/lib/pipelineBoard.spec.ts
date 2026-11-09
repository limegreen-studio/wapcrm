import { describe, expect, it } from 'vitest'
import { POSITION_GAP, adjacentStageId, formatDuration, positionFor, telHref, templateVariables, windowLeft } from './pipelineBoard'

const cards = (...positions: number[]) => positions.map(pipeline_position => ({ pipeline_position }))

describe('positionFor', () => {
  it('uses the midpoint between two neighbours', () => {
    expect(positionFor(cards(100, 200), 1, 2, false)).toBe(150)
  })

  it('goes before the first card', () => {
    expect(positionFor(cards(5000), 0, 1, false)).toBe(5000 - POSITION_GAP)
  })

  it('appends after the last card of a fully loaded column', () => {
    expect(positionFor(cards(100), 1, 1, false)).toBe(100 + POSITION_GAP)
  })

  it('leaves appending to the server when the column is partly loaded', () => {
    expect(positionFor(cards(100), 1, 40, false)).toBeUndefined()
  })

  it('does not count the moving card as an unloaded card', () => {
    expect(positionFor(cards(100), 1, 2, true)).toBe(100 + POSITION_GAP)
  })

  it('starts an empty column at one gap', () => {
    expect(positionFor([], 0, 0, false)).toBe(POSITION_GAP)
  })
})

describe('adjacentStageId', () => {
  const stages = ['a', 'b', 'c']

  it('finds the neighbour on either side', () => {
    expect(adjacentStageId(stages, 'b', -1)).toBe('a')
    expect(adjacentStageId(stages, 'b', 1)).toBe('c')
  })

  it('stops at the ends and on unknown stages', () => {
    expect(adjacentStageId(stages, 'a', -1)).toBeNull()
    expect(adjacentStageId(stages, 'c', 1)).toBeNull()
    expect(adjacentStageId(stages, 'zz', 1)).toBeNull()
  })
})

describe('formatDuration', () => {
  it('formats minutes, hours and days', () => {
    expect(formatDuration(120)).toBe('2m')
    expect(formatDuration(7200)).toBe('2h')
    expect(formatDuration(100800)).toBe('1d 4h')
    expect(formatDuration(172800)).toBe('2d')
  })
})

describe('windowLeft', () => {
  const now = Date.parse('2026-01-01T00:00:00Z')
  const at = (minutes: number) => new Date(now + minutes * 60_000).toISOString()

  it('is null when closed or unknown', () => {
    expect(windowLeft(at(-1), now)).toBeNull()
    expect(windowLeft(undefined, now)).toBeNull()
  })

  it('shows hours, and is urgent in the last two hours', () => {
    expect(windowLeft(at(600), now)).toEqual({ label: '10h', urgent: false })
    expect(windowLeft(at(90), now)).toEqual({ label: '1h', urgent: true })
    expect(windowLeft(at(45), now)).toEqual({ label: '45m', urgent: true })
  })
})

describe('templateVariables', () => {
  it('lists each variable once across parts', () => {
    expect(templateVariables('Hi {{1}}, {{ name }}', 'From {{1}}')).toEqual(['1', 'name'])
    expect(templateVariables('No variables')).toEqual([])
  })
})

describe('telHref', () => {
  it('builds a dial link', () => {
    expect(telHref('919000012345')).toBe('tel:+919000012345')
    expect(telHref('+91 90000-12345')).toBe('tel:+919000012345')
  })

  it('refuses masked or short numbers', () => {
    expect(telHref('91******1224')).toBeNull()
    expect(telHref('12')).toBeNull()
  })
})
