import logging
import os
from typing import Optional

import aiohttp

from prompts import PROMPT_NEWSLETTER_TEMPLATE, STYLES, SYSTEM_PROMPT_BASE

logger = logging.getLogger(__name__)

OPENAI_API_KEY = os.getenv("OPENAI_API_KEY", "")
OPENAI_MODEL = os.getenv("OPENAI_MODEL", "gpt-3.5-turbo")
OPENAI_API_URL = "https://api.openai.com/v1/chat/completions"


async def _call_openai(system: str, user: str) -> str:
    if not OPENAI_API_KEY:
        return "Error: OPENAI_API_KEY not set"
    headers = {
        "Authorization": f"Bearer {OPENAI_API_KEY}",
        "Content-Type": "application/json",
    }
    payload = {
        "model": OPENAI_MODEL,
        "messages": [
            {"role": "system", "content": system},
            {"role": "user", "content": user},
        ],
        "temperature": 0.7,
        "max_tokens": 1500,
    }
    try:
        async with aiohttp.ClientSession() as session:
            async with session.post(OPENAI_API_URL, json=payload, headers=headers, timeout=aiohttp.ClientTimeout(total=60)) as resp:
                if resp.status == 200:
                    data = await resp.json()
                    return data["choices"][0]["message"]["content"]
                error = await resp.text()
                return f"Error {resp.status}: {error[:200]}"
    except Exception as e:
        return f"Error: {e}"


class TemplateGenerator:
    async def generate_text(self, task_description: str, style: str = "professional") -> dict:
        style_desc = STYLES.get(style, STYLES["professional"])
        user_prompt = PROMPT_NEWSLETTER_TEMPLATE.format(SYSTEM_PROMPT_BASE, task_description, style_desc)
        logger.info("Generating newsletter text...")
        text = await _call_openai(SYSTEM_PROMPT_BASE, user_prompt)
        if text.startswith("Error"):
            return {"success": False, "error": text}
        return {"success": True, "text": text.strip(), "style": style}

    async def generate_html_template(
        self,
        newsletter_text: str,
        brand_name: str = "Company",
        brand_email: str = "info@example.com",
        image_base64: Optional[str] = None,
    ) -> dict:
        image_block = ""
        if image_base64:
            image_block = f'<img src="{image_base64}" alt="image" style="max-width:600px;width:100%;height:auto;">'

        system = "Ты мастер HTML/CSS email-дизайна. Создаёшь красивые кроссбраузерные HTML5 шаблоны для email-рассылок."
        user_prompt = f"""Создай профессиональный HTML5 шаблон email-рассылки.

Требования: встроенный CSS, адаптивный дизайн, Google Fonts, обязательная ссылка отписки.
Структура: Header → {image_block or 'Hero'} → Контент → CTA кнопка → Footer.
Компания: {brand_name}, email: {brand_email}

Текст рассылки:
{newsletter_text}

Верни ТОЛЬКО HTML код от <html> до </html> без бэктиков."""

        logger.info("Generating HTML template...")
        html = await _call_openai(system, user_prompt)
        if html.startswith("Error"):
            return {"success": False, "error": html}

        html = html.replace("```html", "").replace("```", "")
        if "<footer" not in html.lower():
            footer = f"""<footer style="text-align:center;padding:24px;background:#f5f5f5;font-size:12px;color:#999;">
<p><strong>{brand_name}</strong> · <a href="mailto:{brand_email}">{brand_email}</a></p>
<p><a href="#" style="color:#999;">Отписаться от рассылки</a></p>
</footer>"""
            html = html.replace("</body>", footer + "</body>")

        return {"success": True, "html": html, "filename": f"newsletter_{brand_name.replace(' ', '_')}.html"}


generator = TemplateGenerator()
