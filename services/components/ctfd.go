package components

import (
	"strings"
	"sync"

	"github.com/hashicorp/go-multierror"
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
	sts     *appsv1.StatefulSet
	svc     *corev1.Service
	ing     *netwv1.Ingress

	// URL contains the CTFd's URL once provided.
	URL pulumi.StringOutput
}

type CTFdArgs struct {
	Namespace         pulumi.StringInput
	RedisSecretName   pulumi.StringInput
	MariaDBSecretName pulumi.StringInput
	Image             pulumi.StringInput
	Registry          pulumi.StringInput
	CTFdCrt           pulumi.StringInput
	CTFdKey           pulumi.StringInput
	Hostname          pulumi.StringInput
	CTFdStorageSize   pulumi.StringInput
	CTFdWorkers       pulumi.IntInput
	CTFdReplicas      pulumi.IntInput
	ChallManagerUrl   pulumi.StringInput
	CTFdLimits        pulumi.StringMapInput
	CTFdRequests      pulumi.StringMapInput

	registry pulumi.StringOutput
	image    pulumi.StringOutput
}

// NewCTFer creates a new pulumi Component Resource and registers it.
func NewCTFd(ctx *pulumi.Context, name string, args *CTFdArgs, opts ...pulumi.ResourceOption) (*CTFd, error) {
	ctfd := &CTFd{}
	args = ctfd.defaults(args)
	if err := ctfd.check(args); err != nil {
		return nil, err
	}
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

	args.image = pulumi.String("ctferio/ctfd:latest").ToStringOutput()
	if args.Image != nil {
		args.image = args.Image.ToStringOutput()
	}

	return args
}

func (ctfd *CTFd) check(args *CTFdArgs) error {
	checks := 0
	wg := &sync.WaitGroup{}
	wg.Add(checks)
	cerr := make(chan error, checks)

	// TODO perform validation checks
	// smth.ApplyT(func(abc def) ghi {
	//     defer wg.Done()
	//
	//     ... the actual test
	//     if err != nil {
	//         cerr <- err
	//         return
	//     }
	// })

	wg.Wait()
	close(cerr)

	var merr error
	for err := range cerr {
		merr = multierror.Append(merr, err)
	}
	return merr
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
			Name:      pulumi.String("ctfd-secret"),
			Namespace: args.Namespace,
			Labels: pulumi.StringMap{
				"ctfer/infra": pulumi.String("ctfd"),
			},
		},
		StringData: pulumi.ToStringMapOutput(map[string]pulumi.StringOutput{
			"secret_key": ctfd.secRand.B64Std,
		}),
	}, opts...)
	if err != nil {
		return
	}

	ctfd.pvc, err = corev1.NewPersistentVolumeClaim(ctx, "ctfd-pvc", &corev1.PersistentVolumeClaimArgs{
		Metadata: metav1.ObjectMetaArgs{
			Name:      pulumi.String("ctfd-pvc"),
			Namespace: args.Namespace,
			Labels: pulumi.StringMap{
				"ctfer/infra": pulumi.String("ctfd"),
			},
		},
		Spec: corev1.PersistentVolumeClaimSpecArgs{
			StorageClassName: pulumi.String("longhorn"),
			AccessModes: pulumi.ToStringArray([]string{
				"ReadWriteMany",
			}),
			Resources: corev1.VolumeResourceRequirementsArgs{
				Requests: pulumi.StringMap{
					"storage": args.CTFdStorageSize,
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
			Value: pulumi.String("true"),
		},
	}

	if args.CTFdWorkers != nil {
		envs = append(envs, corev1.EnvVarArgs{
			Name:  pulumi.String("WORKERS"),
			Value: pulumi.Sprintf("%d", args.CTFdWorkers),
		})
	}

	if args.ChallManagerUrl != nil {
		envs = append(envs, corev1.EnvVarArgs{
			Name:  pulumi.String("PLUGIN_SETTINGS_CM_API_URL"),
			Value: args.ChallManagerUrl,
		})
	}

	ctfd.sts, err = appsv1.NewStatefulSet(ctx, "ctfd-sts", &appsv1.StatefulSetArgs{
		Metadata: metav1.ObjectMetaArgs{
			Name:      pulumi.String("ctfd-sts"),
			Namespace: args.Namespace,
			Labels: pulumi.StringMap{
				"ctfer/infra": pulumi.String("ctfd"),
			},
		},
		Spec: appsv1.StatefulSetSpecArgs{
			Selector: metav1.LabelSelectorArgs{
				MatchLabels: pulumi.StringMap{
					"ctfer/infra": pulumi.String("ctfd"),
				},
			},
			Replicas: args.CTFdReplicas,
			Template: &corev1.PodTemplateSpecArgs{
				Metadata: &metav1.ObjectMetaArgs{
					Namespace: args.Namespace,
					Labels: pulumi.StringMap{
						"ctfer/infra": pulumi.String("ctfd"),
					},
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
								Requests: args.CTFdRequests,
								Limits:   args.CTFdLimits,
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
	}, opts...)
	if err != nil {
		return
	}

	// Export Service or Ingress with its URL
	ctfd.svc, err = corev1.NewService(ctx, "ctfd-svc", &corev1.ServiceArgs{
		Metadata: metav1.ObjectMetaArgs{
			Labels: pulumi.StringMap{
				"ctfer/infra": pulumi.String("ctfd"),
			},
			Name:      pulumi.String("ctfd-svc"),
			Namespace: args.Namespace,
		},
		Spec: &corev1.ServiceSpecArgs{
			Selector: pulumi.StringMap{
				"ctfer/infra": pulumi.String("ctfd"),
			},
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
	if args.CTFdCrt != nil && args.CTFdKey != nil {
		ctfd.tlssec, err = corev1.NewSecret(ctx, "ctfd-secret-tls", &corev1.SecretArgs{
			Metadata: metav1.ObjectMetaArgs{
				Namespace: args.Namespace,
				Labels: pulumi.StringMap{
					"ctfer/infra": pulumi.String("ctfd"),
				},
			},
			Type: pulumi.String("kubernetes.io/tls"),
			StringData: pulumi.StringMap{
				"tls.crt": args.CTFdCrt.ToStringOutput(),
				"tls.key": args.CTFdKey.ToStringOutput(),
			},
		}, opts...)
		if err != nil {
			return err
		}

		tlsOps = append(tlsOps,
			netwv1.IngressTLSArgs{
				SecretName: ctfd.tlssec.Metadata.Name(),
			})
	}

	ctfd.ing, err = netwv1.NewIngress(ctx, "ctfd-ingress", &netwv1.IngressArgs{
		Metadata: metav1.ObjectMetaArgs{
			Labels: pulumi.StringMap{
				"ctfer/infra": pulumi.String("ctfd"),
			},
			Name:      pulumi.String("ctfd-ingress"),
			Namespace: args.Namespace,
			Annotations: pulumi.ToStringMap(map[string]string{
				"pulumi.com/skipAwait": "true",
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

	return ctx.RegisterResourceOutputs(ctfd, pulumi.Map{
		"url": ctfd.URL,
	})
}
