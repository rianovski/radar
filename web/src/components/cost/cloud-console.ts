export interface CloudConsoleLink {
  label: string
  url: string
}

export function nodeCloudConsoleLink(providerID?: string): CloudConsoleLink | null {
  if (!providerID) return null

  const gce = parseGCEProviderID(providerID)
  if (gce) {
    return {
      label: 'Open in Google Cloud Console',
      url: `https://console.cloud.google.com/compute/instancesDetail/zones/${encodeURIComponent(gce.zone)}/instances/${encodeURIComponent(gce.instance)}?project=${encodeURIComponent(gce.project)}`,
    }
  }

  const aws = parseAWSProviderID(providerID)
  if (aws) {
    return {
      label: 'Open in AWS Console',
      url: `https://${aws.region}.console.aws.amazon.com/ec2/home?region=${aws.region}#InstanceDetails:instanceId=${encodeURIComponent(aws.instanceID)}`,
    }
  }

  return null
}

export function clusterCloudConsoleLink(context?: string): CloudConsoleLink | null {
  if (!context) return null

  const gke = parseGKEContext(context)
  if (gke) {
    return {
      label: 'Open cluster in Google Cloud Console',
      url: `https://console.cloud.google.com/kubernetes/clusters/details/${encodeURIComponent(gke.location)}/${encodeURIComponent(gke.cluster)}/details?project=${encodeURIComponent(gke.project)}`,
    }
  }

  const eks = parseEKSContext(context)
  if (eks) {
    return {
      label: 'Open cluster in AWS Console',
      url: `https://${eks.region}.console.aws.amazon.com/eks/home?region=${eks.region}#/clusters/${encodeURIComponent(eks.cluster)}`,
    }
  }

  return null
}

function parseGCEProviderID(providerID: string): { project: string; zone: string; instance: string } | null {
  if (!providerID.startsWith('gce://')) return null
  const parts = providerID.replace(/^gce:\/\/\/?/, '').split('/')
  if (parts.length !== 3 || parts.some((part) => part === '')) return null
  return { project: parts[0], zone: parts[1], instance: parts[2] }
}

function parseAWSProviderID(providerID: string): { region: string; instanceID: string } | null {
  if (!providerID.startsWith('aws://')) return null
  const parts = providerID.replace(/^aws:\/\/\/?/, '').split('/')
  if (parts.length !== 2 || parts.some((part) => part === '')) return null
  const region = regionFromAWSZone(parts[0])
  if (!region) return null
  return { region, instanceID: parts[1] }
}

function parseGKEContext(context: string): { project: string; location: string; cluster: string } | null {
  const match = /^gke_([^_]+)_([^_]+)_(.+)$/.exec(context)
  if (!match) return null
  return { project: match[1], location: match[2], cluster: match[3] }
}

function parseEKSContext(context: string): { region: string; cluster: string } | null {
  const match = /^arn:aws:eks:([^:]+):\d{12}:cluster\/(.+)$/.exec(context)
  if (!match) return null
  return { region: match[1], cluster: match[2] }
}

function regionFromAWSZone(zone: string): string | null {
  const match = /^([a-z]{2}-[a-z]+-\d)[a-z]$/.exec(zone)
  return match?.[1] ?? null
}
