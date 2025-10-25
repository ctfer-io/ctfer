package components

import (
	"bytes"
	"strings"
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
	clusterName pulumi.StringInput

	// Netpols
	pgToApi      *yamlv2.ConfigGroup
	pgFromClient *netwv1.NetworkPolicy

	URL       pulumi.StringOutput
	PodLabels pulumi.StringMapOutput
	podLabels pulumi.StringMapInput
}

type PostgreSQLArgs struct {
	Namespace pulumi.StringInput

	Registry pulumi.StringPtrInput

	// PgToApiServerTemplate is a Go text/template that defines the NetworkPolicy
	// YAML schema to use.
	// If none set, it is defaulted to a cilium.io/v2 CiliumNetworkPolicy.
	PgToApiServerTemplate pulumi.StringPtrInput
	pgToApiServerTemplate pulumi.StringOutput

	ClusterNamePrefix pulumi.StringPtrInput
	clusterNamePrefix pulumi.StringOutput

	// PostgresOperatorNamespace is the namespace where the postgres-operator
	// from zalando is installed.
	// If none set, it is defaulted to "default" namespace.
	PostgresOperatorNamespace pulumi.StringPtrInput
	postgresOperatorNamespace pulumi.StringOutput

	StorageClassName pulumi.StringInput
	storageClassName pulumi.StringPtrOutput
}

