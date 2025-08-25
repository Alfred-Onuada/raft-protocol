package flags

import (
	"flag"
	"strconv"

	customtypes "github.com/Alfred-Onuada/raft-protocol/internal/types"
)

type cliFlags struct {
	// The address of the node to connect to
	NodeAddress string
	// The command to execute
	// This can be set to "set", "increase", "decrease", "del", or "get"
	CommandType customtypes.CommandType
	// The key to use for the command
	Key string
	// The value to use for the command if applicable
	Value *int
}

func InitCLIFlags() *cliFlags {
	config := cliFlags{}

	nodeAddress := flag.String("node-address", "", "The address of the node to connect to")

	// parse the flags to populate its value
	flag.Parse()

	config.NodeAddress = *nodeAddress
	if config.NodeAddress == "" {
		panic("Node address is required. Please provide the -node-address flag.")
	}

	// get the rest of the args
	args := flag.Args()
	if len(args) < 1 {
		panic("Not enough arguments provided. Expected at least 1 arguments: <command>.")
	}
	cmd := args[0]
	value, key := "", ""

	if len(args) > 1 {
		key = args[1]
	}

	// Prepend the value if provided, will throw an error later if the command doesn't accept a value
	if len(args) > 2 {
		value = args[2]
	}

	// check that the command is valid
	config.CommandType = customtypes.CommandType(cmd)
	if !config.CommandType.IsValid() {
		panic("Invalid command provided. Valid commands are: set, increase, decrease, del, get.")
	}

	// check that the key is not empty
	if config.CommandType != customtypes.TopologyCommand && key == "" {
		panic("Key cannot be empty. Please provide a valid key.")
	}
	// set the key in the config
	config.Key = key

	// check that value is not empty if the command requires it
	if config.CommandType == customtypes.SetCommand && value == "" {
		panic("Value cannot be empty for the command: " + string(config.CommandType) + ". Please provide a valid value.")
	}
	// set the value in the config if provided
	if value != "" {
		val, err := strconv.Atoi(value)
		if err != nil {
			panic("Invalid value provided. Value must be an integer.")
		}
		config.Value = &val
	} else {
		config.Value = nil // Set to nil if no value is provided
	}

	return &config
}
