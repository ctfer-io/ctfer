# CTFer

CTFer is High-Availability and secure CTF deployment tool over Kubernetes. 

## How to use

### Prerequisites

- Kubernetes cluster up and running (you can use our solution for that https://github.com/ctfer-io/ctfer-l3) ;
- Generate and store your certs in certs folder.
- Install the PostgreSQL operator by Zalando https://github.com/zalando/postgres-operator

```bash
# Add helm repo
helm repo add postgres-operator-charts https://opensource.zalando.com/postgres-operator/charts/postgres-operator
helm repo update

# Install the operator with inherited_labels
helm install postgres-operator postgres-operator-charts/postgres-operator --set "configKubernetes.inherited_labels={app.kubernetes.io/component,app.kubernetes.io/part-of,ctfer.io/stack-name}" --create-namespace --namespace postgres-operator

```

### Deploy

If you want to use local images.

```bash
# Air-Gapped 
cd hack
hauler store sync -f hauler-manifest-ha.yaml
hauler store copy registry://registry.dev1.ctfer-io.lab

pulumi config set images-repository registry.dev1.ctfer-io.lab
pulumi config set charts-repository oci://registry.dev1.ctfer-io.lab/hauler
```

If you want to use custom images of ctfd (i.e with your plugin/themes).

```bash
# Use custom images
pulumi config set --path platform.image ctferio/ctfd:3.8.1-0.9.0
```

If you want to configure the ChallManager URL.

```bash
# Use custom images
pulumi config set chall-manager-url http://chall-manager-svc.ctfer:8080/api/v1
```

If you want to use a custom certificate.

```bash
# export PULUMI_CONFIG_PASSPHRASE before
# https://github.com/pulumi/pulumi/issues/6015
cat /path/to/crt.pem | pulumi config set --secret --path platform.crt
cat /path/to/key.pem | pulumi config set --secret --path platform.key
```

If you want to have a larger filesystem for uploads on CTFd.

```bash
pulumi config set --path plateform.storage-size 10Gi
```

If you want to configure several workers on CTFd.

```bash
pulumi config set --path platform.workers 3
pulumi config set --path platform.replicas 3

# You will need a ReadWriteMany compatible CSI (e.g longhorn) if the Pods is schedule on several nodes
pulumi config set --path platform.pvc-access-modes[0] ReadWriteMany
pulumi config set --path platform.storage-class longhorn
```

If you want to configure other resources than default.

```bash
pulumi config set --path platform.requests.cpu 1
pulumi config set --path platform.requests.memory 2Gi

pulumi config set --path platform.limits.cpu 1
pulumi config set --path platform.limits.memory 1Gi
```

Deploy CTFer.

```bash
pulumi config set --path platform.hostname ctfd.dev1.ctfer-io.lab
pulumi config set --path ingress-labels.name traefik
pulumi config set --path db.operator-namespace cnpg-system
pulumi up 
```

# KEDA

Pour garder une trace pour plus tard...

## Prérequis

```bash
helm repo add kedacore https://kedacore.github.io/charts

# install keda + http-add-on
helm install keda kedacore/keda --namespace keda --create-namespace
helm install http-add-on kedacore/keda-add-ons-http -n keda
```

## Modifications à prévoir

Modifier `internal/componentes/ctfer.go` :
Créer un service `ExternalName` pour faire référence au proxy http de keda qui se situe dans un autre namespace.
Modifier l'ingress pour faire référence à ce service plutôt que `ctfd-svc`.

```go
	// If KEDA is enabled
	_, err = corev1.NewService(ctx, "ctfd-keda-svc", &corev1.ServiceArgs{
		Metadata: metav1.ObjectMetaArgs{
			Labels:    ctfdLabels,
			Name:      pulumi.String("ctfd-keda-svc"),
			Namespace: ns,
		},
		Spec: &corev1.ServiceSpecArgs{
			ExternalName: pulumi.String("keda-add-ons-http-interceptor-proxy.keda.svc.cluster.local"),
			Type:         pulumi.String("ExternalName"),
			Ports: corev1.ServicePortArray{
				corev1.ServicePortArgs{
					TargetPort: pulumi.Int(8080),
					Port:       pulumi.Int(8080),
					Name:       pulumi.String("web"),
				},
			},
		},
	}, pulumi.Parent(ctfer))
	if err != nil {
		return pulumi.StringOutput{}, err
	}
```

Modifier `internal/components/traefik.go` :
Par défaut, traefik ne route pas les paquets vers un service en mode `ExternalName`. 
Activer la feature dans le helm.

```go
// ...

"providers": pulumi.Map{
    "kubernetesCRD": pulumi.Map{
        "enabled": pulumi.Bool(false),
    },
    "kubernetesIngress": pulumi.Map{
        "allowCrossNamespace": pulumi.Bool(true), // challenge instances on demand
        "allowExternalNameServices": pulumi.Bool(true), // If KEDA is enabled
    },
},
// ...
```

Enfin, appliquer le manifest `hack/httpscaleobject.yaml`.

# PostgreSQL DEBUG

## Zalando

```bash

# create kind cluster without CNI
kind create cluster --config hack/kind-config.yaml

# install cilium 
helm install cilium cilium/cilium --version 1.18.2 \
	--namespace kube-system   \
	--set ipam.mode=kubernetes \
	--set hubble.relay.enabled=true \
    --set hubble.ui.enabled=true

# install postgresql operator
helm install postgres-operator postgres-operator-charts/postgres-operator --set "configKubernetes.inherited_labels={app.kubernetes.io/component,app.kubernetes.io/part-of,ctfer.io/stack-name}" --create-namespace --namespace postgres-operator

# mimic node failure
kubectl cordon kind-worker
# then delete the master node

```

## CNPG

```bash

# create kind cluster without CNI
kind create cluster --config hack/kind-config.yaml

# install cilium 
helm install cilium cilium/cilium --version 1.18.2 \
	--namespace kube-system   \
	--set ipam.mode=kubernetes \
	--set hubble.relay.enabled=true \
    --set hubble.ui.enabled=true

# Install cnpg operator
helm repo add cnpg https://cloudnative-pg.github.io/charts
helm upgrade --install cnpg \
  --namespace cnpg-system \
  --create-namespace \
  cnpg/cloudnative-pg

# mimic node failure
kubectl cordon kind-worker
# then delete the master node

```

## Storage

To ensure data redundancy and availability, our setup initially relied on Longhorn to replicate Persistent Volume Claims (PVCs) across multiple nodes. However, this approach had a significant impact on storage usage.

**Previous configuration:**
- Each 1 GB of data on the primary database was replicated across 3 worker nodes via Longhorn.
- Each database replica also stored the same 1 GB of data, which was again replicated across all 3 workers.
- In total, 1 GB of useful data resulted in approximately 9 GB of actual storage consumption.

This configuration provided strong resilience but was inefficient in terms of storage usage.

**Current configuration:**
- The `storageClass` for the database pods has been changed to `local-path`.
- For each 1 GB of primary data, around 3 GB are now stored to maintain fault tolerance and support failover to a replica if the primary node becomes unavailable.

This change greatly optimizes storage efficiency while preserving reliability and automatic failover capabilities.

### Talos-Specific Configuration

Ref: https://docs.siderolabs.com/kubernetes-guides/csi/local-storage#local-path-provisioner

For the GreHack 2025 production environment, we are using **Talos**, which requires specific configuration for the `local-path-provisioner`.
Our virtual machines are equipped with two disks : one dedicated to the OS and another for data. In this setup, `/dev/sdb` is used as the data disk.

To allow the local-path-provisioner to write to a specific path, we first need to define a `UserVolumeConfig`:

```yaml
# uservolume.yml
apiVersion: v1alpha1
kind: UserVolumeConfig
name: local-path-provisioner
provisioning:
  diskSelector:
    match: disk.dev_path == '/dev/sdb'
  minSize: 20GB
  maxSize: 50GB
``` 

This configuration creates a writable partition at /var/mnt/local-path-provisioner.

```bash
talosctl patch mc --patch @uservolume.yml
```

Next, we deploy the **local-path-provisioner** using **Kustomize**, which allows us to override the default storage path.
```yaml
# kustomization.yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- github.com/rancher/local-path-provisioner/deploy?ref=v0.0.32
patches:
- patch: |-
    kind: ConfigMap
    apiVersion: v1
    metadata:
      name: local-path-config
      namespace: local-path-storage
    data:
      config.json: |-
        {
                "nodePathMap":[
                {
                        "node":"DEFAULT_PATH_FOR_NON_LISTED_NODES",
                        "paths":["/var/mnt/local-path-provisioner"]
                }
                ]
        }
- patch: |-
    apiVersion: storage.k8s.io/v1
    kind: StorageClass
    metadata:
      name: local-path

- patch: |-
    apiVersion: v1
    kind: Namespace
    metadata:
      name: local-path-storage
      labels:
        pod-security.kubernetes.io/enforce: privileged

```
Finally, apply the Kustomize configuration:

```bash
mkdir local-path
touch local-path/kustomize.yml # copy content here

# apply
kubectl apply -k local-path/
```

This setup ensures the local-path-provisioner correctly uses the Talos-managed data disk for persistent volumes, maintaining compatibility and data persistence across the cluster.

# Kite Dashboard

```bash
# Install kube-prometheus-stack to see montoring
# Add Prometheus community Helm repository
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update

# Install kube-prometheus-stack
helm install prometheus prometheus-community/kube-prometheus-stack \
  --namespace monitoring \
  --create-namespace

helm repo add kite https://zxh326.github.io/kite
helm install kite kite/kite -n kube-system


```