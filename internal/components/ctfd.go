package components

import (
	appsv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/apps/v1"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	netwv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/networking/v1"
	"github.com/pulumi/pulumi-random/sdk/v4/go/random"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// CTFd is a pulumi Component that deploy a pre-configured CTFd stack
// in an on-premise K8s cluster with Traefik as Ingress Controller.
type CTFd struct {
	pulumi.ResourceState

	// URL contains the CTFd's URL once provided.
	URL pulumi.StringOutput
}

// NewCTFer creates a new pulumi Component Resource and registers it.
func NewCTFd(ctx *pulumi.Context, name string, args *CTFdArgs, opts ...pulumi.ResourceOption) (*CTFd, error) {
	ctfd := &CTFd{}

	// Register the Component Resource
	err := ctx.RegisterComponentResource("ctfer:l4:CTFd", name, ctfd, opts...)
	if err != nil {
		return nil, err
	}

	// Provision K8s resources
	url, err := ctfd.provisionK8s(ctx, args)
	if err != nil {
		return nil, err
	}
	ctfd.URL = url

	return ctfd, nil
}

// ProvisionK8s setup the K8s infrastructure needed
func (ctfer *CTFd) provisionK8s(ctx *pulumi.Context, args *CTFdArgs) (pulumi.StringOutput, error) {

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
			Namespace: args.Namespace,
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
			Namespace: args.Namespace,
			Labels:    ctfdLabels,
		},
		Spec: corev1.PersistentVolumeClaimSpecArgs{
			StorageClassName: pulumi.String("longhorn"),
			AccessModes: pulumi.ToStringArray([]string{
				"ReadWriteMany",
			}),
			Resources: corev1.VolumeResourceRequirementsArgs{
				Requests: pulumi.ToStringMap(map[string]string{
					"storage": "2Gi", // TODO make it configurable
				}),
			},
		},
	}, pulumi.Parent(ctfer))
	if err != nil {
		return pulumi.StringOutput{}, err
	}

	image := args.Image
	if args.Registry != "" {
		image = args.Registry + "/" + args.Image
	}

	_, err = appsv1.NewStatefulSet(ctx, "ctfd-sts", &appsv1.StatefulSetArgs{
		Metadata: metav1.ObjectMetaArgs{
			Name:      pulumi.String("ctfd-sts"),
			Namespace: args.Namespace,
			Labels:    ctfdLabels,
		},
		Spec: appsv1.StatefulSetSpecArgs{
			Selector: metav1.LabelSelectorArgs{
				MatchLabels: ctfdLabels,
			},
			Replicas: pulumi.Int(3),
			Template: &corev1.PodTemplateSpecArgs{
				Metadata: &metav1.ObjectMetaArgs{
					Namespace: args.Namespace,
					Labels:    ctfdLabels,
				},
				Spec: &corev1.PodSpecArgs{
					Containers: corev1.ContainerArray{
						corev1.ContainerArgs{
							Name:  pulumi.String("ctfd"),
							Image: image,
							Env: corev1.EnvVarArray{
								corev1.EnvVarArgs{
									Name: pulumi.String("DATABASE_URL"),
									ValueFrom: corev1.EnvVarSourceArgs{
										SecretKeyRef: corev1.SecretKeySelectorArgs{
											Name: args.MariaDBSecretName,
											Key:  pulumi.String("mariadb-url"),
										},
									},
								},
								corev1.EnvVarArgs{
									Name: pulumi.String("REDIS_URL"),
									ValueFrom: corev1.EnvVarSourceArgs{
										SecretKeyRef: corev1.SecretKeySelectorArgs{
											Name: args.RedisSecretName,
											Key:  pulumi.String("redis-url"),
										},
									},
								},
								corev1.EnvVarArgs{
									Name:  pulumi.String("UPLOAD_FOLDER"), // contains scenario.zip
									Value: pulumi.String("/var/uploads"),
								},
								corev1.EnvVarArgs{
									Name:  pulumi.String("REVERSE_PROXY"),
									Value: pulumi.String("2,2,2,2,2"),
								},
							},
							Ports: corev1.ContainerPortArray{
								corev1.ContainerPortArgs{
									ContainerPort: pulumi.Int(8000),
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
							ReadinessProbe: corev1.ProbeArgs{
								HttpGet: corev1.HTTPGetActionArgs{
									Path: pulumi.String("/"),
									Port: pulumi.Int(8000),
								},
								InitialDelaySeconds: pulumi.Int(30),
								PeriodSeconds:       pulumi.Int(3),
								TimeoutSeconds:      pulumi.Int(5),
								SuccessThreshold:    pulumi.Int(1),
								FailureThreshold:    pulumi.Int(3),
							},
						},
					},
					Tolerations: corev1.TolerationArray{
						corev1.TolerationArgs{
							Key:               pulumi.String("node.kubernetes.io/not-ready"),
							Operator:          pulumi.String("Exists"),
							Effect:            pulumi.String("NoExecute"),
							TolerationSeconds: pulumi.Int(30),
						},
						corev1.TolerationArgs{
							Key:               pulumi.String("node.kubernetes.io/unreachable"),
							Operator:          pulumi.String("Exists"),
							Effect:            pulumi.String("NoExecute"),
							TolerationSeconds: pulumi.Int(30),
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
	_, err = corev1.NewService(ctx, "ctfd-svc", &corev1.ServiceArgs{
		Metadata: metav1.ObjectMetaArgs{
			Labels:    ctfdLabels,
			Name:      pulumi.String("ctfd-svc"),
			Namespace: args.Namespace,
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

	ing, err := netwv1.NewIngress(ctx, "ctfd-ingress", &netwv1.IngressArgs{
		Metadata: metav1.ObjectMetaArgs{
			Labels:    ctfdLabels,
			Name:      pulumi.String("ctfd-ingress"),
			Namespace: args.Namespace,
			Annotations: pulumi.ToStringMap(map[string]string{
				"traefik.ingress.kubernetes.io/router.entrypoints": "websecure",
				"pulumi.com/skipAwait":                             "true",
			}),
		},
		Spec: netwv1.IngressSpecArgs{
			Rules: netwv1.IngressRuleArray{
				netwv1.IngressRuleArgs{
					Host: args.Hostname,
					Http: netwv1.HTTPIngressRuleValueArgs{
						Paths: netwv1.HTTPIngressPathArray{
							netwv1.HTTPIngressPathArgs{
								Path:     pulumi.String("/"),
								PathType: pulumi.String("Prefix"),
								Backend: netwv1.IngressBackendArgs{
									Service: netwv1.IngressServiceBackendArgs{
										// Name: pulumi.String("ctfd-keda-svc"),
										Name: pulumi.String("ctfd-svc"),
										Port: netwv1.ServiceBackendPortArgs{
											Name: pulumi.String("web"),
											// Number: pulumi.Int(8080),
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

	return ing.Spec.ApplyT(func(spec netwv1.IngressSpec) string {
		return *spec.Rules[0].Host
	}).(pulumi.StringOutput), nil
}

type CTFdArgs struct {
	// Namespace to deploy to.
	Namespace         pulumi.String
	RedisSecretName   pulumi.StringInput
	MariaDBSecretName pulumi.StringInput
	Image             pulumi.String
	Registry          pulumi.String
	Hostname          pulumi.String
}
