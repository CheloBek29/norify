import os


class Settings:
    OLLAMA_URL: str = os.getenv("OLLAMA_URL", "http://localhost:11434")
    OLLAMA_MODEL: str = os.getenv("OLLAMA_MODEL", "mistral:7b")
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
