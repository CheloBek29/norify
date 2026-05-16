from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]


def read_file(name: str) -> str:
    return (ROOT / name).read_text(encoding="utf-8")


class PostgresViewerConfigTest(unittest.TestCase):
    def test_docker_compose_exposes_postgres_viewer(self):
        compose = read_file("docker-compose.yml")

        self.assertIn("  postgres-viewer:", compose)
        self.assertIn("    image: sosedoff/pgweb:latest", compose)
        self.assertIn("      - --url=postgres://norify:norify@postgres:5432/norify?sslmode=disable", compose)
        self.assertIn('      - "8089:8081"', compose)
        self.assertIn("      postgres:", compose)
        self.assertIn("        condition: service_healthy", compose)

    def test_readme_documents_postgres_viewer_login(self):
        readme = read_file("README.md")

        self.assertIn("http://localhost:8089", readme)
        self.assertIn("pgweb", readme)
        self.assertIn("postgres://norify:norify@postgres:5432/norify?sslmode=disable", readme)
