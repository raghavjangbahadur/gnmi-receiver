package gnmireceiver

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/openconfig/gnmi/proto/gnmi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.uber.org/zap"
)

type gnmiReceiver struct {
	host         component.Host
	cancel       context.CancelFunc
	logger       *zap.Logger
	nextConsumer consumer.Metrics
	config       *Config
}

// Launches receiver's subscription in new goroutine
func (rcvr *gnmiReceiver) Start(ctx context.Context, host component.Host) error {
	// Create cancellable context
	rcvr.host = host
	ctx = context.Background()
	ctx, rcvr.cancel = context.WithCancel(ctx)

	rcvr.logger.Info("Starting gNMI receiver", zap.String("target", rcvr.config.Target))

	// Start subcription in goroutine
	go func() {
		if err := rcvr.startSubscription(ctx); err != nil {
			rcvr.logger.Error("gNMI subscription failed", zap.Error(err))
		}
	}()

	return nil
}

// Shutdown signals the receiver to stop using context cancellation
func (rcvr *gnmiReceiver) Shutdown(ctx context.Context) error {
	rcvr.logger.Info("Shutting down gNMI receiver")
	if rcvr.cancel != nil {
		rcvr.cancel()
	}
	return nil
}

func (rcvr *gnmiReceiver) startSubscription(ctx context.Context) error {
	rcvr.logger.Info("Connecting to gNMI target",
		zap.String("target", rcvr.config.Target),
		zap.Int("port", rcvr.config.Port))

	if len(rcvr.config.Paths) == 0 {
		return fmt.Errorf("no valid paths found")
	}

	// Connect to gNMI target
	target := fmt.Sprintf("%s:%d", rcvr.config.Target, rcvr.config.Port)
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("failed to connect to %s: %w", target, err)
	}
	defer conn.Close()

	client := gnmi.NewGNMIClient(conn)
	rcvr.logger.Info("Connected to gNMI target", zap.String("target", target))

	// Build subscription request
	subscriptions := make([]*gnmi.Subscription, 0, len(rcvr.config.Paths))
	for _, pathStr := range rcvr.config.Paths {
		path := rcvr.parsePath(pathStr)
		subscription := &gnmi.Subscription{
			Path:           path,
			Mode:           gnmi.SubscriptionMode_SAMPLE,
			SampleInterval: uint64(rcvr.config.Interval.Nanoseconds()),
		}
		subscriptions = append(subscriptions, subscription)
	}

	req := &gnmi.SubscribeRequest{
		Request: &gnmi.SubscribeRequest_Subscribe{
			Subscribe: &gnmi.SubscriptionList{
				Mode:         gnmi.SubscriptionList_STREAM,
				Encoding:     gnmi.Encoding_JSON,
				Subscription: subscriptions,
			},
		},
	}

	// Create subscription stream
	stream, err := client.Subscribe(ctx)
	if err != nil {
		return fmt.Errorf("failed to create subscription: %w", err)
	}

	// Send subscription request
	if err := stream.Send(req); err != nil {
		return fmt.Errorf("failed to send subscription: %w", err)
	}

	rcvr.logger.Info("gNMI subscription started", zap.Int("paths", len(rcvr.config.Paths)))

	// Process responses
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			resp, err := stream.Recv()
			if err != nil {
				return fmt.Errorf("stream receive error: %w", err)
			}
			rcvr.processResponse(ctx, resp)
		}
	}
}

// gNMI notification handler
func (rcvr *gnmiReceiver) processResponse(ctx context.Context, response *gnmi.SubscribeResponse) {
	update := response.GetUpdate()
	if update == nil {
		return
	}

	// Create metrics
	metrics := pmetric.NewMetrics()
	resourceMetrics := metrics.ResourceMetrics().AppendEmpty()

	// Set resource attributes
	resourceAttrs := resourceMetrics.Resource().Attributes()
	resourceAttrs.PutStr("gnmi.target", rcvr.config.Target)

	scopeMetrics := resourceMetrics.ScopeMetrics().AppendEmpty()
	scopeMetrics.Scope().SetName("gnmi-receiver")

	// Process each update
	for _, upd := range update.Update {
		rcvr.createMetric(scopeMetrics, upd, update.Prefix)
	}

	// Send metrics
	if scopeMetrics.Metrics().Len() > 0 {
		if err := rcvr.nextConsumer.ConsumeMetrics(ctx, metrics); err != nil {
			rcvr.logger.Error("Failed to consume metrics", zap.Error(err))
		}
	}
}

