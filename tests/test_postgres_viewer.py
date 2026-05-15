from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]


def read_file(name: str) -> str:
    return (ROOT / name).read_text(encoding="utf-8")


class PostgresViewerConfigTest(unittest.TestCase):
    def test_docker_compose_exposes_postgres_viewer(self):
        compose = read_file("docker-compose.yml")

        self.assertIn("  postgres-viewer:", compose)
        self.assertIn("    image: adminer:4.8.1", compose)
        self.assertIn('      - "8089:8080"', compose)
        self.assertIn("      ADMINER_DEFAULT_SERVER: postgres", compose)
        self.assertIn("      postgres:", compose)
        self.assertIn("        condition: service_healthy", compose)

    def test_readme_documents_postgres_viewer_login(self):
        readme = read_file("README.md")

        self.assertIn("http://localhost:8089", readme)
        self.assertIn("`System`: `PostgreSQL`", readme)
        self.assertIn("`Server`: `postgres`", readme)
        self.assertIn("`Username`: `norify`", readme)
        self.assertIn("`Password`: `norify`", readme)
        self.assertIn("`Database`: `norify`", readme)
