import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { CompositeRenderer } from './CompositeRenderer'
import type { CrossplaneResourceRef } from '../resource-utils-crossplane'

const claim = {
  apiVersion: 'platform.example.org/v1alpha1',
  kind: 'DatabaseClaim',
  metadata: { name: 'example-database', namespace: 'demo-app' },
  spec: {
    compositionRef: { name: 'database' },
    resourceRef: {
      apiVersion: 'platform.example.org/v1alpha1',
      kind: 'Database',
      name: 'example-database-x7k2m',
    },
  },
}

const composed: CrossplaneResourceRef[] = [
  { apiVersion: 'database.example.org/v1beta1', kind: 'Instance', name: 'example-database-instance' },
  { apiVersion: 'identity.example.org/v1beta1', kind: 'Role', name: 'example-database-access' },
  { apiVersion: 'kubernetes.crossplane.io/v1alpha2', kind: 'Object', name: 'example-database-connection' },
]

const html = (props: any) => renderToString(<CompositeRenderer {...props} />)

describe('CompositeRenderer — claim composed-resources panel (issue coverage)', () => {
  it('lists composed resources resolved from the bound XR (the fix)', () => {
    const out = html({ data: claim, composedRefs: composed })
    expect(out).toContain('Composed Resources (3)')
    for (const ref of composed) {
      expect(out).toContain(ref.name)
    }
    expect(out).not.toContain('No composed resources yet')
    // claim identity section + the bound-XR link
    expect(out).toContain('Claim')
    expect(out).toContain('Bound Composite')
    expect(out).toContain('example-database-x7k2m')
  })

  it('shows a loading state while the bound XR is being fetched — not "none"', () => {
    const out = html({ data: claim, composedRefs: [], boundXRStatus: { loading: true } })
    expect(out).toContain('Loading composed resources')
    expect(out).not.toContain('No composed resources yet')
  })

  it('distinguishes a not-found / unreadable bound XR from genuinely-none', () => {
    const out = html({ data: claim, composedRefs: [], boundXRStatus: { missing: true } })
    expect(out).toContain('was not found')
    expect(out).not.toContain('No composed resources yet')
  })

  it('surfaces a bound-XR read error (RBAC/500) distinctly, with the message', () => {
    const out = html({
      data: claim,
      composedRefs: [],
      boundXRStatus: { error: true, errorMessage: 'forbidden: cannot get databases' },
    })
    expect(out).toContain('read the bound composite') // apostrophe is HTML-escaped in SSR output
    expect(out).toContain('forbidden: cannot get databases')
    expect(out).not.toContain('No composed resources yet')
  })

  it('falls back to the genuine empty state for a claim whose XR simply has none yet', () => {
    const out = html({ data: claim, composedRefs: [] })
    expect(out).toContain('No composed resources yet')
  })

  it('for an XR viewed directly (no bound-XR status), reads its own resourceRefs', () => {
    const xr = {
      apiVersion: 'platform.example.org/v1alpha1',
      kind: 'Database',
      metadata: { name: 'example-database-x7k2m' },
      spec: { resourceRefs: composed },
    }
    const out = html({ data: xr })
    expect(out).toContain('Composed Resources (3)')
    expect(out).toContain('Composite Resource') // not "Claim"
  })
})
