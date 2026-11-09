"""Single-member root is an explicit envelope contract, not a role override."""
import asyncio
import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock
from test_adapter_contract import adapter


class SingleMemberTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.settings = adapter.RedisTeamSettings(enabled=True, redis_url="redis://local.invalid:6379/0", team_id="42", member_id="editor", role="worker", shared_dir=self.tmp.name)
        adapter.ensure_team_dirs(self.settings)
        self.envelope = {"messageId":"new-input", "taskId":"team-42-task-9", "rootTaskId":"team-42-task-9", "from":"clawmanager", "assignmentId":"single-member", "workId":"single-member", "revision":1, "executionType":"single_member", "executorMemberKey":"editor", "followupContext":{"text":"Keep earlier citations", "artifactRefs":[]}}

    def test_normalization_and_trust_boundary(self):
        normalized = adapter.normalize_envelope(self.envelope)
        self.assertEqual(normalized["followupContext"], self.envelope["followupContext"])
        self.assertTrue(adapter._is_single_member_envelope(self.settings, normalized))
        for field, value in [("executionType","team"),("from","leader"),("executorMemberKey","other"),("rootTaskId","team-43-task-9"),("assignmentId","old-work")]:
            self.assertFalse(adapter._is_single_member_envelope(self.settings, {**self.envelope, field:value}))

    def test_completion_root_and_ordinary_worker_contract_are_separate(self):
        for single in [False, True]:
            envelope = dict(self.envelope)
            if not single: envelope.pop("executionType")
            adapter._persist_active_envelope(self.settings, envelope)
            captured = {}
            class FakeRedis:
                def __init__(self, _url): pass
                async def connect(self): pass
                def close(self): pass
            async def publish(_r,_s,_key,event):
                captured.update(event)
                return {"published":True,"streamId":"1-0"}
            async def ack(*_args): return {"decision":"accepted"}
            with mock.patch.object(adapter,"AsyncRedisClient",FakeRedis), mock.patch.object(adapter,"_publish_once",publish), mock.patch.object(adapter,"_completion_ack",ack):
                result = asyncio.run(adapter._propose_completion(self.settings,envelope,status="succeeded",summary="报告修改完成",result_markdown="保留原始引用的报告",explicit=True))
            self.assertEqual(captured["assignmentResultOnly"],not single)
            self.assertEqual(captured["rootTaskTerminal"],single)
            self.assertEqual(captured["rootTaskId"],"team-42-task-9")
            self.assertEqual(result["decision"],"accepted")
            self.assertTrue(adapter._load_active_envelope(self.settings)["terminal"])

    def test_no_delegation_before_redis_io_and_old_file_preserved(self):
        adapter._persist_active_envelope(self.settings,self.envelope)
        with mock.patch.object(adapter,"load_settings",return_value=self.settings), mock.patch.object(adapter,"AsyncRedisClient") as redis:
            result=json.loads(asyncio.run(adapter._tool_team_send({"to":"leader","text":"summarize"})))
            self.assertEqual(result["code"],"single_member_no_delegation")
            redis.assert_not_called()
        old=Path(self.tmp.name)/"artifacts/team-42-task-7/members/editor/old/report.md"
        old.parent.mkdir(parents=True); old.write_text("original citations",encoding="utf-8")
        adapter.write_task_result(self.settings,"team-42-task-9",envelope=self.envelope,status="failed",summary="需要文件",result_markdown="请求的文件不存在，未修改旧文件")
        self.assertEqual(old.read_text(encoding="utf-8"),"original citations")


if __name__ == "__main__": unittest.main()
