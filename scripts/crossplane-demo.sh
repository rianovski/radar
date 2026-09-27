#!/usr/bin/env bash
# Bootstrap a kind cluster pre-populated with a curated set of Crossplane
# fixtures for visual-testing the Crossplane UI surfaces. Idempotent — can
# be re-run to refresh state without recreating the cluster.
#
# Subcommands:
#   up      Create cluster (if missing), install Crossplane + provider-kubernetes
#           + function-patch-and-transform, apply fixtures.
#   down    Delete the kind cluster.
#   reset   down + up.
#   status  Show what's installed and inventory the Crossplane resources.
#   help    Show this message.
#
# Prerequisites:
#   - kind     https://kind.sigs.k8s.io/
#   - kubectl
#   - helm     https://helm.sh/docs/intro/install/
#
# Set CLUSTER_NAME=foo to use a different cluster (default: radar-crossplane-demo).
#
# Fixtures cover both Crossplane API models on a single cluster:
#   - v1 Claim -> bound XR -> composed resources (the v1 claim/XR/MR flow), via
#     an apiextensions.crossplane.io/v1 XRD with claimNames.
#   - v2 namespaced XR -> composed resources, via an apiextensions.crossplane.io/v2
#     XRD with scope: Namespaced composing namespaced provider-kubernetes Objects.
# Both reconcile healthy. See README for the coverage matrix and the version
# constraints that make this work.

set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-radar-crossplane-demo}"
KUBECTL_CTX="kind-${CLUSTER_NAME}"

# Versions pinned so the demo behaves consistently across runs. Bump when
# you want a newer release; otherwise leave alone.
# provider-kubernetes must be v1.0.0+ — it is the first release to ship the
# namespaced Object (kubernetes.m.crossplane.io), which a v2 Namespaced XR
# needs to compose (a namespaced XR cannot compose cluster-scoped resources).
CROSSPLANE_CHART_VERSION="${CROSSPLANE_CHART_VERSION:-2.3.4}"
PROVIDER_KUBERNETES_VERSION="${PROVIDER_KUBERNETES_VERSION:-v1.0.0}"
FUNCTION_PATCH_VERSION="${FUNCTION_PATCH_VERSION:-v0.9.0}"

# Pretty colors for status output. Quietly turn off in non-interactive env.
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  C_BLUE='\033[34m'; C_GREEN='\033[32m'; C_YELLOW='\033[33m'; C_RED='\033[31m'; C_DIM='\033[2m'; C_RESET='\033[0m'
else
  C_BLUE=''; C_GREEN=''; C_YELLOW=''; C_RED=''; C_DIM=''; C_RESET=''
fi

step()    { printf "${C_BLUE}==> %s${C_RESET}\n" "$1"; }
ok()      { printf "${C_GREEN}    ✓ %s${C_RESET}\n" "$1"; }
warn()    { printf "${C_YELLOW}    ! %s${C_RESET}\n" "$1"; }
fail()    { printf "${C_RED}    ✗ %s${C_RESET}\n" "$1"; exit 1; }
note()    { printf "${C_DIM}    %s${C_RESET}\n" "$1"; }

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    fail "$1 not found in PATH. Install: $2"
  fi
}

kctl() {
  kubectl --context "${KUBECTL_CTX}" "$@"
}

# --- Cluster lifecycle -----------------------------------------------------

cluster_exists() {
  kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"
}

cmd_up() {
  require_cmd kind "https://kind.sigs.k8s.io/docs/user/quick-start/#installation (or 'brew install kind')"
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"
  require_cmd helm "https://helm.sh/docs/intro/install/ (or 'brew install helm')"

  if cluster_exists; then
    step "Cluster '${CLUSTER_NAME}' already exists — reusing"
  else
    step "Creating kind cluster '${CLUSTER_NAME}'"
    kind create cluster --name "${CLUSTER_NAME}" --wait 60s
    ok "Cluster created"
  fi

  kctl cluster-info >/dev/null || fail "kind context not reachable"

  install_crossplane
  install_provider_kubernetes
  grant_provider_cluster_admin
  install_function_patch_and_transform
  apply_provider_config
  cleanup_legacy_fixtures
  apply_standalone_managed_resources
  apply_v1_claim_fixture
  apply_v2_namespaced_fixture
  wait_for_chains_healthy
  print_summary
}

