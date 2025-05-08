package main

import (
	"fmt"

	"github.com/ctfer-io/ctfer/services"
	"github.com/ctfer-io/ctfer/services/components"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) (err error) {
		cfg := InitConfig(ctx)

		_, err = components.NewTraefik(ctx, ctx.Project(), &components.TraefikArgs{
			Namespace:        pulumi.String(cfg.Namespace),
			ChartsRepository: pulumi.String(cfg.ChartsRepository),
			ChartVersion:     pulumi.String("35.2.0"),
			Registry:         pulumi.String(cfg.ImagesRepository),
		})
		if err != nil {
			return err
		}

		// Create CTF's namespace
		if _, err = corev1.NewNamespace(ctx, "namespace", &corev1.NamespaceArgs{
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
			ChartsRepository: pulumi.String(cfg.ChartsRepository),
			ImagesRepository: pulumi.String(cfg.ImagesRepository),
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
}

func InitConfig(ctx *pulumi.Context) *Config {
	config := config.New(ctx, "ctfer")
	return &Config{
		Namespace:        def(config.Get("namespace"), "ctfer"),
		Hostname:         def(config.Get("hostname"), "localhost"),
		ImagesRepository: def(config.Get("images-repository"), ""),          // registry.dev1.ctfer-io.lab
		ChartsRepository: def(config.Get("charts-repository"), ""),          // oci://registry.dev1.ctfer-io.lab
		CTFdImage:        def(config.Get("ctfd-image"), "ctfd/ctfd:latest"), // ctferio/ctfd:3.7.7-0.3.0
	}
}

func def[T comparable](act, def T) T {
	zero := *new(T)
	if act != zero {
		return act
	}
	return def
}
