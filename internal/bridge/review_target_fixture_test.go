package bridge

import (
	"strings"

	"github.com/pairtask/pairtask/internal/protocol"
)

func reviewBridgeTarget() protocol.ReviewTarget {
	return protocol.ReviewTarget{Forge: "fake", Repository: "fake.example/space/repo", Change: "CR:7~revision"}
}

func reviewBridgePins() protocol.Revisions {
	return protocol.Revisions{Base: strings.Repeat("a", 40), Head: strings.Repeat("b", 40)}
}
