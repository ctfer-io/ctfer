# CTFER

CTFER is high-Availability and secure CTF deployment tool over Kubernetes. 

## How to use

### Prerequisites
- Kubernetes cluster up and running (you can use our solution for that https://github.com/ctfer-io/ctfer-l3 )
- Generate and store your certs in certs folder


If you want to use local images
```bash
# Air-Gapped 
cd hack
hauler store sync -f hauler-manifest-ha.yaml
hauler store copy registry://registry.dev1.ctfer-io.lab

pulumi config set images-repository registry.dev1.ctfer-io.lab
pulumi config set charts-repository oci://registry.dev1.ctfer-io.lab/hauler
```

If you want to use custom images of ctfd (i.e with your plugin/themes)
```bash
# Use custom images
pulumi config set ctfd-image ctferio/ctfd:3.7.7-0.3.0-rc1
```

Deploy ctfer 
```bash
# Install local-path for mariadb
kubectl apply -f https://raw.githubusercontent.com/rancher/local-path-provisioner/v0.0.31/deploy/local-path-storage.yaml
pulumi config set hostname ctfd.dev1.ctfer-io.lab
pulumi up 
```


# KEDA
Pour garder une trace pour plus tard.

## Prérequis
```bash
helm repo add kedacore https://kedacore.github.io/charts

# install keda + http-add-on
helm install keda kedacore/keda --namespace keda --create-namespace
kelm install http-add-on kedacore/keda-add-ons-http -n keda

```

## Modifications à prévoir 
Modifier `internal/componentes/ctfer.go` :
Créer un service `ExternalName` pour faire référence au proxy http de keda qui se situe dans un autre namespace.
Modifier l'ingress pour faire référence à ce service plutôt que `ctfd-svc`.
```go

	// If Keda enabled
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
Par défaut, treafik ne route pas les paquets vers un service en mode `ExternalName`. 
Activer la feature dans le helm.

```go
// ...

"providers": pulumi.Map{
    "kubernetesCRD": pulumi.Map{
        "enabled": pulumi.Bool(false),
    },
    "kubernetesIngress": pulumi.Map{
        "allowCrossNamespace": pulumi.Bool(true), // challenge on-demand
        "allowExternalNameServices": pulumi.Bool(true), // if keda enabled
    },
},
// ...
            
```

Enfin, appliquer le manifest `hack/httpscaleobject.yaml` 