#!/usr/bin/env python3
"""개발 환경 정리의 소유권·경로·설정 보존 검사. Docker 변경 없음."""

import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import call, patch


spec = importlib.util.spec_from_file_location("dev_lab", Path(__file__).with_name("dev-lab.py"))
lab = importlib.util.module_from_spec(spec)
spec.loader.exec_module(lab)


class LabTests(unittest.TestCase):
    def test_up_rejects_another_checkout_image_before_initializing(self):
        foreign = json.dumps([{"Config": {"Labels": {lab.OWNER: "/another/checkout"}}}])
        with patch.object(lab, "docker", side_effect=["image-id\n", foreign]), \
                patch.object(lab, "initialize") as initialize:
            with self.assertRaises(lab.LabError):
                lab.up()
            initialize.assert_not_called()

    def test_clean_rejects_foreign_volume_before_any_deletion(self):
        foreign = json.dumps([{"Labels": {lab.OWNER: "/another/checkout"}}])
        with patch.object(lab, "docker", side_effect=["", "volume-id\n", foreign]) as docker, \
                patch.object(lab.shutil, "rmtree") as remove:
            with self.assertRaises(lab.LabError):
                lab.clean()
            self.assertTrue(all(call.args[1] in ("ls", "inspect") for call in docker.call_args_list))
            remove.assert_not_called()

    def test_cleanup_failure_preserves_local_state(self):
        with patch.object(lab, "resources", side_effect=[["container-id"], [], []]), \
                patch.object(lab, "owned_image_exists", return_value=False), \
                patch.object(lab, "docker", side_effect=lab.LabError("fixture stop failure")), \
                patch.object(lab.shutil, "rmtree") as remove:
            with self.assertRaises(lab.LabError):
                lab.clean()
            remove.assert_not_called()

    def test_clean_rejects_external_mcp_before_any_deletion(self):
        own, external = "a" * 64, "b" * 64
        with patch.object(lab, "resources", side_effect=[[own], ["volume-id"], ["network-id"]]), \
                patch.object(lab, "owned_image_exists") as image, \
                patch.object(lab, "docker", return_value=f"{own}\n{external}\n") as docker, \
                patch.object(lab.shutil, "rmtree") as remove:
            with self.assertRaisesRegex(lab.LabError, "외부 MCP 프로젝트를 먼저 down"):
                lab.clean()
            docker.assert_called_once_with("container", "ls", "--all", "--no-trunc", "-q",
                                           "--filter", "network=network-id")
            image.assert_not_called()
            remove.assert_not_called()

    def test_clean_owned_network_without_env_preserves_other_local_data(self):
        own = "a" * 64
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            state = root / "state"
            state.mkdir()
            (state / "run.log").write_text("fixture")
            other = root / "other-project"
            other.mkdir()
            marker = other / "marker"
            marker.write_text("preserve")
            with patch.object(lab, "ROOT", root), patch.object(lab, "STATE", state), \
                    patch.object(lab, "ENV_FILE", state / ".env"), \
                    patch.object(lab, "resources", side_effect=[[own], ["volume-id"], ["network-id"]]), \
                    patch.object(lab, "owned_image_exists", return_value=False), \
                    patch.object(lab, "docker", side_effect=[f"{own}\n", "", "", "", ""]) as docker:
                lab.clean()
                self.assertEqual(docker.call_args_list, [
                    call("container", "ls", "--all", "--no-trunc", "-q", "--filter", "network=network-id"),
                    call("stop", "--time", "30", own),
                    call("container", "rm", "--volumes", own),
                    call("volume", "rm", "volume-id"),
                    call("network", "rm", "network-id"),
                ])
            self.assertFalse(state.exists())
            self.assertEqual(marker.read_text(), "preserve")

    def test_missing_environment_with_saved_volumes_is_not_reinitialized(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory) / "state"
            with patch.object(lab, "ROOT", Path(directory)), patch.object(lab, "STATE", state), \
                    patch.object(lab, "ENV_FILE", state / ".env"), \
                    patch.object(lab, "resources", side_effect=[[], ["saved-volume"], []]):
                with self.assertRaises(lab.LabError):
                    lab.initialize()
                self.assertFalse(state.exists())

    def test_cleanup_rejects_symlink_state(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = root / "preserve"
            target.mkdir()
            marker = target / "marker"
            marker.write_text("preserve")
            state = root / "state"
            state.symlink_to(target, target_is_directory=True)
            with patch.object(lab, "ROOT", root), patch.object(lab, "STATE", state), \
                    patch.object(lab, "ENV_FILE", state / ".env"), patch.object(lab, "docker") as docker:
                with self.assertRaises(lab.LabError):
                    lab.clean()
                docker.assert_not_called()
                self.assertEqual(marker.read_text(), "preserve")

    def test_host_credentials_and_compose_overrides_are_not_inherited(self):
        values = {"OPENAI_API_KEY": "fixture", "ARTEX_ADMIN_PASSWORD": "fixture",
                  "COMPOSE_FILE": "/another/compose.yml", "POSTGRES_PASSWORD": "fixture",
                  "PATH": "/usr/bin"}
        with patch.dict(lab.os.environ, values, clear=True):
            env = lab.environment()
        self.assertEqual(env, {"PATH": "/usr/bin", "ARTEX_DEV_LAB_ROOT": str(lab.ROOT), "COMPOSE_ANSI": "never"})


if __name__ == "__main__":
    unittest.main()
