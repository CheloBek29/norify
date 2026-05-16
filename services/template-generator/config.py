import os


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
