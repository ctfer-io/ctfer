package components

import (
	"fmt"

	"github.com/pandatix/24hiut-2023/l4/infra/internal"
	appsv1 "github.com/pulumi/pulumi-kubernetes/sdk/v3/go/kubernetes/apps/v1"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v3/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v3/go/kubernetes/meta/v1"
	netwv1 "github.com/pulumi/pulumi-kubernetes/sdk/v3/go/kubernetes/networking/v1"
	"github.com/pulumi/pulumi-random/sdk/v4/go/random"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// CTFer is a pulumi Component that deploy a pre-configured CTFd stack
// in an on-premise K8s cluster with Traefik as Ingress Controller.
type CTFer struct {
	pulumi.ResourceState

	// URL contains the CTFd's URL once provided.
	URL pulumi.StringOutput
}

// NewCTFer creates a new pulumi Component Resource and registers it.
func NewCTFer(ctx *pulumi.Context, name string, opts ...pulumi.ResourceOption) (*CTFer, error) {
	ctfer := &CTFer{}

	// Register the Component Resource
	err := ctx.RegisterComponentResource("ctfer:l4:CTFer", name, ctfer, opts...)
	if err != nil {
		return nil, err
	}

	// Provision K8s resources
	url, err := ctfer.provisionK8s(ctx)
	if err != nil {
		return nil, err
	}
	ctfer.URL = url

	return ctfer, nil
}

// ProvisionK8s setup the K8s infrastructure needed
func (ctfer *CTFer) provisionK8s(ctx *pulumi.Context) (pulumi.StringOutput, error) {
	// Create CTF's namespace
	ns := internal.GetConfig().Namespace
	_, err := corev1.NewNamespace(ctx, "ctfer-ns", &corev1.NamespaceArgs{
		Metadata: metav1.ObjectMetaArgs{
			Labels: pulumi.StringMap{
				"ctfer/infra": pulumi.String("global"),
			},
			Name: ns,
		},
	}, pulumi.Parent(ctfer))
	if err != nil {
		return pulumi.StringOutput{}, err
	}

	// Deploy HA MariaDB
	// TODO scale up to >=3
	// FIXME when scaled to 3, ctfd replicas errors
	mdb, err := NewMariaDB(ctx, "mariadb-ctfd", &MariaDBArgs{
		Namespace: ns,
		Replicas:  pulumi.Int(1),
	}, pulumi.Parent(ctfer))
	if err != nil {
		return pulumi.StringOutput{}, err
	}

	// Deploy Redis
	// TODO scale up to >=3
	// FIXME when scaled to 3, ctfd replicas errors
	rd, err := NewRedis(ctx, "redis-ctfd", &RedisArgs{
		Namespace: ns,
		Replicas:  pulumi.Int(1),
	}, pulumi.Parent(ctfer))
	if err != nil {
		return pulumi.StringOutput{}, err
	}

	// Deploy CTFd
	ctfdLabels := pulumi.StringMap{
		"ctfer/infra": pulumi.String("ctfd"),
	}

	ctfdSecret, err := random.NewRandomId(ctx, "ctfd-secret", &random.RandomIdArgs{
		ByteLength: pulumi.Int(64),
	}, pulumi.Parent(ctfer))
	if err != nil {
		return pulumi.StringOutput{}, err
	}
	_, err = corev1.NewSecret(ctx, "ctfd-secret", &corev1.SecretArgs{
		Metadata: metav1.ObjectMetaArgs{
			Name:      pulumi.String("ctfd-secret"),
			Namespace: ns,
			Labels:    ctfdLabels,
		},
		StringData: pulumi.ToStringMapOutput(map[string]pulumi.StringOutput{
			"secret_key": ctfdSecret.B64Std,
		}),
	}, pulumi.Parent(ctfer))
	if err != nil {
		return pulumi.StringOutput{}, err
	}

	_, err = corev1.NewPersistentVolumeClaim(ctx, "ctfd-pvc", &corev1.PersistentVolumeClaimArgs{
		Metadata: metav1.ObjectMetaArgs{
			Name:      pulumi.String("ctfd-pvc"),
			Namespace: ns,
			Labels:    ctfdLabels,
		},
		Spec: corev1.PersistentVolumeClaimSpecArgs{
			StorageClassName: pulumi.String("longhorn"),
			AccessModes: pulumi.ToStringArray([]string{
				"ReadWriteMany",
			}),
			Resources: corev1.ResourceRequirementsArgs{
				Requests: pulumi.ToStringMap(map[string]string{
					"storage": "2Gi",
				}),
			},
		},
	}, pulumi.Parent(ctfer))
	if err != nil {
		return pulumi.StringOutput{}, err
	}

	_, err = appsv1.NewDeployment(ctx, "ctfd-dep", &appsv1.DeploymentArgs{
		Metadata: metav1.ObjectMetaArgs{
			Name:      pulumi.String("ctfd-dep"),
			Namespace: ns,
			Labels:    ctfdLabels,
		},
		Spec: appsv1.DeploymentSpecArgs{
			Selector: metav1.LabelSelectorArgs{
				MatchLabels: ctfdLabels,
			},
			// TODO make CTFd replicas configurable
			Replicas: pulumi.Int(1),
			Template: &corev1.PodTemplateSpecArgs{
				Metadata: &metav1.ObjectMetaArgs{
					Namespace: ns,
					Labels:    ctfdLabels,
				},
				Spec: &corev1.PodSpecArgs{
					Containers: corev1.ContainerArray{
						corev1.ContainerArgs{
							Name:  pulumi.String("ctfd"),
							Image: pulumi.String("registry.pandatix.dev/ctfd/ctfd:3.5.2"),
							Env: corev1.EnvVarArray{
								corev1.EnvVarArgs{
									Name:  pulumi.String("DATABASE_URL"),
									Value: mdb.URL,
								},
								corev1.EnvVarArgs{
									Name:  pulumi.String("REDIS_URL"),
									Value: rd.URL,
								},
								corev1.EnvVarArgs{
									Name:  pulumi.String("UPLOAD_FOLDER"),
									Value: pulumi.String("/var/uploads"),
								},
								corev1.EnvVarArgs{
									Name:  pulumi.String("REVERSE_PROXY"),
									Value: pulumi.String("2,2,2,2,2"),
								},
							},
							Ports: corev1.ContainerPortArray{
								corev1.ContainerPortArgs{
									ContainerPort: pulumi.Int(8080),
								},
							},
							VolumeMounts: corev1.VolumeMountArray{
								corev1.VolumeMountArgs{
									Name:      pulumi.String("secret-key"),
									MountPath: pulumi.String("/opt/CTFd/.ctfd_secret_key"),
									SubPath:   pulumi.String("secret_key"),
								},
								corev1.VolumeMountArgs{
									Name:      pulumi.String("assets"),
									MountPath: pulumi.String("/var/uploads"),
								},
							},
							Resources: corev1.ResourceRequirementsArgs{
								Requests: pulumi.ToStringMap(map[string]string{
									"cpu":    "1",
									"memory": "256Mi",
								}),
								Limits: pulumi.ToStringMap(map[string]string{
									"memory": "512Mi",
								}),
							},
						},
					},
					Volumes: corev1.VolumeArray{
						corev1.VolumeArgs{
							Name: pulumi.String("secret-key"),
							Secret: corev1.SecretVolumeSourceArgs{
								SecretName: pulumi.String("ctfd-secret"),
							},
						},
						corev1.VolumeArgs{
							Name: pulumi.String("assets"),
							PersistentVolumeClaim: corev1.PersistentVolumeClaimVolumeSourceArgs{
								ClaimName: pulumi.String("ctfd-pvc"),
							},
						},
					},
				},
			},
		},
	}, pulumi.Parent(ctfer))
	if err != nil {
		return pulumi.StringOutput{}, err
	}

	// Export Service or Ingress with its URL
	svc, err := corev1.NewService(ctx, "ctfd-svc", &corev1.ServiceArgs{
		Metadata: metav1.ObjectMetaArgs{
			Labels:    ctfdLabels,
			Name:      pulumi.String("ctfd-svc"),
			Namespace: ns,
		},
		Spec: &corev1.ServiceSpecArgs{
			Selector: ctfdLabels,
			Ports: corev1.ServicePortArray{
				corev1.ServicePortArgs{
					TargetPort: pulumi.Int(8000),
					Port:       pulumi.Int(8000),
					Name:       pulumi.String("web"),
				},
			},
		},
	}, pulumi.Parent(ctfer))
	if err != nil {
		return pulumi.StringOutput{}, err
	}

	if internal.GetConfig().IsMinikube {
		return svc.Spec.ApplyT(func(spec *corev1.ServiceSpec) string {
			// FIXME
			return ""
		}).(pulumi.StringOutput), nil
	}

	ing, err := netwv1.NewIngress(ctx, "ctfd-ingress", &netwv1.IngressArgs{
		Metadata: metav1.ObjectMetaArgs{
			Labels:    ctfdLabels,
			Name:      pulumi.String("ctfd-ingress"),
			Namespace: ns,
			Annotations: pulumi.ToStringMap(map[string]string{
				"traefik.ingress.kubernetes.io/router.entrypoints": "web",
			}),
		},
		Spec: netwv1.IngressSpecArgs{
			Rules: netwv1.IngressRuleArray{
				netwv1.IngressRuleArgs{
					Host: internal.GetConfig().Hostname,
					Http: netwv1.HTTPIngressRuleValueArgs{
						Paths: netwv1.HTTPIngressPathArray{
							netwv1.HTTPIngressPathArgs{
								Path:     pulumi.String("/"),
								PathType: pulumi.String("Prefix"),
								Backend: netwv1.IngressBackendArgs{
									Service: netwv1.IngressServiceBackendArgs{
										Name: pulumi.String("ctfd-svc"),
										Port: netwv1.ServiceBackendPortArgs{
											Name: pulumi.String("web"),
										},
									},
								},
							},
						},
					},
				},
			},
			Tls: netwv1.IngressTLSArray{
				netwv1.IngressTLSArgs{
					// The TLS secret is defered to Traefik for TLS unpacking
					SecretName: pulumi.String("domain-tls-secret"),
				},
			},
		},
	}, pulumi.Parent(ctfer))
	if err != nil {
		return pulumi.StringOutput{}, err
	}

	return ing.Status.ApplyT(func(status *netwv1.IngressStatus) string {
		ingress := status.LoadBalancer.Ingress[0]
		if ingress.Hostname != nil {
			return fmt.Sprintf("http://%s", *ingress.Hostname)
		}
		return fmt.Sprintf("http://%s", *ingress.Ip)
	}).(pulumi.StringOutput), nil
}
