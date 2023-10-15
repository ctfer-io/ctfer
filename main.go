package main

import (
	"fmt"

	"github.com/ctfer-io/ctfer/internal"
	"github.com/ctfer-io/ctfer/internal/components"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) (err error) {
		internal.InitConfig(ctx)

		_, err = components.NewTraefik(ctx, ctx.Project(), &components.TraefikArgs{
			Namespace: pulumi.String("ctfer"),
		})
		if err != nil {
			return err
		}

		ctfer, err := components.NewCTFer(ctx, ctx.Stack())
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
