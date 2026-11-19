import asyncio
import json
import tempfile
import types
import unittest
import uuid
from pathlib import Path
from unittest import mock

from test_adapter_redis_integration import REDIS_URL, HermesRedisTeamRedisIntegrationTests
from test_adapter_contract import adapter


@unittest.skipUnless(REDIS_URL, "requires disposable Redis")
class BackupMaintenanceTests(unittest.TestCase):
    settings = HermesRedisTeamRedisIntegrationTests.settings
    cleanup = HermesRedisTeamRedisIntegrationTests.cleanup
    wait_pending_zero = HermesRedisTeamRedisIntegrationTests.wait_pending_zero

    def test_maintenance_stops_new_dispatch_and_resumes_pending(self):
        async def exercise():
            with tempfile.TemporaryDirectory() as tmp:
                settings = self.settings(Path(tmp), "backup-" + uuid.uuid4().hex)
                control = adapter.AsyncRedisClient(REDIS_URL)
                await control.connect()
                instance = None
                seen = []
                try:
                    with mock.patch.object(adapter, "load_settings", return_value=settings):
                        instance = adapter.RedisTeamAdapter(types.SimpleNamespace(extra={}))
                    async def dispatch(envelope, **_kwargs):
                        instance._accepted_messages[envelope["messageId"]] = adapter._assignment_identity(envelope)
                        seen.append(envelope["messageId"])
                    instance._dispatch_envelope = dispatch
                    self.assertTrue(await instance.connect())
                    maintenance = f"claw:team:{settings.team_id}:maintenance"
                    await control.command("SET", maintenance, json.dumps({"enabled": True, "rolloutId": "test-maintenance"}), "PX", 10000)
                    await adapter.xadd_json(control, adapter.inbox_key(settings), {"messageId":"backup-paused", "taskId":"backup-task", "rootTaskId":"backup-task", "assignmentId":"dev-1", "teamId":settings.team_id, "from":"leader", "to":"developer", "text":"must wait"})
                    await asyncio.sleep(0.3)
                    self.assertEqual(seen, [], "maintenance must prevent new message dispatch")
                    await control.command("DEL", maintenance)
                    deadline = asyncio.get_running_loop().time()+5
                    while not seen and asyncio.get_running_loop().time()<deadline:
                        await asyncio.sleep(0.05)
                    self.assertEqual(seen, ["backup-paused"])
                    await self.wait_pending_zero(control, settings)
                finally:
                    if instance is not None:
                        await instance.disconnect()
                    await self.cleanup(control, settings)
                    control.close()
        asyncio.run(exercise())


if __name__ == "__main__":
    unittest.main()
