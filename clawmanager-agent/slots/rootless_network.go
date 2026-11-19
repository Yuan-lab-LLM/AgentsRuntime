package slots

import (
	"fmt"
	"strconv"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/sandboxprofile"
)

func rootlessNetworkGroups(req gateway.CreateGatewayRequest) ([]uint32, error) {
	raw, ok := gateway.RequestEnvValue(req, "CLAWMANAGER_TEAM_SHARED_GID")
	if !ok || raw == "" {
		return nil, nil
	}
	shared, ok := gateway.RequestEnvValue(req, "CLAWMANAGER_TEAM_SHARED_DIR")
	gid, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || !ok || shared == "" || gid == 0 || gid == 65534 || gid == sandboxprofile.ManagementGID || gid == 1<<32-1 {
		return nil, fmt.Errorf("invalid rootless Team group")
	}
	return []uint32{uint32(gid)}, nil
}
