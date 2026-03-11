package parts

import (
	"bytes"
	"context"
	"sync"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	netwv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/networking/v1"
	yamlv2 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/yaml/v2"
	"github.com/pulumi/pulumi-random/sdk/v4/go/random"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"go.uber.org/multierr"
)

type PostgreSQL struct {
	pulumi.ResourceState

	// owner access
	userName pulumi.String
	userPass *random.RandomPassword
	userSec  *corev1.Secret

	cluster     *yamlv2.ConfigGroup
	pooler      *yamlv2.ConfigGroup
	clusterName pulumi.StringInput

	// Netpols
	pgToAPI          *yamlv2.ConfigGroup
	poolerFromClient *netwv1.NetworkPolicy
	poolerToPg       *netwv1.NetworkPolicy
	pgFromPooler     *netwv1.NetworkPolicy
	pgReplication    *netwv1.NetworkPolicy
	pgFromOperator   *netwv1.NetworkPolicy

	URL       pulumi.StringOutput
	PodLabels pulumi.StringMapOutput

	commonLabels    pulumi.StringMapOutput
	poolerPodLabels pulumi.StringMapOutput
	pgPodLabels     pulumi.StringMapOutput
}

type PostgreSQLArgs struct {
	Namespace pulumi.StringInput

	Registry pulumi.StringPtrInput

	// PgToAPIServerTemplate is a Go text/template that defines the NetworkPolicy
	// YAML schema to use.
	// If none set, it is defaulted to a cilium.io/v2 CiliumNetworkPolicy.
	PgToAPIServerTemplate pulumi.StringPtrInput
	pgToAPIServerTemplate pulumi.StringOutput

	ClusterNamePrefix pulumi.StringPtrInput
	clusterNamePrefix pulumi.StringOutput

	// PostgresOperatorNamespace is the namespace where the postgres-operator
	// from cnpg is installed.
	// If none set, it is defaulted to "default" namespace.
	PostgresOperatorNamespace pulumi.StringPtrInput
	postgresOperatorNamespace pulumi.StringOutput

	StorageClassName pulumi.StringInput
	storageClassName pulumi.StringPtrOutput
}

const (
	defaultClusterNamePrefix         = "ctfer-database"
	defaultPostgresOperatorNamespace = "default"
	defaultPgToAPIServerTemplate     = `
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata:
  name: cilium-pg-to-apiserver-allow-{{ .Stack }}
  namespace: {{ .Namespace }}
spec:
  endpointSelector:
    matchLabels:
    {{- range $k, $v := .PodLabels }}
      {{ $k }}: {{ $v }}
    {{- end }}
  egress:
  - toEntities:
    - kube-apiserver
  - toPorts:
    - ports:
      - port: "6443"
        protocol: TCP
`
)

// NewPostgreSQL creates a HA PostgreSQL cluster.
// The https://github.com/zalando/postgres-operator with CRDs need to be installed on the cluster before.
func NewPostgreSQL(
	ctx *pulumi.Context,
	name string,
	args *PostgreSQLArgs,
	opts ...pulumi.ResourceOption,
) (*PostgreSQL, error) {

	psql := &PostgreSQL{}
	args = psql.defaults(args)
	if err := psql.check(args); err != nil {
		return nil, err
	}
	err := ctx.RegisterComponentResource("ctfer-io:ctfer:postgresql", name, psql, opts...)
	if err != nil {
		return nil, err
	}
	opts = append(opts, pulumi.Parent(psql))

	if err := psql.provision(ctx, args, opts...); err != nil {
		return nil, err
	}
	if err := psql.outputs(ctx); err != nil {
		return nil, err
	}

	return psql, nil
}