// Converts gNMI update to OpenTelemetry metric
func (rcvr *gnmiReceiver) createMetric(scopeMetrics pmetric.ScopeMetrics, update *gnmi.Update, prefix *gnmi.Path) {
	// Extract numeric value
	value, ok := rcvr.extractValue(update.Val)
	if !ok {
		return
	}

	// Extract resource name from prefix
	resourceName := rcvr.extractResourceName(prefix)
	if resourceName == "" {
		resourceName = "unknown"
	}

	// Build full metric name from prefix + update path
	metricName := rcvr.buildFullMetricName(prefix, update.Path)
	if metricName == "" {
		rcvr.logger.Debug("Could not build metric name")
		return
	}

	// Create gauge metric
	metric := scopeMetrics.Metrics().AppendEmpty()
	metric.SetName(metricName)
	metric.SetDescription(fmt.Sprintf("gNMI metric: %s", metricName))

	gauge := metric.SetEmptyGauge()
	dataPoint := gauge.DataPoints().AppendEmpty()
	dataPoint.SetTimestamp(pcommon.NewTimestampFromTime(time.Now()))
	dataPoint.SetDoubleValue(value)

	// Add resource name as primary label
	attrs := dataPoint.Attributes()
	attrs.PutStr("resource", resourceName)
}

// Extracts numeric value from gNMI TypedValue
func (rcvr *gnmiReceiver) extractValue(val *gnmi.TypedValue) (float64, bool) {
	if val == nil {
		return 0, false
	}

	switch v := val.Value.(type) {
	case *gnmi.TypedValue_IntVal:
		return float64(v.IntVal), true
	case *gnmi.TypedValue_UintVal:
		return float64(v.UintVal), true
	case *gnmi.TypedValue_FloatVal:
		return float64(v.FloatVal), true
	case *gnmi.TypedValue_StringVal:
		if f, err := strconv.ParseFloat(v.StringVal, 64); err == nil {
			return f, true
		}
		return 0, false
	case *gnmi.TypedValue_BoolVal:
		if v.BoolVal {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

// Converts string path to gNMI Path
func (rcvr *gnmiReceiver) parsePath(pathStr string) *gnmi.Path {
	pathStr = strings.TrimPrefix(pathStr, "/")
	elements := strings.Split(pathStr, "/")

	path := &gnmi.Path{
		Origin: "openconfig",
	}
	for _, elem := range elements {
		if elem != "" {
			path.Elem = append(path.Elem, &gnmi.PathElem{Name: elem})
		}
	}
	return path
}

// Gets resource identifier from prefix keys
func (rcvr *gnmiReceiver) extractResourceName(prefix *gnmi.Path) string {
	if prefix == nil {
		return ""
	}

	for _, elem := range prefix.Elem {
		if elem.Key != nil {
			for _, value := range elem.Key {
				return value // Return first key value found
			}
		}
	}
	return ""
}

// Creates metric name from full path structure
func (rcvr *gnmiReceiver) buildFullMetricName(prefix *gnmi.Path, updatePath *gnmi.Path) string {
	var parts []string

	// Start with gnmi prefix
	parts = append(parts, "gnmi")

	// Add prefix elements (skip ones with keys since those are resources)
	if prefix != nil {
		for _, elem := range prefix.Elem {
			// Skip elements that have keys (those are resource identifiers)
			if elem.Key == nil || len(elem.Key) == 0 {
				cleanName := rcvr.cleanPathElement(elem.Name)
				if cleanName != "" {
					parts = append(parts, cleanName)
				}
			}
		}
	}

	// Add update path elements
	if updatePath != nil {
		for _, elem := range updatePath.Elem {
			cleanName := rcvr.cleanPathElement(elem.Name)
			if cleanName != "" {
				parts = append(parts, cleanName)
			}
		}
	}

	return strings.Join(parts, "_")
}

// Cleans up path element names for metric names
func (rcvr *gnmiReceiver) cleanPathElement(name string) string {
	// Remove namespace prefixes like "openconfig-interfaces:"
	if colonIndex := strings.Index(name, ":"); colonIndex != -1 {
		name = name[colonIndex+1:]
	}

	// Replace hyphens with underscores
	name = strings.ReplaceAll(name, "-", "_")

	// Remove common prefixes to shorten names
	name = strings.TrimPrefix(name, "openconfig_")

	return name
}