cmd_down() {
  require_cmd kind "https://kind.sigs.k8s.io/"
  if cluster_exists; then
    step "Deleting cluster '${CLUSTER_NAME}'"
    kind delete cluster --name "${CLUSTER_NAME}"
    ok "Deleted"
  else
    warn "Cluster '${CLUSTER_NAME}' does not exist; nothing to do"
  fi
}

cmd_reset() {
  cmd_down
  cmd_up
}

# --- Crossplane ------------------------------------------------------------

install_crossplane() {
  step "Installing Crossplane (chart ${CROSSPLANE_CHART_VERSION}) into 'crossplane-system' namespace"

  helm repo add crossplane-stable https://charts.crossplane.io/stable >/dev/null 2>&1 || true
  helm repo update crossplane-stable >/dev/null

  # --wait gates on the crossplane + rbac-manager Deployments going Ready.
  helm upgrade --install crossplane crossplane-stable/crossplane \
    --kube-context "${KUBECTL_CTX}" \
    --namespace crossplane-system --create-namespace \
    --version "${CROSSPLANE_CHART_VERSION}" \
    --wait --timeout 5m >/dev/null

  ok "Crossplane healthy"
}

# --- Providers + Functions -------------------------------------------------

install_provider_kubernetes() {
  step "Installing provider-kubernetes (${PROVIDER_KUBERNETES_VERSION})"

  cat <<EOF | kctl apply -f - >/dev/null
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-kubernetes
spec:
  package: xpkg.crossplane.io/crossplane-contrib/provider-kubernetes:${PROVIDER_KUBERNETES_VERSION}
EOF

  wait_for_provider_healthy provider-kubernetes
  ok "provider-kubernetes healthy"
}

# wait_for_provider_healthy polls the Healthy condition on a Provider object.
# Provider install pulls an OCI artifact then unpacks/installs a CRD bundle —
# typically 30–90s on a fresh cluster. Fail loudly on timeout so the caller
# knows the demo state isn't usable yet.
wait_for_provider_healthy() {
  local name="$1" deadline=$((SECONDS + 240))
  while [ $SECONDS -lt $deadline ]; do
    local healthy
    healthy=$(kctl get provider.pkg "$name" \
      -o jsonpath='{.status.conditions[?(@.type=="Healthy")].status}' 2>/dev/null || echo "")
    if [ "$healthy" = "True" ]; then
      return 0
    fi
    sleep 3
  done
  fail "Provider '$name' did not reach Healthy=True within 4m"
}

# grant_provider_cluster_admin patches the provider's auto-generated SA with
# cluster-admin so it can manage arbitrary resources. provider-kubernetes
# generates its SA name with a revision-hash suffix (e.g.
# "provider-kubernetes-d7f6a..."), so we discover it dynamically rather than
# hard-coding. Idempotent — overwrites the binding each run.
grant_provider_cluster_admin() {
  step "Granting provider-kubernetes SA cluster-admin"

  local sa
  # Wait briefly for the SA to be created (Provider install creates it
  # asynchronously after the package unpacks).
  local deadline=$((SECONDS + 120))
  while [ $SECONDS -lt $deadline ]; do
    sa=$(kctl -n crossplane-system get sa \
      -o jsonpath='{range .items[?(@.metadata.name)]}{.metadata.name}{"\n"}{end}' 2>/dev/null \
      | grep '^provider-kubernetes-' | head -n1 || true)
    if [ -n "${sa:-}" ]; then
      break
    fi
    sleep 2
  done

  if [ -z "${sa:-}" ]; then
    fail "Could not find provider-kubernetes SA in crossplane-system"
  fi

  note "provider SA: ${sa}"

  cat <<EOF | kctl apply -f - >/dev/null
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: provider-kubernetes-cluster-admin
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
subjects:
  - kind: ServiceAccount
    name: ${sa}
    namespace: crossplane-system
EOF

  ok "ClusterRoleBinding applied"
}

install_function_patch_and_transform() {
  step "Installing function-patch-and-transform (${FUNCTION_PATCH_VERSION})"

  cat <<EOF | kctl apply -f - >/dev/null
apiVersion: pkg.crossplane.io/v1
kind: Function
metadata:
  name: function-patch-and-transform
spec:
  package: xpkg.crossplane.io/crossplane-contrib/function-patch-and-transform:${FUNCTION_PATCH_VERSION}
EOF

  wait_for_function_healthy function-patch-and-transform
  ok "function-patch-and-transform healthy"
}

