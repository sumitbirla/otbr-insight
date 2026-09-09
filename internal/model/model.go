package model

import "time"

// Overview is the stable, normalized representation exposed to the UI.
// Provider-specific response shapes must not leak into this type.
type Overview struct {
	Status                string     `json:"status"`
	Stale                 bool       `json:"stale"`
	HasData               bool       `json:"hasData"`
	NetworkName           string     `json:"networkName,omitempty"`
	Role                  string     `json:"role,omitempty"`
	State                 string     `json:"state,omitempty"`
	RLOC16                string     `json:"rloc16,omitempty"`
	RouterID              *int       `json:"routerId,omitempty"`
	ExtendedAddress       string     `json:"extendedAddress,omitempty"`
	ExtendedPANID         string     `json:"extendedPanId,omitempty"`
	PartitionID           *uint32    `json:"partitionId,omitempty"`
	LeaderRouterID        *int       `json:"leaderRouterId,omitempty"`
	RouterCount           *int       `json:"routerCount,omitempty"`
	OMRIPv6Address        string     `json:"omrIpv6Address,omitempty"`
	RLOCAddress           string     `json:"rlocAddress,omitempty"`
	LinkLocalAddress      string     `json:"linkLocalAddress,omitempty"`
	MeshLocalAddress      string     `json:"meshLocalAddress,omitempty"`
	MeshLocalPrefix       string     `json:"meshLocalPrefix,omitempty"`
	PANID                 string     `json:"panId,omitempty"`
	OpenThreadVersion     string     `json:"openThreadVersion,omitempty"`
	OpenThreadAPIVersion  string     `json:"openThreadApiVersion,omitempty"`
	RCPChannel            string     `json:"rcpChannel,omitempty"`
	RCPEUI64              string     `json:"rcpEui64,omitempty"`
	RCPState              string     `json:"rcpState,omitempty"`
	RCPTxPower            string     `json:"rcpTxPower,omitempty"`
	RCPVersion            string     `json:"rcpVersion,omitempty"`
	WPANService           string     `json:"wpanService,omitempty"`
	BorderAgentState      string     `json:"borderAgentState,omitempty"`
	APIHealth             string     `json:"apiHealth"`
	LastSuccessfulRefresh *time.Time `json:"lastSuccessfulRefresh,omitempty"`
	LastAttempt           time.Time  `json:"lastAttempt"`
	RequestLatencyMs      int64      `json:"requestLatencyMs"`
	Error                 string     `json:"error,omitempty"`
}

type Capability struct {
	Name        string    `json:"name"`
	Endpoint    string    `json:"endpoint"`
	Supported   bool      `json:"supported"`
	Required    bool      `json:"required"`
	StatusCode  int       `json:"statusCode,omitempty"`
	LatencyMs   int64     `json:"latencyMs"`
	LastChecked time.Time `json:"lastChecked"`
	Error       string    `json:"error,omitempty"`
}

type Capabilities struct {
	Items       []Capability `json:"items"`
	LastChecked time.Time    `json:"lastChecked"`
}

// Device is a provider-independent view of a Thread node. Pointer fields are
// used where OTBR versions may omit a value entirely.
type Device struct {
	ID              string     `json:"id"`
	Name            string     `json:"name,omitempty"`
	CustomName      string     `json:"customName,omitempty"`
	Role            string     `json:"role,omitempty"`
	RLOC16          string     `json:"rloc16,omitempty"`
	RouterID        *int       `json:"routerId,omitempty"`
	Parent          string     `json:"parent,omitempty"`
	ExtendedAddress string     `json:"extendedAddress,omitempty"`
	MLEIDIID        string     `json:"mlEidIid,omitempty"`
	EUI64           string     `json:"eui64,omitempty"`
	IPv6Addresses   []string   `json:"ipv6Addresses,omitempty"`
	OMRIPv6Address  string     `json:"omrIpv6Address,omitempty"`
	ThreadVersion   string     `json:"threadVersion,omitempty"`
	FirstSeen       *time.Time `json:"firstSeen,omitempty"`
	LastSeen        *time.Time `json:"lastSeen,omitempty"`
	RSSI            *int       `json:"rssi,omitempty"`
	// Frame and message error rates as fractions (0-1). High rates with a strong
	// RSSI indicate interference or retry timeouts rather than range.
	FrameErrorRate   *float64 `json:"frameErrorRate,omitempty"`
	MessageErrorRate *float64 `json:"messageErrorRate,omitempty"`
	LinkQuality      *int     `json:"linkQuality,omitempty"`
	LinkMargin       *int     `json:"linkMargin,omitempty"`
	IsBorderRouter   bool     `json:"isBorderRouter"`
}

