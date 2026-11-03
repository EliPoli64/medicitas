FROM python:3.12-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY *.py .

RUN useradd --create-home --uid 10001 seed \
    && chown -R seed:seed /app

USER seed

CMD ["python", "seed.py"]
