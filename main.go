package main

import (
	"fmt"

	"github.com/ctfer-io/ctfer/services"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) (err error) {
		cfg := InitConfig(ctx)

		// Create CTF's namespace
		if _, err := corev1.NewNamespace(ctx, "namespace", &corev1.NamespaceArgs{
			Metadata: metav1.ObjectMetaArgs{
				Labels: pulumi.StringMap{
					"ctfer.io/app-name": pulumi.String("ctfd"),
					"ctfer.io/part-of":  pulumi.String("ctfer"),
				},
				Name: pulumi.String(cfg.Namespace),
			},
		}); err != nil {
			return err
		}

		ctfer, err := services.NewCTFer(ctx, ctx.Stack(), &services.CTFerArgs{
			Namespace:        pulumi.String(cfg.Namespace),
			CTFdImage:        pulumi.String(cfg.CTFdImage),
			Hostname:         pulumi.String(cfg.Hostname),
			CTFdCrt:          cfg.CTFdCrt,
			CTFdKey:          cfg.CTFdKey,
			CTFdStorageSize:  pulumi.String(cfg.CTFdStorageSize),
			CTFdWorkers:      pulumi.Int(cfg.CTFdWorkers),
			CTFdReplicas:     pulumi.Int(cfg.CTFdReplicas),
			ChartsRepository: pulumi.String(cfg.ChartsRepository),
			ImagesRepository: pulumi.String(cfg.ImagesRepository),
			ChallManagerUrl:  pulumi.String(cfg.ChallManagerUrl),
		})
		if err != nil {
			return err
		}
		ctfer.URL.ApplyT(func(url string) string {
			fmt.Println(url)
			return url
		})
		return nil
	})
}

// Config holds the values configured using pulumi CLI.
type Config struct {
	// Namespace in which ctfer will deploy the CTF.
	Namespace        string
	Hostname         string
	ImagesRepository string
	ChartsRepository string
	CTFdImage        string
	ChallManagerUrl  string
	CTFdStorageSize  string
	CTFdCrt          pulumi.StringInput
	CTFdKey          pulumi.StringInput
	CTFdReplicas     int
	CTFdWorkers      int
}

func InitConfig(ctx *pulumi.Context) *Config {
	config := config.New(ctx, "ctfer")
	return &Config{
		Namespace:        def(config.Get("namespace"), "ctfer"),
		Hostname:         def(config.Get("hostname"), "localhost"),
		ImagesRepository: config.Get("images-repository"),                   // registry.dev1.ctfer-io.lab
		ChartsRepository: config.Get("charts-repository"),                   // oci://registry.dev1.ctfer-io.lab
		CTFdImage:        def(config.Get("ctfd-image"), "ctfd/ctfd:latest"), // ctferio/ctfd:3.7.7-0.3.0
		ChallManagerUrl:  config.Get("chall-manager-url"),                   // http://chall-manager-svc.ctfer:8080/api/v1
		CTFdCrt:          config.GetSecret("ctfd-crt"),
		CTFdKey:          config.GetSecret("ctfd-key"),
		CTFdStorageSize:  def(config.Get("ctfd-storage-size"), "2Gi"),
		CTFdReplicas:     def(config.GetInt("ctfd-replicas"), 1),
		CTFdWorkers:      def(config.GetInt("ctfd-workers"), 1),
	}
}

func def[T comparable](act, def T) T {
	zero := *new(T)
	if act != zero {
		return act
	}
	return def
}
