import { describe, expect, it } from 'vitest'
import { ALLOCATION_USE_TOOLTIP, formatAllocatedUse } from './CurrentAllocationUse'

describe('formatAllocatedUse', () => {
  it('keeps unavailable, zero-allocation, and zero-use states distinct', () => {
    expect(formatAllocatedUse(1, 0, false)).toBe('Usage unavailable')
    expect(formatAllocatedUse(0, 0, false)).toBe('No allocation')
    expect(formatAllocatedUse(1, 0, true)).toBe('0% allocated use')
    expect(formatAllocatedUse(1, 0, true, false)).toBe('—')
  })

  it('caps bursty or noisy ratios at the allocation ceiling', () => {
    expect(formatAllocatedUse(1, 42.4, true)).toBe('42% allocated use')
    expect(formatAllocatedUse(1, 120, true)).toBe('100% allocated use')
  })

  it('does not frame the measure as a grade or recoverable savings', () => {
    expect(ALLOCATION_USE_TOOLTIP.toLowerCase()).not.toMatch(/efficient|waste|unused capacity/)
    expect(ALLOCATION_USE_TOOLTIP).toContain('not request headroom')
  })
})
