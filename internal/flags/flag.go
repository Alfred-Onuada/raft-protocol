// Package flags define the CLI flags that can be specified at startup
package flags

import (
	"flag"
)

type Flags struct {
	ConfigFilePath string
}

func InitFlags() *Flags {
	config := Flags{}

	configFile := flag.String("config", "", "The configuration file path for this node")

	// parse the flags to populate it's value
	flag.Parse()

	config.ConfigFilePath = *configFile

	return &config
}
