package main

import (
	"os"

	"github.com/Alfred-Onuada/raft-protocol/internal/flags"
	logger "github.com/Alfred-Onuada/raft-protocol/internal/logging"
	customtypes "github.com/Alfred-Onuada/raft-protocol/internal/types"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/yaml.v3"
)

func main() {
	// Parse the flags
	cliFlags := flags.InitFlags()
	if cliFlags.ConfigFilePath == "" {
		panic("Please provide a configuration file as --config=path/to/file")
	}

	// Read the file returned
	fileBytes, err := os.ReadFile(cliFlags.ConfigFilePath)
	if err != nil {
		panic(err)
	}

	// Parse the config file returned
	var config customtypes.Config
	err = yaml.Unmarshal(fileBytes, &config)
	if err != nil {
		panic(err)
	}

	// Initialize the logger
	err = logger.Init(zapcore.Level(config.Logging.Level), config.Logging.Destination)
	if err != nil {
		panic(err)
	}

	logger.Log.Info("Spinning up Raft node", zap.Any("config", config))

	// Attempt to parse the config provided
}
