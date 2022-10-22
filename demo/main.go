package main

import (
	"github.com/spf13/cobra"
)

func main() {
	opt := &options{}
	cmd := cobra.Command{
		Use:           "tifancy-demo",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			svr := newService(opt)
			return svr.serve()
		},
	}

	opt.addFlags(cmd.Flags())
	cobra.CheckErr(cmd.Execute())
}
