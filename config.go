package tuna

import (
	"time"

	"github.com/nknorg/tuna/filter"

	"github.com/imdario/mergo"
	"github.com/nknorg/tuna/geo"
	"github.com/nknorg/tuna/pb"
	"github.com/nknorg/tuna/types"
)

const (
	DefaultSubscriptionPrefix = "tuna_v1."
	DefaultReverseServiceName = "reverse"

	defaultNanoPayDuration                   = 4320 * 30
	defaultNanoPayUpdateInterval             = time.Minute
	defaultNanoPayMinFlushAmount             = "0.01"
	defaultServiceListenIP                   = "127.0.0.1"
	defaultReverseServiceListenIP            = "0.0.0.0"
	defaultGetSubscribersBatchSize           = 128
	defaultEncryptionAlgo                    = pb.EncryptionAlgo_ENCRYPTION_NONE
	defaultMeasureDelayTimeout               = 1 * time.Second
	defaultMeasureDelayConcurrentWorkers     = 64
	defaultMeasureBandwidthConcurrentWorkers = 16 // should be >= measureBandwidthTopCount
	defaultMeasureBandwidthTimeout           = 2  // second
	defaultMeasureBandwidthWorkersTimeout    = 8  // second
	defaultMeasurementBytesDownLink          = 256 << 10
	defaultMaxMeasureWorkerPoolSize          = 64
	defaultReverseTestTimeout                = 3 * time.Second
	maxMeasureBandwidthTimeout               = 30 * time.Second
	nanoPayClaimerLinger                     = 24 * time.Hour
)

type EntryConfiguration struct {
	SeedRPCServerAddr              []string               `json:"seedRPCServerAddr"`
	Services                       map[string]ServiceInfo `json:"services"`
	DialTimeout                    int32                  `json:"dialTimeout"`
	UDPTimeout                     int32                  `json:"udpTimeout"`
	NanoPayFee                     string                 `json:"nanoPayFee"`
	SubscriptionPrefix             string                 `json:"subscriptionPrefix"`
	Reverse                        bool                   `json:"reverse"`
	ReverseBeneficiaryAddr         string                 `json:"reverseBeneficiaryAddr"`
	ReverseTCP                     int32                  `json:"reverseTCP"`
	ReverseUDP                     int32                  `json:"reverseUDP"`
	ReverseServiceListenIP         string                 `json:"reverseServiceListenIP"`
	ReversePrice                   string                 `json:"reversePrice"`
	ReverseClaimInterval           int32                  `json:"reverseClaimInterval"`
	ReverseMinFlushAmount          string                 `json:"reverseMinFlushAmount"`
	ReverseServiceName             string                 `json:"reverseServiceName"`
	ReverseSubscriptionPrefix      string                 `json:"reverseSubscriptionPrefix"`
	ReverseSubscriptionDuration    int32                  `json:"reverseSubscriptionDuration"`
	ReverseSubscriptionFee         string                 `json:"reverseSubscriptionFee"`
	GeoDBPath                      string                 `json:"geoDBPath"`
	DownloadGeoDB                  bool                   `json:"downloadGeoDB"`
	GetSubscribersBatchSize        int32                  `json:"getSubscribersBatchSize"`
	MeasureBandwidth               bool                   `json:"measureBandwidth"`
	MeasureBandwidthTimeout        int32                  `json:"measureBandwidthTimeout"`
	MeasureBandwidthWorkersTimeout int32                  `json:"measureBandwidthWorkersTimeout"`
	MeasurementBytesDownLink       int32                  `json:"measurementBytesDownLink"`
	MeasureStoragePath             string                 `json:"measureStoragePath"`
	MaxMeasureWorkerPoolSize       int32                  `json:"maxMeasureWorkerPoolSize"`
	SortMeasuredNodes              func(types.Nodes)      `json:"-"`
}

var defaultEntryConfiguration = EntryConfiguration{
	SubscriptionPrefix:             DefaultSubscriptionPrefix,
	GetSubscribersBatchSize:        defaultGetSubscribersBatchSize,
	MeasureBandwidthTimeout:        defaultMeasureBandwidthTimeout,
	MeasureBandwidthWorkersTimeout: defaultMeasureBandwidthWorkersTimeout,
	MeasurementBytesDownLink:       defaultMeasurementBytesDownLink,
	MaxMeasureWorkerPoolSize:       defaultMaxMeasureWorkerPoolSize,
	ReverseSubscriptionPrefix:      DefaultSubscriptionPrefix,
	ReverseServiceName:             DefaultReverseServiceName,
	ReverseMinFlushAmount:          defaultNanoPayMinFlushAmount,
	ReverseServiceListenIP:         defaultReverseServiceListenIP,
}

