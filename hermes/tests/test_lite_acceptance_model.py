import importlib.util
import json
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("acceptance_model_stub", Path(__file__).with_name("acceptance_model_stub.py"))
stub = importlib.util.module_from_spec(spec)
spec.loader.exec_module(stub)


class AcceptanceModelTests(unittest.TestCase):
    def setUp(self):
        self.run_id = "a" * 32
        self.config = {"schema_version": 1, "run_id": self.run_id, "observer_token": "o" * 32,
                       "instances": [{"instance_id": n, "token": str(n) * 32,
                                      "fixture_root": f"/workspaces/hermes/user-1/instance-{n}/.acceptance-{self.run_id}"} for n in (71, 72)]}
        self.fixture = stub.Fixture(self.config)

    def test_wrong_model_credential_and_observer_are_not_model_credentials(self):
        self.assertIsNone(self.fixture.authenticate("Bearer " + self.config["observer_token"]))
        self.assertIsNone(self.fixture.authenticate("Bearer wrong"))
        self.assertEqual(71, self.fixture.authenticate("Bearer " + "71" * 32)["instance_id"])

    def test_only_current_campaign_can_trigger_deterministic_responses(self):
        with self.assertRaisesRegex(ValueError, "campaign_marker_required"):
            self.fixture.completion(self.config["instances"][0], {"messages": [{"role": "user", "content": "An unrelated real user request"}]})
        self.assertEqual(0, self.fixture.observation()["request_count"])

    def test_tool_fixture_scoped_and_independent_per_instance(self):
        marker = "ACCEPTANCE_" + self.run_id + "_ONCE"
        payload = {"messages": [{"role": "user", "content": marker}]}
        for item in self.config["instances"]:
            _, message, finish = self.fixture.completion(item, payload)
            self.assertEqual("tool_calls", finish)
            command = json.loads(message["tool_calls"][0]["function"]["arguments"])["command"]
            self.assertEqual("rm -rf -- " + item["fixture_root"] + "/allow-fixture", command)
            _, _, finish = self.fixture.completion(item, {"messages": payload["messages"] + [{"role": "tool", "content": "fixture result"}]})
            self.assertEqual("stop", finish)
        observation = self.fixture.observation()
        self.assertEqual(0, observation["external_requests"])
        self.assertEqual(2, observation["tool_results_observed"])
        self.assertEqual([71, 72], observation["instances"])
        self.assertNotIn("fixture result", json.dumps(observation))

    def test_workspace_escape_and_reused_credentials_rejected(self):
        for value in ("/etc", "/workspaces/hermes/user-1/instance-72/.acceptance-" + self.run_id,
                      "/workspaces/hermes/user-1/instance-71/../.acceptance-" + self.run_id):
            config = json.loads(json.dumps(self.config))
            config["instances"][0]["fixture_root"] = value
            with self.assertRaises(ValueError):
                stub.Fixture(config)
        config = json.loads(json.dumps(self.config))
        config["instances"][0]["token"] = config["observer_token"]
        with self.assertRaises(ValueError):
            stub.Fixture(config)

    def test_observer_records_actual_registered_tool_schema(self):
        payload = {"messages": [{"role": "user", "content": "ACCEPTANCE_" + self.run_id + "_TEXT"}],
                   "tools": [{"type": "function", "function": {"name": "terminal"}},
                             {"type": "function", "function": {"name": "computer"}}]}
        self.fixture.completion(self.config["instances"][0], payload)
        self.assertEqual({"terminal": 1, "computer": 1}, self.fixture.observation()["tool_schema_counts"])
        self.assertEqual({}, self.fixture.observation()["tool_counts"])


if __name__ == "__main__":
    unittest.main()
