package customtypes

type network struct {
	Host string `yaml:"host"`
	IP   string `yaml:"ip"`
}

type logLevel int8

const (
	DebugLevel logLevel = -1
	InfoLevel  logLevel = 0
	ErrorLevel logLevel = 2
)

type logging struct {
	Level       logLevel `yaml:"level"`
	Destination *string  `yaml:"destination"`
}

type group struct {
	Name    string   `yaml:"name"`
	Members []string `yaml:"members"`
}

type Config struct {
	Network network `yaml:"network"`
	Logging logging `yaml:"logging"`
	Group   group   `yaml:"group"`
}
