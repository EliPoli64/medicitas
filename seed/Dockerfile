FROM python:3.12-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

WORKDIR /app

# psql se usa para ejecutar scripts .sql con múltiples sentencias y funciones PL/pgSQL.
RUN apt-get update \
    && apt-get install -y --no-install-recommends postgresql-client \
    && rm -rf /var/lib/apt/lists/*

COPY seed/requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY seed/*.py .
COPY db/migrations/ /migrations/

RUN useradd --create-home --uid 10001 seed \
    && chown -R seed:seed /app

USER seed

CMD ["python", "seed.py"]
