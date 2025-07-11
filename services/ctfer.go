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

	"github.com/ctfer-io/ctfer/services/components"
)

// CTFer is a pulumi Component that deploy a pre-configured CTFd stack
// in an on-premise K8s cluster with Traefik as Ingress Controller.
type CTFer struct {
	pulumi.ResourceState

	maria *components.MariaDB
	redis *components.Redis
	ctfd  *components.CTFd

	ctfdNetpol *netwv1.NetworkPolicy

	// URL contains the CTFd's URL once provided.
	URL       pulumi.StringOutput
	PodLabels pulumi.StringMapOutput
}

type CTFerArgs struct {
	Namespace       pulumi.StringInput
	CTFdImage       pulumi.StringInput
	ChallManagerUrl pulumi.StringInput

	CTFdCrt         pulumi.StringInput
	CTFdKey         pulumi.StringInput
	CTFdStorageSize pulumi.StringInput
	CTFdWorkers     pulumi.IntInput
	CTFdReplicas    pulumi.IntInput
	CTFdRequests    pulumi.StringMapInput
	CTFdLimits      pulumi.StringMapInput

	Hostname         pulumi.StringInput
	ChartsRepository pulumi.StringInput
	ImagesRepository pulumi.StringInput
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
	// Deploy HA MariaDB
	// TODO scale up to >=3
	// FIXME when scaled to 3, ctfd replicas errors
	ctfer.maria, err = components.NewMariaDB(ctx, "database", &components.MariaDBArgs{
		Namespace:        args.Namespace,
		ChartsRepository: args.ChartsRepository,
		ChartVersion:     pulumi.String("20.5.3"),
		Registry:         args.ImagesRepository,
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
	}, opts...)
	if err != nil {
		return
	}

	ctfer.ctfd, err = components.NewCTFd(ctx, "platform", &components.CTFdArgs{
		Namespace:       args.Namespace,
		RedisURL:        ctfer.redis.URL,
		MariaDBURL:      ctfer.maria.URL,
		Image:           args.CTFdImage,
		Registry:        args.ImagesRepository,
		Hostname:        args.Hostname,
		CTFdCrt:         args.CTFdCrt,
		CTFdKey:         args.CTFdKey,
		CTFdStorageSize: args.CTFdStorageSize,
		CTFdWorkers:     args.CTFdWorkers,
		CTFdReplicas:    args.CTFdReplicas,
		ChallManagerUrl: args.ChallManagerUrl,
		CTFdRequests:    args.CTFdRequests,
		CTFdLimits:      args.CTFdLimits,
	}, append(opts, pulumi.DependsOn([]pulumi.Resource{
		ctfer.maria,
		ctfer.redis,
	}))...)
	if err != nil {
		return
	}

	// TODO top-level NetworkPolicies
	// - IngressController -> CTFd
	// - CTFd -> Redis
	// - CTFd -> MariaDB
	ctfer.ctfdNetpol, err = netwv1.NewNetworkPolicy(ctx, "ctfd-netpol", &netwv1.NetworkPolicyArgs{
		Metadata: metav1.ObjectMetaArgs{
			Namespace: args.Namespace,
			Labels: pulumi.StringMap{
				"app.kubernetes.io/components": pulumi.String("ctfer"),
				"app.kubernetes.io/part-of":    pulumi.String("ctfer"),
				"ctfer.io/stack-name":          pulumi.String(ctx.Stack()),
			},
		},
		Spec: netwv1.NetworkPolicySpecArgs{
			PolicyTypes: pulumi.ToStringArray([]string{
				"Egress",
			}),
			PodSelector: metav1.LabelSelectorArgs{
				MatchLabels: ctfer.ctfd.PodLabels,
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
				// -> MariaDB
				netwv1.NetworkPolicyEgressRuleArgs{
					To: netwv1.NetworkPolicyPeerArray{
						netwv1.NetworkPolicyPeerArgs{
							NamespaceSelector: metav1.LabelSelectorArgs{
								MatchLabels: pulumi.StringMap{
									"kubernetes.io/metadata.name": args.Namespace,
								},
							},
							PodSelector: metav1.LabelSelectorArgs{
								MatchLabels: ctfer.maria.PodLabels,
							},
						},
					},
					Ports: netwv1.NetworkPolicyPortArray{
						netwv1.NetworkPolicyPortArgs{
							Port:     parseURLPort(ctfer.maria.URL),
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
