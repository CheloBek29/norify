import os
from pathlib import Path

try:
    from dotenv import load_dotenv
except ImportError:  # pragma: no cover - python-dotenv is installed in the service image
    def load_dotenv(*_args, **_kwargs):
        return False


ROOT_DOTENV_PATH = Path(__file__).resolve().parents[2] / ".env"


def load_service_env(dotenv_path: Path = ROOT_DOTENV_PATH) -> bool:
    return bool(load_dotenv(dotenv_path))


load_service_env()


class Settings:
    MISTRAL_API_KEY: str = os.getenv("MISTRAL_API_KEY", "")
    MISTRAL_MODEL: str = os.getenv("MISTRAL_MODEL", "mistral-small-latest")
    SERVICE_NAME: str = "template-generator"
    SERVICE_PORT: int = 8003
    SERVICE_HOST: str = "0.0.0.0"
    ALLOWED_ORIGINS: list = [
        "http://localhost:3000",
        "http://localhost:5173",
    ]
    GENERATION_TIMEOUT: int = 300
    MAX_RETRIES: int = 2


settings = Settings()
