const OPEN_COST_WORKLOAD_KINDS = new Set(['Deployment', 'StatefulSet', 'DaemonSet'])

export function isOpenCostWorkloadKind(kind: string): boolean {
  return OPEN_COST_WORKLOAD_KINDS.has(kind)
}
