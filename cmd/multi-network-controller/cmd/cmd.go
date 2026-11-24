/*
Copyright (c) 2026 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package cmd implements the command line interface for the multi-network controller.
package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

// Execute adds all child commands to the root command sets flags appropriately.
// This is called by the main function.
func Execute() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := getRootCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func getRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "multi-network-controller",
		Short: "CLI",
		Long:  `CLI for interacting with the multi-network controller`,
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd: true,
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	rootCmd.AddCommand(newCmdRun())

	return rootCmd
}
