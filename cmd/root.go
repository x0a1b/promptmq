package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/x0a1b/promptmq/cmd/server"
)

var cfgFile string

var RootCmd = &cobra.Command{
	Use:   "promptmq",
	Short: "PromptMQ - High-performance MQTT broker with persistent messaging",
	Long: `PromptMQ is a high-performance MQTT v5 broker written in Go that provides:
- Persistent messaging with SQLite storage
- Optimized SQLite storage with PRAGMA tuning
- Reliable SQLite persistence and recovery
- Clustering support for horizontal scaling
- Comprehensive monitoring and metrics
- Daemon mode for production deployment`,
}

func Execute() {
	err := RootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	RootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.promptmq.yaml)")
	RootCmd.PersistentFlags().String("log-level", "info", "log level (trace, debug, info, warn, error, fatal, panic)")
	RootCmd.PersistentFlags().String("log-format", "json", "log format (json, console)")

	viper.BindPFlag("log.level", RootCmd.PersistentFlags().Lookup("log-level"))
	viper.BindPFlag("log.format", RootCmd.PersistentFlags().Lookup("log-format"))

	// Add subcommands
	RootCmd.AddCommand(server.ServerCmd)
}

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		home, err := os.UserHomeDir()
		cobra.CheckErr(err)

		viper.AddConfigPath(home)
		viper.AddConfigPath(".")
		viper.SetConfigType("yaml")
		viper.SetConfigName(".promptmq")
	}

	viper.AutomaticEnv()
	viper.SetEnvPrefix("PROMPTMQ")

	if err := viper.ReadInConfig(); err == nil {
		fmt.Fprintln(os.Stderr, "Using config file:", viper.ConfigFileUsed())
	}
}
