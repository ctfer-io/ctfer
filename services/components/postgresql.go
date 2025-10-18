package components

import (
	"strings"
	"sync"

	"github.com/hashicorp/go-multierror"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	netwv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/networking/v1"
	yamlv2 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/yaml/v2"
	"github.com/pulumi/pulumi-random/sdk/v4/go/random"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type PostgreSQL struct {
	pulumi.ResourceState

	// Secret
	sec      *corev1.Secret
	userPass *random.RandomPassword

	cluster *yamlv2.ConfigGroup

	// Netpols
	pgToApi      *yamlv2.ConfigGroup
	pgFromClient *netwv1.NetworkPolicy

	URL          pulumi.StringOutput
	AccessSecret pulumi.StringOutput
	PodLabels    pulumi.StringMapOutput
}

type PostgreSQLArgs struct {
	Namespace pulumi.StringInput

	Registry pulumi.StringInput
	registry pulumi.StringOutput
}

// NewPostgreSQL creates a HA PostgreSQL cluster.
func NewPostgreSQL(ctx *pulumi.Context, name string, args *PostgreSQLArgs, opts ...pulumi.ResourceOption) (*PostgreSQL, error) {
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

	// Define private registry if any
	args.registry = pulumi.String("ghcr.io/").ToStringOutput()
	if args.Registry != nil {
		args.registry = args.Registry.ToStringPtrOutput().ApplyT(func(in *string) string {
			// No private registry -> defaults to GitHub Container Registry
			if in == nil || *in == "" {
				return "ghcr.io/"
			}

			str := *in
			// If one set, make sure it ends with one '/'
			if str != "" && !strings.HasSuffix(str, "/") {
				str = str + "/"
			}
			return str
		}).(pulumi.StringOutput)
	}

	return args
}

func (psql *PostgreSQL) check(_ *PostgreSQLArgs) error {
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

func (psql *PostgreSQL) provision(ctx *pulumi.Context, args *PostgreSQLArgs, opts ...pulumi.ResourceOption) (err error) {

	// postgreSQL to kube-apiserver
	psql.pgToApi, err = yamlv2.NewConfigGroup(ctx, "postgres-to-apiserver-cnp", &yamlv2.ConfigGroupArgs{
		Objs: pulumi.Array{
			pulumi.Map{
				"apiVersion": pulumi.String("cilium.io/v2"),
				"kind":       pulumi.String("CiliumNetworkPolicy"),
				"metadata": pulumi.Map{
					"name":      pulumi.Sprintf("cilium-pg-to-apiserver-netpol-%s", ctx.Stack()),
					"namespace": args.Namespace,
					"labels": pulumi.StringMap{
						"app.kubernetes.io/component": pulumi.String("postgresql"),
						"app.kubernetes.io/part-of":   pulumi.String("ctfer"),
						"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
					},
				},
				"spec": pulumi.Map{
					"endpointSelector": pulumi.Map{
						"matchLabels": pulumi.StringMap{
							"app.kubernetes.io/component": pulumi.String("postgresql"),
							"app.kubernetes.io/part-of":   pulumi.String("ctfer"),
							"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
						},
					},
					"egress": pulumi.Array{
						pulumi.Map{
							"toEntities": pulumi.Array{
								pulumi.String("kube-apiserver"),
							},
							"toPorts": pulumi.Array{
								pulumi.Map{
									"ports": pulumi.Array{
										pulumi.Map{
											"port":     pulumi.String("6443"),
											"protocol": pulumi.String("TCP"),
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}, opts...)
	if err != nil {
		return err
	}

	// password for postgres user
	psql.userPass, err = random.NewRandomPassword(ctx, "userPass-secret", &random.RandomPasswordArgs{
		Length:  pulumi.Int(32),
		Special: pulumi.BoolPtr(false),
	}, opts...)
	if err != nil {
		return err
	}

	psql.sec, err = corev1.NewSecret(ctx, "database-access-secret", &corev1.SecretArgs{
		Metadata: metav1.ObjectMetaArgs{
			Name:      pulumi.Sprintf("postgres.ctfd-database-%s.credentials.postgresql.acid.zalan.do", ctx.Stack()), //need to hardcode the name to override the generated secret from operator
			Namespace: args.Namespace,
			Labels: pulumi.StringMap{
				"app.kubernetes.io/component": pulumi.String("postgresql"),
				"app.kubernetes.io/part-of":   pulumi.String("ctfer"),
				"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
			},
		},
		Type: pulumi.String("Opaque"),
		StringData: pulumi.ToStringMapOutput(map[string]pulumi.StringOutput{
			"username": pulumi.String("postgres").ToStringOutput(),
			"password": psql.userPass.Result,
		}),
	}, opts...)
	if err != nil {
		return err
	}

	// Allows clients from the same stack
	psql.pgFromClient, err = netwv1.NewNetworkPolicy(ctx, "pg-from-client-netpol", &netwv1.NetworkPolicyArgs{
		Metadata: metav1.ObjectMetaArgs{
			Namespace: args.Namespace,
			Labels: pulumi.StringMap{
				"app.kubernetes.io/component": pulumi.String("postgresql"),
				"app.kubernetes.io/part-of":   pulumi.String("ctfer"),
				"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
			},
		},
		Spec: netwv1.NetworkPolicySpecArgs{
			PolicyTypes: pulumi.ToStringArray([]string{
				"Ingress",
				"Egress",
			}),
			PodSelector: metav1.LabelSelectorArgs{
				MatchLabels: pulumi.StringMap{
					"app.kubernetes.io/component": pulumi.String("postgresql"),
					"app.kubernetes.io/part-of":   pulumi.String("ctfer"),
					"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
				},
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
									"postgresql-client":         pulumi.String("true"), // CTFd
									"app.kubernetes.io/part-of": pulumi.String("ctfer"),
									"ctfer.io/stack-name":       pulumi.String(ctx.Stack()),
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
				// Allows from postgresql-operator
				netwv1.NetworkPolicyIngressRuleArgs{
					From: netwv1.NetworkPolicyPeerArray{
						netwv1.NetworkPolicyPeerArgs{
							PodSelector: metav1.LabelSelectorArgs{
								MatchLabels: pulumi.StringMap{
									"app.kubernetes.io/name": pulumi.String("postgres-operator"),
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
							Port:     pulumi.Int(8008), // Patroni
							Protocol: pulumi.String("TCP"),
						},
					},
				},
				// Allows from itself for replication
				netwv1.NetworkPolicyIngressRuleArgs{
					From: netwv1.NetworkPolicyPeerArray{
						netwv1.NetworkPolicyPeerArgs{
							PodSelector: metav1.LabelSelectorArgs{
								MatchLabels: pulumi.StringMap{
									"app.kubernetes.io/component": pulumi.String("postgresql"),
									"app.kubernetes.io/part-of":   pulumi.String("ctfer"),
									"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
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
							Port:     pulumi.Int(8008), // Patroni
							Protocol: pulumi.String("TCP"),
						},
					},
				},
			},
			Egress: netwv1.NetworkPolicyEgressRuleArray{
				// Allows to itself for replication
				netwv1.NetworkPolicyEgressRuleArgs{
					To: netwv1.NetworkPolicyPeerArray{
						netwv1.NetworkPolicyPeerArgs{
							PodSelector: metav1.LabelSelectorArgs{
								MatchLabels: pulumi.StringMap{
									"app.kubernetes.io/component": pulumi.String("postgresql"),
									"app.kubernetes.io/part-of":   pulumi.String("ctfer"),
									"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
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
							Port:     pulumi.Int(8008), // Patroni
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

	opts = append(opts, pulumi.DependsOn([]pulumi.Resource{psql.pgToApi, psql.sec, psql.pgFromClient}))

	// Create cluster with postgres-operator
	psql.cluster, err = yamlv2.NewConfigGroup(ctx, "database-cluster", &yamlv2.ConfigGroupArgs{
		Objs: pulumi.Array{
			pulumi.Map{
				"apiVersion": pulumi.String("acid.zalan.do/v1"),
				"kind":       pulumi.String("postgresql"),
				"metadata": pulumi.Map{
					"name":      pulumi.Sprintf("ctfd-database-%s", ctx.Stack()),
					"namespace": args.Namespace,
					"labels": pulumi.StringMap{
						"app.kubernetes.io/component": pulumi.String("postgresql"),
						"app.kubernetes.io/part-of":   pulumi.String("ctfer"),
						"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
					},
				},
				"spec": pulumi.Map{
					"dockerImage":       pulumi.Sprintf("%szalando/spilo-17:4.0-p3", args.registry),
					"teamId":            pulumi.String("ctfd"),
					"numberOfInstances": pulumi.Int(3), // TODO make it configurable
					"users": pulumi.Map{
						"ctfd": pulumi.StringArray{}, // XXX quid
					},
					"databases": pulumi.Map{
						"ctfd": pulumi.String("ctfd"),
					},
					"postgresql": pulumi.Map{
						"version": pulumi.String("17"),
					},
					"volume": pulumi.Map{
						"size": pulumi.String("10Gi"),
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
					// "patroni": pulumi.Map{
					// 	"failsafe_mode": pulumi.Bool(false),
					// 	"initdb": pulumi.Map{
					// 		"encoding":       pulumi.String("UTF8"),
					// 		"locale":         pulumi.String("en_US.UTF-8"),
					// 		"data-checksums": pulumi.String("true"),
					// 	},
					// 	"ttl":                     pulumi.Int(30),
					// 	"loop_wait":               pulumi.Int(10),
					// 	"retry_timeout":           pulumi.Int(10),
					// 	"synchronous_mode":        pulumi.Bool(false),
					// 	"synchronous_mode_strict": pulumi.Bool(false),
					// 	"synchronous_node_count":  pulumi.Int(1),
					// 	"maximum_lag_on_failover": pulumi.Int(33554432),
					// },
				},
			},
		},
	}, opts...)
	if err != nil {
		return err
	}

	// check that the cluster is read, how ?

	return nil
}

func (psql *PostgreSQL) outputs(ctx *pulumi.Context) error {

	psql.URL = pulumi.Sprintf("postgresql+psycopg2://postgres:%s@ctfd-database-dev1:5432/ctfd", psql.userPass.Result)
	psql.AccessSecret = pulumi.Sprintf("ctfd.ctfd-database-%s.credentials.postgresql.acid.zalan.do", ctx.Stack())
	psql.PodLabels = pulumi.StringMap{
		"app.kubernetes.io/component": pulumi.String("postgresql"),
		"app.kubernetes.io/part-of":   pulumi.String("ctfer"),
		"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
	}.ToStringMapOutput()

	return ctx.RegisterResourceOutputs(psql, pulumi.Map{
		"url":          psql.URL,
		"accessSecret": psql.AccessSecret,
		"podLabels":    psql.PodLabels,
	})
}
