FROM golang:1.23

WORKDIR /app

COPY . .

RUN go test ./... \
    && git init -b main \
    && git config user.email "docker@example.test" \
    && git config user.name "Docker Build" \
    && git add . \
    && git commit -m "Initial idempotency keys fixture"

ENV PORT=8803
EXPOSE 8803

CMD ["go", "run", "."]