wait_for_function_healthy() {
  local name="$1" deadline=$((SECONDS + 240))
  while [ $SECONDS -lt $deadline ]; do
    local healthy
    healthy=$(kctl get function.pkg "$name" \
      -o jsonpath='{.status.conditions[?(@.type=="Healthy")].status}' 2>/dev/null || echo "")
    if [ "$healthy" = "True" ]; then
      return 0
    fi
    sleep 3
  done
  fail "Function '$name' did not reach Healthy=True within 4m"
}

# --- ProviderConfig --------------------------------------------------------

apply_provider_config() {
  step "Applying ProviderConfig (InjectedIdentity)"

  cat <<EOF | kctl apply -f - >/dev/null
apiVersion: kubernetes.crossplane.io/v1alpha1
kind: ProviderConfig
metadata:
  name: default
spec:
  credentials:
    source: InjectedIdentity
EOF

  ok "ProviderConfig 'default' applied"
}

# --- Standalone Managed Resources -----------------------------------------

# Two MR Object instances wrapping a ConfigMap and a Namespace. These
# exercise the standalone MR renderer path — managed resources created
# directly by the user without an XR/Composition in the picture.
apply_standalone_managed_resources() {
  step "Applying standalone Managed Resources"

  cat <<EOF | kctl apply -f - >/dev/null
apiVersion: kubernetes.crossplane.io/v1alpha1
kind: Object
metadata:
  name: standalone-configmap
spec:
  providerConfigRef:
    name: default
  forProvider:
    manifest:
      apiVersion: v1
      kind: ConfigMap
      metadata:
        name: standalone-demo
        namespace: default
      data:
        managed-by: crossplane
        purpose: radar-demo-standalone-mr
---
apiVersion: kubernetes.crossplane.io/v1alpha1
kind: Object
metadata:
  name: standalone-namespace
spec:
  providerConfigRef:
    name: default
  forProvider:
    manifest:
      apiVersion: v1
      kind: Namespace
      metadata:
        name: crossplane-demo-managed
        labels:
          managed-by: crossplane
EOF

  ok "Standalone MRs applied (Object/standalone-configmap, Object/standalone-namespace)"
}

# cleanup_legacy_fixtures removes the previous demo's AppBundle XRD/Composition
# when `up` reuses an existing cluster. Deleting the XRD cascades its XR
# instances, so stale broken AppBundle XRs don't contaminate the new fixture
# set or the status/topology output. Best-effort and idempotent.
cleanup_legacy_fixtures() {
  if kctl get xrd appbundles.demo.example.io >/dev/null 2>&1; then
    step "Removing legacy AppBundle fixtures from a previous demo"
    kctl delete composition appbundles.demo.example.io --ignore-not-found >/dev/null 2>&1 || true
    kctl delete xrd appbundles.demo.example.io --ignore-not-found >/dev/null 2>&1 || true
    ok "Legacy AppBundle XRD/Composition removed"
  fi
}

# --- v1 Claim fixture (v1 Claim -> bound XR -> composed resources) ---------

