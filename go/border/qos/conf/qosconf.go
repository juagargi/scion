package conf

import (
	"io/ioutil"

	"github.com/scionproto/scion/go/lib/log"
	"gopkg.in/yaml.v2"
)

// PoliceAction is the action that will be taken depending on the fill level of the queue
// is configured in qosConfig.yaml
type PoliceAction uint8

// Actions to execute on packets in queues.
const (
	PASS PoliceAction = iota
	NOTIFY
	DROP
	DROPNOTIFY
)

// ActionProfile specifies which actions are taken on which fill level of the queue
type ActionProfile struct {
	FillLevel int          `yaml:"fill-level"`
	Prob      int          `yaml:"prob"`
	Action    PoliceAction `yaml:"action"`
}

// ExternalPacketQueue is the configuration loaded from the configuration file
type ExternalPacketQueue struct {
	Name         string          `yaml:"name"`
	ID           int             `yaml:"id"`
	MinBandwidth int             `yaml:"CIR"`
	MaxBandWidth int             `yaml:"PIR"`
	PoliceRate   string          `yaml:"policeRate"`
	MaxLength    int             `yaml:"maxLength"`
	Priority     int             `yaml:"priority"`
	Profile      []ActionProfile `yaml:"profile"`
}

// ExternalProtocolMatchType is the match type loaded from the configuration file
type ExternalProtocolMatchType struct {
	BaseProtocol int `yaml:"Protocol"`
	Extension    int `yaml:"Extension"`
}

// ExternalClassRule contains a rule for matching packets loaded from the configuration file
// note that these fields (at least the string types) need to be exported.
// Otherwise the yaml unmarshal will fail.
type ExternalClassRule struct {
	Name                 string                      `yaml:"name"`
	Priority             int                         `yaml:"priority"`
	SourceAs             string                      `yaml:"sourceAs"`
	SourceMatchMode      int                         `yaml:"sourceMatchMode"`
	DestinationAs        string                      `yaml:"destinationAs"`
	DestinationMatchMode int                         `yaml:"destinationMatchMode"`
	L4Type               []ExternalProtocolMatchType `yaml:"L4Type"`
	QueueNumber          int                         `yaml:"queueNumber"`
}

// SchedulerConfig is the configuration for the scheduler loaded from the configuration file
type SchedulerConfig struct {
	Latency   int    `yaml:"Latency"`
	Bandwidth string `yaml:"Bandwidth"`
}

// ExternalConfig is what I am loading from the config file
type ExternalConfig struct {
	SchedulerConfig SchedulerConfig       `yaml:"Scheduler"`
	ExternalQueues  []ExternalPacketQueue `yaml:"Queues"`
	ExternalRules   []ExternalClassRule   `yaml:"Rules"`
}

// LoadConfig reads the configuration file from path and returns the external configuration based
// on this file.
func LoadConfig(path string) (ExternalConfig, error) {
	var ec ExternalConfig

	yamlFile, err := ioutil.ReadFile(path)
	if err != nil {
		return ExternalConfig{}, err
	}
	err = yaml.Unmarshal(yamlFile, &ec)
	if err != nil {
		log.Error("Loading the config file has failed", "error", err)
		return ExternalConfig{}, err
	}

	return ec, nil
}
