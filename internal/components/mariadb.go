package components

import (
	"github.com/ctfer-io/ctfer/internal"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	helmv4 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/helm/v4"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi-random/sdk/v4/go/random"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type MariaDB struct {
	pulumi.ResourceState

	// URL        pulumi.StringOutput
	SecretName pulumi.StringOutput
}

var _ pulumi.ComponentResource = (*MariaDB)(nil)

// NewMariaDB creates a HA MariaDB cluster.
func NewMariaDB(ctx *pulumi.Context, name string, args *MariaDBArgs, opts ...pulumi.ResourceOption) (*MariaDB, error) {
	mdb := &MariaDB{}

	// remote chart url
	chartUrl := pulumi.String("oci://registry-1.docker.io/bitnamicharts/mariadb").ToStringOutput()

	// offline chart url
	if internal.GetConfig().ChartsRepository != "" {
		chartUrl = pulumi.Sprintf("%s/mariadb", internal.GetConfig().ChartsRepository)
	}

	// Register the Component Resource
	err := ctx.RegisterComponentResource("ctfer:l4:MariaDB", name, mdb, opts...)
	if err != nil {
		return nil, err
	}

	// Provision K8s resources
	mariadbLabels := pulumi.StringMap{
		"ctfer.io/app-name": pulumi.String("mariadb"),
		"ctfer.io/part-of":  pulumi.String("ctfer"),
	}

	// => Secret
	masterUser := pulumi.String("ctfer")
	masterPass, err := random.NewRandomPassword(ctx, "masterPass-secret", &random.RandomPasswordArgs{
		Length:  pulumi.Int(32),
		Special: pulumi.BoolPtr(false),
	}, pulumi.Parent(mdb))
	if err != nil {
		return nil, err
	}
	userPass, err := random.NewRandomPassword(ctx, "userPass-secret", &random.RandomPasswordArgs{
		Length:  pulumi.Int(32),
		Special: pulumi.BoolPtr(false),
	}, pulumi.Parent(mdb))
	if err != nil {
		return nil, err
	}
	replicationPass, err := random.NewRandomPassword(ctx, "replication-secret", &random.RandomPasswordArgs{
		Length:  pulumi.Int(32),
		Special: pulumi.BoolPtr(false),
	}, pulumi.Parent(mdb))
	if err != nil {
		return nil, err
	}

	secret, err := corev1.NewSecret(ctx, "mariadb-secret", &corev1.SecretArgs{
		Metadata: metav1.ObjectMetaArgs{
			Labels:    mariadbLabels,
			Name:      pulumi.String("mariadb-secret"),
			Namespace: args.Namespace,
		},
		Type: pulumi.String("Opaque"),
		StringData: pulumi.ToStringMapOutput(map[string]pulumi.StringOutput{
			"mariadb-root-password":        masterPass.Result,
			"mariadb-password":             userPass.Result,
			"mariadb-replication-password": replicationPass.Result,
			"mariadb-url":                  pulumi.Sprintf("mysql+pymysql://%s:%s@mariadb-headless/ctfd", masterUser, userPass.Result),
		}),
	}, pulumi.Parent(mdb))
	if err != nil {
		return nil, err
	}

	mdb.SecretName = secret.Metadata.Name().Elem()

	_, err = helmv4.NewChart(ctx, "mariadb", &helmv4.ChartArgs{
		Namespace: args.Namespace,
		Version:   pulumi.String("20.5.3"),
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
				"username":       masterUser,
				"database":       pulumi.String("ctfd"),
				"existingSecret": secret.Metadata.Name(), // use secret with generated passwords above
			},
			"primary": pulumi.Map{
				"podLabels": mariadbLabels,
				"persistence": pulumi.Map{
					"storageClass": pulumi.String("longhorn"),
					"accessModes": pulumi.StringArray{
						pulumi.String("ReadWriteMany"),
					},
				},
				// Taint-Based Eviction
				"tolerations": pulumi.MapArray{
					pulumi.Map{
						"key":               pulumi.String("node.kubernetes.io/not-ready"),
						"operator":          pulumi.String("Exists"),
						"effect":            pulumi.String("NoExecute"),
						"tolerationSeconds": pulumi.Int(30),
					},
					pulumi.Map{
						"key":               pulumi.String("node.kubernetes.io/unreachable"),
						"operator":          pulumi.String("Exists"),
						"effect":            pulumi.String("NoExecute"),
						"tolerationSeconds": pulumi.Int(30),
					},
				},
			},
			"architecture": pulumi.String("standalone"), // explicit
		},
	}, pulumi.Parent(mdb))
	if err != nil {
		return nil, err
	}

	return mdb, nil
}

type MariaDBArgs struct {
	// Namespace to deploy to.
	Namespace pulumi.String
}