func (psql *PostgreSQL) defaults(args *PostgreSQLArgs) *PostgreSQLArgs {
	if args == nil {
		args = &PostgreSQLArgs{}
	}

	// Define custom clusterName prefix if any
	args.clusterNamePrefix = pulumi.String(defaultClusterNamePrefix).ToStringOutput()
	if args.ClusterNamePrefix != nil {
		args.clusterNamePrefix = args.ClusterNamePrefix.ToStringPtrOutput().ApplyT(func(in *string) string {
			// No custom ClusterName
			if in == nil || *in == "" {
				return defaultClusterNamePrefix
			}
			return *in
		}).(pulumi.StringOutput)
	}

	args.pgToAPIServerTemplate = pulumi.String(defaultPgToAPIServerTemplate).ToStringOutput()
	if args.PgToAPIServerTemplate != nil {
		args.pgToAPIServerTemplate = args.PgToAPIServerTemplate.ToStringPtrOutput().
			ApplyT(func(pgToApiServerTemplate *string) string {
				if pgToApiServerTemplate == nil || *pgToApiServerTemplate == "" {
					return defaultPgToAPIServerTemplate
				}
				return *pgToApiServerTemplate
			}).(pulumi.StringOutput)
	}

	// Define custom postgres-operator
	args.postgresOperatorNamespace = pulumi.String(defaultPostgresOperatorNamespace).ToStringOutput()
	if args.PostgresOperatorNamespace != nil {
		args.postgresOperatorNamespace = args.PostgresOperatorNamespace.ToStringPtrOutput().ApplyT(func(in *string) string {
			// No custom ClusterName
			if in == nil || *in == "" {
				return defaultPostgresOperatorNamespace
			}
			return *in
		}).(pulumi.StringOutput)
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

	return args
}

func (psql *PostgreSQL) check(args *PostgreSQLArgs) error {
	checks := 1
	wg := &sync.WaitGroup{}
	wg.Add(checks)
	cerr := make(chan error, checks)

	// Verify the template is syntactically valid.
	args.pgToAPIServerTemplate.ApplyT(func(pgToApiServerTemplate string) error {
		defer wg.Done()

		_, err := template.New("pg-to-apiserver").
			Funcs(sprig.FuncMap()).
			Parse(pgToApiServerTemplate)
		cerr <- err
		return nil
	})

	wg.Wait()
	close(cerr)

	var merr error
	for err := range cerr {
		merr = multierr.Append(merr, err)
	}
	return merr
}

func (psql *PostgreSQL) provision(
	ctx *pulumi.Context,
	args *PostgreSQLArgs,
	opts ...pulumi.ResourceOption,
) (err error) {

	psql.clusterName = pulumi.Sprintf("%s-%s", args.clusterNamePrefix, ctx.Stack())

	psql.commonLabels = pulumi.ToStringMap(map[string]string{
		"app.kubernetes.io/part-of": "ctfer",
		"ctfer.io/stack-name":       ctx.Stack(),
	}).ToStringMapOutput()

	// CNPG manages a set of labels, including app.kubernetes.io/component and app.kubernetes.io/name for its
	// resources (including the Pooler and Cluster).
	// The minimal set of shared labels are used to parallel deployability while keeping network interactions
	// tied to one Pulumi stack. For this reason, we have to stick with the common labels, thus not define the
	// component and name we would want to give it...
	// Here we define what CNPG is going to set, to ensure proper network policies.
	psql.poolerPodLabels = pulumi.ToStringMap(map[string]string{
		"app.kubernetes.io/component": "pooler",     // DO NOT CHANGE
		"app.kubernetes.io/name":      "postgresql", // DO NOT CHANGE
		"app.kubernetes.io/part-of":   "ctfer",
		"ctfer.io/stack-name":         ctx.Stack(),
	}).ToStringMapOutput()

	// In the case of PostgreSQL pods, we can define them ! :D
	psql.pgPodLabels = pulumi.ToStringMap(map[string]string{
		"app.kubernetes.io/component": "database",
		"app.kubernetes.io/name":      "postgresql",
		"app.kubernetes.io/part-of":   "ctfer",
		"ctfer.io/stack-name":         ctx.Stack(),
	}).ToStringMapOutput()

	// postgreSQL to kube-apiserver
	psql.pgToAPI, err = yamlv2.NewConfigGroup(ctx, "kube-apiserver-netpol", &yamlv2.ConfigGroupArgs{
		Yaml: pulumi.All(args.pgToAPIServerTemplate, args.Namespace, psql.commonLabels).
			ApplyT(func(all []any) (string, error) {
				pgToAPIServerTemplate := all[0].(string)
				namespace := all[1].(string)
				podLabels := all[2].(map[string]string)

				tmpl, _ := template.New("pg-to-apiserver").
					Funcs(sprig.FuncMap()).
					Parse(pgToAPIServerTemplate)

				buf := &bytes.Buffer{}
				if err := tmpl.Execute(buf, map[string]any{
					"Stack":     ctx.Stack(),
					"Namespace": namespace,
					"PodLabels": podLabels,
				}); err != nil {
					return "", err
				}
				return buf.String(), nil
			}).(pulumi.StringOutput),
	}, opts...)
	if err != nil {
		return err
	}

	// password for owner user (ctfd)
	username := "ctfd"
	psql.userName = pulumi.String(username)
	psql.userPass, err = random.NewRandomPassword(ctx, "owner-secret", &random.RandomPasswordArgs{
		Length:  pulumi.Int(32),
		Special: pulumi.BoolPtr(false),
	}, opts...)
	if err != nil {
		return err
	}

	psql.userSec, err = corev1.NewSecret(ctx, "owner-access-secret", &corev1.SecretArgs{
		Metadata: metav1.ObjectMetaArgs{
			Namespace: args.Namespace,
			Labels:    psql.commonLabels,
		},
		Type: pulumi.String("kubernetes.io/basic-auth"),
		StringData: pulumi.ToStringMapOutput(map[string]pulumi.StringOutput{
			"username": psql.userName.ToStringOutput(),
			"password": psql.userPass.Result,
		}),
	}, opts...)
	if err != nil {
		return err
	}

	// Allow traffic from cnpg-system to Pg and Pooler
	psql.pgFromOperator, err = netwv1.NewNetworkPolicy(ctx, "pg-from-operator-netpol", &netwv1.NetworkPolicyArgs{
		Metadata: metav1.ObjectMetaArgs{
			Namespace: args.Namespace,
			Labels:    psql.commonLabels,
		},
		Spec: netwv1.NetworkPolicySpecArgs{
			PolicyTypes: pulumi.ToStringArray([]string{
				"Ingress",
			}),
			PodSelector: metav1.LabelSelectorArgs{
				MatchLabels: psql.commonLabels,
			},
			Ingress: netwv1.NetworkPolicyIngressRuleArray{
				netwv1.NetworkPolicyIngressRuleArgs{
					From: netwv1.NetworkPolicyPeerArray{
						netwv1.NetworkPolicyPeerArgs{
							NamespaceSelector: metav1.LabelSelectorArgs{
								MatchLabels: pulumi.StringMap{
									"kubernetes.io/metadata.name": args.postgresOperatorNamespace,
								},
							},
						},
					},
					Ports: netwv1.NetworkPolicyPortArray{
						netwv1.NetworkPolicyPortArgs{
							Port:     pulumi.Int(5432), // PostgreSQL
							Protocol: pulumi.String("TCP"),
						},
						netwv1.NetworkPolicyPortArgs{
							Port:     pulumi.Int(8000), // Status
							Protocol: pulumi.String("TCP"),
						},
					},
				},
			},
		},
	}, opts...)
	if err != nil {
		return err
	}

	// Allows Postgres from PGBouncer
	psql.pgFromPooler, err = netwv1.NewNetworkPolicy(ctx, "pg-from-pooler-netpol", &netwv1.NetworkPolicyArgs{
		Metadata: metav1.ObjectMetaArgs{
			Namespace: args.Namespace,
			Labels:    psql.commonLabels,
		},
		Spec: netwv1.NetworkPolicySpecArgs{
			PolicyTypes: pulumi.ToStringArray([]string{
				"Ingress",
			}),
			PodSelector: metav1.LabelSelectorArgs{
				MatchLabels: psql.pgPodLabels,
			},
			Ingress: netwv1.NetworkPolicyIngressRuleArray{
				netwv1.NetworkPolicyIngressRuleArgs{
					From: netwv1.NetworkPolicyPeerArray{
						netwv1.NetworkPolicyPeerArgs{
							PodSelector: metav1.LabelSelectorArgs{
								MatchLabels: psql.poolerPodLabels,
							},
						},
					},
					Ports: netwv1.NetworkPolicyPortArray{
						netwv1.NetworkPolicyPortArgs{
							Port:     pulumi.Int(5432), // PostgreSQL
							Protocol: pulumi.String("TCP"),
						},
						// Check if Status needed ?
					},
				},
			},
		},
	}, opts...)
	if err != nil {
		return err
	}

	// Allows PGBouncer to Postgres
	psql.poolerToPg, err = netwv1.NewNetworkPolicy(ctx, "pooler-to-pg-netpol", &netwv1.NetworkPolicyArgs{
		Metadata: metav1.ObjectMetaArgs{
			Namespace: args.Namespace,
			Labels:    psql.commonLabels,
		},
		Spec: netwv1.NetworkPolicySpecArgs{
			PolicyTypes: pulumi.ToStringArray([]string{
				"Egress",
			}),
			PodSelector: metav1.LabelSelectorArgs{
				MatchLabels: psql.poolerPodLabels,
			},
			Egress: netwv1.NetworkPolicyEgressRuleArray{
				netwv1.NetworkPolicyEgressRuleArgs{
					To: netwv1.NetworkPolicyPeerArray{
						netwv1.NetworkPolicyPeerArgs{
							PodSelector: metav1.LabelSelectorArgs{
								MatchLabels: psql.pgPodLabels,
							},
						},
					},
					Ports: netwv1.NetworkPolicyPortArray{
						netwv1.NetworkPolicyPortArgs{
							Port:     pulumi.Int(5432), // PostgreSQL
							Protocol: pulumi.String("TCP"),
						},
					},
				},
			},
		},
	}, opts...)
	if err != nil {
		return err
	}

	// Allows Postgres to each other for replication
	psql.pgReplication, err = netwv1.NewNetworkPolicy(ctx, "pg-replication-netpol", &netwv1.NetworkPolicyArgs{
		Metadata: metav1.ObjectMetaArgs{
			Namespace: args.Namespace,
			Labels:    psql.commonLabels,
		},
		Spec: netwv1.NetworkPolicySpecArgs{
			PolicyTypes: pulumi.ToStringArray([]string{
				"Ingress",
				"Egress",
			}),
			PodSelector: metav1.LabelSelectorArgs{
				MatchLabels: psql.pgPodLabels,
			},
			Ingress: netwv1.NetworkPolicyIngressRuleArray{
				netwv1.NetworkPolicyIngressRuleArgs{
					From: netwv1.NetworkPolicyPeerArray{
						netwv1.NetworkPolicyPeerArgs{
							PodSelector: metav1.LabelSelectorArgs{
								MatchLabels: psql.pgPodLabels,
							},
						},
					},
					Ports: netwv1.NetworkPolicyPortArray{
						netwv1.NetworkPolicyPortArgs{
							Port:     pulumi.Int(5432), // PostgreSQL
							Protocol: pulumi.String("TCP"),
						},
						netwv1.NetworkPolicyPortArgs{
							Port:     pulumi.Int(8000), // Status
							Protocol: pulumi.String("TCP"),
						},
					},
				},
			},
			Egress: netwv1.NetworkPolicyEgressRuleArray{
				netwv1.NetworkPolicyEgressRuleArgs{
					To: netwv1.NetworkPolicyPeerArray{
						netwv1.NetworkPolicyPeerArgs{
							PodSelector: metav1.LabelSelectorArgs{
								MatchLabels: psql.pgPodLabels,
							},
						},
					},
					Ports: netwv1.NetworkPolicyPortArray{
						netwv1.NetworkPolicyPortArgs{
							Port:     pulumi.Int(5432), // PostgreSQL
							Protocol: pulumi.String("TCP"),
						},
						netwv1.NetworkPolicyPortArgs{
							Port:     pulumi.Int(8000), // Status
							Protocol: pulumi.String("TCP"),
						},
					},
				},
			},
		},
	}, opts...)
	if err != nil {
		return err
	}

	// Allows clients from the same stack to pooler
	psql.poolerFromClient, err = netwv1.NewNetworkPolicy(ctx, "pooler-from-client-netpol", &netwv1.NetworkPolicyArgs{
		Metadata: metav1.ObjectMetaArgs{
			Namespace: args.Namespace,
			Labels:    psql.commonLabels,
		},
		Spec: netwv1.NetworkPolicySpecArgs{
			PolicyTypes: pulumi.ToStringArray([]string{
				"Ingress",
			}),
			PodSelector: metav1.LabelSelectorArgs{
				MatchLabels: psql.poolerPodLabels,
			},
			Ingress: netwv1.NetworkPolicyIngressRuleArray{
				// Allows from explicit clients (e.g CTFd)
				netwv1.NetworkPolicyIngressRuleArgs{
					From: netwv1.NetworkPolicyPeerArray{
						netwv1.NetworkPolicyPeerArgs{
							NamespaceSelector: metav1.LabelSelectorArgs{
								MatchLabels: pulumi.StringMap{
									"kubernetes.io/metadata.name": args.Namespace,
								},
							},
							PodSelector: metav1.LabelSelectorArgs{
								MatchLabels: pulumi.StringMap{
									"postgresql-client": pulumi.String("true"), // CTFd
								},
							},
						},
					},
					Ports: netwv1.NetworkPolicyPortArray{
						netwv1.NetworkPolicyPortArgs{
							Port:     pulumi.Int(5432),
							Protocol: pulumi.String("TCP"),
						},
					},
				},
			},
		},
	}, opts...)
	if err != nil {
		return err
	}

	opts = append(opts, pulumi.DependsOn([]pulumi.Resource{
		psql.userSec,
		psql.pgToAPI,
		psql.poolerFromClient,
		psql.poolerToPg,
		psql.pgFromPooler,
		psql.pgReplication,
		psql.pgFromOperator,
	}))

	// Create cluster with postgres-operator
	psql.cluster, err = yamlv2.NewConfigGroup(ctx, "database-cluster", &yamlv2.ConfigGroupArgs{
		Objs: pulumi.Array{
			// Cluster
			pulumi.Map{
				"apiVersion": pulumi.String("postgresql.cnpg.io/v1"),
				"kind":       pulumi.String("Cluster"),
				"metadata": pulumi.Map{
					"name":      psql.clusterName,
					"namespace": args.Namespace,
					"labels":    psql.pgPodLabels,
				},
				"spec": pulumi.Map{
					"instances": pulumi.Int(3), // TODO make it configurable
					"inheritedMetadata": pulumi.Map{
						"labels": psql.pgPodLabels,
					},
					"storage": pulumi.Map{
						"size":         pulumi.String("10Gi"), // TODO make it configurable
						"storageClass": args.storageClassName,
					},
					"bootstrap": pulumi.Map{
						"initdb": pulumi.Map{
							"database": pulumi.String("ctfd"),
							"owner":    psql.userName,
							"secret": pulumi.Map{
								"name": psql.userSec.Metadata.Name(),
							},
						},
					},
					"resources": pulumi.Map{
						"requests": pulumi.Map{
							"cpu":    pulumi.String("500m"),
							"memory": pulumi.String("500Mi"),
						},
						"limits": pulumi.Map{
							"cpu":    pulumi.String("500m"),
							"memory": pulumi.String("500Mi"),
						},
					},
				},
			},
		},
	}, opts...)
	if err != nil {
		return err
	}

	// pooler explicit deps with cluster
	psql.pooler, err = yamlv2.NewConfigGroup(ctx, "database-pooler", &yamlv2.ConfigGroupArgs{
		Objs: pulumi.Array{
			// Pooler
			pulumi.Map{
				"apiVersion": pulumi.String("postgresql.cnpg.io/v1"),
				"kind":       pulumi.String("Pooler"),
				"metadata": pulumi.Map{
					"name":      pulumi.Sprintf("%s-pooler", psql.clusterName),
					"namespace": args.Namespace,
					"labels":    psql.poolerPodLabels,
				},
				"spec": pulumi.Map{
					"cluster": pulumi.Map{
						"name": psql.clusterName,
					},
					"instances": pulumi.Int(3),
					"type":      pulumi.String("rw"),
					"template": pulumi.Map{
						"metadata": pulumi.Map{
							"labels": psql.poolerPodLabels, // label on Pooler Pod's
						},
					},
					"serviceTemplate": pulumi.Map{
						"metadata": pulumi.Map{
							"labels": psql.poolerPodLabels,
						},
						"spec": pulumi.Map{
							"type": pulumi.String("ClusterIP"),
						},
					},
					"pgbouncer": pulumi.Map{
						"poolMode": pulumi.String("transaction"),
						"parameters": pulumi.Map{
							"max_client_conn":   pulumi.String("1000"),
							"default_pool_size": pulumi.String("10"),
						},
					},
				},
			},
		},
	}, append(
		opts,
		pulumi.DependsOn([]pulumi.Resource{
			psql.cluster,
		}),
		pulumi.Transforms([]pulumi.ResourceTransform{
			func(_ context.Context, args *pulumi.ResourceTransformArgs) *pulumi.ResourceTransformResult {
				switch args.Type {
				case "postgresql.cnpg.io/v1:Pooler":
					// Remove the finalizers
					args.Props["metadata"].(pulumi.Map)["finalizers"] = pulumi.StringArray{}
					return &pulumi.ResourceTransformResult{
						Props: args.Props,
						Opts:  args.Opts,
					}

				default:
					return nil
				}
			},
		}),
	)...)
	if err != nil {
		return err
	}

	return nil
}

func (psql *PostgreSQL) outputs(ctx *pulumi.Context) error {
	psql.URL = pulumi.Sprintf(
		"postgresql+psycopg2://%[1]s:%[2]s@%[3]s-pooler:5432/%[1]s",
		psql.userName, psql.userPass.Result, psql.clusterName,
	)
	psql.PodLabels = psql.poolerPodLabels

	return ctx.RegisterResourceOutputs(psql, pulumi.Map{
		"url":       psql.URL,
		"podLabels": psql.PodLabels,
	})
}