type DeviceInventory struct {
	Status                string     `json:"status"`
	Stale                 bool       `json:"stale"`
	NetworkMismatch       bool       `json:"networkMismatch"`
	CollectionSupported   bool       `json:"collectionSupported"`
	Items                 []Device   `json:"items"`
	Source                string     `json:"source"`
	LastSuccessfulRefresh *time.Time `json:"lastSuccessfulRefresh,omitempty"`
	LastAttempt           time.Time  `json:"lastAttempt"`
	RequestLatencyMs      int64      `json:"requestLatencyMs"`
	Error                 string     `json:"error,omitempty"`
}

// TopologyNode and TopologyLink are normalized from OTBR network diagnostics.
// Diagnostic children may not expose a stable extended address, so their ID is
// derived from the parent router and child ID and must not be treated as device
// identity outside a topology snapshot.
type TopologyNode struct {
	ID              string `json:"id"`
	Name            string `json:"name,omitempty"`
	CustomName      string `json:"customName,omitempty"`
	Role            string `json:"role"`
	RLOC16          string `json:"rloc16,omitempty"`
	RouterID        *int   `json:"routerId,omitempty"`
	ExtendedAddress string `json:"extendedAddress,omitempty"`
	ParentID        string `json:"parentId,omitempty"`
	LinkQuality     *int   `json:"linkQuality,omitempty"`
	Timeout         *int   `json:"timeout,omitempty"`
	RxOnWhenIdle    *bool  `json:"rxOnWhenIdle,omitempty"`
	DeviceTypeFTD   *bool  `json:"deviceTypeFtd,omitempty"`
	FullNetworkData *bool  `json:"fullNetworkData,omitempty"`
	IsBorderRouter  bool   `json:"isBorderRouter"`
}

type TopologyLink struct {
	Source           string   `json:"source"`
	Target           string   `json:"target"`
	Type             string   `json:"type"`
	LinkQuality      *int     `json:"linkQuality,omitempty"`
	LinkQualityIn    *int     `json:"linkQualityIn,omitempty"`
	LinkQualityOut   *int     `json:"linkQualityOut,omitempty"`
	RouteCost        *int     `json:"routeCost,omitempty"`
	LinkMargin       *int     `json:"linkMargin,omitempty"`
	AverageRSSI      *int     `json:"averageRssi,omitempty"`
	LastRSSI         *int     `json:"lastRssi,omitempty"`
	FrameErrorRate   *float64 `json:"frameErrorRate,omitempty"`
	MessageErrorRate *float64 `json:"messageErrorRate,omitempty"`
}

type Topology struct {
	Status                string         `json:"status"`
	Stale                 bool           `json:"stale"`
	CollectionSupported   bool           `json:"collectionSupported"`
	Nodes                 []TopologyNode `json:"nodes"`
	Links                 []TopologyLink `json:"links"`
	Source                string         `json:"source"`
	LastSuccessfulRefresh *time.Time     `json:"lastSuccessfulRefresh,omitempty"`
	LastAttempt           time.Time      `json:"lastAttempt"`
	RequestLatencyMs      int64          `json:"requestLatencyMs"`
	Error                 string         `json:"error,omitempty"`
}

// Dataset is a credential-masked view of the active operational dataset. The
// network key and PSKc are never included; only their presence is reported, so
// the polled read API cannot leak Thread credentials.
type Dataset struct {
	Present         bool   `json:"present"`
	NetworkName     string `json:"networkName,omitempty"`
	Channel         *int   `json:"channel,omitempty"`
	PANID           string `json:"panId,omitempty"`
	ExtPANID        string `json:"extPanId,omitempty"`
	MeshLocalPrefix string `json:"meshLocalPrefix,omitempty"`
	ActiveTimestamp *int64 `json:"activeTimestamp,omitempty"`
	HasNetworkKey   bool   `json:"hasNetworkKey"`
	HasPSKc         bool   `json:"hasPskc"`
}

