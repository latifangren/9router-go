import { describe, expect, test } from 'bun:test'
import { getStatusVariant, getLatencyBadge } from './helpers'

describe('getStatusVariant', () => {
  test('returns success for active and passed', () => {
    expect(getStatusVariant('active')).toBe('success')
    expect(getStatusVariant('passed')).toBe('success')
  })

  test('returns error for error and failed', () => {
    expect(getStatusVariant('error')).toBe('error')
    expect(getStatusVariant('failed')).toBe('error')
  })

  test('returns default for unknown or unset states', () => {
    expect(getStatusVariant('unknown')).toBe('default')
    expect(getStatusVariant('')).toBe('default')
    expect(getStatusVariant(null)).toBe('default')
    expect(getStatusVariant(undefined)).toBe('default')
  })
})

describe('getLatencyBadge', () => {
  test('returns null for missing, zero, or negative latency', () => {
    expect(getLatencyBadge(undefined)).toBeNull()
    expect(getLatencyBadge(null)).toBeNull()
    expect(getLatencyBadge(0)).toBeNull()
    expect(getLatencyBadge(-50)).toBeNull()
  })

  test('returns green lightning badge for latency < 300ms', () => {
    expect(getLatencyBadge(50)).toEqual({ variant: 'success', text: '⚡ 50ms' })
    expect(getLatencyBadge(299)).toEqual({ variant: 'success', text: '⚡ 299ms' })
  })

  test('returns amber hourglass badge for latency between 300ms and 800ms', () => {
    expect(getLatencyBadge(300)).toEqual({ variant: 'warning', text: '⏳ 300ms' })
    expect(getLatencyBadge(550)).toEqual({ variant: 'warning', text: '⏳ 550ms' })
    expect(getLatencyBadge(800)).toEqual({ variant: 'warning', text: '⏳ 800ms' })
  })

  test('returns red turtle badge for latency > 800ms', () => {
    expect(getLatencyBadge(801)).toEqual({ variant: 'error', text: '🐢 801ms' })
    expect(getLatencyBadge(1250)).toEqual({ variant: 'error', text: '🐢 1250ms' })
  })

  test('rounds fractional latency to nearest integer', () => {
    expect(getLatencyBadge(123.4)).toEqual({ variant: 'success', text: '⚡ 123ms' })
    expect(getLatencyBadge(456.7)).toEqual({ variant: 'warning', text: '⏳ 457ms' })
  })
})

describe('proxy pool filtering & sorting contract', () => {
  test('failed filter matches failed or error', () => {
    const isFailed = (p: { testStatus?: string }) =>
      p.testStatus === 'failed' || p.testStatus === 'error'
    expect(isFailed({ testStatus: 'failed' })).toBe(true)
    expect(isFailed({ testStatus: 'error' })).toBe(true)
    expect(isFailed({ testStatus: 'passed' })).toBe(false)
    expect(isFailed({ testStatus: 'active' })).toBe(false)
  })

  test('fastest sort assigns Infinity to pools without passed or active status', () => {
    const sortFastest = (
      pools: Array<{ name: string; latency?: number; testStatus?: string }>
    ) => {
      return [...pools].sort((a, b) => {
        const aValid = a.testStatus === 'passed' || a.testStatus === 'active'
        const bValid = b.testStatus === 'passed' || b.testStatus === 'active'
        const aLat = aValid && typeof a.latency === 'number' && a.latency > 0 ? a.latency : Infinity
        const bLat = bValid && typeof b.latency === 'number' && b.latency > 0 ? b.latency : Infinity
        if (aLat !== bLat) return aLat - bLat
        return (a.name || '').localeCompare(b.name || '')
      })
    }

    const pools = [
      { name: 'dead-low-latency', latency: 10, testStatus: 'failed' },
      { name: 'error-low-latency', latency: 15, testStatus: 'error' },
      { name: 'passed-high-latency', latency: 300, testStatus: 'passed' },
      { name: 'active-med-latency', latency: 120, testStatus: 'active' },
    ]
    const sorted = sortFastest(pools)
    expect(sorted[0].name).toBe('active-med-latency')
    expect(sorted[1].name).toBe('passed-high-latency')
    expect(sorted[2].name).toBe('dead-low-latency')
    expect(sorted[3].name).toBe('error-low-latency')
  })
})

