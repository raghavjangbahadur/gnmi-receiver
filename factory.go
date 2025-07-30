package gnmireceiver

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
)

var (
	typeStr      = component.MustNewType("gnmireceiver")
	defaultPaths = []string{}
)

const (
	defaultTarget   = ""
	defaultPort     = 9339
	defaultInterval = 30 * time.Second
)

func createDefaultConfig() component.Config {
	return &Config{
		Target:   defaultTarget,
		Port:     defaultPort,
		Paths:    defaultPaths,
		Interval: defaultInterval,
	}
}

func createMetricsReceiver(ctx context.Context, settings receiver.Settings, baseCfg component.Config, consumer consumer.Metrics) (receiver.Metrics, error) {
	if consumer == nil {
		return nil, fmt.Errorf("nil next consumer")
	}

	logger := settings.Logger
	gnmiCfg := baseCfg.(*Config)

	receiver := &gnmiReceiver{
		logger:       logger,
		nextConsumer: consumer,
		config:       gnmiCfg,
	}

	return receiver, nil
}

// Creates a factory for gnmireceiver receivers
func NewFactory() receiver.Factory {
	return receiver.NewFactory(
		typeStr,
		createDefaultConfig,
		receiver.WithMetrics(createMetricsReceiver, component.StabilityLevelAlpha))
}
