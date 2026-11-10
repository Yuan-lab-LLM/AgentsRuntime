#!/usr/bin/env python3
"""Apply the reviewed v0.22.5 network/provider/session-worker boundary."""
import hashlib
from pathlib import Path
import shutil
import sys

EXPECTED = {'hermes_cli/web_server.py': '07db739369a150244dbc48adc5a7e51dbfb2421f87847b98aa449ab24a770424',
 'hermes_cli/web_server_lifecycle.py': '07ad78e476508159d69d69ab4dc7d9a3062ae105ef8728d6f99950211f8ba422',
 'hermes_cli/web_server_chat.py': '9517bf55c0ef12927c4421108e1439e6204bb5bb8fb50cb3aa22a42cc19bd64f',
 'ui-tui/src/gatewayClient.ts': 'b08ae986b8db0a2b61ac6efecc7601539b22ea61ad30910a27cd7e3572bdadb0',
 'hermes_cli/runtime_provider_custom.py': '8744c52b92cf84b1a5e57077c3e6fcaffacaa2799e46cd9149809aba7211847c'}


def replace_once(source, before, after):
    if source.count(before) != 1:
        raise ValueError("Lite boundary patch anchor changed")
    return source.replace(before, after)


def patched_sources(root):
    sources = {}
    for relative, digest in EXPECTED.items():
        content = (root / relative).read_bytes()
        if hashlib.sha256(content).hexdigest() != digest:
            raise ValueError("Lite boundary patch requires exact locked upstream sources")
        sources[relative] = content.decode("utf-8")
    server = sources["hermes_cli/web_server.py"]
    server = replace_once(server, "def start_server(\n",
                          "from hermes_cli.lite_gateway_boundary import install_lite_boundary\n"
                          "install_lite_boundary(app)\n\n\ndef start_server(\n")
    server = replace_once(server, "proxy_headers=bool(app.state.auth_required),",
                          "proxy_headers=False, access_log=False,  # Preserve the socket peer; never log credentials")
    sources["hermes_cli/web_server.py"] = server
    lifecycle = sources["hermes_cli/web_server_lifecycle.py"]
    lifecycle = replace_once(lifecycle, "proxy_headers=auth_required,",
                             "proxy_headers=False, access_log=False,  # Lite verifies the socket peer")
    sources["hermes_cli/web_server_lifecycle.py"] = lifecycle
    chat = sources["hermes_cli/web_server_chat.py"]
    for before, after in {
        'internal = ws.query_params.get("internal", "")':
            'from hermes_cli.lite_gateway_boundary import internal_credential\n'
            '        internal = internal_credential(ws.scope.get("headers", []))',
        'argv, cwd = _make_tui_argv(PROJECT_ROOT / "ui-tui", tui_dev=False)':
            'argv, cwd = ["/usr/local/bin/node", "--expose-gc", str(PROJECT_ROOT / "hermes_cli/tui_dist/entry.js")], PROJECT_ROOT / "hermes_cli/tui_dist"',
        'env["HERMES_TUI_DASHBOARD"] = "1"':
            'env["HERMES_TUI_DASHBOARD"] = "1"\n'
            '    from hermes_cli.dashboard_auth.ws_tickets import internal_ws_credential\n'
            '    env["HERMES_LITE_INTERNAL_WS_CREDENTIAL"] = internal_ws_credential()',
        'auth = {"internal": internal_ws_credential()}':
            'auth = {}  # Credential travels in the child environment / WS protocol, never a URL',
    }.items():
        chat = replace_once(chat, before, after)
    sources["hermes_cli/web_server_chat.py"] = chat
    client = sources["ui-tui/src/gatewayClient.ts"]
    client = replace_once(client, 'new WebSocketCtor(this.sidecarUrl)',
                          'new WebSocketCtor(this.sidecarUrl, liteInternalProtocols())')
    client = replace_once(client, 'new WebSocketCtor(attachUrl, protocols)',
                          'new WebSocketCtor(attachUrl, protocols ?? liteInternalProtocols())')
    client += '\nfunction liteInternalProtocols(): string[] {\n'
    client += '  const token = process.env.HERMES_LITE_INTERNAL_WS_CREDENTIAL\n'
    client += '  if (!token) throw new Error("missing_internal_websocket_credential")\n'
    client += '  return ["hermes-gateway-v1", "hermes-internal." + token]\n}\n'
    sources["ui-tui/src/gatewayClient.ts"] = client
    provider = sources["hermes_cli/runtime_provider_custom.py"]
    before = '    rp = _rp()\n    try:\n        # A frozen route'
    after = ('    # Managed instance credentials must outrank saved endpoint pools.\n'
             '    managed_name = str(provider_name or "").strip().lower()\n'
             '    if (os.environ.get("CLAWMANAGER_HERMES_DESKTOP_WEB_ENABLED", "").strip().lower() in {"1", "true", "yes", "on"}\n'
             '            and (managed_name == "clawmanager" or managed_name.startswith("clawmanager-"))):\n'
             '        return None\n' + before)
    sources["hermes_cli/runtime_provider_custom.py"] = replace_once(provider, before, after)
    for relative, source in sources.items():
        if relative.endswith(".py"):
            compile(source, relative, "exec")
    return sources


def apply(root):
    # Check all inputs and anchors before changing any file.
    for relative, source in patched_sources(root).items():
        (root / relative).write_text(source, encoding="utf-8", newline="\n")
    shutil.copyfile(Path(__file__).with_name("lite_gateway_boundary.py"), root / "hermes_cli/lite_gateway_boundary.py")


if __name__ == "__main__":
    apply(Path(sys.argv[1]))
