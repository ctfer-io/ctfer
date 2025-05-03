package components

import (
	"github.com/ctfer-io/ctfer/internal"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	helmv4 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/helm/v4"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi-random/sdk/v4/go/random"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type Redis struct {
	pulumi.ResourceState

	URL pulumi.StringOutput
}

var _ pulumi.ComponentResource = (*Redis)(nil)

func NewRedis(ctx *pulumi.Context, name string, args *RedisArgs, opts ...pulumi.ResourceOption) (*Redis, error) {
	rd := &Redis{}

	// remote chart url
	chartUrl := pulumi.String("oci://registry-1.docker.io/bitnamicharts/redis").ToStringOutput()

	// offline chart url
	if internal.GetConfig().ChartsRepository != "" {
		chartUrl = pulumi.Sprintf("%s/redis", internal.GetConfig().ChartsRepository)
	}

	// Register the Component Resource
	err := ctx.RegisterComponentResource("ctfer:l4:Redis", name, rd, opts...)
	if err != nil {
		return nil, err
	}

	// Provision K8s resources
	redisLabels := pulumi.StringMap{
		"ctfer/infra": pulumi.String("redis"),
	}

	// => Secret
	redisPass, err := random.NewRandomPassword(ctx, "redis-pass", &random.RandomPasswordArgs{
		Length:  pulumi.Int(64),
		Special: pulumi.BoolPtr(false),
	}, pulumi.Parent(rd))
	if err != nil {
		return nil, err
	}

	rd.URL = pulumi.Sprintf("redis://:%s@redis-master:6379", redisPass.Result)

	secret, err := corev1.NewSecret(ctx, "redis-secret", &corev1.SecretArgs{
		Metadata: metav1.ObjectMetaArgs{
			Labels:    redisLabels,
			Name:      pulumi.String("redis-secret"),
			Namespace: args.Namespace,
		},
		Type: pulumi.String("Opaque"),
		StringData: pulumi.ToStringMapOutput(map[string]pulumi.StringOutput{
			"redis-password": redisPass.Result,
			"redis-url":      rd.URL,
		}),
	}, pulumi.Parent(rd))
	if err != nil {
		return nil, err
	}

	_, err = helmv4.NewChart(ctx, "redis", &helmv4.ChartArgs{
		Namespace: args.Namespace,
		Version:   pulumi.String("20.13.4"),
		Chart:     chartUrl,
		Values: pulumi.Map{
			"global": internal.GetConfig().ImagesRepository.ToStringOutput().ApplyT(func(repo string) map[string]any {
				mp := map[string]any{}
				mp["imageRegistry"] = repo

				// Enable pulling images from private registry
				if repo != "" {
					mp["security"] = map[string]any{
						"allowInsecureImages": true,
					}
				}
				return mp
			}).(pulumi.MapOutput),
			"auth": pulumi.Map{
				"existingSecret": secret.Metadata.Name(), // use secret with generated passwords above
			},
			"master": pulumi.Map{
				"persistence": pulumi.Map{
					"storageClass": pulumi.String("longhorn"), // make the master deployable on all nodes if crash
					"accessModes": pulumi.StringArray{
						pulumi.String("ReadWriteMany"), // make the master deployable on all nodes if crash
					},
				},
			},
			"architecture": pulumi.String("standalone"), // we don't use replicas for RO actions, TODO enable sentinel
		},
	}, pulumi.Parent(rd))
	if err != nil {
		return nil, err
	}

	return rd, nil
}

type RedisArgs struct {
	// Namespace to deploy to.
	Namespace pulumi.String
	// Replicas is the number of secondary replicas to run.
	Replicas pulumi.Int
}
