package internal

import (
	"github.com/ctfer-io/ctfer/internal/components"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
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
	ns := GetConfig().Namespace
	_, err := corev1.NewNamespace(ctx, "ctfer-ns", &corev1.NamespaceArgs{
		Metadata: metav1.ObjectMetaArgs{
			Labels: pulumi.StringMap{
				"ctfer.io/app-name": pulumi.String("ctfd"),
				"ctfer.io/part-of":  pulumi.String("ctfer"),
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
	mdb, err := components.NewMariaDB(ctx, "mariadb-ctfd", &components.MariaDBArgs{
		Namespace:        ns,
		ChartsRepository: GetConfig().ChartsRepository,
		ChartVersion:     pulumi.String("20.5.3"),
		Registry:         GetConfig().ImagesRepository,
	}, pulumi.Parent(ctfer))
	if err != nil {
		return pulumi.StringOutput{}, err
	}

	// Deploy Redis
	// TODO scale up to >=3
	// FIXME when scaled to 3, ctfd replicas errors
	rd, err := components.NewRedis(ctx, "redis-ctfd", &components.RedisArgs{
		Namespace:        ns,
		ChartsRepository: GetConfig().ChartsRepository,
		ChartVersion:     pulumi.String("20.13.4"),
		Registry:         GetConfig().ImagesRepository,
	}, pulumi.Parent(ctfer))
	if err != nil {
		return pulumi.StringOutput{}, err
	}

	ctfd, err := components.NewCTFd(ctx, "ctfd", &components.CTFdArgs{
		Namespace:         ns,
		RedisSecretName:   rd.SecretName,
		MariaDBSecretName: mdb.SecretName,
		Image:             GetConfig().CtfdImage,
		Registry:          GetConfig().ImagesRepository,
		Hostname:          GetConfig().Hostname,
	}, pulumi.Parent(ctfer), pulumi.DependsOn([]pulumi.Resource{mdb, rd}))
	if err != nil {
		return pulumi.StringOutput{}, err
	}

	return ctfd.URL, nil
}
