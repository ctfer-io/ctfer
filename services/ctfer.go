package services

import (
	"net/url"
	"strconv"
	"sync"

	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	netwv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/networking/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ctfer-io/ctfer/services/common"
	"github.com/ctfer-io/ctfer/services/components"
)

// CTFer is a pulumi Component that deploy a pre-configured CTFd stack
// in an on-premise K8s cluster with Traefik as Ingress Controller.
type CTFer struct {
	pulumi.ResourceState

	postgres *components.PostgreSQL
	redis    *components.Redis
	ctfd     *components.CTFd

	ctfdNetpol *netwv1.NetworkPolicy

	// URL contains the CTFd's URL once provided.
	URL       pulumi.StringOutput
	PodLabels pulumi.StringMapOutput
}

type CTFerArgs struct {
	Namespace pulumi.StringInput

	CTFdImage       pulumi.StringInput
	ChallManagerURL pulumi.StringInput

	Crt         pulumi.StringInput
	Key         pulumi.StringInput
	StorageSize pulumi.StringInput
	Workers     pulumi.IntInput
	Replicas    pulumi.IntInput
	Requests    pulumi.StringMapInput
	Limits      pulumi.StringMapInput

	Hostname         pulumi.StringInput
	ChartsRepository pulumi.StringInput
	ImagesRepository pulumi.StringInput
	StorageClassName pulumi.StringInput

	// PVCAccessModes defines the access modes supported by the PVC.
	PVCAccessModes pulumi.StringArrayInput

	IngressNamespace          pulumi.StringInput
	IngressLabels             pulumi.StringMapInput
	PostgresOperatorNamespace pulumi.StringInput
	Annotations               pulumi.StringMapInput

	OTel *common.OTelArgs
}

// NewCTFer creates a new pulumi Component Resource and registers it.
func NewCTFer(ctx *pulumi.Context, name string, args *CTFerArgs, opts ...pulumi.ResourceOption) (*CTFer, error) {
	ctfer := &CTFer{}
	args = ctfer.defaults(args)
	if err := ctfer.check(args); err != nil {
		return nil, err
	}
	err := ctx.RegisterComponentResource("ctfer-io:ctfer", name, ctfer, opts...)
	if err != nil {
		return nil, err
	}
	opts = append(opts, pulumi.Parent(ctfer))

	if err := ctfer.provision(ctx, args, opts...); err != nil {
		return nil, err
	}
	if err := ctfer.outputs(ctx); err != nil {
		return nil, err
	}

	return ctfer, nil
}

func (ctfer *CTFer) defaults(args *CTFerArgs) *CTFerArgs {
	if args == nil {
		args = &CTFerArgs{}
	}

	return args
}

