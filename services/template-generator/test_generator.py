import pytest

import config
import generator as generator_module
from generator import TemplateGenerator, extract_mistral_content, mistral_ssl_context


def test_config_loads_repo_dotenv(monkeypatch):
    loaded = {}

    def fake_load_dotenv(dotenv_path):
        loaded["path"] = dotenv_path
        return True

    monkeypatch.setattr(config, "load_dotenv", fake_load_dotenv)

    assert config.load_service_env() is True
    assert loaded["path"].name == ".env"
    assert loaded["path"].parent.name == "norify"


@pytest.mark.asyncio
async def test_generate_text_uses_mistral_and_returns_clean_text(monkeypatch):
    calls = {}

    async def fake_call_mistral(system, user):
        calls["system"] = system
        calls["user"] = user
        return "\nЗдравствуйте, {first_name}!\n\nВаше предложение готово.\n"

    monkeypatch.setattr(generator_module, "_call_mistral", fake_call_mistral)

    result = await TemplateGenerator().generate_text(
        task_description="Сделай рассылку про весеннюю распродажу",
        style="ecommerce",
    )

    assert result == {
        "success": True,
        "text": "Здравствуйте, {first_name}!\n\nВаше предложение готово.",
        "style": "ecommerce",
    }
    assert "весеннюю распродажу" in calls["user"]
    assert "E-commerce" in calls["user"]
    assert "РУССКОМ" in calls["system"]


def test_extract_mistral_content_accepts_string_and_text_parts():
    assert extract_mistral_content({"choices": [{"message": {"content": "Готово"}}]}) == "Готово"
    assert extract_mistral_content({
        "choices": [{"message": {"content": [{"text": "При"}, {"text": "вет"}]}}],
    }) == "Привет"


def test_extract_mistral_content_rejects_invalid_response():
    with pytest.raises(ValueError):
        extract_mistral_content({"choices": []})


def test_mistral_ssl_context_uses_valid_ca_bundle():
    context = mistral_ssl_context()

    assert context.verify_mode.name == "CERT_REQUIRED"
    assert context.check_hostname is True
