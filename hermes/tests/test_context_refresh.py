import os
import sys
import types
import unittest
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0,str(Path(__file__).resolve().parents[1]/"patches/hermes-agent"))
import managed_model_catalog as catalog

class ContextRefreshTest(unittest.TestCase):
    def setUp(self):
        self.env=patch.dict(os.environ,{catalog.PROTOCOL:"1","CLAWMANAGER_LLM_BASE_URL":"http://gateway/v1","CLAWMANAGER_LLM_API_KEY":"test"},clear=True)
        self.env.start(); self.addCleanup(self.env.stop)
        self.agent=types.SimpleNamespace(base_url="http://gateway/v1",api_key="test",model="auto",context_compressor=types.SimpleNamespace(context_length=128000),history=["keep"])
    def test_updates_existing_agent_next_turn(self):
        with patch.object(catalog,"load_catalog",return_value=[{"id":"auto","context_window":64000}]) as load:
            catalog.refresh_managed_context(self.agent)
            self.assertEqual(self.agent.context_compressor.context_length,64000)
            load.return_value=[{"id":"auto","context_window":32000}]
            catalog.refresh_managed_context(self.agent)
        self.assertEqual(self.agent.context_compressor.context_length,32000)
        self.assertEqual(self.agent._config_context_length,32000)
        self.assertEqual(self.agent.history,["keep"])
    def test_unknown_preserves_native_value(self):
        with patch.object(catalog,"load_catalog",return_value=[{"id":"auto","context_window":0}]): catalog.refresh_managed_context(self.agent)
        self.assertEqual(self.agent.context_compressor.context_length,128000)
    def test_foreign_and_team_untouched(self):
        with patch.object(catalog,"load_catalog") as load:
            self.agent.base_url="http://user/v1"
            catalog.refresh_managed_context(self.agent)
            self.agent.base_url="http://gateway/v1"
            with patch.dict(os.environ,{"CLAWMANAGER_TEAM_ENABLED":"true"}): catalog.refresh_managed_context(self.agent)
            load.assert_not_called()
    def test_bad_or_ambiguous_catalog_rejected(self):
        for items in [[],[{"id":"auto","context_window":True}],[{"id":"auto","context_window":-1}],[{"id":"auto"},{"id":"auto"}]]:
            with patch.object(catalog,"load_catalog",return_value=items):
                with self.assertRaises((catalog.ManagedModelUnavailable,catalog.ManagedCatalogUnavailable)): catalog.refresh_managed_context(self.agent)

if __name__=="__main__": unittest.main()
