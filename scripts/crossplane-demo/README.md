# Crossplane Demo Cluster

Bootstraps a `kind` cluster with Crossplane installed (core + provider-kubernetes +
function-patch-and-transform) and a curated set of Crossplane fixtures covering the
resource kinds Radar's Crossplane UI needs to render. Use it for visual-testing
changes to the Crossplane surfaces (resource list, MR/XR/Claim/Composition/XRD/Function
renderers, composed-resources panel) without needing a real cluster running a cloud
provider.

Both Crossplane API models are exercised on the one cluster:

- **v1 Claim → bound XR → composed resources** (the v1 claim/XR/MR flow).
- **v2 namespaced XR → composed resources** (the v2 `scope: Namespaced` flow).

Both reconcile **healthy**.

## Quick start

```bash
# Prerequisites: kind, kubectl, helm
./scripts/crossplane-demo.sh up        # ~4-6 minutes on first run
./scripts/crossplane-demo.sh status    # inventory what's installed

# Run Radar against it
kubectl config use-context kind-radar-crossplane-demo
./scripts/visual-test-start.sh

# When done
./scripts/crossplane-demo.sh down
```

The Make target wraps the same command:

```bash
make crossplane-demo            # = ./scripts/crossplane-demo.sh up
make crossplane-demo-status     # = ./scripts/crossplane-demo.sh status
make crossplane-demo-down       # = ./scripts/crossplane-demo.sh down
```

## What's in the cluster

| Resource | Kind | What it exercises |
|---|---|---|
| `crossplane-system/crossplane` | Deployment | Core Crossplane controller — Healthy controller path |
| `provider-kubernetes` | `Provider` (pkg.crossplane.io/v1) | Provider list view + provider detail renderer + Healthy=True condition + revision tracking |
| `function-patch-and-transform` | `Function` (pkg.crossplane.io/v1) | Function list view + function detail renderer (used inside Composition Pipeline) |
| `default` | `ProviderConfig` (kubernetes.crossplane.io/v1alpha1) | ProviderConfig renderer + InjectedIdentity credential source path |
| `v2-app/default` | `ProviderConfig` (kubernetes.m.crossplane.io/v1alpha1) | Namespaced ProviderConfig — used by the v2 namespaced XR's composed Objects |
| `xdatabases.demo.example.io` | `CompositeResourceDefinition` (apiextensions.crossplane.io/v1) | XRD renderer + v1 XRD-with-`claimNames` path + Established/Offered conditions |
| `appstacks.demo.example.io` | `CompositeResourceDefinition` (apiextensions.crossplane.io/v2) | XRD renderer + v2 `scope: Namespaced` path |
| `xdatabases.demo.example.io`, `appstacks.demo.example.io` | `Composition` (apiextensions.crossplane.io/v1) | Composition renderer + Pipeline mode + function reference + composed resources list |
| `standalone-configmap`, `standalone-namespace` | `Object` (kubernetes.crossplane.io) | **Standalone MR renderer** — Managed Resources created directly (no XR), wrapping a ConfigMap and a Namespace. Synced=True/Ready=True healthy MR path. |
| `demo-app/example-database` | `DatabaseClaim` (demo.example.io/v1alpha1, **v1 Claim**) | **Claim renderer + composed-resources-via-bound-XR path.** Claim's singular `spec.resourceRef` -> bound `XDatabase` XR -> the XR's `spec.resourceRefs` (3 composed Objects). |
| `example-database-*` | `XDatabase` (demo.example.io/v1alpha1, **v1 XR**) | **Composite (XR) renderer** — cluster-scoped XR bound to the claim, healthy, 3 composed cluster-scoped Objects. |
| `v2-app/web-stack` | `AppStack` (demo.example.io/v1alpha1, **v2 namespaced XR**) | **Composite (XR) renderer** — namespaced XR, healthy, 2 composed namespaced Objects (`kubernetes.m.crossplane.io`). Composed refs live at `spec.crossplane.resourceRefs` (v2 path). |

### Resource kind coverage

- ✅ Core Crossplane controller (Deployment in `crossplane-system`)
- ✅ Provider (Healthy=True, with a Revision)
- ✅ Function (Healthy=True, referenced from a Composition Pipeline)
- ✅ ProviderConfig — cluster-scoped (`kubernetes.crossplane.io`) + namespaced (`kubernetes.m.crossplane.io`)
- ✅ CompositeResourceDefinition — v1 (with `claimNames`) + v2 (`scope: Namespaced`)
- ✅ Composition (Pipeline mode + function-patch-and-transform input)
- ✅ Standalone Managed Resource — Synced=True, Ready=True (healthy MR path)
- ✅ **v1 Claim → bound XR → composed resources** (healthy; the composed-resources-via-bound-XR panel path)
- ✅ **v2 namespaced XR → composed resources** (healthy)
- ✅ Managed Resources — cluster-scoped and namespaced variants

## Version constraints (important)

- **Crossplane chart `2.3.4`.** v2 introduced the `scope` field on XRDs (`Namespaced` / `Cluster`).
- **provider-kubernetes `v1.0.0`.** This is the first release to ship the **namespaced** `Object`
  (`kubernetes.m.crossplane.io`). A v2 `scope: Namespaced` XR **cannot compose cluster-scoped
  resources**, so the namespaced XR fixture composes the namespaced `Object`. Older provider
  versions (v0.18–v0.20) only ship the cluster-scoped `Object` and will not reconcile the v2 fixture.
- **function-patch-and-transform `v0.9.0`.**

Pinned at the top of `scripts/crossplane-demo.sh`; override via the matching env vars.

### How Claims work on Crossplane 2.x

v2 XRDs (`apiextensions.crossplane.io/v2`) do **not** offer Claims. To keep the v1 Claim flow,
the claim fixture uses the **`apiextensions.crossplane.io/v1` XRD API with `claimNames`** — deprecated
on 2.x but still served, and the only way to get a Claim. `scope: LegacyCluster` does **not** exist in
2.3.4 (only `Namespaced` / `Cluster`).

## Scenarios NOT covered (intentional gaps)

- **Cloud providers (AWS / GCP / Azure)** — would require real credentials and create real cloud
  infrastructure. `provider-kubernetes` is the right choice for an offline-friendly demo.
- **Nested XRs (an XR composing another XR)** — the fixtures are one level deep.
- **`Usage` / `ClusterUsage`, provider revision conflicts** — add a fixture if/when those become
  rendered surfaces.

## Implementation notes

- The script grants the provider's auto-generated ServiceAccount `cluster-admin` via a
  `ClusterRoleBinding`. provider-kubernetes generates the SA name with a revision-hash suffix, so
  the script discovers the SA name dynamically rather than hard-coding it.
- ProviderConfigs use `source: InjectedIdentity` so the provider uses its own SA token — no external
  kubeconfig, no secrets to manage.
- Compositions use **Pipeline mode** with `function-patch-and-transform`. Patches use
  `type: FromCompositeFieldPath` with `string.type: Format` transforms — that exact shape is what the
  function expects; shorthand patches (no `type:`) or string transforms without `string.type: Format`
  silently no-op.
- The namespaced `Object` (`kubernetes.m.crossplane.io`) requires `spec.providerConfigRef.kind`
  (to disambiguate namespaced `ProviderConfig` from `ClusterProviderConfig`), and the wrapped
  manifest must carry a namespace — otherwise the provider fails to observe it.
