#!/usr/bin/env python3
"""비공개 설정 보존과 API 키 발급·재사용의 회귀 검사. 외부 요청 없음."""

import contextlib
import importlib.util
import io
import json
from pathlib import Path
import secrets
import stat
import subprocess
import tempfile
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("prepare_mcp", Path(__file__).with_name("prepare-mcp.py"))
setup = importlib.util.module_from_spec(spec)
spec.loader.exec_module(setup)


class ScopeSentryTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.env = self.root / "scopesentry" / ".env"
        self.state = self.root / "private state"
        self.output = io.StringIO()
        with contextlib.redirect_stdout(self.output):
            setup.initialize("scopesentry", self.env, self.state, "artex-test_default")

    def test_init_preserves_existing_credentials_and_state(self):
        before = self.env.read_bytes()
        (self.state / "server-password").write_text(secrets.token_hex(16))
        state_before = {path.name: path.read_bytes() for path in self.state.iterdir()}
        with contextlib.redirect_stdout(self.output):
            setup.initialize("scopesentry", self.env, self.state, "artex-test_default")
        self.assertEqual(self.env.read_bytes(), before)
        self.assertEqual({path.name: path.read_bytes() for path in self.state.iterdir()}, state_before)
        self.assertEqual(stat.S_IMODE(self.env.stat().st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(self.state.stat().st_mode), 0o700)
        for path in self.state.iterdir():
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(self.env.parent.stat().st_mode), 0o700)
        values = setup.read_env(self.env)
        self.assertEqual(values["SCOPESENTRY_STATE_DIR"], str(self.state))
        self.assertEqual(values["COMPOSE_PROJECT_NAME"], "artex-scopesentry")
        self.assertFalse(any(key.startswith("MSF_") for key in values))
        self.assertEqual(len((self.state / "server-plugin-key").read_text()), 32)
        for name in ("SCOPESENTRY_MONGO_PASSWORD", "SCOPESENTRY_REDIS_PASSWORD"):
            self.assertNotIn(values[name], self.output.getvalue())

    def test_init_does_not_replace_existing_state_without_env(self):
        self.env.unlink()
        before = {path.name: path.read_bytes() for path in self.state.iterdir()}
        with self.assertRaises(setup.SetupError):
            setup.initialize("scopesentry", self.env, self.state, "artex-test_default")
        self.assertEqual({path.name: path.read_bytes() for path in self.state.iterdir()}, before)

    def test_init_reports_missing_existing_state_without_regenerating_it(self):
        before = self.env.read_bytes()
        (self.state / "server-plugin-key").unlink()
        with self.assertRaises(setup.SetupError):
            setup.initialize("scopesentry", self.env, self.state, "artex-test_default")
        self.assertEqual(self.env.read_bytes(), before)
        self.assertFalse((self.state / "server-plugin-key").exists())

    def test_connect_issues_once_preserves_comments_and_reuses_key(self):
        (self.state / "server-password").write_text(secrets.token_hex(16))
        with self.env.open("a") as handle:
            handle.write("# 사용자 설정 보존\nUSER_SETTING='custom'\n")
        key = "ssk_" + secrets.token_hex(20)
        result = {"result": {"serverInfo": {"name": "ScopeSentry"}}}
        with patch.object(setup, "request", side_effect=[
            {"data": {"access_token": secrets.token_hex(16)}},
            {"data": {"key": key}}, result,
        ]) as request, contextlib.redirect_stdout(self.output):
            setup.connect(self.env)
        self.assertEqual([call.args[1] for call in request.call_args_list],
                         ["/api/user/login", "/api/apikey/create", "/mcp"])
        before = self.env.read_bytes()
        self.assertIn("# 사용자 설정 보존", self.env.read_text())
        self.assertEqual(setup.read_env(self.env)["USER_SETTING"], "custom")
        self.assertEqual(stat.S_IMODE(self.env.stat().st_mode), 0o600)
        with patch.object(setup, "request", return_value=result) as request, contextlib.redirect_stdout(self.output):
            setup.connect(self.env)
        self.assertEqual(request.call_count, 1)
        self.assertEqual(request.call_args.args[1], "/mcp")
        self.assertEqual(self.env.read_bytes(), before)
        self.assertNotIn(key, self.output.getvalue())

    def test_connect_keeps_issued_key_when_mcp_verification_temporarily_fails(self):
        (self.state / "server-password").write_text(secrets.token_hex(16))
        key = "ssk_" + secrets.token_hex(20)
        with patch.object(setup, "request", side_effect=[
            {"data": {"access_token": secrets.token_hex(16)}},
            {"data": {"key": key}}, setup.SetupError("일시적인 연결 실패"),
        ]):
            with self.assertRaises(setup.SetupError):
                setup.connect(self.env)
        self.assertEqual(setup.read_env(self.env)["ARTEX_SCOPESENTRY_API_KEY"], key)
        with patch.object(setup, "request", return_value={
            "result": {"serverInfo": {"name": "ScopeSentry"}},
        }) as request, contextlib.redirect_stdout(self.output):
            setup.connect(self.env)
        self.assertEqual(request.call_count, 1)
        self.assertEqual(request.call_args.args[1], "/mcp")

    def test_invalid_existing_key_is_not_replaced(self):
        setup.save_api_key(self.env, "ssk_" + secrets.token_hex(20))
        before = self.env.read_bytes()
        with patch.object(setup, "request", side_effect=setup.SetupError("인증 실패")) as request:
            with self.assertRaises(setup.SetupError):
                setup.connect(self.env)
        self.assertEqual(request.call_count, 1)
        self.assertEqual(request.call_args.args[1], "/mcp")
        self.assertEqual(self.env.read_bytes(), before)


class MetasploitTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.env = self.root / "metasploit" / ".env"
        self.output = io.StringIO()
        with contextlib.redirect_stdout(self.output):
            setup.initialize("metasploit", self.env, None, "artex-test_default")

    def test_init_only_prepares_private_metasploit_settings(self):
        values = setup.read_env(self.env)
        self.assertEqual(values["COMPOSE_PROJECT_NAME"], "artex-metasploit")
        self.assertEqual(values["ARTEX_NETWORK"], "artex-test_default")
        self.assertEqual(values["MSF_MCP_DANGEROUS_ACTIONS"], "false")
        self.assertFalse(any(key.startswith("SCOPESENTRY_") for key in values))
        self.assertEqual(stat.S_IMODE(self.env.stat().st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(self.env.parent.stat().st_mode), 0o700)
        for name in ("MSF_MCP_AUTH_TOKEN", "MSF_POSTGRES_PASSWORD"):
            self.assertTrue(values[name])
            self.assertNotIn(values[name], self.output.getvalue())

    def test_init_preserves_explicit_execution_permission_network_and_credentials(self):
        self.env.write_text(self.env.read_text().replace(
            "MSF_MCP_DANGEROUS_ACTIONS='false'", "MSF_MCP_DANGEROUS_ACTIONS='true'",
        ) + "# 사용자 설정\nUSER_SETTING='custom'\n")
        before = self.env.read_bytes()
        with contextlib.redirect_stdout(self.output):
            setup.initialize("metasploit", self.env, None, "different-network")
        self.assertEqual(self.env.read_bytes(), before)
        self.assertEqual(setup.read_env(self.env)["MSF_MCP_DANGEROUS_ACTIONS"], "true")

    def test_init_rejects_invalid_network_without_creating_files(self):
        other = self.root / "other" / ".env"
        with self.assertRaises(setup.SetupError):
            setup.initialize("metasploit", other, None, "network'\nSETTING=true")
        self.assertFalse(other.exists())

    def test_init_rejects_symlink_without_changing_target(self):
        linked = self.root / "linked-env"
        linked.symlink_to(self.env)
        before = self.env.read_bytes()
        with self.assertRaises(setup.SetupError):
            setup.initialize("metasploit", linked, None, "artex-test_default")
        self.assertEqual(self.env.read_bytes(), before)


class UpTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.env = self.root / ".env"
        self.state = self.root / "state"
        self.output = io.StringIO()

    def test_network_discovery_uses_the_running_artex_network(self):
        with patch.object(setup, "docker", side_effect=[
            "artex-id\n", json.dumps({"custom-artex_default": {}}), "custom-artex_default\n",
        ]) as docker:
            self.assertEqual(setup.resolve_network(self.env, None), "custom-artex_default")
        self.assertEqual(docker.call_args.args, ("network", "inspect", "custom-artex_default", "--format", "{{.Name}}"))

    def test_ambiguous_or_missing_artex_requires_an_explicit_network(self):
        for replies in ([""], ["first\nsecond\n"], ["artex-id\n", '{"one":{},"two":{}}']):
            with self.subTest(replies=replies), patch.object(setup, "docker", side_effect=replies):
                with self.assertRaisesRegex(setup.SetupError, "--network"):
                    setup.up("metasploit", self.env, self.state, None)
                self.assertFalse(self.env.exists())

    def test_saved_network_is_reused_and_conflicting_argument_does_not_overwrite(self):
        self.env.write_text("ARTEX_NETWORK='saved-network'\n")
        with patch.object(setup, "docker", return_value="saved-network\n") as docker:
            self.assertEqual(setup.resolve_network(self.env, None), "saved-network")
            self.assertEqual(docker.call_count, 1)
        with patch.object(setup, "docker") as docker:
            with self.assertRaisesRegex(setup.SetupError, "기존 ARTEX_NETWORK"):
                setup.resolve_network(self.env, "another-network")
            docker.assert_not_called()
        self.assertEqual(setup.read_env(self.env)["ARTEX_NETWORK"], "saved-network")

    def test_missing_env_with_existing_resources_does_not_generate_new_passwords(self):
        for resources in (["existing-container"], ["", "existing-volume"]):
            with self.subTest(resources=resources), patch.object(setup, "docker", side_effect=["network", *resources]):
                with self.assertRaisesRegex(setup.SetupError, "환경 파일을 복원"):
                    setup.up("metasploit", self.env, self.state, "network")
                self.assertFalse(self.env.exists())

    def test_up_initializes_and_waits_before_issuing_scope_key(self):
        calls = []
        def docker(*args):
            calls.append(args)
            return ""
        def connect(path):
            self.assertIn("--wait", calls[-1])
            self.assertIn("up", calls[-1])
            setup.save_api_key(path, "ssk_" + secrets.token_hex(20))
        with patch.object(setup, "docker", side_effect=docker), \
                patch.object(setup, "connect", side_effect=connect) as connect_mock, \
                contextlib.redirect_stdout(self.output):
            setup.up("scopesentry", self.env, self.state, "selected-network")
            before = self.env.read_bytes()
            connect_mock.side_effect = None
            setup.up("scopesentry", self.env, self.state, None)
        self.assertEqual(connect_mock.call_count, 2)
        self.assertEqual(self.env.read_bytes(), before)
        self.assertEqual([call[0] for call in calls], ["network", "container", "volume", "compose", "network", "compose"])
        self.assertIn(str(self.env), calls[-1])
        self.assertIn("artex-scopesentry", calls[-1])
        values = setup.read_env(self.env)
        for name in ("ARTEX_SCOPESENTRY_API_KEY", "SCOPESENTRY_MONGO_PASSWORD", "SCOPESENTRY_REDIS_PASSWORD"):
            self.assertNotIn(values[name], self.output.getvalue())

    def test_failed_start_keeps_files_and_does_not_issue_key(self):
        def docker(*args):
            if args[0] == "compose":
                raise setup.SetupError("기동 실패")
            return ""
        with patch.object(setup, "docker", side_effect=docker), \
                patch.object(setup, "connect") as connect, contextlib.redirect_stdout(self.output):
            with self.assertRaisesRegex(setup.SetupError, "기동 실패"):
                setup.up("scopesentry", self.env, self.state, "network")
            connect.assert_not_called()
        self.assertTrue(self.env.is_file())
        self.assertTrue((self.state / "server-plugin-key").is_file())
        self.assertEqual(setup.read_env(self.env)["ARTEX_SCOPESENTRY_API_KEY"], "")

    def test_docker_does_not_inherit_conflicting_settings_or_print_error_secrets(self):
        host = {"PATH": "/usr/bin", "DOCKER_HOST": "fixture-socket", "MSF_MCP_AUTH_TOKEN": "secret",
                "SCOPESENTRY_PORT": "9999", "COMPOSE_FILE": "other.yml", "ARTEX_NETWORK": "other"}
        with patch.dict(setup.os.environ, host, clear=True), patch.object(setup.subprocess, "run") as run:
            run.return_value = subprocess.CompletedProcess([], 1, "secret", "secret")
            with self.assertRaises(setup.SetupError) as failure:
                setup.docker("compose", "up")
            self.assertNotIn("secret", str(failure.exception))
        self.assertEqual(run.call_args.kwargs["env"],
                         {"PATH": "/usr/bin", "DOCKER_HOST": "fixture-socket", "COMPOSE_ANSI": "never"})


if __name__ == "__main__":
    unittest.main()
