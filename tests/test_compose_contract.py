from pathlib import Path
import re
import unittest


ROOT = Path(__file__).resolve().parents[1]
COMPOSE = (ROOT / "docker-compose.yml").read_text(encoding="utf-8")


class ComposeServiceInventoryTest(unittest.TestCase):
    def test_expected_services_are_declared(self):
        expected = [
            "auth-service",
            "user-service",
            "template-service",
            "channel-service",
            "campaign-service",
            "dispatcher-service",
            "sender-worker",
            "notification-error-service",
            "status-service",
            "frontend",
            "postgres",
            "rabbitmq",
            "redis",
            "postgres-viewer",
        ]
        for service in expected:
            with self.subTest(service=service):
                self.assertRegex(COMPOSE, rf"(?m)^  {re.escape(service)}:\n")

    def test_postgres_viewer_uses_pgweb_not_adminer(self):
        self.assertIn("sosedoff/pgweb", COMPOSE)
        self.assertIn("--url=postgres://norify:norify@postgres:5432/norify?sslmode=disable", COMPOSE)
        self.assertNotIn("adminer:4.8.1", COMPOSE)
        self.assertNotIn("ADMINER_DEFAULT_SERVER", COMPOSE)


class ComposePortMappingTest(unittest.TestCase):
    def test_documented_host_ports_are_mapped(self):
        expected_ports = [
            "3000:80",
            "8081:8080",
            "8082:8080",
            "8083:8080",
            "8084:8080",
            "8085:8080",
            "8086:8080",
            "8087:8080",
            "8088:8080",
            "8089:8081",
            "8090:8080",
            "15672:15672",
        ]
        for mapping in expected_ports:
            with self.subTest(mapping=mapping):
                self.assertIn(f'"{mapping}"', COMPOSE)

    def test_core_dependencies_have_healthchecks(self):
        for service in ["postgres", "rabbitmq", "redis"]:
            with self.subTest(service=service):
                service_block = service_block_for(service)
                self.assertIn("healthcheck:", service_block)


def service_block_for(service: str) -> str:
    match = re.search(rf"(?ms)^  {re.escape(service)}:\n(?P<body>.*?)(?=^  [a-zA-Z0-9_-]+:\n|\Z)", COMPOSE)
    if not match:
        raise AssertionError(f"service {service!r} not found")
    return match.group("body")


if __name__ == "__main__":
    unittest.main()
