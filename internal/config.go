package internal

import (
	"fmt"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

// Config holds the values configured using pulumi CLI.
type Config struct {
	// Namespace in which ctfer will deploy the CTF.
	Namespace pulumi.String
	// Configure this variable using `pulumi config set isMinikube <bool>`.
	IsMinikube      bool
	Hostname        pulumi.String
	ImageRepository pulumi.String
}

var (
	conf *Config
)

func InitConfig(ctx *pulumi.Context) {
	config := config.New(ctx, "ctfer")
	conf = &Config{
		Namespace:       pulumi.String(def(config.Get("namespace"), "ctfer")),
		IsMinikube:      false,
		Hostname:        pulumi.String(def(config.Get("hostname"), "localhost")),
		ImageRepository: pulumi.String(def(config.Get("image-repository"), "")),
	}
}

func GetConfig() *Config {
	return conf
}

func GetImage(image string) string {
	if GetConfig().ImageRepository == "" {
		return image
	}

	return fmt.Sprint(GetConfig().ImageRepository, "/", image)
}

func def[T comparable](act, def T) T {
	zero := *new(T)
	if act != zero {
		return act
	}
	return def
}
