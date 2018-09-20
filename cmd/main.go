package main

import (
	"flag"

	"github.com/golang/glog"
	"github.com/spf13/cobra"
)

const (
	componentName      = "machine-api-operator"
	componentNamespace = "openshift-machine-api-operator"
)

var (
	rootCmd = &cobra.Command{
		Use:   componentName,
		Short: "Run Cluster API Controller",
		Long:  "",
	}
	config string
)

func init() {
	rootCmd.PersistentFlags().StringVar(&config, "config", "/etc/mao-config/config", "path to the mao config")
	rootCmd.PersistentFlags().AddGoFlagSet(flag.CommandLine)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		glog.Exitf("Error executing mao: %v", err)
	}
}
