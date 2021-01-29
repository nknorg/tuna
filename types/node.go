package types

import (
	"github.com/nknorg/tuna/pb"
)

type Node struct {
	Address     string
	Metadata    *pb.ServiceMetadata
	MetadataRaw string
	Delay       float32 // ms
	Bandwidth   float32 // byte/s
}

type Nodes []*Node

func (fs Nodes) Len() int {
	return len(fs)
}

func (fs Nodes) Swap(i, j int) {
	fs[i], fs[j] = fs[j], fs[i]
}

type SortByDelay struct{ Nodes }

func (s SortByDelay) Less(i, j int) bool {
	return s.Nodes[i].Delay < s.Nodes[j].Delay
}

type SortByBandwidth struct{ Nodes }

func (s SortByBandwidth) Less(i, j int) bool {
	return s.Nodes[i].Bandwidth > s.Nodes[j].Bandwidth
}
