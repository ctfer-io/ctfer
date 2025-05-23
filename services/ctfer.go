package services

import (
	"sync"

	"github.com/ctfer-io/ctfer/services/components"
	"github.com/hashicorp/go-multierror"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// CTFer is a pulumi Component that deploy a pre-configured CTFd stack
// in an on-premise K8s cluster with Traefik as Ingress Controller.
type CTFer struct {
	pulumi.ResourceState

	maria *components.MariaDB
	redis *components.Redis
	ctfd  *components.CTFd

	// URL contains the CTFd's URL once provided.
	URL pulumi.StringOutput
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
		Namespace:         args.Namespace,
		MariaDBSecretName: ctfer.maria.SecretName,
		RedisSecretName:   ctfer.redis.SecretName,
		Image:             args.CTFdImage,
		Registry:          args.ImagesRepository,
		Hostname:          args.Hostname,
		CTFdCrt:           args.CTFdCrt,
		CTFdKey:           args.CTFdKey,
		CTFdStorageSize:   args.CTFdStorageSize,
		CTFdWorkers:       args.CTFdWorkers,
		CTFdReplicas:      args.CTFdReplicas,
		ChallManagerUrl:   args.ChallManagerUrl,
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
	return
}

func (ctfer *CTFer) outputs(ctx *pulumi.Context) error {
	ctfer.URL = ctfer.ctfd.URL

	return ctx.RegisterResourceOutputs(ctfer, pulumi.Map{
		"url": ctfer.URL,
	})
}
