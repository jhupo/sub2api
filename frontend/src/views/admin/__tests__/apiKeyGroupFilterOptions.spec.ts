import { describe, it, expect } from 'vitest'
import { buildApiKeyGroupFilterOptions } from '../apiKeyGroupFilterOptions'
import type { AdminGroup } from '@/types'

const labels = {
  all: 'All',
  exclusive: 'Exclusive',
  public: 'Public',
  disabled: 'Disabled',
}

function g(partial: Partial<AdminGroup>): AdminGroup {
  return {
    id: 0,
    name: '',
    status: 'active',
    is_exclusive: false,
    ...partial,
  } as AdminGroup
}

describe('buildApiKeyGroupFilterOptions', () => {
  it('partitions active groups into exclusive and public sections', () => {
    const groups = [
      g({ id: 1, name: 'Excl', is_exclusive: true }),
      g({ id: 2, name: 'Pub', is_exclusive: false }),
      g({ id: 3, name: 'Other public', is_exclusive: false }),
    ]
    expect(buildApiKeyGroupFilterOptions(groups, labels)).toEqual([
      { value: null, label: 'All' },
      { value: -1, label: 'Exclusive', kind: 'group', disabled: true },
      { value: 1, label: 'Excl' },
      { value: -2, label: 'Public', kind: 'group', disabled: true },
      { value: 2, label: 'Pub' },
      { value: 3, label: 'Other public' },
    ])
  })

  it('skips empty section headers', () => {
    const groups = [g({ id: 2, name: 'Pub', is_exclusive: false })]
    const opts = buildApiKeyGroupFilterOptions(groups, labels)
    expect(opts.find((o) => o.label === 'Exclusive')).toBeUndefined()
    expect(opts).toContainEqual({ value: -2, label: 'Public', kind: 'group', disabled: true })
  })

  it('places non-active groups in a separate disabled section (not omitted)', () => {
    const groups = [
      g({ id: 1, name: 'Active', is_exclusive: true }),
      g({ id: 2, name: 'Inactive', is_exclusive: true, status: 'inactive' }),
    ]
    const opts = buildApiKeyGroupFilterOptions(groups, labels)
    // Active exclusive group appears in Exclusive section
    expect(opts).toContainEqual({ value: 1, label: 'Active' })
    // Disabled group appears in Disabled section
    expect(opts).toContainEqual({ value: 2, label: 'Inactive' })
    // Disabled section header present
    expect(opts).toContainEqual({ value: -3, label: 'Disabled', kind: 'group', disabled: true })
    // Not in Exclusive section
    const exclIdx = opts.findIndex((o) => o.value === -1)
    const disabledItemIdx = opts.findIndex((o) => o.value === 2)
    expect(exclIdx).toBeLessThan(disabledItemIdx)
  })

  it('section headers use distinct negative values (no duplicate Vue :key)', () => {
    const groups = [
      g({ id: 1, name: 'E', is_exclusive: true }),
      g({ id: 2, name: 'P', is_exclusive: false }),
      g({ id: 3, name: 'P2', is_exclusive: false }),
      g({ id: 4, name: 'D', status: 'inactive' }),
    ]
    const opts = buildApiKeyGroupFilterOptions(groups, labels)
    const headerValues = opts.filter((o) => o.kind === 'group').map((o) => o.value)
    const unique = new Set(headerValues)
    expect(unique.size).toBe(headerValues.length) // all distinct
    headerValues.forEach((v) => expect(v).toBeLessThan(0)) // all negative
  })

  it('returns only the all-option when there are no groups', () => {
    expect(buildApiKeyGroupFilterOptions([], labels)).toEqual([{ value: null, label: 'All' }])
  })
})
