import { describe, expect, it } from 'vitest'
import { clusterCloudConsoleLink, nodeCloudConsoleLink } from './cloud-console'

describe('cloud console links', () => {
  it('builds Google Compute Engine node links from GCE provider IDs', () => {
    expect(nodeCloudConsoleLink('gce://proj-1/us-east1-b/gke-node-1')).toEqual({
      label: 'Open in Google Cloud Console',
      url: 'https://console.cloud.google.com/compute/instancesDetail/zones/us-east1-b/instances/gke-node-1?project=proj-1',
    })
  })

  it('builds AWS EC2 node links from AWS provider IDs', () => {
    expect(nodeCloudConsoleLink('aws:///us-east-1a/i-0123456789abcdef0')).toEqual({
      label: 'Open in AWS Console',
      url: 'https://us-east-1.console.aws.amazon.com/ec2/home?region=us-east-1#InstanceDetails:instanceId=i-0123456789abcdef0',
    })
  })

  it('omits ambiguous node links', () => {
    expect(nodeCloudConsoleLink('aws:///us-east-1-wl1-bos-wlz-1/i-0123456789abcdef0')).toBeNull()
    expect(nodeCloudConsoleLink('azure:///subscriptions/123/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/vm')).toBeNull()
  })

  it('builds high-confidence cluster links from canonical contexts', () => {
    expect(clusterCloudConsoleLink('gke_proj-1_us-east1_nonprod')).toEqual({
      label: 'Open cluster in Google Cloud Console',
      url: 'https://console.cloud.google.com/kubernetes/clusters/details/us-east1/nonprod/details?project=proj-1',
    })
    expect(clusterCloudConsoleLink('arn:aws:eks:us-west-2:123456789012:cluster/prod')).toEqual({
      label: 'Open cluster in AWS Console',
      url: 'https://us-west-2.console.aws.amazon.com/eks/home?region=us-west-2#/clusters/prod',
    })
  })

  it('omits renamed or non-commercial-partition cluster contexts', () => {
    expect(clusterCloudConsoleLink('nonprod-cluster-us-east1')).toBeNull()
    expect(clusterCloudConsoleLink('arn:aws-us-gov:eks:us-gov-west-1:123456789012:cluster/prod')).toBeNull()
  })
})
