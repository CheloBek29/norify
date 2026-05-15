import logging
from typing import Optional

from fastapi import FastAPI, HTTPException
from fastapi.middleware.cors import CORSMiddleware
from pydantic import BaseModel

from config import settings
from generator import generator

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

app = FastAPI(title=settings.SERVICE_NAME, version="1.0.0")

app.add_middleware(
    CORSMiddleware,
    allow_origins=settings.ALLOWED_ORIGINS,
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)


class GenerateTextRequest(BaseModel):
    task_description: str
    style: str = "professional"


class GenerateHTMLRequest(BaseModel):
    newsletter_text: str
    brand_name: str = "My Company"
    brand_email: str = "info@example.com"
    image_base64: Optional[str] = None


@app.get("/health")
async def health():
    return {"status": "ok", "service": settings.SERVICE_NAME}


@app.post("/api/generate-text")
async def generate_text(request: GenerateTextRequest):
    if not request.task_description or len(request.task_description) < 10:
        raise HTTPException(status_code=400, detail="Task description must be at least 10 characters")

    valid_styles = {"professional", "creative", "luxury", "minimal", "ecommerce"}
    if request.style not in valid_styles:
        request.style = "professional"

    result = await generator.generate_text(
        task_description=request.task_description,
        style=request.style,
    )

    if not result["success"]:
        raise HTTPException(status_code=500, detail=result.get("error"))

    return result


@app.post("/api/generate-html")
async def generate_html(request: GenerateHTMLRequest):
    if not request.newsletter_text:
        raise HTTPException(status_code=400, detail="Newsletter text is required")

    result = await generator.generate_html_template(
        newsletter_text=request.newsletter_text,
        brand_name=request.brand_name,
        brand_email=request.brand_email,
        image_base64=request.image_base64,
    )

    if not result["success"]:
        raise HTTPException(status_code=500, detail=result.get("error"))

    return result


if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host=settings.SERVICE_HOST, port=settings.SERVICE_PORT)