# An apiextensions.crossplane.io/v1 XRD with claimNames gives the classic v1
# flow: a namespaced Claim (DatabaseClaim) bound to a cluster-scoped XR
# (XDatabase) that composes three cluster-scoped provider-kubernetes Objects.
# The v1 XRD API is deprecated on Crossplane 2.x but still served — it is the
# only way to get Claims, which v2 XRDs (scope: Namespaced|Cluster) do not offer.
apply_v1_claim_fixture() {
  step "Applying v1 Claim fixture (XRD + Composition)"

  cat <<'EOF' | kctl apply -f - >/dev/null
apiVersion: apiextensions.crossplane.io/v1
kind: CompositeResourceDefinition
metadata:
  name: xdatabases.demo.example.io
spec:
  group: demo.example.io
  names:
    kind: XDatabase
    plural: xdatabases
  claimNames:
    kind: DatabaseClaim
    plural: databaseclaims
  versions:
    - name: v1alpha1
      served: true
      referenceable: true
      schema:
        openAPIV3Schema:
          type: object
          properties:
            spec:
              type: object
              properties:
                appName:
                  type: string
                  description: Name used for the composed resources.
                namespace:
                  type: string
                  default: default
                  description: Namespace the composed resources are written into.
              required:
                - appName
EOF

  cat <<'EOF' | kctl apply -f - >/dev/null
apiVersion: apiextensions.crossplane.io/v1
kind: Composition
metadata:
  name: xdatabases.demo.example.io
spec:
  compositeTypeRef:
    apiVersion: demo.example.io/v1alpha1
    kind: XDatabase
  mode: Pipeline
  pipeline:
    - step: patch-and-transform
      functionRef:
        name: function-patch-and-transform
      input:
        apiVersion: pt.fn.crossplane.io/v1beta1
        kind: Resources
        resources:
          - name: configmap
            base:
              apiVersion: kubernetes.crossplane.io/v1alpha2
              kind: Object
              spec:
                providerConfigRef:
                  name: default
                forProvider:
                  manifest:
                    apiVersion: v1
                    kind: ConfigMap
                    metadata:
                      name: placeholder
                      namespace: placeholder
                    data:
                      managed-by: crossplane-composition
            patches:
              - { type: FromCompositeFieldPath, fromFieldPath: spec.appName, toFieldPath: spec.forProvider.manifest.metadata.name, transforms: [{ type: string, string: { type: Format, fmt: "%s-config" } }] }
              - { type: FromCompositeFieldPath, fromFieldPath: spec.namespace, toFieldPath: spec.forProvider.manifest.metadata.namespace }
              - { type: FromCompositeFieldPath, fromFieldPath: spec.appName, toFieldPath: metadata.name, transforms: [{ type: string, string: { type: Format, fmt: "%s-configmap" } }] }
          - name: service
            base:
              apiVersion: kubernetes.crossplane.io/v1alpha2
              kind: Object
              spec:
                providerConfigRef:
                  name: default
                forProvider:
                  manifest:
                    apiVersion: v1
                    kind: Service
                    metadata:
                      name: placeholder
                      namespace: placeholder
                    spec:
                      selector:
                        app: placeholder
                      ports:
                        - port: 80
                          targetPort: 8080
            patches:
              - { type: FromCompositeFieldPath, fromFieldPath: spec.appName, toFieldPath: spec.forProvider.manifest.metadata.name, transforms: [{ type: string, string: { type: Format, fmt: "%s-svc" } }] }
              - { type: FromCompositeFieldPath, fromFieldPath: spec.namespace, toFieldPath: spec.forProvider.manifest.metadata.namespace }
              - { type: FromCompositeFieldPath, fromFieldPath: spec.appName, toFieldPath: spec.forProvider.manifest.spec.selector.app }
              - { type: FromCompositeFieldPath, fromFieldPath: spec.appName, toFieldPath: metadata.name, transforms: [{ type: string, string: { type: Format, fmt: "%s-service" } }] }
          - name: secret
            base:
              apiVersion: kubernetes.crossplane.io/v1alpha2
              kind: Object
              spec:
                providerConfigRef:
                  name: default
                forProvider:
                  manifest:
                    apiVersion: v1
                    kind: Secret
                    metadata:
                      name: placeholder
                      namespace: placeholder
                    type: Opaque
                    # base64("crossplane-composition"). Using `data` (not
                    # stringData) avoids provider-kubernetes SSA drift — the
                    # apiserver never returns stringData, so an observe would
                    # perpetually diff and re-apply.
                    data:
                      managed-by: Y3Jvc3NwbGFuZS1jb21wb3NpdGlvbg==
            patches:
              - { type: FromCompositeFieldPath, fromFieldPath: spec.appName, toFieldPath: spec.forProvider.manifest.metadata.name, transforms: [{ type: string, string: { type: Format, fmt: "%s-conn" } }] }
              - { type: FromCompositeFieldPath, fromFieldPath: spec.namespace, toFieldPath: spec.forProvider.manifest.metadata.namespace }
              - { type: FromCompositeFieldPath, fromFieldPath: spec.appName, toFieldPath: metadata.name, transforms: [{ type: string, string: { type: Format, fmt: "%s-connection" } }] }
EOF

  wait_for_established xrd xdatabases.demo.example.io
  wait_for_crd databaseclaims.demo.example.io

  kctl apply -f - >/dev/null <<'EOF'
apiVersion: v1
kind: Namespace
metadata:
  name: demo-app
---
apiVersion: demo.example.io/v1alpha1
kind: DatabaseClaim
metadata:
  name: example-database
  namespace: demo-app
spec:
  appName: example-database
  namespace: demo-app
EOF

  ok "v1 Claim applied (DatabaseClaim/example-database in demo-app)"
}

