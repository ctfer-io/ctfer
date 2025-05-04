package internal

import (
	"fmt"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

// Config holds the values configured using pulumi CLI.
type Config struct {
	// Namespace in which ctfer will deploy the CTF.
	Namespace        pulumi.String
	Hostname         pulumi.String
	ImagesRepository pulumi.String
	ChartsRepository pulumi.String
	CtfdImage        pulumi.String
}

var (
	conf *Config
)

func InitConfig(ctx *pulumi.Context) {
	config := config.New(ctx, "ctfer")
	conf = &Config{
		Namespace:        pulumi.String(def(config.Get("namespace"), "ctfer")),
		Hostname:         pulumi.String(def(config.Get("hostname"), "localhost")),
		ImagesRepository: pulumi.String(def(config.Get("images-repository"), "")),          // registry.dev1.ctfer-io.lab
		ChartsRepository: pulumi.String(def(config.Get("charts-repository"), "")),          // oci://registry.dev1.ctfer-io.lab
		CtfdImage:        pulumi.String(def(config.Get("ctfd-image"), "ctfd/ctfd:latest")), // ctferio/ctfd:3.7.7-0.3.0-rc1
	}
}

func GetConfig() *Config {
	return conf
}

func GetImage(image string) string {
	if GetConfig().ImagesRepository == "" {
		return image
	}

	return fmt.Sprint(GetConfig().ImagesRepository, "/", image)
}

func def[T comparable](act, def T) T {
	zero := *new(T)
	if act != zero {
		return act
	}
	return def
}
