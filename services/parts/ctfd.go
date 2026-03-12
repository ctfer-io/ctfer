package parts

import (
	"strings"
	"sync"

	"github.com/ctfer-io/ctfer/services/common"
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

	secRand *random.RandomId
	sec     *corev1.Secret
	tlssec  *corev1.Secret
	pvc     *corev1.PersistentVolumeClaim
	dep     *appsv1.Deployment
	svc     *corev1.Service
	ing     *netwv1.Ingress

	// URL contains the CTFd's URL once provided.
	URL pulumi.StringOutput

	PodLabels pulumi.StringMapOutput
}

type CTFdArgs struct {
	Namespace pulumi.StringInput

	// Functional dependencies

	// PVCAccessModes defines the access modes supported by the PVC.
	PVCAccessModes pulumi.StringArrayInput
	pvcAccessModes pulumi.StringArrayOutput

	RedisURL        pulumi.StringInput
	DatabaseURL     pulumi.StringInput
	ChallManagerURL pulumi.StringInput
	OTel            *common.OTelArgs

	// CTFd settings

	Image pulumi.StringInput
	image pulumi.StringOutput

	Registry pulumi.StringInput
	registry pulumi.StringOutput

	Workers  pulumi.IntInput
	Replicas pulumi.IntInput
	Limits   pulumi.StringMapInput
	Requests pulumi.StringMapInput

	// Storage settings

	StorageSize pulumi.StringInput

	StorageClassName pulumi.StringInput
	storageClassName pulumi.StringPtrOutput

	// Exposure settings

	Crt     pulumi.StringInput
	Key     pulumi.StringInput
	withTLS bool

	Hostname pulumi.StringInput

	Annotations pulumi.StringMapInput
	annotations pulumi.StringMapOutput
}

const (
	defaultCTFdImage = "ctferio/ctfd:latest"
)

// NewCTFd creates a new pulumi Component Resource and registers it.
func NewCTFd(ctx *pulumi.Context, name string, args *CTFdArgs, opts ...pulumi.ResourceOption) (*CTFd, error) {
	ctfd := &CTFd{}
	args = ctfd.defaults(args)

	err := ctx.RegisterComponentResource("ctfer-io:ctfer:ctfd", name, ctfd, opts...)
	if err != nil {
		return nil, err
	}
	opts = append(opts, pulumi.Parent(ctfd))

	if err := ctfd.provision(ctx, args, opts...); err != nil {
		return nil, err
	}
	if err := ctfd.outputs(ctx); err != nil {
		return nil, err
	}

	return ctfd, nil
}

func (ctfd *CTFd) defaults(args *CTFdArgs) *CTFdArgs {
	if args == nil {
		args = &CTFdArgs{}
	}

	// Define private registry if any
	args.registry = pulumi.String("").ToStringOutput()
	if args.Registry != nil {
		args.registry = args.Registry.ToStringPtrOutput().ApplyT(func(in *string) string {
			// No private registry -> defaults to Docker Hub
			if in == nil {
				return ""
			}

			str := *in
			// If one set, make sure it ends with one '/'
			if str != "" && !strings.HasSuffix(str, "/") {
				str = str + "/"
			}
			return str
		}).(pulumi.StringOutput)
	}

	args.image = pulumi.String(defaultCTFdImage).ToStringOutput()
	if args.Image != nil {
		args.image = args.Image.ToStringOutput()
	}

	// Don't default storage class name -> will select the default one
	// on the K8s cluster.
	if args.StorageClassName != nil {
		args.storageClassName = args.StorageClassName.ToStringOutput().ApplyT(func(scm string) *string {
			if scm == "" {
				return nil
			}
			return &scm
		}).(pulumi.StringPtrOutput)
	}

	args.pvcAccessModes = pulumi.ToStringArray(defaultPVCAccessModes).ToStringArrayOutput()
	if args.PVCAccessModes != nil {
		args.pvcAccessModes = args.PVCAccessModes.ToStringArrayOutput().ApplyT(func(am []string) []string {
			if len(am) == 0 {
				return defaultPVCAccessModes
			}
			return am
		}).(pulumi.StringArrayOutput)
	}

	// Make sure the annotations are non-nil
	args.annotations = pulumi.StringMap{}.ToStringMapOutput()
	if args.Annotations != nil {
		args.annotations = args.Annotations.ToStringMapOutput()
	}

	// Check if will need TLS on ingress
	args.withTLS = args.Crt != nil && args.Key != nil
	if args.withTLS {
		wg := &sync.WaitGroup{}
		wg.Add(1)
		pulumi.All(args.Crt, args.Key).ApplyT(func(all []any) error {
			crt := all[0].(string)
			key := all[1].(string)
			args.withTLS = crt != "" && key != ""
			wg.Done()
			return nil
		})
		wg.Wait()
	}

	return args
}