func (ctfer *CTFer) check(args *CTFerArgs) error {
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

func (ctfer *CTFer) provision(ctx *pulumi.Context, args *CTFerArgs, opts ...pulumi.ResourceOption) (err error) {
	// Deploy HA Dababase with PostgreSQL Operator
	ctfer.postgres, err = components.NewPostgreSQL(ctx, "database", &components.PostgreSQLArgs{
		Namespace:                 args.Namespace,
		Registry:                  args.ImagesRepository,
		PostgresOperatorNamespace: args.PostgresOperatorNamespace,
	}, opts...)
	if err != nil {
		return
	}

	// Deploy Redis
	// TODO scale up to >=3
	// FIXME when scaled to 3, ctfd replicas errors
	ctfer.redis, err = components.NewRedis(ctx, "cache", &components.RedisArgs{
		Namespace:        args.Namespace,
		ChartsRepository: args.ChartsRepository,
		ChartVersion:     pulumi.String("20.13.4"),
		Registry:         args.ImagesRepository,
		StorageClassName: args.StorageClassName,
		PVCAccessModes:   args.PVCAccessModes,
	}, opts...)
	if err != nil {
		return
	}

	ctfdArgs := &components.CTFdArgs{
		Namespace: args.Namespace,

		RedisURL:        ctfer.redis.URL,
		DatabaseURL:     ctfer.postgres.URL,
		ChallManagerURL: args.ChallManagerURL,

		Image:    args.CTFdImage,
		Registry: args.ImagesRepository,
		Workers:  args.Workers,
		Replicas: args.Replicas,
		Limits:   args.Limits,
		Requests: args.Requests,

		StorageSize:      args.StorageSize,
		StorageClassName: args.StorageClassName,
		PVCAccessModes:   args.PVCAccessModes,

		Crt:         args.Crt,
		Key:         args.Key,
		Hostname:    args.Hostname,
		Annotations: args.Annotations,
	}
	if args.OTel != nil {
		ctfdArgs.OTel = &common.OTelArgs{
			ServiceName: pulumi.Sprintf("%s-ctfd", args.OTel.ServiceName),
			Endpoint:    args.OTel.Endpoint,
			Insecure:    args.OTel.Insecure,
		}
	}
	ctfer.ctfd, err = components.NewCTFd(ctx, "platform", ctfdArgs, append(opts, pulumi.DependsOn([]pulumi.Resource{
		ctfer.postgres,
		ctfer.redis,
	}))...)
	if err != nil {
		return
	}

	// Top-level NetworkPolicies
	// - IngressController -> CTFd
	// - CTFd -> Redis
	// - CTFd -> MariaDB
	ctfer.ctfdNetpol, err = netwv1.NewNetworkPolicy(ctx, "ctfd-netpol", &netwv1.NetworkPolicyArgs{
		Metadata: metav1.ObjectMetaArgs{
			Namespace: args.Namespace,
			Labels: pulumi.StringMap{
				"app.kubernetes.io/component": pulumi.String("ctfer"),
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
				MatchLabels: ctfer.ctfd.PodLabels,
			},
			Ingress: netwv1.NetworkPolicyIngressRuleArray{
				// Ingress ->
				netwv1.NetworkPolicyIngressRuleArgs{
					From: netwv1.NetworkPolicyPeerArray{
						netwv1.NetworkPolicyPeerArgs{
							NamespaceSelector: metav1.LabelSelectorArgs{
								MatchLabels: pulumi.StringMap{
									"kubernetes.io/metadata.name": args.IngressNamespace,
								},
							},
							PodSelector: metav1.LabelSelectorArgs{
								MatchLabels: args.IngressLabels,
							},
						},
					},
					Ports: netwv1.NetworkPolicyPortArray{
						netwv1.NetworkPolicyPortArgs{
							Port: pulumi.Int(8000),
						},
					},
				},
			},
			Egress: netwv1.NetworkPolicyEgressRuleArray{
				// -> Redis
				netwv1.NetworkPolicyEgressRuleArgs{
					To: netwv1.NetworkPolicyPeerArray{
						netwv1.NetworkPolicyPeerArgs{
							NamespaceSelector: metav1.LabelSelectorArgs{
								MatchLabels: pulumi.StringMap{
									"kubernetes.io/metadata.name": args.Namespace,
								},
							},
							PodSelector: metav1.LabelSelectorArgs{
								MatchLabels: ctfer.redis.PodLabels,
							},
						},
					},
					Ports: netwv1.NetworkPolicyPortArray{
						netwv1.NetworkPolicyPortArgs{
							Port:     parseURLPort(ctfer.redis.URL),
							Protocol: pulumi.String("TCP"),
						},
					},
				},
				// -> PostgreSQL
				netwv1.NetworkPolicyEgressRuleArgs{
					To: netwv1.NetworkPolicyPeerArray{
						netwv1.NetworkPolicyPeerArgs{
							NamespaceSelector: metav1.LabelSelectorArgs{
								MatchLabels: pulumi.StringMap{
									"kubernetes.io/metadata.name": args.Namespace,
								},
							},
							PodSelector: metav1.LabelSelectorArgs{
								MatchLabels: ctfer.postgres.PodLabels,
							},
						},
					},
					Ports: netwv1.NetworkPolicyPortArray{
						netwv1.NetworkPolicyPortArgs{
							Port:     parseURLPort(ctfer.postgres.URL),
							Protocol: pulumi.String("TCP"),
						},
					},
				},
			},
		},
	}, opts...)

	return
}

func (ctfer *CTFer) outputs(ctx *pulumi.Context) error {
	ctfer.URL = ctfer.ctfd.URL
	ctfer.PodLabels = ctfer.ctfd.PodLabels

	return ctx.RegisterResourceOutputs(ctfer, pulumi.Map{
		"url":       ctfer.URL,
		"podLabels": ctfer.PodLabels,
	})
}

// parseURLPort parses the input endpoint formatted as a URL to return its port.
// Example: http://some.thing:port -> port
func parseURLPort(edp pulumi.StringOutput) pulumi.IntOutput {
	return edp.ToStringOutput().ApplyT(func(edp string) (int, error) {
		u, err := url.Parse(edp)
		if err != nil {
			return 0, errors.Wrapf(err, "parsing endpoint %s as a URL", edp)
		}
		p, err := strconv.Atoi(u.Port())
		if err != nil {
			return 0, errors.Wrapf(err, "parsing endpoint %s for port", edp)
		}
		return p, nil
	}).(pulumi.IntOutput)
}
