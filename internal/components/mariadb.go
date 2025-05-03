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

	URL pulumi.StringOutput
}

var _ pulumi.ComponentResource = (*MariaDB)(nil)

// NewMariaDB creates a HA MariaDB cluster.
func NewMariaDB(ctx *pulumi.Context, name string, args *MariaDBArgs, opts ...pulumi.ResourceOption) (*MariaDB, error) {
	mdb := &MariaDB{}

	// remote chart url
	chartUrl := pulumi.String("oci://registry-1.docker.io/bitnamicharts/mariadb-galera").ToStringOutput()

	// offline chart url
	if internal.GetConfig().ChartsRepository != "" {
		chartUrl = pulumi.Sprintf("%s/mariadb-galera", internal.GetConfig().ChartsRepository)
	}

	// Register the Component Resource
	err := ctx.RegisterComponentResource("ctfer:l4:MariaDB", name, mdb, opts...)
	if err != nil {
		return nil, err
	}

	// Provision K8s resources
	mariadbLabels := pulumi.StringMap{
		"ctfer/infra": pulumi.String("mariadb"),
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
	backupPass, err := random.NewRandomPassword(ctx, "backupPass-secret", &random.RandomPasswordArgs{
		Length:  pulumi.Int(32),
		Special: pulumi.BoolPtr(false),
	}, pulumi.Parent(mdb))
	if err != nil {
		return nil, err
	}

	mdb.URL = pulumi.Sprintf("mysql+pymysql://%s:%s@mariadb-mariadb-galera/ctfd", masterUser, userPass.Result)

	secret, err := corev1.NewSecret(ctx, "mariadb-secret", &corev1.SecretArgs{
		Metadata: metav1.ObjectMetaArgs{
			Labels:    mariadbLabels,
			Name:      pulumi.String("mariadb-secret"),
			Namespace: args.Namespace,
		},
		Type: pulumi.String("Opaque"),
		StringData: pulumi.ToStringMapOutput(map[string]pulumi.StringOutput{
			"mariadb-root-password":               masterPass.Result,
			"mariadb-password":                    userPass.Result,
			"mariadb-galera-mariabackup-password": backupPass.Result,
			"mariadb-url":                         mdb.URL, // debug purpose
		}),
	}, pulumi.Parent(mdb))
	if err != nil {
		return nil, err
	}

	_, err = helmv4.NewChart(ctx, "mariadb", &helmv4.ChartArgs{
		Namespace: args.Namespace,
		Version:   pulumi.String("14.2.3"),
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
			"db": pulumi.Map{
				"user": masterUser,
				"name": pulumi.String("ctfd"),
			},
			"existingSecret": secret.Metadata.Name(), // use secret with generated passwords above
			"podLabels":      mariadbLabels,
			"persistence": pulumi.Map{
				"storageClass": pulumi.String("local-path"), // do not use longhorn here, replicas will be handle this
			},
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
	// Replicas is the number of secondary replicas to run.
	Replicas pulumi.Int
}