func (ctfd *CTFd) provision(ctx *pulumi.Context, args *CTFdArgs, opts ...pulumi.ResourceOption) (err error) {
	ctfd.secRand, err = random.NewRandomId(ctx, "ctfd-secret-random", &random.RandomIdArgs{
		ByteLength: pulumi.Int(64),
	}, opts...)
	if err != nil {
		return
	}

	ctfd.sec, err = corev1.NewSecret(ctx, "ctfd-secret", &corev1.SecretArgs{
		Metadata: metav1.ObjectMetaArgs{
			Namespace: args.Namespace,
			Labels: pulumi.StringMap{
				"app.kubernetes.io/component": pulumi.String("ctfd"),
				"app.kubernetes.io/part-of":   pulumi.String("ctfer"),
				"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
			},
		},
		Data: pulumi.StringMap{
			"secret_key": ctfd.secRand.B64Std,
		},
		StringData: pulumi.StringMap{
			"redis-url":    args.RedisURL,
			"database-url": args.DatabaseURL,
		},
	}, opts...)
	if err != nil {
		return
	}

	ctfd.pvc, err = corev1.NewPersistentVolumeClaim(ctx, "ctfd-pvc", &corev1.PersistentVolumeClaimArgs{
		Metadata: metav1.ObjectMetaArgs{
			Namespace: args.Namespace,
			Annotations: pulumi.StringMap{
				"pulumi.com/skipAwait": pulumi.String("true"),
			},
			Labels: pulumi.StringMap{
				"app.kubernetes.io/component": pulumi.String("ctfd"),
				"app.kubernetes.io/part-of":   pulumi.String("ctfer"),
				"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
			},
		},
		Spec: corev1.PersistentVolumeClaimSpecArgs{
			StorageClassName: args.storageClassName,
			AccessModes:      args.pvcAccessModes,
			Resources: corev1.VolumeResourceRequirementsArgs{
				Requests: pulumi.StringMap{
					"storage": args.StorageSize,
				},
			},
		},
	}, opts...)
	if err != nil {
		return
	}

	envs := corev1.EnvVarArray{
		corev1.EnvVarArgs{
			Name: pulumi.String("DATABASE_URL"),
			ValueFrom: corev1.EnvVarSourceArgs{
				SecretKeyRef: corev1.SecretKeySelectorArgs{
					Name: ctfd.sec.Metadata.Name(),
					Key:  pulumi.String("database-url"),
				},
			},
		},
		corev1.EnvVarArgs{
			Name: pulumi.String("REDIS_URL"),
			ValueFrom: corev1.EnvVarSourceArgs{
				SecretKeyRef: corev1.SecretKeySelectorArgs{
					Name: ctfd.sec.Metadata.Name(),
					Key:  pulumi.String("redis-url"),
				},
			},
		},
		corev1.EnvVarArgs{
			Name:  pulumi.String("UPLOAD_FOLDER"),
			Value: pulumi.String("/var/uploads"),
		},
		corev1.EnvVarArgs{
			Name:  pulumi.String("REVERSE_PROXY"),
			Value: pulumi.String("true"),
		},
	}

	if args.Workers != nil {
		envs = append(envs, corev1.EnvVarArgs{
			Name:  pulumi.String("WORKERS"),
			Value: pulumi.Sprintf("%d", args.Workers),
		})
	}

	if args.ChallManagerURL != nil {
		envs = append(envs, corev1.EnvVarArgs{
			Name:  pulumi.String("PLUGIN_SETTINGS_CM_API_URL"),
			Value: args.ChallManagerURL,
		})
	}

	if args.OTel != nil {
		envs = append(envs,
			corev1.EnvVarArgs{
				Name:  pulumi.String("OTEL_SERVICE_NAME"),
				Value: args.OTel.ServiceName,
			},
			corev1.EnvVarArgs{
				Name:  pulumi.String("OTEL_EXPORTER_OTLP_ENDPOINT"),
				Value: args.OTel.Endpoint,
			},
		)
		if args.OTel.Insecure {
			envs = append(envs,
				corev1.EnvVarArgs{
					Name:  pulumi.String("OTEL_EXPORTER_OTLP_INSECURE"),
					Value: pulumi.String("true"),
				},
			)
		}
	}

	ctfd.PodLabels = pulumi.StringMap{
		"app.kubernetes.io/name":      pulumi.String("ctfd"),
		"app.kubernetes.io/component": pulumi.String("ctfd"),
		"app.kubernetes.io/part-of":   pulumi.String("ctfer"),
		"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
		"redis-client":                pulumi.String("true"), // netpol podSelector
		"postgresql-client":           pulumi.String("true"), // netpol podSelector
	}.ToStringMapOutput()
	ctfd.dep, err = appsv1.NewDeployment(ctx, "ctfd-dep", &appsv1.DeploymentArgs{
		Metadata: metav1.ObjectMetaArgs{
			Namespace: args.Namespace,
			Labels: pulumi.StringMap{
				"app.kubernetes.io/name":      pulumi.String("ctfd"),
				"app.kubernetes.io/component": pulumi.String("ctfd"),
				"app.kubernetes.io/part-of":   pulumi.String("ctfer"),
				"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
			},
		},
		Spec: appsv1.DeploymentSpecArgs{
			Selector: metav1.LabelSelectorArgs{
				MatchLabels: ctfd.PodLabels,
			},
			Replicas: args.Replicas,
			Template: &corev1.PodTemplateSpecArgs{
				Metadata: &metav1.ObjectMetaArgs{
					Namespace: args.Namespace,
					Labels:    ctfd.PodLabels,
				},
				Spec: &corev1.PodSpecArgs{
					Containers: corev1.ContainerArray{
						corev1.ContainerArgs{
							Name:  pulumi.String("ctfd"),
							Image: pulumi.Sprintf("%s%s", args.registry, args.image),
							Env:   envs,
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
								Requests: args.Requests,
								Limits:   args.Limits,
							},
							StartupProbe: corev1.ProbeArgs{
								HttpGet: corev1.HTTPGetActionArgs{
									Path: pulumi.String("/healthcheck"),
									Port: pulumi.Int(8000),
								},
								InitialDelaySeconds: pulumi.Int(30),
								PeriodSeconds:       pulumi.Int(3),
								TimeoutSeconds:      pulumi.Int(5),
							},
							LivenessProbe: corev1.ProbeArgs{
								HttpGet: corev1.HTTPGetActionArgs{
									Path: pulumi.String("/healthcheck"),
									Port: pulumi.Int(8000),
								},
								TimeoutSeconds: pulumi.Int(5),
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
								SecretName: ctfd.sec.Metadata.Name(),
							},
						},
						corev1.VolumeArgs{
							Name: pulumi.String("assets"),
							PersistentVolumeClaim: corev1.PersistentVolumeClaimVolumeSourceArgs{
								ClaimName: ctfd.pvc.Metadata.Name().Elem(),
							},
						},
					},
				},
			},
		},
	}, opts...)
	if err != nil {
		return
	}

	// Export Service or Ingress with its URL
	ctfd.svc, err = corev1.NewService(ctx, "ctfd-svc", &corev1.ServiceArgs{
		Metadata: metav1.ObjectMetaArgs{
			Labels: pulumi.StringMap{
				"app.kubernetes.io/component": pulumi.String("ctfd"),
				"app.kubernetes.io/part-of":   pulumi.String("ctfer"),
				"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
			},
			Namespace: args.Namespace,
		},
		Spec: &corev1.ServiceSpecArgs{
			Selector: ctfd.PodLabels,
			Ports: corev1.ServicePortArray{
				corev1.ServicePortArgs{
					TargetPort: pulumi.Int(8000),
					Port:       pulumi.Int(8000),
					Name:       pulumi.String("web"),
				},
			},
		},
	}, opts...)
	if err != nil {
		return
	}

	// FIXME the secret still be created even if the pulumi config does not exists
	// The secret is not valid so the default traefik cert will be used
	tlsOps := netwv1.IngressTLSArray{}
	if args.withTLS {
		ctfd.tlssec, err = corev1.NewSecret(ctx, "ctfd-secret-tls", &corev1.SecretArgs{
			Metadata: metav1.ObjectMetaArgs{
				Namespace: args.Namespace,
				Labels: pulumi.StringMap{
					"app.kubernetes.io/component": pulumi.String("ctfd"),
					"app.kubernetes.io/part-of":   pulumi.String("ctfer"),
					"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
				},
			},
			Type: pulumi.String("kubernetes.io/tls"),
			StringData: pulumi.StringMap{
				"tls.crt": args.Crt.ToStringOutput(),
				"tls.key": args.Key.ToStringOutput(),
			},
		}, opts...)
		if err != nil {
			return err
		}

		tlsOps = append(tlsOps,
			netwv1.IngressTLSArgs{
				SecretName: ctfd.tlssec.Metadata.Name(),
			},
		)
	}

	ctfd.ing, err = netwv1.NewIngress(ctx, "ctfd-ingress", &netwv1.IngressArgs{
		Metadata: metav1.ObjectMetaArgs{
			Labels: pulumi.StringMap{
				"app.kubernetes.io/component": pulumi.String("ctfd"),
				"app.kubernetes.io/part-of":   pulumi.String("ctfer"),
				"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
			},
			Namespace: args.Namespace,
			Annotations: args.annotations.ApplyT(func(annotations map[string]string) map[string]string {
				annotations["pulumi.com/skipAwait"] = "true" // Don't wait for the LoadBalancer
				return annotations
			}).(pulumi.StringMapOutput),
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
										Name: ctfd.svc.Metadata.Name().Elem(),
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
			Tls: tlsOps,
		},
	}, opts...)
	if err != nil {
		return
	}

	return
}

func (ctfd *CTFd) outputs(ctx *pulumi.Context) error {
	ctfd.URL = ctfd.ing.Spec.ApplyT(func(spec netwv1.IngressSpec) string {
		return *spec.Rules[0].Host
	}).(pulumi.StringOutput)

	// ctfd.PodLabels are set ahead of deployment to avoid deadlocks with mariadb

	return ctx.RegisterResourceOutputs(ctfd, pulumi.Map{
		"url":       ctfd.URL,
		"podLabels": ctfd.PodLabels,
	})
}