# --- v2 namespaced XR fixture (namespaced XR -> composed resources) --------

# An apiextensions.crossplane.io/v2 XRD with scope: Namespaced gives the v2
# flow: a namespaced XR (AppStack) composing namespaced provider-kubernetes
# Objects (kubernetes.m.crossplane.io). A namespaced XR cannot compose
# cluster-scoped resources, so this uses the namespaced Object variant and a
# namespaced ProviderConfig in the XR's namespace.
apply_v2_namespaced_fixture() {
  step "Applying v2 namespaced XR fixture (XRD + Composition + ProviderConfig)"

  cat <<'EOF' | kctl apply -f - >/dev/null
apiVersion: apiextensions.crossplane.io/v2
kind: CompositeResourceDefinition
metadata:
  name: appstacks.demo.example.io
spec:
  scope: Namespaced
  group: demo.example.io
  names:
    kind: AppStack
    plural: appstacks
  versions:
    - name: v1alpha1
      served: true
      referenceable: true
      schema:
        openAPIV3Schema:
          type: object
          properties:
            spec:
              type: object
              properties:
                appName:
                  type: string
                  description: Name used for the composed resources.
              required:
                - appName
EOF

  cat <<'EOF' | kctl apply -f - >/dev/null
apiVersion: apiextensions.crossplane.io/v1
kind: Composition
metadata:
  name: appstacks.demo.example.io
spec:
  compositeTypeRef:
    apiVersion: demo.example.io/v1alpha1
    kind: AppStack
  mode: Pipeline
  pipeline:
    - step: patch-and-transform
      functionRef:
        name: function-patch-and-transform
      input:
        apiVersion: pt.fn.crossplane.io/v1beta1
        kind: Resources
        resources:
          - name: configmap
            base:
              apiVersion: kubernetes.m.crossplane.io/v1alpha1
              kind: Object
              spec:
                providerConfigRef:
                  name: default
                  kind: ProviderConfig
                forProvider:
                  manifest:
                    apiVersion: v1
                    kind: ConfigMap
                    metadata:
                      name: placeholder
                    data:
                      managed-by: crossplane-composition
            patches:
              - { type: FromCompositeFieldPath, fromFieldPath: spec.appName, toFieldPath: spec.forProvider.manifest.metadata.name, transforms: [{ type: string, string: { type: Format, fmt: "%s-config" } }] }
              - { type: FromCompositeFieldPath, fromFieldPath: metadata.namespace, toFieldPath: spec.forProvider.manifest.metadata.namespace }
              - { type: FromCompositeFieldPath, fromFieldPath: spec.appName, toFieldPath: metadata.name, transforms: [{ type: string, string: { type: Format, fmt: "%s-configmap" } }] }
          - name: service
            base:
              apiVersion: kubernetes.m.crossplane.io/v1alpha1
              kind: Object
              spec:
                providerConfigRef:
                  name: default
                  kind: ProviderConfig
                forProvider:
                  manifest:
                    apiVersion: v1
                    kind: Service
                    metadata:
                      name: placeholder
                    spec:
                      selector:
                        app: placeholder
                      ports:
                        - port: 80
                          targetPort: 8080
            patches:
              - { type: FromCompositeFieldPath, fromFieldPath: spec.appName, toFieldPath: spec.forProvider.manifest.metadata.name, transforms: [{ type: string, string: { type: Format, fmt: "%s-svc" } }] }
              - { type: FromCompositeFieldPath, fromFieldPath: metadata.namespace, toFieldPath: spec.forProvider.manifest.metadata.namespace }
              - { type: FromCompositeFieldPath, fromFieldPath: spec.appName, toFieldPath: spec.forProvider.manifest.spec.selector.app }
              - { type: FromCompositeFieldPath, fromFieldPath: spec.appName, toFieldPath: metadata.name, transforms: [{ type: string, string: { type: Format, fmt: "%s-service" } }] }
EOF

  wait_for_established xrd appstacks.demo.example.io
  wait_for_crd appstacks.demo.example.io

  # Namespaced ProviderConfig lives in the XR's namespace; the namespaced
  # Object references it by name within that namespace.
  kctl apply -f - >/dev/null <<'EOF'
apiVersion: v1
kind: Namespace
metadata:
  name: v2-app
---
apiVersion: kubernetes.m.crossplane.io/v1alpha1
kind: ProviderConfig
metadata:
  name: default
  namespace: v2-app
spec:
  credentials:
    source: InjectedIdentity
---
apiVersion: demo.example.io/v1alpha1
kind: AppStack
metadata:
  name: web-stack
  namespace: v2-app
spec:
  appName: web-stack
EOF

  ok "v2 namespaced XR applied (AppStack/web-stack in v2-app)"
}

