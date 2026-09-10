package protocol

const Version = 1
const DefaultMaxShardBytes int64 = 64 << 20
const ShareHeader = "X-Dstore-Share"

// Info describes an opted-in agent, independently of its discovery transport.
type Info struct {
	Service         string `json:"service"`
	ProtocolVersion int    `json:"protocol_version"`
	Name            string `json:"name"`
	OS              string `json:"os"`
	Arch            string `json:"arch"`
	ShareID         string `json:"share_id,omitempty"`
	Storage         bool   `json:"storage"`
	UsedBytes       int64  `json:"used_bytes"`
	QuotaBytes      int64  `json:"quota_bytes"`
	MaxShardBytes   int64  `json:"max_shard_bytes"`
}
