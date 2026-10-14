FROM python:3.12-slim-bookworm
WORKDIR /app
RUN pip install --no-cache-dir uv==0.8.22
COPY ai-service/pyproject.toml ai-service/uv.lock ./
RUN uv sync --frozen --no-dev --no-install-project
COPY ai-service/app/ ./app/
ENV PATH="/app/.venv/bin:$PATH" PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 AI_PROVIDER_ENABLED=false AI_HTTP_HOST=0.0.0.0
USER 65532:65532
EXPOSE 8000
CMD ["python", "-m", "app.serve"]
