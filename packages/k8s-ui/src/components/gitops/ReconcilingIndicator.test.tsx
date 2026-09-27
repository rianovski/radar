import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { ReconcilingIndicator } from './GitOpsStatusBadge'

const LABEL = 'Reconcile pass in flight'

// A reconcile pass in flight is worth showing only where the sync word cannot
// already say it. Radar's own precedent for a secondary indicator (the Velero
// schedule cell's paused-and-rejected case) suppresses the icon when the badge
// carries the same fact, and without that rule one row shows two spinners.
describe('ReconcilingIndicator', () => {
  it('shows on an applied object whose pass never finished', () => {
    // The case the sync word cannot express, and the reason this exists: a
    // wedged controller behind a correct "Synced".
    const html = renderToString(
      <ReconcilingIndicator reconciling since="2026-08-11T06:05:00Z" sync="Synced" />,
    )
    expect(html).toContain(LABEL)
  })

  it('is suppressed when the sync badge already reports Reconciling', () => {
    const html = renderToString(
      <ReconcilingIndicator reconciling since="2026-08-11T06:05:00Z" sync="Reconciling" />,
    )
    expect(html).toBe('')
  })

  it('is absent when nothing is in flight', () => {
    expect(renderToString(<ReconcilingIndicator reconciling={false} sync="Synced" />)).toBe('')
  })
})