// NetworkConfig is the manage-view payload: the interface state plus the masked
// active dataset.
type NetworkConfig struct {
	State   string  `json:"state"`
	Dataset Dataset `json:"dataset"`
}

type AvailableNetwork struct {
	Name            string `json:"name,omitempty"`
	ExtendedPANID   string `json:"extendedPanId,omitempty"`
	PANID           string `json:"panId,omitempty"`
	Channel         *int   `json:"channel,omitempty"`
	HardwareAddress string `json:"hardwareAddress,omitempty"`
}

type NetworkScan struct {
	Status     string             `json:"status"`
	Items      []AvailableNetwork `json:"items"`
	Source     string             `json:"source"`
	ScannedAt  time.Time          `json:"scannedAt"`
	DurationMs int64              `json:"durationMs"`
	Error      string             `json:"error,omitempty"`
}

// PingResult reports a reachability test run from the border router.
type PingResult struct {
	Address   string   `json:"address"`
	Reachable bool     `json:"reachable"`
	Sent      int      `json:"sent"`
	Received  int      `json:"received"`
	MinMs     *float64 `json:"minMs,omitempty"`
	AverageMs *float64 `json:"averageMs,omitempty"`
	MaxMs     *float64 `json:"maxMs,omitempty"`
}

// NetworkHistoryEntry records a change of role or partition, from OpenThread's own
// event recorder rather than anything this app observed.
type NetworkHistoryEntry struct {
	At          time.Time `json:"at"`
	Role        string    `json:"role,omitempty"`
	Mode        string    `json:"mode,omitempty"`
	RLOC16      string    `json:"rloc16,omitempty"`
	PartitionID *uint32   `json:"partitionId,omitempty"`
}

// NeighborHistoryEntry records a device attaching or detaching, with the signal at
// the time — which is what explains why it happened.
type NeighborHistoryEntry struct {
	At              time.Time `json:"at"`
	Type            string    `json:"type,omitempty"`
	Event           string    `json:"event,omitempty"`
	ExtendedAddress string    `json:"extendedAddress,omitempty"`
	CustomName      string    `json:"customName,omitempty"`
	RLOC16          string    `json:"rloc16,omitempty"`
	AverageRSSI     *int      `json:"averageRssi,omitempty"`
}

type History struct {
	Status    string                 `json:"status"`
	Source    string                 `json:"source,omitempty"`
	Error     string                 `json:"error,omitempty"`
	Network   []NetworkHistoryEntry  `json:"network"`
	Neighbors []NeighborHistoryEntry `json:"neighbors"`
}

// ChannelEnergy is the peak RSSI heard on one IEEE 802.15.4 channel during an
// energy-detect scan. It measures everything on the air — Wi-Fi, Bluetooth, other
// Thread and Zigbee networks — not just Thread beacons.
type ChannelEnergy struct {
	Channel int `json:"channel"`
	// MaxRSSI is the loudest reading across every sweep: the worst case, which is
	// what choosing a channel should be judged on.
	MaxRSSI int `json:"maxRssi"`
	// TypicalRSSI is the median across sweeps, which separates a channel that is
	// always busy from one that caught a single burst. Absent for a single sweep.
	TypicalRSSI *int `json:"typicalRssi,omitempty"`
}

// EnergyScan is one pass over the 2.4 GHz channels, for choosing a quiet one.
// The absolute figures depend on how long the radio listened per channel, so
// channels are meaningfully compared with each other within one scan rather
// than against a fixed threshold.
type EnergyScan struct {
	Status         string    `json:"status"`
	Source         string    `json:"source"`
	ScannedAt      time.Time `json:"scannedAt"`
	DurationMs     int64     `json:"durationMs"`
	CurrentChannel *int      `json:"currentChannel,omitempty"`
	// Sweeps is how many passes over the channels were combined into Channels.
	Sweeps   int             `json:"sweeps"`
	Channels []ChannelEnergy `json:"channels"`
	Error    string          `json:"error,omitempty"`
}
