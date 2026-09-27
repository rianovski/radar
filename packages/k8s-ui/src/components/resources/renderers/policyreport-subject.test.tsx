import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { PolicyReportCell } from './kyverno-cells'
import { getPolicyReportScope } from '../resource-utils-kyverno'

// Kyverno names each per-resource report after the subject's UID, so the Name
// column is a bare UUID. Without the subject, the table is a list of
// unidentifiable rows with counts beside them.
describe('PolicyReport subject column', () => {
  const report = {
    metadata: { name: '4c1d2f8a-91e3-4b7c-9f11-2a6e0c5d7b83', namespace: 'payments' },
    scope: { kind: 'Deployment', namespace: 'payments', name: 'checkout-api' },
  }

  it('names the workload the report is about', () => {
    const html = renderToString(<PolicyReportCell resource={report} column="scope" />)
    expect(html).toContain('checkout-api')
    expect(html).toContain('Deployment')
  })

  it('degrades to a dash rather than an empty cell when there is no subject', () => {
    // Cluster-scoped reports and some producers omit scope entirely.
    expect(getPolicyReportScope({ metadata: { name: 'x' } })).toBe('-')
    const html = renderToString(<PolicyReportCell resource={{ metadata: { name: 'x' } }} column="scope" />)
    expect(html).toContain('-')
  })
})