const (
	defaultClusterNamePrefix         = "ctfer-database"
	defaultPostgresOperatorNamespace = "default"
	defaultPgToApiServerTemplate     = `
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

	args.pgToApiServerTemplate = pulumi.String(defaultPgToApiServerTemplate).ToStringOutput()
	if args.PgToApiServerTemplate != nil {
		args.pgToApiServerTemplate = args.PgToApiServerTemplate.ToStringPtrOutput().ApplyT(func(pgToApiServerTemplate *string) string {
			if pgToApiServerTemplate == nil || *pgToApiServerTemplate == "" {
				return defaultPgToApiServerTemplate
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
	args.pgToApiServerTemplate.ApplyT(func(pgToApiServerTemplate string) error {
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

func (psql *PostgreSQL) provision(ctx *pulumi.Context, args *PostgreSQLArgs, opts ...pulumi.ResourceOption) (err error) {

	psql.podLabels = pulumi.StringMap{
		"app.kubernetes.io/component": pulumi.String("postgresql"),
		"app.kubernetes.io/part-of":   pulumi.String("ctfer"),
		"ctfer.io/stack-name":         pulumi.String(ctx.Stack()),
	}

	// postgreSQL to kube-apiserver
	psql.pgToApi, err = yamlv2.NewConfigGroup(ctx, "kube-apiserver-netpol", &yamlv2.ConfigGroupArgs{
		Yaml: pulumi.All(args.pgToApiServerTemplate, args.Namespace, psql.podLabels).ApplyT(func(all []any) (string, error) {
			cmToApiServerTemplate := all[0].(string)
			namespace := all[1].(string)
			podLabels := all[2].(map[string]string)

			tmpl, _ := template.New("cm-to-apiserver").
				Funcs(sprig.FuncMap()).
				Parse(cmToApiServerTemplate)

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

	psql.clusterName = pulumi.Sprintf("%s-%s", args.clusterNamePrefix, ctx.Stack())

	// see https://github.com/zalando/postgres-operator/issues/553
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
			Name:      pulumi.Sprintf("%s.%s.credentials.postgresql.acid.zalan.do", psql.userName, psql.clusterName), //need to hardcode the name to override the generated secret from operator
			Namespace: args.Namespace,
			Labels:    psql.podLabels,
		},
		Type: pulumi.String("Opaque"),
		StringData: pulumi.ToStringMapOutput(map[string]pulumi.StringOutput{
			"username": psql.userName.ToStringOutput(),
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
			Labels:    psql.podLabels,
		},
		Spec: netwv1.NetworkPolicySpecArgs{
			PolicyTypes: pulumi.ToStringArray([]string{
				"Ingress",
				"Egress",
			}),
			PodSelector: metav1.LabelSelectorArgs{
				MatchLabels: psql.podLabels,
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
							NamespaceSelector: metav1.LabelSelectorArgs{
								MatchLabels: pulumi.StringMap{
									"kubernetes.io/metadata.name": args.postgresOperatorNamespace,
								},
							},
							// Do not use the PodSelector for the operator
							// PodSelector: metav1.LabelSelectorArgs{
							// 	MatchLabels: pulumi.StringMap{
							// 		"app.kubernetes.io/name":     pulumi.String("postgres-operator"),
							// 		"app.kubernetes.io/instance": pulumi.String("postgres-operator"),
							// 	},
							// },
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
								MatchLabels: psql.podLabels,
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
								MatchLabels: psql.podLabels,
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

	opts = append(opts, pulumi.DependsOn([]pulumi.Resource{psql.pgToApi, psql.userSec, psql.pgFromClient}))

	// Create cluster with postgres-operator
	psql.cluster, err = yamlv2.NewConfigGroup(ctx, "database-cluster", &yamlv2.ConfigGroupArgs{
		Objs: pulumi.Array{
			pulumi.Map{
				"apiVersion": pulumi.String("acid.zalan.do/v1"),
				"kind":       pulumi.String("postgresql"),
				"metadata": pulumi.Map{
					"name":      psql.clusterName,
					"namespace": args.Namespace,
					"labels":    psql.podLabels,
				},
				"spec": pulumi.Map{
					// XXX tag
					"dockerImage": args.Registry.ToStringPtrOutput().ApplyT(func(in *string) string {
						// No private registry
						if in == nil || *in == "" {
							return "ghcr.io/zalando/spilo-17:4.0-p3" // default
						}

						str := *in
						// If one set, make sure it ends with one '/'
						if str != "" && !strings.HasSuffix(str, "/") {
							str = str + "/"
						}
						return str + "zalando/spilo-17:4.0-p3"
					}).(pulumi.StringOutput),
					"teamId":            psql.userName,
					"numberOfInstances": pulumi.Int(3), // TODO make it configurable
					"users": pulumi.Map{
						username: pulumi.StringArray{},
					},
					"databases": pulumi.Map{
						username: psql.userName,
					},
					"postgresql": pulumi.Map{
						"version": pulumi.String("17"),
						"parameters": pulumi.Map{
							"password_encryption": pulumi.String("scram-sha-256"),
						},
					},
					"volume": pulumi.Map{
						"size":         pulumi.String("10Gi"),
						"storageClass": args.storageClassName,
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
					"enableConnectionPooler": pulumi.Bool(true), // enable PGBouncer, 2 Pods will be created by default
					"connectionPooler": pulumi.Map{
						// XXX tag
						"dockerImage": args.Registry.ToStringPtrOutput().ApplyT(func(in *string) string {
							// No private registry
							if in == nil || *in == "" {
								return "registry.opensource.zalan.do/acid/pgbouncer:master-32" // default
							}

							str := *in
							// If one set, make sure it ends with one '/'
							if str != "" && !strings.HasSuffix(str, "/") {
								str = str + "/"
							}
							return str + "acid/pgbouncer:master-32"
						}).(pulumi.StringOutput),
					},
				},
			},
		},
	}, opts...)
	if err != nil {
		return err
	}

	return nil
}

func (psql *PostgreSQL) outputs(ctx *pulumi.Context) error {
	psql.URL = pulumi.Sprintf("postgresql+psycopg2://%s:%s@%s-pooler:5432/%s", psql.userName, psql.userPass.Result, psql.clusterName, psql.userName)
	psql.PodLabels = psql.podLabels.ToStringMapOutput()

	return ctx.RegisterResourceOutputs(psql, pulumi.Map{
		"url":       psql.URL,
		"podLabels": psql.PodLabels,
	})
}
