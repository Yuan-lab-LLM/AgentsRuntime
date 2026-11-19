package control

import (
	"github.com/iamlovingit/clawmanager-agent/control"
	"github.com/iamlovingit/clawmanager-agent/gateway"
)

type Config = gateway.Config
type CreateGatewayRequest = gateway.CreateGatewayRequest
type CreateGatewayResponse = gateway.CreateGatewayResponse
type GatewayStartSpec = gateway.GatewayStartSpec
type HeartbeatPayload = gateway.HeartbeatPayload
type ManagedProcess = gateway.ManagedProcess
type PortRange = gateway.PortRange
type SkillReportPayload = gateway.SkillReportPayload

var NewGatewayManager = gateway.NewGatewayManager
var NewPortAllocator = gateway.NewPortAllocator
var ErrGatewayStartFailed = gateway.ErrGatewayStartFailed

var NewControlHandler = control.NewControlHandler
var NewReportClient = control.NewReportClient

const ControlTokenHeader = control.ControlTokenHeader