# wait_for_established blocks until an XRD reports Established=True so the
# apiserver has registered the generated CRD endpoints before we create an
# instance — otherwise the first apply races the XRD reconciler and fails
# with "no matches for kind".
wait_for_established() {
  local kind="$1" name="$2" deadline=$((SECONDS + 90))
  while [ $SECONDS -lt $deadline ]; do
    if kctl get "$kind" "$name" \
         -o jsonpath='{.status.conditions[?(@.type=="Established")].status}' 2>/dev/null \
         | grep -q "True"; then
      return 0
    fi
    sleep 2
  done
  warn "$kind/$name did not report Established=True within 90s"
}

wait_for_crd() {
  local name="$1" deadline=$((SECONDS + 90))
  while [ $SECONDS -lt $deadline ]; do
    kctl get crd "$name" >/dev/null 2>&1 && return 0
    sleep 2
  done
  warn "CRD $name did not register within 90s"
}

# CHAINS_READY_HINT reflects the real reconcile state in the final summary, so
# the summary never claims "Ready" when the wait timed out.
CHAINS_READY_HINT="not checked"

# wait_for_chains_healthy gates on the claim and the namespaced XR reaching
# Ready=True so the summary reflects real reconciled state. Warns rather than
# fails on timeout — a slow first image pull shouldn't abort the whole demo.
wait_for_chains_healthy() {
  step "Waiting for composed resources to reconcile (up to 4m)"
  local deadline=$((SECONDS + 240)) claim_ready="" xr_ready=""
  while [ $SECONDS -lt $deadline ]; do
    claim_ready=$(kctl -n demo-app get databaseclaim example-database \
      -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || echo "")
    xr_ready=$(kctl -n v2-app get appstack web-stack \
      -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || echo "")
    if [ "$claim_ready" = "True" ] && [ "$xr_ready" = "True" ]; then
      ok "v1 claim and v2 XR both Ready"
      CHAINS_READY_HINT="all Ready"
      return 0
    fi
    sleep 5
  done
  CHAINS_READY_HINT="NOT all Ready yet (claim Ready=${claim_ready:-?}, XR Ready=${xr_ready:-?}) — run '$0 status'"
  warn "chains not fully Ready within 4m (claim Ready=${claim_ready:-?}, XR Ready=${xr_ready:-?}); check '$0 status'"
}


# --- Status ----------------------------------------------------------------