func DefaultEntryConfig() *EntryConfiguration {
	conf := defaultEntryConfiguration
	return &conf
}

type ExitConfiguration struct {
	SeedRPCServerAddr              []string                   `json:"seedRPCServerAddr"`
	BeneficiaryAddr                string                     `json:"beneficiaryAddr"`
	ListenTCP                      int32                      `json:"listenTCP"`
	ListenUDP                      int32                      `json:"listenUDP"`
	DialTimeout                    int32                      `json:"dialTimeout"`
	UDPTimeout                     int32                      `json:"udpTimeout"`
	SubscriptionPrefix             string                     `json:"subscriptionPrefix"`
	SubscriptionDuration           int32                      `json:"subscriptionDuration"`
	SubscriptionFee                string                     `json:"subscriptionFee"`
	ClaimInterval                  int32                      `json:"claimInterval"`
	MinFlushAmount                 string                     `json:"minFlushAmount"`
	Services                       map[string]ExitServiceInfo `json:"services"`
	Reverse                        bool                       `json:"reverse"`
	ReverseRandomPorts             bool                       `json:"reverseRandomPorts"`
	ReverseMaxPrice                string                     `json:"reverseMaxPrice"`
	ReverseNanoPayFee              string                     `json:"reverseNanopayfee"`
	ReverseServiceName             string                     `json:"reverseServiceName"`
	ReverseSubscriptionPrefix      string                     `json:"reverseSubscriptionPrefix"`
	ReverseEncryption              string                     `json:"reverseEncryption"`
	GeoDBPath                      string                     `json:"geoDBPath"`
	DownloadGeoDB                  bool                       `json:"downloadGeoDB"`
	GetSubscribersBatchSize        int32                      `json:"getSubscribersBatchSize"`
	ReverseIPFilter                geo.IPFilter               `json:"reverseIPFilter"`
	ReverseNknFilter               filter.NknFilter           `json:"reverseNknFilter"`
	MeasureBandwidth               bool                       `json:"measureBandwidth"`
	MeasureBandwidthTimeout        int32                      `json:"measureBandwidthTimeout"`
	MeasureBandwidthWorkersTimeout int32                      `json:"measureBandwidthWorkersTimeout"`
	MeasurementBytesDownLink       int32                      `json:"measurementBytesDownLink"`
	MeasureStoragePath             string                     `json:"measureStoragePath"`
	MaxMeasureWorkerPoolSize       int32                      `json:"maxMeasureWorkerPoolSize"`
	SortMeasuredNodes              func(types.Nodes)          `json:"-"`
}

var defaultExitConfiguration = ExitConfiguration{
	SubscriptionPrefix:             DefaultSubscriptionPrefix,
	GetSubscribersBatchSize:        defaultGetSubscribersBatchSize,
	MeasureBandwidthTimeout:        defaultMeasureBandwidthTimeout,
	MeasureBandwidthWorkersTimeout: defaultMeasureBandwidthWorkersTimeout,
	MeasurementBytesDownLink:       defaultMeasurementBytesDownLink,
	MaxMeasureWorkerPoolSize:       defaultMaxMeasureWorkerPoolSize,
	MinFlushAmount:                 defaultNanoPayMinFlushAmount,
	ReverseSubscriptionPrefix:      DefaultSubscriptionPrefix,
	ReverseServiceName:             DefaultReverseServiceName,
}

func DefaultExitConfig() *ExitConfiguration {
	conf := defaultExitConfiguration
	return &conf
}

func MergedEntryConfig(conf *EntryConfiguration) (*EntryConfiguration, error) {
	merged := DefaultEntryConfig()
	if conf != nil {
		err := mergo.Merge(merged, conf, mergo.WithOverride)
		if err != nil {
			return nil, err
		}
	}
	return merged, nil
}

func MergedExitConfig(conf *ExitConfiguration) (*ExitConfiguration, error) {
	merged := DefaultExitConfig()
	if conf != nil {
		err := mergo.Merge(merged, conf, mergo.WithOverride)
		if err != nil {
			return nil, err
		}
	}
	return merged, nil
}
