package hermes

import "github.com/iamlovingit/clawmanager-agent/gateway"

func (Profile) GatewayBaseEnvironment(base []string) []string {
	return gateway.AppendImageEnvironment(base, "PYTHONDONTWRITEBYTECODE", "PYTHONUNBUFFERED", "VIRTUAL_ENV", "UV_PYTHON_DOWNLOADS", "UV_LINK_MODE", "PLAYWRIGHT_BROWSERS_PATH", "HERMES_TUI_DIR", "HERMES_VERSION", "HERMES_GIT_REF", "HERMES_GIT_COMMIT")
}