cmd_status() {
  require_cmd kubectl "https://kubernetes.io/docs/tasks/tools/"

  if ! cluster_exists; then
    warn "Cluster '${CLUSTER_NAME}' does not exist. Run: $0 up"
    return
  fi

  step "Cluster: ${CLUSTER_NAME} (context ${KUBECTL_CTX})"

  printf "\n${C_BLUE}Crossplane pods${C_RESET}\n"
  kctl -n crossplane-system get pods --no-headers 2>/dev/null \
    | awk '{ printf "    %s %s\n", $1, $3 }' || warn "no pods (Crossplane not installed?)"

  printf "\n${C_BLUE}Providers${C_RESET}\n"
  kctl get providers.pkg.crossplane.io --no-headers 2>/dev/null \
    | awk '{ printf "    %s installed=%s healthy=%s\n", $1, $2, $3 }' || note "(none)"

  printf "\n${C_BLUE}Functions${C_RESET}\n"
  kctl get functions.pkg.crossplane.io --no-headers 2>/dev/null \
    | awk '{ printf "    %s installed=%s healthy=%s\n", $1, $2, $3 }' || note "(none)"

  printf "\n${C_BLUE}ProviderConfigs (kubernetes.crossplane.io)${C_RESET}\n"
  kctl get providerconfigs.kubernetes.crossplane.io --no-headers 2>/dev/null \
    | awk '{ printf "    %s\n", $1 }' || note "(none)"

  printf "\n${C_BLUE}XRDs${C_RESET}\n"
  kctl get xrds --no-headers 2>/dev/null \
    | awk '{ printf "    %s established=%s offered=%s\n", $1, $2, $3 }' || note "(none)"

  printf "\n${C_BLUE}Compositions${C_RESET}\n"
  kctl get compositions --no-headers 2>/dev/null \
    | awk '{ printf "    %s\n", $1 }' || note "(none)"

  printf "\n${C_BLUE}CompositionRevisions${C_RESET}\n"
  kctl get compositionrevisions --no-headers 2>/dev/null \
    | awk '{ printf "    %s revision=%s\n", $1, $2 }' || note "(none)"

  printf "\n${C_BLUE}v1 Claim (demo-app)${C_RESET}\n"
  kctl -n demo-app get databaseclaims.demo.example.io --no-headers 2>/dev/null \
    | awk '{ printf "    %s synced=%s ready=%s\n", $1, $2, $3 }' || note "(none)"

  printf "\n${C_BLUE}Composites (XRs)${C_RESET}\n"
  kctl get xdatabases.demo.example.io --no-headers 2>/dev/null \
    | awk '{ printf "    XDatabase/%s synced=%s ready=%s\n", $1, $2, $3 }' || note "(none)"
  kctl get appstacks.demo.example.io -A --no-headers 2>/dev/null \
    | awk '{ printf "    AppStack/%s (ns %s) synced=%s ready=%s\n", $2, $1, $3, $4 }' || note "(none)"

  printf "\n${C_BLUE}Managed Resources — cluster-scoped (kubernetes.crossplane.io)${C_RESET}\n"
  kctl get objects.kubernetes.crossplane.io --no-headers 2>/dev/null \
    | awk '{ printf "    %s synced=%s ready=%s\n", $1, $2, $3 }' || note "(none)"

  printf "\n${C_BLUE}Managed Resources — namespaced (kubernetes.m.crossplane.io)${C_RESET}\n"
  kctl get objects.kubernetes.m.crossplane.io -A --no-headers 2>/dev/null \
    | awk '{ printf "    %s/%s synced=%s ready=%s\n", $1, $2, $3, $4 }' || note "(none)"

  printf "\n"
}

# --- Final summary ---------------------------------------------------------

print_summary() {
  printf "\n"
  step "Demo cluster ready"
  cat <<EOF

  Context:    ${KUBECTL_CTX}

  Fixtures baked in:
    Crossplane core    — crossplane + rbac-manager in crossplane-system
    Providers          — provider-kubernetes (${PROVIDER_KUBERNETES_VERSION}), Healthy
    Functions          — function-patch-and-transform (${FUNCTION_PATCH_VERSION}), Healthy
    ProviderConfig     — kubernetes.crossplane.io/default (InjectedIdentity)
    Standalone MRs     — Object/standalone-configmap, Object/standalone-namespace
    v1 Claim chain     — DatabaseClaim/example-database (demo-app) -> bound XR
                         XDatabase -> 3 composed cluster-scoped Objects
    v2 namespaced XR   — AppStack/web-stack (v2-app) -> 2 composed namespaced
                         Objects (kubernetes.m.crossplane.io)

  Live reconcile state (Ready/Synced): ${CHAINS_READY_HINT}

  Run Radar against this cluster:
    kubectl config use-context ${KUBECTL_CTX}
    ./scripts/visual-test-start.sh

  Other commands:
    $0 status   # inventory the cluster
    $0 reset    # nuke + recreate
    $0 down     # delete cluster

EOF
}

# --- Entry point -----------------------------------------------------------

cmd_help() {
  sed -n '2,/^$/p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

# Support both subcommand form (`up`, `down`, ...) and a flag-style alias
# (`--reset`) requested by the spec. The flag is a thin wrapper around
# `reset` so the rest of the script stays subcommand-shaped like gitops-demo.sh.
case "${1:-help}" in
  up)              cmd_up      ;;
  down)            cmd_down    ;;
  reset|--reset)   cmd_reset   ;;
  status)          cmd_status  ;;
  help|-h|--help)  cmd_help    ;;
  *)
    printf "${C_RED}Unknown subcommand: %s${C_RESET}\n\n" "$1"
    cmd_help
    exit 1
    ;;
esac
